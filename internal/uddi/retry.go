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
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Retry policy. universal-ddi-go-client v0.4.0 has no retry of its own, so the operator
// carries one (DESIGN.md section 8).
const (
	// retryMaxRetries is the number of retries after the first attempt, so a request is
	// sent at most retryMaxRetries+1 times.
	retryMaxRetries = 3
	// retryBaseDelay is the ceiling of the first backoff. The ceiling doubles per retry
	// and the delay is drawn uniformly from [0, ceiling) (full jitter).
	retryBaseDelay = 500 * time.Millisecond
	// retryMaxWait caps the total time one request spends waiting between attempts. A
	// Retry-After that does not fit in what is left ends the retrying instead of being
	// shortened: coming back before the server asked would only draw another 429.
	retryMaxWait = 30 * time.Second
	// retryDrainLimit bounds how much of a discarded response body is read so the
	// connection can be reused.
	retryDrainLimit = 64 << 10
	// retryAfterClamp is what an absurdly large Retry-After value is reduced to, so the
	// arithmetic on it cannot overflow. It is far beyond retryMaxWait either way.
	retryAfterClamp = 24 * time.Hour
)

// retryTransport re-sends requests that Universal DDI answered with 429, 502, 503 or 504.
//
// Only GET, PATCH and DELETE are retried. POST is never retried because the API has no
// idempotency key: a create whose response was lost, or which was refused after it took
// effect, would be sent twice. A failed create is instead retried by the controller's
// requeue, which always starts with a natural-key lookup. Transport errors (a refused
// connection, a timeout) are not retried here for the same reason; only well-formed
// HTTP answers are.
//
// A wait honours Retry-After, given in seconds or as an HTTP date, and otherwise backs off
// exponentially with full jitter. At most retryMaxRetries retries happen within
// retryMaxWait of total waiting; the last response is returned when either runs out. The
// wait is also skipped when it would outlast the request context's deadline, so a caller
// gets the real 429 or 503 rather than a bare "context deadline exceeded". A cancelled
// context ends the wait at once and its error is returned.
//
// The request body is rewound with req.GetBody before every retry. A request whose body
// cannot be rewound is sent once and never retried. Discarded response bodies are drained
// (up to retryDrainLimit) and closed.
//
// Build one with newRetryTransport; sleep, jitter and now are replaced by tests.
type retryTransport struct {
	base       http.RoundTripper
	connection string

	maxRetries int
	baseDelay  time.Duration
	maxWait    time.Duration

	// sleep waits for d or until ctx is done, in which case it returns ctx.Err().
	sleep func(ctx context.Context, d time.Duration) error
	// jitter returns a delay in [0, ceiling).
	jitter func(ceiling time.Duration) time.Duration
	// now is the clock Retry-After dates are measured against.
	now func() time.Time
}

// newRetryTransport returns a retryTransport with the production policy. connection is
// the "connection" label stamped on uddi_operator_api_retries_total.
func newRetryTransport(base http.RoundTripper, connection string) *retryTransport {
	return &retryTransport{
		base:       base,
		connection: connection,
		maxRetries: retryMaxRetries,
		baseDelay:  retryBaseDelay,
		maxWait:    retryMaxWait,
		sleep:      sleepContext,
		jitter:     fullJitter,
		now:        time.Now,
	}
}

// RoundTrip implements http.RoundTripper.
func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if !retryableMethod(req.Method) || !rewindable(req) {
		return base.RoundTrip(req)
	}

	ctx := req.Context()
	cur := req
	var waited time.Duration
	for attempt := 0; ; attempt++ {
		resp, err := base.RoundTrip(cur)
		if err != nil || !retryableStatus(resp.StatusCode) || attempt >= t.maxRetries {
			return resp, err
		}

		delay := t.delay(resp, attempt)
		if waited+delay > t.maxWait || !fitsDeadline(ctx, delay) {
			return resp, nil
		}

		code := strconv.Itoa(resp.StatusCode)
		drainAndClose(resp.Body)
		if sleepErr := t.sleep(ctx, delay); sleepErr != nil {
			return nil, sleepErr
		}
		waited += delay

		if cur, err = rewind(req); err != nil {
			return nil, err
		}
		domain, method := classifyRequest(req)
		apiRetriesTotal.WithLabelValues(t.connection, domain, method, code).Inc()
	}
}

// delay is how long to wait before retry number attempt+1: the server's Retry-After when
// it sent a usable one, otherwise full-jitter exponential backoff.
func (t *retryTransport) delay(resp *http.Response, attempt int) time.Duration {
	if d, ok := parseRetryAfter(resp.Header.Get("Retry-After"), t.now()); ok {
		return d
	}
	return t.jitter(t.baseDelay << attempt)
}

// retryableMethod reports whether a request with this verb may be sent again. An empty
// method is a GET, as in net/http.
func retryableMethod(method string) bool {
	switch method {
	case "", http.MethodGet, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// retryableStatus reports whether an HTTP status is worth another attempt: rate limited,
// or a gateway or availability failure.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// rewindable reports whether req can be sent a second time: it has no body, or a body
// that req.GetBody can produce again.
func rewindable(req *http.Request) bool {
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

// rewind returns a copy of req with a fresh body. It never modifies req, as the
// http.RoundTripper contract requires.
func rewind(req *http.Request) (*http.Request, error) {
	next := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		next.Body = body
	}
	return next, nil
}

// fitsDeadline reports whether waiting d still leaves time before ctx's deadline.
func fitsDeadline(ctx context.Context, d time.Duration) bool {
	deadline, ok := ctx.Deadline()
	return !ok || time.Until(deadline) > d
}

// drainAndClose reads what is left of a body, within a limit, and closes it, so the
// underlying connection goes back to the pool.
func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, retryDrainLimit))
	_ = body.Close()
}

// parseRetryAfter reads a Retry-After header, which is either a non-negative number of
// seconds or an HTTP date. A date in the past is a delay of zero. It reports false for an
// absent or malformed value.
func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if isDigits(value) {
		secs, err := strconv.ParseInt(value, 10, 64)
		if err != nil || secs > int64(retryAfterClamp/time.Second) {
			return retryAfterClamp, true
		}
		return time.Duration(secs) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	d := when.Sub(now)
	if d < 0 {
		d = 0
	}
	if d > retryAfterClamp {
		d = retryAfterClamp
	}
	return d, true
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// fullJitter returns a uniformly random delay in [0, ceiling).
func fullJitter(ceiling time.Duration) time.Duration {
	if ceiling <= 0 {
		return 0
	}
	return rand.N(ceiling)
}

// sleepContext waits for d, or until ctx is done, in which case it returns ctx.Err().
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
