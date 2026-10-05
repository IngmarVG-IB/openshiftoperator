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
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// sleepRecorder stands in for the real sleep: it records each wait the transport asks for
// and returns at once, so no test here waits.
type sleepRecorder struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (s *sleepRecorder) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waits = append(s.waits, d)
	return ctx.Err()
}

func (s *sleepRecorder) got() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.waits)
}

// noSleep skips every wait.
func noSleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// testNow is the instant HTTP-date Retry-After values are measured against.
var testNow = time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)

// newTestRetry returns a retryTransport over the default transport whose waits are
// recorded instead of slept, whose jitter always returns its ceiling (so the backoff
// sequence is exactly 500ms, 1s, 2s) and whose clock is fixed at testNow.
func newTestRetry(connection string) (*retryTransport, *sleepRecorder) {
	rec := &sleepRecorder{}
	rt := newRetryTransport(http.DefaultTransport, connection)
	rt.sleep = rec.sleep
	rt.jitter = func(ceiling time.Duration) time.Duration { return ceiling }
	rt.now = func() time.Time { return testNow }
	return rt, rec
}

// reply is one scripted answer of a scriptedServer.
type reply struct {
	status     int
	retryAfter string
}

// scriptedServer answers its n-th request with script[n], repeating the last entry once
// the script is used up, and records the method and body of every request.
type scriptedServer struct {
	*httptest.Server
	mu      sync.Mutex
	methods []string
	bodies  []string
}

func newScriptedServer(t *testing.T, script ...reply) *scriptedServer {
	t.Helper()
	s := &scriptedServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		n := len(s.methods)
		s.methods = append(s.methods, r.Method)
		s.bodies = append(s.bodies, string(body))
		s.mu.Unlock()

		rep := script[min(n, len(script)-1)]
		if rep.retryAfter != "" {
			w.Header().Set("Retry-After", rep.retryAfter)
		}
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, "reply "+http.StatusText(rep.status))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *scriptedServer) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.methods)
}

func (s *scriptedServer) seenBodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.bodies)
}

// viewPath is a path that classifies as domain "dns", resource "view".
const viewPath = "/api/ddi/v1/dns/view/00000000-0000-4000-8000-000000000001"

// send issues one request straight through rt and closes the response body. It returns
// the status code, or the error rt returned.
func send(t *testing.T, rt http.RoundTripper, method, target string, body []byte) (int, error) {
	t.Helper()
	resp, err := sendCtx(t, context.Background(), rt, method, target, body)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

func sendCtx(t *testing.T, ctx context.Context, rt http.RoundTripper, method, target string, body []byte) (*http.Response, error) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rdr)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return rt.RoundTrip(req)
}

func retries(connection, method, code string) float64 {
	return testutil.ToFloat64(apiRetriesTotal.WithLabelValues(connection, "dns", method, code))
}

func TestRetryHonoursRetryAfterSeconds(t *testing.T) {
	srv := newScriptedServer(t, reply{http.StatusTooManyRequests, "1"}, reply{http.StatusOK, ""})
	rt, rec := newTestRetry(t.Name())

	status, err := send(t, rt, http.MethodGet, srv.URL+viewPath, nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("send = (%d, %v), want 200", status, err)
	}
	if n := srv.requests(); n != 2 {
		t.Fatalf("server saw %d requests, want 2", n)
	}
	if got := rec.got(); !slices.Equal(got, []time.Duration{time.Second}) {
		t.Fatalf("waits = %v, want [1s], the Retry-After value", got)
	}
	if got := retries(t.Name(), "GET view", "429"); got != 1 {
		t.Fatalf("retries{GET view, 429} = %v, want 1", got)
	}
}

