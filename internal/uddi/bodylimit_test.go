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

package uddi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBodyLimitTransport(t *testing.T) {
	for _, tt := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"under the limit", "short", false},
		{"exactly the limit", strings.Repeat("x", 10), false},
		{"one byte over", strings.Repeat("x", 11), true},
		{"far over", strings.Repeat("x", 4096), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			c := &http.Client{Transport: &bodyLimitTransport{limit: 10}}
			resp, err := c.Get(srv.URL)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			got, err := io.ReadAll(resp.Body)
			if tt.wantErr {
				if !errors.Is(err, ErrResponseTooLarge) {
					t.Fatalf("ReadAll err = %v, want ErrResponseTooLarge", err)
				}
				if len(got) > 10 {
					t.Fatalf("read %d bytes past a 10-byte limit", len(got))
				}
				return
			}
			if err != nil || string(got) != tt.body {
				t.Fatalf("ReadAll = (%q, %v), want (%q, nil)", got, err, tt.body)
			}
		})
	}
}

// TestNewHTTPClientCapsResponseBodies checks the cap sits under metricsTransport in the
// production chain.
func TestNewHTTPClientCapsResponseBodies(t *testing.T) {
	c := newHTTPClient(&http.Client{}, 0, 0, "test-"+t.Name())
	retry, ok := c.Transport.(*retryTransport)
	if !ok {
		t.Fatalf("outermost transport is %T, want *retryTransport", c.Transport)
	}
	m, ok := retry.base.(*metricsTransport)
	if !ok {
		t.Fatalf("retry wraps %T, want *metricsTransport", retry.base)
	}
	bl, ok := m.base.(*bodyLimitTransport)
	if !ok || bl.limit != maxResponseBytes {
		t.Fatalf("metricsTransport wraps %T (%+v), want *bodyLimitTransport with the 32 MiB limit", m.base, m.base)
	}
}

// TestSDKClientFailsOnAnOversizedResponse drives the real SDK client at an endpoint that
// answers with more than maxResponseBytes: the call fails with a clear error instead of
// buffering the whole body.
func TestSDKClientFailsOnAnOversizedResponse(t *testing.T) {
	chunk := strings.Repeat(" ", 1<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for range maxResponseBytes>>20 + 2 {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := newTestSDK(t, srv.URL)
	_, err := c.DNSConfig.ListViews(context.Background(), ListOptions{Limit: 1})
	if err == nil || !strings.Contains(err.Error(), ErrResponseTooLarge.Error()) {
		t.Fatalf("ListViews err = %v, want it to report %q", err, ErrResponseTooLarge)
	}
}
