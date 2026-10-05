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
	"net/http"
	"time"

	"golang.org/x/time/rate"
)

// rateLimitedTransport applies a client-side token bucket to every Universal DDI
// request, and records how long each one waited for its token in apiRateLimitWait.
// universal-ddi-go-client v0.4.0 ships no limiter and no retry of its own (unlike the
// deprecated bloxone-go-client), so the limiter lives here and the retry loop lives in
// retry.go. Because retryTransport sits outside this transport, a retried request waits
// for a fresh token like any other.
type rateLimitedTransport struct {
	base       http.RoundTripper
	limiter    *rate.Limiter
	connection string
}

// RoundTrip implements http.RoundTripper.
func (t *rateLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.limiter != nil {
		// The wait is observed whether or not it succeeds: a request that gave up in the
		// queue still spent that time there, and those are the requests that matter when
		// the limiter saturates.
		start := time.Now()
		err := t.limiter.Wait(req.Context())
		apiRateLimitWait.WithLabelValues(t.connection).Observe(time.Since(start).Seconds())
		if err != nil {
			return nil, err
		}
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// newHTTPClient returns an http.Client for the Universal DDI API. Outermost first, a
// request passes through:
//
//	retryTransport        retries 429, 502, 503 and 504 for GET, PATCH and DELETE
//	rateLimitedTransport  waits for a token; omitted when rps is not positive
//	metricsTransport      records the request counter and the duration histogram
//	base transport        the network
//
// The order matters. Retrying outside the limiter makes every attempt, retries
// included, wait for a token, so retries cannot push the request rate above the
// configured limit and a throttled tenant is not hammered. Recording inside the limiter
// makes every attempt, not just the logical call, show up in
// uddi_operator_api_requests_total under its own status code, so 429s and 503s that a
// retry later hid from the caller are still counted and the UDDIAPIThrottled and
// UDDIAPIServerErrors alerts see them. The duration histogram sits closest to the
// network, so it measures the API round trip only: neither the wait for a token nor the
// backoff between attempts is counted. The wait for a token has its own histogram,
// uddi_operator_api_ratelimit_wait_seconds, and each retry is counted in
// uddi_operator_api_retries_total.
//
// base.Timeout, when set, bounds the whole call: the waits for tokens, every attempt
// and the backoff between them.
func newHTTPClient(base *http.Client, rps float64, burst int, connLabel string) *http.Client {
	out := &http.Client{}
	if base != nil {
		*out = *base
	}
	var rt http.RoundTripper = &metricsTransport{base: out.Transport, connection: connLabel}
	if rps > 0 {
		if burst <= 0 {
			burst = 1
		}
		rt = &rateLimitedTransport{
			base:       rt,
			limiter:    rate.NewLimiter(rate.Limit(rps), burst),
			connection: connLabel,
		}
	}
	out.Transport = newRetryTransport(rt, connLabel)
	return out
}