func TestRetryBacksOffExponentiallyAfterRepeated503(t *testing.T) {
	srv := newScriptedServer(t,
		reply{http.StatusServiceUnavailable, ""}, reply{http.StatusServiceUnavailable, ""}, reply{http.StatusOK, ""})
	rt, rec := newTestRetry(t.Name())

	status, err := send(t, rt, http.MethodPatch, srv.URL+viewPath, []byte(`{"comment":"x"}`))
	if err != nil || status != http.StatusOK {
		t.Fatalf("send = (%d, %v), want 200", status, err)
	}
	if n := srv.requests(); n != 3 {
		t.Fatalf("server saw %d requests, want 3", n)
	}
	want := []time.Duration{500 * time.Millisecond, time.Second}
	if got := rec.got(); !slices.Equal(got, want) {
		t.Fatalf("waits = %v, want %v", got, want)
	}
	if got := retries(t.Name(), "PATCH view", "503"); got != 2 {
		t.Fatalf("retries{PATCH view, 503} = %v, want 2", got)
	}
}

func TestRetryJitterStaysBelowTheCeiling(t *testing.T) {
	rt := newRetryTransport(http.DefaultTransport, t.Name())
	noHeader := &http.Response{Header: http.Header{}}

	for attempt := range retryMaxRetries {
		ceiling := retryBaseDelay << attempt
		distinct := map[time.Duration]bool{}
		for range 200 {
			d := rt.delay(noHeader, attempt)
			if d < 0 || d >= ceiling {
				t.Fatalf("attempt %d: delay %v outside [0, %v)", attempt, d, ceiling)
			}
			distinct[d] = true
		}
		if len(distinct) < 2 {
			t.Fatalf("attempt %d: 200 delays were all identical, so there is no jitter", attempt)
		}
	}
}

func TestRetryNeverRetriesPOST(t *testing.T) {
	srv := newScriptedServer(t, reply{http.StatusServiceUnavailable, ""}, reply{http.StatusOK, ""})
	rt, rec := newTestRetry(t.Name())

	status, err := send(t, rt, http.MethodPost, srv.URL+"/api/ddi/v1/dns/view", []byte(`{"name":"v"}`))
	if err != nil || status != http.StatusServiceUnavailable {
		t.Fatalf("send = (%d, %v), want the 503 back", status, err)
	}
	if n := srv.requests(); n != 1 {
		t.Fatalf("server saw %d requests, want 1: a POST must never be retried", n)
	}
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("waits = %v, want none", got)
	}
	if got := retries(t.Name(), "POST view", "503"); got != 0 {
		t.Fatalf("retries{POST view, 503} = %v, want 0", got)
	}
}

// stubTransport returns the scripted statuses in turn, then 200, and counts calls.
func stubTransport(statuses ...int) (http.RoundTripper, *int) {
	calls := new(int)
	return roundTripFunc(func(*http.Request) (*http.Response, error) {
		n := *calls
		*calls++
		status := http.StatusOK
		if n < len(statuses) {
			status = statuses[n]
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}), calls
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRetryOnlyRetriesGETPATCHAndDELETE(t *testing.T) {
	tests := []struct {
		method    string
		wantCalls int
	}{
		{http.MethodGet, 2},
		{http.MethodPatch, 2},
		{http.MethodDelete, 2},
		{"", 2}, // net/http treats an empty method as GET
		{http.MethodPost, 1},
		{http.MethodPut, 1},
		{http.MethodHead, 1},
		{http.MethodOptions, 1},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			base, calls := stubTransport(http.StatusServiceUnavailable)
			rt, _ := newTestRetry(t.Name())
			rt.base = base
			req := httptest.NewRequest(http.MethodGet, "http://example.invalid"+viewPath, nil)
			req.Method = tt.method
			req.RequestURI = ""
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			_ = resp.Body.Close()
			if *calls != tt.wantCalls {
				t.Fatalf("%q made %d attempts, want %d", tt.method, *calls, tt.wantCalls)
			}
		})
	}
}

