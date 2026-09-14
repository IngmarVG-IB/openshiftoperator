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

	"golang.org/x/time/rate"
)

// rateLimitedTransport applies a client-side token bucket to every Universal DDI
// request. universal-ddi-go-client v0.4.0 ships no limiter and no retry of its own
// (unlike the deprecated bloxone-go-client), so the limiter lives here and there is
// nothing to disable: a failed call is retried by the controller's requeue, always
// preceded by a natural-key lookup, which keeps creates idempotent.
type rateLimitedTransport struct {
	base    http.RoundTripper
	limiter *rate.Limiter
}

// RoundTrip implements http.RoundTripper.
func (t *rateLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.limiter != nil {
		if err := t.limiter.Wait(req.Context()); err != nil {
			return nil, err
		}
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// newRateLimitedClient returns an http.Client that admits at most rps requests per
// second with the given burst. A non-positive rps disables limiting.
func newRateLimitedClient(base *http.Client, rps float64, burst int) *http.Client {
	out := &http.Client{}
	if base != nil {
		*out = *base
	}
	if rps <= 0 {
		return out
	}
	if burst <= 0 {
		burst = 1
	}
	out.Transport = &rateLimitedTransport{
		base:    out.Transport,
		limiter: rate.NewLimiter(rate.Limit(rps), burst),
	}
	return out
}
