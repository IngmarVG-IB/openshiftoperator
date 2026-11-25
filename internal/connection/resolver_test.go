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

package connection

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

func TestValidateCSPURL(t *testing.T) {
	for _, tt := range []struct {
		url      string
		wantHost string // empty means the URL is rejected
	}{
		{"https://csp.infoblox.com", "csp.infoblox.com"},
		{"https://csp.infoblox.com/", "csp.infoblox.com"},
		{"https://csp.infoblox.com:443", "csp.infoblox.com"},
		{"https://csp.infoblox.com:8443/", "csp.infoblox.com"},
		{"https://CSP.Infoblox.COM", "csp.infoblox.com"},
		{"https://127.0.0.1:9443", "127.0.0.1"},
		{"http://csp.infoblox.com", ""},
		{"HTTPS://csp.infoblox.com", ""},
		{"csp.infoblox.com", ""},
		{"", ""},
		{"https://", ""},
		{"https:///path", ""},
		{"https://user:pass@evil.example.com", ""},
		{"https://csp.infoblox.com@evil.example.com", ""},
		{"https://evil.example.com#", ""},
		{"https://evil.example.com/#x", ""},
		{"https://evil.example.com?", ""},
		{"https://evil.example.com/?a=b", ""},
		{"https://csp.infoblox.com/api", ""},
		{"https://csp.infoblox.com//", ""},
		{"https://csp.infoblox.com/%2e%2e", ""},
		{"https://csp.infoblox.com:", ""},
		{"https://csp.infoblox.com:0", ""},
		{"https://csp.infoblox.com:65536", ""},
		{"https://csp.infoblox.com:https", ""},
		{"https://csp.infoblox.com\\@evil.example.com", ""},
		{"https://csp infoblox.com", ""},
		{"https://-csp.infoblox.com", ""},
		{"https://[::1]:443", ""},
	} {
		host, err := ValidateCSPURL(tt.url)
		if tt.wantHost == "" {
			if !errors.Is(err, ErrInvalidEndpoint) {
				t.Errorf("ValidateCSPURL(%q) = (%q, %v), want ErrInvalidEndpoint", tt.url, host, err)
			}
			continue
		}
		if err != nil || host != tt.wantHost {
			t.Errorf("ValidateCSPURL(%q) = (%q, %v), want (%q, nil)", tt.url, host, err, tt.wantHost)
		}
	}
}

func TestValidateCSPURLErrorNamesOnlyTheHost(t *testing.T) {
	_, err := ValidateCSPURL("https://evil.example.com/collect/secret-path")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "secret-path") || !strings.Contains(err.Error(), "evil.example.com") {
		t.Fatalf("error %q should name the host and nothing after it", err)
	}
	_, err = ValidateCSPURL("https://evil.example.com/?token=abc")
	if err == nil || strings.Contains(err.Error(), "token") {
		t.Fatalf("error %v should not echo the query", err)
	}
}

func TestParseAllowedHosts(t *testing.T) {
	got := ParseAllowedHosts(" CSP.infoblox.com, ,csp.eu.infoblox.com:443,csp.infoblox.com,")
	want := []string{"csp.infoblox.com", "csp.eu.infoblox.com"}
	if !slices.Equal(got, want) {
		t.Fatalf("ParseAllowedHosts = %v, want %v", got, want)
	}
	if got := ParseAllowedHosts(""); len(got) != 0 {
		t.Fatalf("ParseAllowedHosts(\"\") = %v, want empty", got)
	}
}