func TestRetryOnlyRetriesThrottleAndAvailabilityStatuses(t *testing.T) {
	retried := []int{
		http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout,
	}
	notRetried := []int{
		http.StatusOK, http.StatusCreated, http.StatusBadRequest, http.StatusUnauthorized,
		http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		http.StatusInternalServerError, http.StatusNotImplemented,
	}
	run := func(t *testing.T, status, wantCalls int) {
		t.Helper()
		base, calls := stubTransport(status)
		rt, _ := newTestRetry(t.Name())
		rt.base = base
		req, _ := http.NewRequest(http.MethodGet, "http://example.invalid"+viewPath, nil)
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		_ = resp.Body.Close()
		if *calls != wantCalls {
			t.Fatalf("status %d: %d attempts, want %d", status, *calls, wantCalls)
		}
	}
	for _, status := range retried {
		t.Run(http.StatusText(status), func(t *testing.T) { run(t, status, 2) })
	}
	for _, status := range notRetried {
		t.Run(http.StatusText(status), func(t *testing.T) { run(t, status, 1) })
	}
}

func TestRetryGivesUpAfterThreeRetriesAndReturnsTheLastResponse(t *testing.T) {
	srv := newScriptedServer(t, reply{http.StatusServiceUnavailable, ""})
	rt, rec := newTestRetry(t.Name())

	resp, err := sendCtx(t, context.Background(), rt, http.MethodDelete, srv.URL+viewPath, nil)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the last 503", resp.StatusCode)
	}
	if body, _ := io.ReadAll(resp.Body); string(body) != "reply Service Unavailable" {
		t.Fatalf("body = %q, want the last response's body, unread and intact", body)
	}
	if n := srv.requests(); n != 1+retryMaxRetries {
		t.Fatalf("server saw %d requests, want %d (one attempt and %d retries)", n, 1+retryMaxRetries, retryMaxRetries)
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}
	if got := rec.got(); !slices.Equal(got, want) {
		t.Fatalf("waits = %v, want %v", got, want)
	}
	if got := retries(t.Name(), "DELETE view", "503"); got != retryMaxRetries {
		t.Fatalf("retries{DELETE view, 503} = %v, want %d", got, retryMaxRetries)
	}
}

func TestRetryStopsWhenTheWaitBudgetIsSpent(t *testing.T) {
	tests := []struct {
		name         string
		retryAfter   string
		wantRequests int
		wantWaits    []time.Duration
	}{
		{"a single Retry-After beyond the budget is not waited out", "31", 1, nil},
		{"the second wait would pass the budget", "20", 2, []time.Duration{20 * time.Second}},
		{"waits that add up to exactly the budget are allowed", "15", 3, []time.Duration{15 * time.Second, 15 * time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newScriptedServer(t, reply{http.StatusTooManyRequests, tt.retryAfter})
			rt, rec := newTestRetry(t.Name())

			status, err := send(t, rt, http.MethodGet, srv.URL+viewPath, nil)
			if err != nil || status != http.StatusTooManyRequests {
				t.Fatalf("send = (%d, %v), want the last 429 back", status, err)
			}
			if n := srv.requests(); n != tt.wantRequests {
				t.Fatalf("server saw %d requests, want %d", n, tt.wantRequests)
			}
			if got := rec.got(); !slices.Equal(got, tt.wantWaits) {
				t.Fatalf("waits = %v, want %v", got, tt.wantWaits)
			}
		})
	}
}

func TestRetryStopsImmediatelyWhenTheContextIsCancelledDuringBackoff(t *testing.T) {
	srv := newScriptedServer(t, reply{http.StatusServiceUnavailable, "10"})
	// The real sleep, so this proves the wait itself is interruptible.
	rt := newRetryTransport(http.DefaultTransport, t.Name())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	resp, err := sendCtx(t, ctx, rt, http.MethodGet, srv.URL+viewPath, nil)
	if resp != nil {
		_ = resp.Body.Close()
		t.Fatalf("got a response (%d), want the context error", resp.StatusCode)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %v: the cancelled context did not end the 10s Retry-After wait", elapsed)
	}
	if n := srv.requests(); n != 1 {
		t.Fatalf("server saw %d requests, want 1: the cancelled wait must not be followed by a retry", n)
	}
	if got := retries(t.Name(), "GET view", "503"); got != 0 {
		t.Fatalf("retries = %v, want 0 for a retry that never happened", got)
	}
}

