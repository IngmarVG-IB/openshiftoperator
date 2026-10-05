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
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"golang.org/x/time/rate"
)

// rateLimitWait returns how many waits apiRateLimitWait has observed for the connection,
// and their total in seconds.
func rateLimitWait(t *testing.T, connection string) (count uint64, sum float64) {
	t.Helper()
	hist, ok := apiRateLimitWait.WithLabelValues(connection).(prometheus.Histogram)
	if !ok {
		t.Fatalf("apiRateLimitWait.WithLabelValues(%s) is not a prometheus.Histogram", connection)
	}
	m := &dto.Metric{}
	if err := hist.Write(m); err != nil {
		t.Fatalf("writing histogram metric: %v", err)
	}
	return m.GetHistogram().GetSampleCount(), m.GetHistogram().GetSampleSum()
}

// countingTransport answers 200 and counts the requests that reach it.
type countingTransport struct{ calls int }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls++
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
}

func TestRateLimitedTransportObservesEveryWaitIncludingZeroWaits(t *testing.T) {
	conn := t.Name()
	base := &countingTransport{}
	// 20 requests per second with a burst of one: the first request takes the token at
	// once, the second has to wait about 50ms for the next one.
	rt := &rateLimitedTransport{base: base, limiter: rate.NewLimiter(rate.Limit(20), 1), connection: conn}
	get := func() {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/api/ddi/v1/dns/view", nil)
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		_ = resp.Body.Close()
	}

	get()
	count, sum := rateLimitWait(t, conn)
	if count != 1 || sum >= 0.04 {
		t.Fatalf("after an uncontended request: %d observations totalling %vs, want 1 observation of about zero", count, sum)
	}

	get()
	count, sum = rateLimitWait(t, conn)
	if count != 2 {
		t.Fatalf("after a second request: %d observations, want 2", count)
	}
	if sum < 0.03 || sum > 5 {
		t.Fatalf("observed waits total %vs, want about 0.05s (the second request's wait)", sum)
	}
	if base.calls != 2 {
		t.Fatalf("%d requests reached the base transport, want 2", base.calls)
	}
}

func TestRateLimitedTransportObservesAWaitThatFails(t *testing.T) {
	conn := t.Name()
	base := &countingTransport{}
	// One token per hour: the first request spends the burst, so the second cannot be
	// admitted within its deadline and fails inside the limiter.
	rt := &rateLimitedTransport{base: base, limiter: rate.NewLimiter(rate.Every(time.Hour), 1), connection: conn}

	first, _ := http.NewRequest(http.MethodGet, "http://example.invalid/api/ddi/v1/dns/view", nil)
	resp, err := rt.RoundTrip(first)
	if err != nil {
		t.Fatalf("first RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	second, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid/api/ddi/v1/dns/view", nil)
	if resp, err := rt.RoundTrip(second); err == nil {
		_ = resp.Body.Close()
		t.Fatal("second RoundTrip succeeded, want a rate-limiter error")
	}

	if count, _ := rateLimitWait(t, conn); count != 2 {
		t.Fatalf("%d observations, want 2: a request that fails in the limiter is observed too", count)
	}
	if base.calls != 1 {
		t.Fatalf("%d requests reached the base transport, want 1", base.calls)
	}
}

func TestNewHTTPClientTransportChain(t *testing.T) {
	conn := t.Name()
	srv := newScriptedServer(t, reply{http.StatusServiceUnavailable, ""}, reply{http.StatusOK, ""})
	client := newHTTPClient(&http.Client{}, 1000, 10, conn)

	// Outermost first: retry, rate limiter, metrics.
	retry, ok := client.Transport.(*retryTransport)
	if !ok {
		t.Fatalf("outermost transport is %T, want *retryTransport", client.Transport)
	}
	limited, ok := retry.base.(*rateLimitedTransport)
	if !ok {
		t.Fatalf("retry wraps %T, want *rateLimitedTransport", retry.base)
	}
	if _, ok := limited.base.(*metricsTransport); !ok {
		t.Fatalf("rate limiter wraps %T, want *metricsTransport", limited.base)
	}
	retry.sleep = noSleep

	resp, err := client.Get(srv.URL + "/api/ddi/v1/dns/view")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after one retry", resp.StatusCode)
	}

	// Both attempts were admitted by the limiter and recorded by the metrics transport,
	// each under its own status; one of them was a retry.
	if count, _ := rateLimitWait(t, conn); count != 2 {
		t.Errorf("rate limiter observed %d requests, want 2: the retry must pass the limiter too", count)
	}
	for code, want := range map[string]float64{"503": 1, "200": 1} {
		if got := testutil.ToFloat64(apiRequestsTotal.WithLabelValues(conn, "dns", "GET view", code)); got != want {
			t.Errorf("requests{GET view, %s} = %v, want %v", code, got, want)
		}
	}
	if n := observationCount(t, conn, "dns", "GET view"); n != 2 {
		t.Errorf("request duration observed %d round trips, want 2", n)
	}
	if got := retries(conn, "GET view", "503"); got != 1 {
		t.Errorf("retries{GET view, 503} = %v, want 1", got)
	}
}

func TestNewHTTPClientWithoutALimiterRecordsNoWait(t *testing.T) {
	conn := t.Name()
	client := newHTTPClient(&http.Client{}, 0, 0, conn)
	retry, ok := client.Transport.(*retryTransport)
	if !ok {
		t.Fatalf("outermost transport is %T, want *retryTransport", client.Transport)
	}
	if _, ok := retry.base.(*metricsTransport); !ok {
		t.Fatalf("retry wraps %T, want *metricsTransport directly when the limiter is off", retry.base)
	}

	srv := newScriptedServer(t, reply{http.StatusOK, ""})
	resp, err := client.Get(srv.URL + "/api/ddi/v1/dns/view")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if count, _ := rateLimitWait(t, conn); count != 0 {
		t.Fatalf("%d waits observed with no limiter, want 0", count)
	}
}
