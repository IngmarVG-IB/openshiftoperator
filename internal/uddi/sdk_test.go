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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestSDKClientRetriesSafeVerbsOnly drives the real SDK client against a scripted server:
// reads, updates and deletes that are throttled or refused are retried, a create is not,
// and the body of a retried update reaches the server intact every time.
func TestSDKClientRetriesSafeVerbsOnly(t *testing.T) {
	const viewID = "00000000-0000-4000-8000-0000000000aa"
	view := `{"result":{"id":"dns/view/` + viewID + `","name":"corp","comment":"hello"}}`

	var (
		mu       sync.Mutex
		counts   = map[string]int{}
		patches  []string
		throttle = map[string]int{ // how many leading attempts per verb fail, and with what
			http.MethodGet:    http.StatusServiceUnavailable,
			http.MethodPatch:  http.StatusTooManyRequests,
			http.MethodDelete: http.StatusGatewayTimeout,
		}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		counts[r.Method]++
		n := counts[r.Method]
		if r.Method == http.MethodPatch {
			patches = append(patches, string(body))
		}
		mu.Unlock()

		switch {
		case r.Method == http.MethodPost:
			http.Error(w, "unavailable", http.StatusServiceUnavailable) // every time
		case n == 1:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "try later", throttle[r.Method])
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, view)
		}
	}))
	defer srv.Close()

	c := newTestSDK(t, srv.URL)
	conn := "test-" + t.Name()
	ctx := context.Background()

	got, err := c.DNSConfig.GetView(ctx, "dns/view/"+viewID)
	if err != nil || got.Name != "corp" {
		t.Fatalf("GetView = (%v, %v), want the view after one retry", got, err)
	}

	comment := "hello"
	if _, err := c.DNSConfig.UpdateView(ctx, "dns/view/"+viewID, dnsconfig.View{Comment: &comment}); err != nil {
		t.Fatalf("UpdateView: %v", err)
	}

	if err := c.DNSConfig.DeleteView(ctx, "dns/view/"+viewID); err != nil {
		t.Fatalf("DeleteView: %v", err)
	}

	_, err = c.DNSConfig.CreateView(ctx, dnsconfig.View{Name: "corp"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("CreateView err = %v, want an APIError with status 503", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for verb, want := range map[string]int{http.MethodGet: 2, http.MethodPatch: 2, http.MethodDelete: 2, http.MethodPost: 1} {
		if counts[verb] != want {
			t.Errorf("%s reached the server %d times, want %d", verb, counts[verb], want)
		}
	}
	if len(patches) != 2 || patches[0] == "" || patches[0] != patches[1] || !strings.Contains(patches[0], `"comment"`) {
		t.Errorf("PATCH bodies = %q, want the same non-empty update body twice", patches)
	}

	// Each attempt is recorded under its own status code, and only the retries are
	// counted as retries.
	for _, tt := range []struct {
		method, code string
		requests     float64
		retries      float64
	}{
		{"GET view", "503", 1, 1},
		{"GET view", "200", 1, 0},
		{"PATCH view", "429", 1, 1},
		{"PATCH view", "200", 1, 0},
		{"DELETE view", "504", 1, 1},
		{"DELETE view", "204", 1, 0},
		{"POST view", "503", 1, 0},
	} {
		if got := testutil.ToFloat64(apiRequestsTotal.WithLabelValues(conn, "dns", tt.method, tt.code)); got != tt.requests {
			t.Errorf("requests{%s, %s} = %v, want %v", tt.method, tt.code, got, tt.requests)
		}
		if got := testutil.ToFloat64(apiRetriesTotal.WithLabelValues(conn, "dns", tt.method, tt.code)); got != tt.retries {
			t.Errorf("retries{%s, %s} = %v, want %v", tt.method, tt.code, got, tt.retries)
		}
	}
}

// TestSDKClientRetriesAThrottledListPage shows a 429 in the middle of a paged list is
// absorbed: the page is asked for again and the caller sees the complete list.
func TestSDKClientRetriesAThrottledListPage(t *testing.T) {
	f := NewFake()
	seedKind(t, f, viewKind(), 1500)
	srv := newListServer(t, f)
	srv.failRequest = func(n int) int {
		if n == 2 { // the first attempt at the second page
			return http.StatusTooManyRequests
		}
		return 0
	}
	c := newTestSDK(t, srv.URL)

	got, err := c.DNSConfig.ListViews(context.Background(), ListOptions{})
	if err != nil || len(got) != 1500 {
		t.Fatalf("ListViews = (%d views, %v), want all 1500", len(got), err)
	}
	if got, want := param(srv.requests(), "_offset"), []string{"", "1000", "1000"}; !slices.Equal(got, want) {
		t.Fatalf("_offset per request = %v, want %v: the throttled page is asked for again", got, want)
	}
}

// TestNewSDKClientRetriesThroughTheProductionConstructor checks the wiring in
// NewSDKClient itself: a throttled read succeeds without the caller seeing the 429. The
// first backoff is at most 500ms.
func TestNewSDKClientRetriesThroughTheProductionConstructor(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"name": "corp"}}})
	}))
	defer srv.Close()

	c, err := NewSDKClient(Config{APIKey: "test-key", CSPURL: srv.URL, ConnectionLabel: t.Name()})
	if err != nil {
		t.Fatalf("NewSDKClient: %v", err)
	}
	views, err := c.DNSConfig.ListViews(context.Background(), ListOptions{Limit: 1})
	if err != nil || len(views) != 1 {
		t.Fatalf("ListViews = (%d views, %v), want 1 view after a retry", len(views), err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("server saw %d requests, want 2", calls)
	}
	if got := testutil.ToFloat64(apiRetriesTotal.WithLabelValues(t.Name(), "dns", "GET view", "429")); got != 1 {
		t.Fatalf("retries{GET view, 429} = %v, want 1", got)
	}
}
