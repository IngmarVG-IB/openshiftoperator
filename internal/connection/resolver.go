/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package connection resolves a Connection CR plus its Secret into a ready-to-use
// Universal DDI client, and caches those clients so a key rotation is picked up on the
// next reconcile without rebuilding a client on every pass.
package connection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// DefaultResyncInterval is used when a Connection leaves spec.resyncInterval empty.
const DefaultResyncInterval = 10 * time.Minute

// Errors returned by Resolve. They are sentinel values so callers can map them onto
// condition reasons without string matching.
var (
	// ErrConnectionNotFound means no Connection CR of that name exists in the namespace.
	ErrConnectionNotFound = errors.New("connection: Connection not found")
	// ErrSecretNotFound means the referenced Secret does not exist.
	ErrSecretNotFound = errors.New("connection: Secret not found")
	// ErrSecretKeyMissing means the Secret exists but has no data under the configured key.
	ErrSecretKeyMissing = errors.New("connection: Secret key missing or empty")
	// ErrConnectionNotReady means the Connection exists but has not been verified.
	ErrConnectionNotReady = errors.New("connection: Connection is not Ready")
)

// ClientFactory builds a Universal DDI client from a config. Tests swap in a factory
// that returns the in-memory fake.
type ClientFactory func(cfg uddi.Config) (*uddi.Client, error)

// Resolver turns a ConnectionRef into a Connection CR and a Universal DDI client.
// It is safe for concurrent use.
type Resolver struct {
	// Client reads Connection CRs and Secrets.
	Client client.Client
	// Factory builds Universal DDI clients. Nil means uddi.NewSDKClient.
	Factory ClientFactory

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	key    string
	client *uddi.Client
}

// Resolved is everything a reconciler needs from a Connection.
type Resolved struct {
	// Connection is the CR itself.
	Connection *uddiv1alpha1.Connection
	// Client talks to the Universal DDI tenant the Connection points at.
	Client *uddi.Client
	// DefaultTags are the Connection's spec.defaultTags.
	DefaultTags map[string]string
	// ResyncInterval is the effective drift-detection period.
	ResyncInterval time.Duration
}

// Get fetches a Connection CR. An empty name resolves to "default".
func (r *Resolver) Get(ctx context.Context, namespace, name string) (*uddiv1alpha1.Connection, error) {
	if name == "" {
		name = uddiv1alpha1.DefaultConnectionName
	}
	conn := &uddiv1alpha1.Connection{}
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, conn); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s/%s", ErrConnectionNotFound, namespace, name)
		}
		return nil, err
	}
	return conn, nil
}

// Resolve fetches the Connection, checks it is Ready, and returns a client for it.
// Object-layer reconcilers call this; the Connection controller calls ClientFor
// directly because it is the one that establishes readiness.
func (r *Resolver) Resolve(ctx context.Context, namespace, name string) (*Resolved, error) {
	conn, err := r.Get(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	if !IsReady(conn) {
		return nil, fmt.Errorf("%w: %s/%s", ErrConnectionNotReady, conn.Namespace, conn.Name)
	}
	c, err := r.ClientFor(ctx, conn)
	if err != nil {
		return nil, err
	}
	return &Resolved{
		Connection:     conn,
		Client:         c,
		DefaultTags:    conn.Spec.DefaultTags,
		ResyncInterval: ResyncInterval(conn),
	}, nil
}

// ClientFor builds or returns the cached Universal DDI client for a Connection. The
// cache key combines the Connection's identity, the Secret's resourceVersion and a hash
// of the client-relevant spec, so rotating the key or editing the spec produces a new
// client on the next reconcile.
//
// The API key is read straight into the client config and is never logged, never stored
// in the cache key and never copied into an error.
func (r *Resolver) ClientFor(ctx context.Context, conn *uddiv1alpha1.Connection) (*uddi.Client, error) {
	secret := &corev1.Secret{}
	secretName := types.NamespacedName{Namespace: conn.Namespace, Name: conn.Spec.SecretRef.Name}
	if err := r.Client.Get(ctx, secretName, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s", ErrSecretNotFound, secretName)
		}
		return nil, err
	}
	key := conn.SecretKey()
	raw, ok := secret.Data[key]
	if !ok || len(strings.TrimSpace(string(raw))) == 0 {
		return nil, fmt.Errorf("%w: %s key %q", ErrSecretKeyMissing, secretName, key)
	}

	cacheKey := fmt.Sprintf("%s/%s|%s|%s", conn.Namespace, conn.Name, secret.ResourceVersion, specHash(conn))

	r.mu.Lock()
	if e, hit := r.cache[cacheKey]; hit && e.key == cacheKey {
		r.mu.Unlock()
		return e.client, nil
	}
	r.mu.Unlock()

	rps, burst := rateLimit(conn)
	cfg := uddi.Config{
		CSPURL:         conn.CSPURL(),
		APIKey:         strings.TrimSpace(string(raw)),
		ClientName:     conn.ClientName(),
		DefaultTags:    conn.Spec.DefaultTags,
		RateLimitRPS:   rps,
		RateLimitBurst: burst,
	}

	factory := r.Factory
	if factory == nil {
		factory = uddi.NewSDKClient
	}
	c, err := factory(cfg)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]cacheEntry{}
	}
	// Only one entry per Connection is kept, so rotating the key evicts the old client.
	for k := range r.cache {
		if strings.HasPrefix(k, conn.Namespace+"/"+conn.Name+"|") {
			delete(r.cache, k)
		}
	}
	r.cache[cacheKey] = cacheEntry{key: cacheKey, client: c}
	r.mu.Unlock()

	return c, nil
}

// Invalidate drops any cached client for a Connection.
func (r *Resolver) Invalidate(namespace, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prefix := namespace + "/" + name + "|"
	for k := range r.cache {
		if strings.HasPrefix(k, prefix) {
			delete(r.cache, k)
		}
	}
}

// IsReady reports whether a Connection carries a Ready=True condition.
func IsReady(conn *uddiv1alpha1.Connection) bool {
	for _, c := range conn.Status.Conditions {
		if c.Type == uddiv1alpha1.ConditionReady {
			return c.Status == "True"
		}
	}
	return false
}

// ResyncInterval returns the effective drift-detection period for a Connection.
func ResyncInterval(conn *uddiv1alpha1.Connection) time.Duration {
	if conn == nil || conn.Spec.ResyncInterval == nil || conn.Spec.ResyncInterval.Duration <= 0 {
		return DefaultResyncInterval
	}
	return conn.Spec.ResyncInterval.Duration
}

func rateLimit(conn *uddiv1alpha1.Connection) (float64, int) {
	if conn.Spec.RateLimit == nil {
		return 0, 0
	}
	return float64(conn.Spec.RateLimit.RequestsPerSecond), int(conn.Spec.RateLimit.Burst)
}

// specHash fingerprints the client-relevant parts of a Connection spec. The Secret
// contents are deliberately not part of it: the Secret's resourceVersion covers those.
func specHash(conn *uddiv1alpha1.Connection) string {
	h := sha256.New()
	fmt.Fprintf(h, "url=%s\n", conn.CSPURL())
	fmt.Fprintf(h, "client=%s\n", conn.ClientName())
	fmt.Fprintf(h, "secret=%s/%s\n", conn.Spec.SecretRef.Name, conn.SecretKey())
	rps, burst := rateLimit(conn)
	fmt.Fprintf(h, "rate=%g/%d\n", rps, burst)
	keys := make([]string, 0, len(conn.Spec.DefaultTags))
	for k := range conn.Spec.DefaultTags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "tag=%s=%s\n", k, conn.Spec.DefaultTags[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