func TestCheckEndpointAllowlist(t *testing.T) {
	for _, tt := range []struct {
		name    string
		allowed []string
		url     string
		wantErr error
	}{
		{"empty list allows the default host", nil, "https://csp.infoblox.com", nil},
		{"empty list denies any other host", nil, "https://csp.eu.infoblox.com", ErrEndpointNotAllowed},
		{"listed host, port ignored", []string{"csp.eu.infoblox.com"}, "https://csp.eu.infoblox.com:443/", nil},
		{"case-insensitive", []string{"CSP.EU.infoblox.com"}, "https://csp.eu.INFOBLOX.com", nil},
		{"exact match, no suffix match", []string{"infoblox.com"}, "https://csp.infoblox.com", ErrEndpointNotAllowed},
		{"exact match, no prefix match", []string{"csp.infoblox.com"}, "https://csp.infoblox.com.evil.example", ErrEndpointNotAllowed},
		{"an explicit list replaces the default", []string{"csp.eu.infoblox.com"}, "https://csp.infoblox.com", ErrEndpointNotAllowed},
		{"invalid before allowlist", []string{"csp.infoblox.com"}, "http://csp.infoblox.com", ErrInvalidEndpoint},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &Resolver{AllowedHosts: tt.allowed}
			err := r.checkEndpoint(tt.url)
			if tt.wantErr == nil && err != nil || tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("checkEndpoint(%q) = %v, want %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

// TestClientForRefusesEndpointBeforeReadingTheSecret proves a bad or disallowed cspURL
// never gets as far as the Secret, so its value cannot be sent anywhere.
func TestClientForRefusesEndpointBeforeReadingTheSecret(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "ns"},
		Data:       map[string][]byte{uddiv1alpha1.DefaultSecretKey: []byte("super-secret")},
	}
	reads := 0
	reader := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(secret).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				reads++
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()

	var built []uddi.Config
	r := &Resolver{
		SecretReader: reader,
		Factory: func(cfg uddi.Config) (*uddi.Client, error) {
			built = append(built, cfg)
			return uddi.NewFake().Client(), nil
		},
	}
	conn := func(url string) *uddiv1alpha1.Connection {
		return &uddiv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "ns"},
			Spec: uddiv1alpha1.ConnectionSpec{
				CSPURL:    url,
				SecretRef: uddiv1alpha1.SecretKeyRef{Name: "creds"},
			},
		}
	}

	for url, want := range map[string]error{
		"https://evil.example.com":        ErrEndpointNotAllowed,
		"https://csp.infoblox.com/x#":     ErrInvalidEndpoint,
		"http://csp.infoblox.com":         ErrInvalidEndpoint,
		"https://u@csp.infoblox.com":      ErrInvalidEndpoint,
		"https://csp.infoblox.com.evil.x": ErrEndpointNotAllowed,
	} {
		if _, err := r.ClientFor(context.Background(), conn(url)); !errors.Is(err, want) {
			t.Errorf("ClientFor(%q) err = %v, want %v", url, err, want)
		}
	}
	if reads != 0 || len(built) != 0 {
		t.Fatalf("refused endpoints read the Secret %d times and built %d clients, want 0 and 0", reads, len(built))
	}

	// The default endpoint goes through the SecretReader and gets the key and the
	// default rate limit.
	if _, err := r.ClientFor(context.Background(), conn("")); err != nil {
		t.Fatalf("ClientFor(default) = %v", err)
	}
	if reads != 1 || len(built) != 1 {
		t.Fatalf("default endpoint: %d Secret reads, %d clients, want 1 and 1", reads, len(built))
	}
	cfg := built[0]
	if cfg.APIKey != "super-secret" || cfg.CSPURL != uddiv1alpha1.DefaultCSPURL {
		t.Fatalf("config = %q at %q, want the Secret value at the default URL", cfg.APIKey, cfg.CSPURL)
	}
	if cfg.RateLimitRPS != DefaultRateLimitRPS || cfg.RateLimitBurst != DefaultRateLimitBurst {
		t.Fatalf("rate limit = %v/%d, want %d/%d", cfg.RateLimitRPS, cfg.RateLimitBurst, DefaultRateLimitRPS, DefaultRateLimitBurst)
	}
}

func TestIsReadyRequiresTheCurrentGeneration(t *testing.T) {
	conn := func(gen, statusGen, condGen int64, status metav1.ConditionStatus) *uddiv1alpha1.Connection {
		return &uddiv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Generation: gen},
			Status: uddiv1alpha1.ConnectionStatus{
				ObservedGeneration: statusGen,
				Conditions: []metav1.Condition{{
					Type: uddiv1alpha1.ConditionReady, Status: status, ObservedGeneration: condGen,
				}},
			},
		}
	}
	for _, tt := range []struct {
		name string
		conn *uddiv1alpha1.Connection
		want bool
	}{
		{"verified at the current generation", conn(2, 2, 2, metav1.ConditionTrue), true},
		{"spec edited after verification", conn(3, 2, 2, metav1.ConditionTrue), false},
		{"condition stale", conn(3, 3, 2, metav1.ConditionTrue), false},
		{"status stale", conn(3, 2, 3, metav1.ConditionTrue), false},
		{"not verified", conn(2, 2, 2, metav1.ConditionFalse), false},
		{"no condition", &uddiv1alpha1.Connection{}, false},
	} {
		if got := IsReady(tt.conn); got != tt.want {
			t.Errorf("%s: IsReady = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestResyncIntervalClamp(t *testing.T) {
	for _, tt := range []struct {
		in   *metav1.Duration
		want time.Duration
	}{
		{nil, DefaultResyncInterval},
		{&metav1.Duration{}, DefaultResyncInterval},
		{&metav1.Duration{Duration: -time.Second}, DefaultResyncInterval},
		{&metav1.Duration{Duration: time.Nanosecond}, MinResyncInterval},
		{&metav1.Duration{Duration: 29 * time.Second}, MinResyncInterval},
		{&metav1.Duration{Duration: 30 * time.Second}, 30 * time.Second},
		{&metav1.Duration{Duration: time.Hour}, time.Hour},
	} {
		conn := &uddiv1alpha1.Connection{Spec: uddiv1alpha1.ConnectionSpec{ResyncInterval: tt.in}}
		if got := ResyncInterval(conn); got != tt.want {
			t.Errorf("ResyncInterval(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestRateLimitDefaults(t *testing.T) {
	for _, tt := range []struct {
		name      string
		in        *uddiv1alpha1.RateLimit
		rps       float64
		wantBurst int
	}{
		{"unset", nil, DefaultRateLimitRPS, DefaultRateLimitBurst},
		{"empty", &uddiv1alpha1.RateLimit{}, DefaultRateLimitRPS, DefaultRateLimitBurst},
		{"rps only", &uddiv1alpha1.RateLimit{RequestsPerSecond: 5}, 5, DefaultRateLimitBurst},
		{"both", &uddiv1alpha1.RateLimit{RequestsPerSecond: 50, Burst: 100}, 50, 100},
	} {
		conn := &uddiv1alpha1.Connection{Spec: uddiv1alpha1.ConnectionSpec{RateLimit: tt.in}}
		rps, burst := rateLimit(conn)
		if rps != tt.rps || burst != tt.wantBurst {
			t.Errorf("%s: rateLimit = %v/%d, want %v/%d", tt.name, rps, burst, tt.rps, tt.wantBurst)
		}
	}
}