func TestRetrySkipsAWaitThatOutlastsTheContextDeadline(t *testing.T) {
	srv := newScriptedServer(t, reply{http.StatusServiceUnavailable, "5"})
	rt, rec := newTestRetry(t.Name())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := sendCtx(t, ctx, rt, http.MethodGet, srv.URL+viewPath, nil)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the 503: a 5s wait cannot fit a 2s deadline", resp.StatusCode)
	}
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("waits = %v, want none", got)
	}
	if n := srv.requests(); n != 1 {
		t.Fatalf("server saw %d requests, want 1", n)
	}
}

func TestRetryResendsTheSameBodyOnEveryAttempt(t *testing.T) {
	const payload = `{"comment":"hello","tags":{"a":"b"}}`
	srv := newScriptedServer(t,
		reply{http.StatusServiceUnavailable, ""}, reply{http.StatusTooManyRequests, ""}, reply{http.StatusOK, ""})
	rt, _ := newTestRetry(t.Name())

	status, err := send(t, rt, http.MethodPatch, srv.URL+viewPath, []byte(payload))
	if err != nil || status != http.StatusOK {
		t.Fatalf("send = (%d, %v), want 200", status, err)
	}
	bodies := srv.seenBodies()
	if len(bodies) != 3 {
		t.Fatalf("server saw %d requests, want 3", len(bodies))
	}
	for i, b := range bodies {
		if b != payload {
			t.Fatalf("attempt %d body = %q, want %q", i+1, b, payload)
		}
	}
}

func TestRetryDoesNotRetryABodyThatCannotBeRewound(t *testing.T) {
	srv := newScriptedServer(t, reply{http.StatusServiceUnavailable, ""}, reply{http.StatusOK, ""})
	rt, rec := newTestRetry(t.Name())

	req, err := http.NewRequest(http.MethodPatch, srv.URL+viewPath, io.NopCloser(strings.NewReader(`{"comment":"x"}`)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if req.GetBody != nil {
		t.Fatal("test setup: the request unexpectedly has GetBody")
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the 503 returned as is", resp.StatusCode)
	}
	if n := srv.requests(); n != 1 {
		t.Fatalf("server saw %d requests, want 1", n)
	}
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("waits = %v, want none", got)
	}
}

func TestRetryReturnsAnErrorWhenTheBodyCannotBeRewoundOnRetry(t *testing.T) {
	base, calls := stubTransport(http.StatusServiceUnavailable)
	rt, _ := newTestRetry(t.Name())
	rt.base = base

	boom := errors.New("cannot rewind")
	req, _ := http.NewRequest(http.MethodPatch, "http://example.invalid"+viewPath, strings.NewReader("x"))
	req.GetBody = func() (io.ReadCloser, error) { return nil, boom }

	resp, err := rt.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the GetBody error", err)
	}
	if *calls != 1 {
		t.Fatalf("%d attempts, want 1", *calls)
	}
}

func TestRetryAfterAcceptsAnHTTPDate(t *testing.T) {
	when := testNow.Add(5 * time.Second).UTC().Format(http.TimeFormat)
	srv := newScriptedServer(t, reply{http.StatusServiceUnavailable, when}, reply{http.StatusOK, ""})
	rt, rec := newTestRetry(t.Name())

	status, err := send(t, rt, http.MethodGet, srv.URL+viewPath, nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("send = (%d, %v), want 200", status, err)
	}
	if got := rec.got(); !slices.Equal(got, []time.Duration{5 * time.Second}) {
		t.Fatalf("waits = %v, want [5s] for Retry-After %q", got, when)
	}
}

