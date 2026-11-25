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

// MinResyncInterval is the shortest drift-detection period. The CRD rejects anything
// shorter; ResyncInterval raises a shorter value that got past it to this.
const MinResyncInterval = 30 * time.Second

// Client-side rate limit applied when spec.rateLimit, or one of its fields, is unset.
// They match the CRD field defaults.
const (
	DefaultRateLimitRPS   = 10
	DefaultRateLimitBurst = 20
)

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
	// Client reads Connection CRs.
	Client client.Client
	// SecretReader reads Secrets. Give it an uncached reader (mgr.GetAPIReader()) so the
	// manager never starts a Secret informer and holds every Secret in memory. Nil means
	// Client, which is fine for tests that pass a plain API client.
	SecretReader client.Reader
	// Factory builds Universal DDI clients. Nil means uddi.NewSDKClient.
	Factory ClientFactory
	// AllowedHosts lists the Cloud Services Portal host names a Connection may point at
	// (exact match, case-insensitive, port ignored). Empty means DefaultAllowedCSPHost
	// only.
	AllowedHosts []string

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
// in the cache key and never copied into an error. The endpoint is checked before the
// Secret is read (ErrInvalidEndpoint, ErrEndpointNotAllowed), so a Connection pointed at
// a host outside the allowlist never gets a key to send there.
func (r *Resolver) ClientFor(ctx context.Context, conn *uddiv1alpha1.Connection) (*uddi.Client, error) {
	if err := r.checkEndpoint(conn.CSPURL()); err != nil {
		return nil, err
	}
	secrets := r.SecretReader
	if secrets == nil {
		secrets = r.Client
	}
	secret := &corev1.Secret{}
	secretName := types.NamespacedName{Namespace: conn.Namespace, Name: conn.Spec.SecretRef.Name}
	if err := secrets.Get(ctx, secretName, secret); err != nil {
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
		// Every API metric carries this as its connection label.
		ConnectionLabel: conn.Namespace + "/" + conn.Name,
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

// IsReady reports whether a Connection carries a Ready=True condition computed from its
// current spec: both status.observedGeneration and the condition's observedGeneration
// must equal metadata.generation. After a spec edit (a new cspURL, say) the Connection is
// therefore not Ready until the Connection controller has verified the new spec, and no
// object reconciler sends requests to an unverified endpoint in the meantime.
func IsReady(conn *uddiv1alpha1.Connection) bool {
	if conn.Status.ObservedGeneration != conn.Generation {
		return false
	}
	for _, c := range conn.Status.Conditions {
		if c.Type == uddiv1alpha1.ConditionReady {
			return c.Status == "True" && c.ObservedGeneration == conn.Generation
		}
	}
	return false
}

// ResyncInterval returns the effective drift-detection period for a Connection, never
// shorter than MinResyncInterval.
func ResyncInterval(conn *uddiv1alpha1.Connection) time.Duration {
	if conn == nil || conn.Spec.ResyncInterval == nil || conn.Spec.ResyncInterval.Duration <= 0 {
		return DefaultResyncInterval
	}
	return max(conn.Spec.ResyncInterval.Duration, MinResyncInterval)
}

// rateLimit returns the effective client-side limit. An unset spec.rateLimit, or an
// unset field in it, gets the CRD default rather than disabling the limiter.
func rateLimit(conn *uddiv1alpha1.Connection) (float64, int) {
	rps, burst := int32(DefaultRateLimitRPS), int32(DefaultRateLimitBurst)
	if rl := conn.Spec.RateLimit; rl != nil {
		if rl.RequestsPerSecond > 0 {
			rps = rl.RequestsPerSecond
		}
		if rl.Burst > 0 {
			burst = rl.Burst
		}
	}
	return float64(rps), int(burst)
}

// specHash fingerprints the client-relevant parts of a Connection spec. The Secret
// contents are deliberately not part of it: the Secret's resourceVersion covers those.
func specHash(conn *uddiv1alpha1.Connection) string {
	rps, burst := rateLimit(conn)
	lines := make([]string, 0, 4+len(conn.Spec.DefaultTags))
	lines = append(lines,
		fmt.Sprintf("url=%s", conn.CSPURL()),
		fmt.Sprintf("client=%s", conn.ClientName()),
		fmt.Sprintf("secret=%s/%s", conn.Spec.SecretRef.Name, conn.SecretKey()),
		fmt.Sprintf("rate=%g/%d", rps, burst),
	)
	keys := make([]string, 0, len(conn.Spec.DefaultTags))
	for k := range conn.Spec.DefaultTags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("tag=%s=%s", k, conn.Spec.DefaultTags[k]))
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\n"))
	return hex.EncodeToString(sum[:])[:16]
}