func TestParseRetryAfter(t *testing.T) {
	future := testNow.Add(90 * time.Second)
	tests := []struct {
		name   string
		value  string
		want   time.Duration
		wantOK bool
	}{
		{"empty", "", 0, false},
		{"seconds", "7", 7 * time.Second, true},
		{"zero seconds", "0", 0, true},
		{"seconds with whitespace", "  12 ", 12 * time.Second, true},
		{"negative seconds", "-3", 0, false},
		{"fractional seconds", "1.5", 0, false},
		{"words", "soon", 0, false},
		{"huge seconds are clamped, not overflowed", "99999999999999999999", retryAfterClamp, true},
		{"IMF-fixdate", future.Format(http.TimeFormat), 90 * time.Second, true},
		{"RFC 850 date", future.Format(time.RFC850), 90 * time.Second, true},
		{"asctime date", future.Format(time.ANSIC), 90 * time.Second, true},
		{"a date in the past is a zero wait", testNow.Add(-time.Hour).Format(http.TimeFormat), 0, true},
		{"a date far in the future is clamped", testNow.Add(1000 * time.Hour).Format(http.TimeFormat), retryAfterClamp, true},
		{"a malformed date", "Sun, 99 Foo 2026 25:61:61 GMT", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRetryAfter(tt.value, testNow)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("parseRetryAfter(%q) = (%v, %v), want (%v, %v)", tt.value, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestRetryFallsBackToBackoffWhenRetryAfterIsUnusable(t *testing.T) {
	srv := newScriptedServer(t, reply{http.StatusServiceUnavailable, "soon"}, reply{http.StatusOK, ""})
	rt, rec := newTestRetry(t.Name())

	if status, err := send(t, rt, http.MethodGet, srv.URL+viewPath, nil); err != nil || status != http.StatusOK {
		t.Fatalf("send = (%d, %v), want 200", status, err)
	}
	if got := rec.got(); !slices.Equal(got, []time.Duration{500 * time.Millisecond}) {
		t.Fatalf("waits = %v, want [500ms], the first backoff", got)
	}
}

// trackedBody records whether it was read to the end and closed.
type trackedBody struct {
	*strings.Reader
	sawEOF bool
	closed bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		b.sawEOF = true
	}
	return n, err
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func TestRetryDrainsAndClosesDiscardedResponses(t *testing.T) {
	first := &trackedBody{Reader: strings.NewReader("overloaded, try again")}
	calls := 0
	rt, _ := newTestRetry(t.Name())
	rt.base = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: first}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})

	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid"+viewPath, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if !first.closed || !first.sawEOF {
		t.Fatalf("discarded body: closed=%v drained=%v, want both true", first.closed, first.sawEOF)
	}
}

func TestRetryDoesNotRetryTransportErrors(t *testing.T) {
	boom := errors.New("connection refused")
	calls := 0
	rt, rec := newTestRetry(t.Name())
	rt.base = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, boom
	})

	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid"+viewPath, nil)
	if _, err := rt.RoundTrip(req); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the transport error", err)
	}
	if calls != 1 || len(rec.got()) != 0 {
		t.Fatalf("calls = %d, waits = %v, want one attempt and no wait", calls, rec.got())
	}
}

func TestSleepContext(t *testing.T) {
	if err := sleepContext(context.Background(), 5*time.Millisecond); err != nil {
		t.Fatalf("sleepContext = %v, want nil", err)
	}
	if err := sleepContext(context.Background(), 0); err != nil {
		t.Fatalf("sleepContext(0) = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepContext on a cancelled context = %v, want context.Canceled", err)
	}
	if err := sleepContext(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepContext(0) on a cancelled context = %v, want context.Canceled", err)
	}
}
