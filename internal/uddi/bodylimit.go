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
	"errors"
	"io"
	"net/http"
)

// maxResponseBytes caps every Universal DDI response body. The SDK reads bodies with an
// unbounded io.ReadAll, so without a cap a broken or hostile endpoint could exhaust the
// operator's memory. A full list page of 1000 objects is far below this.
const maxResponseBytes = 32 << 20

// ErrResponseTooLarge is returned, by a read of the response body, when the body is
// longer than maxResponseBytes. The SDK surfaces it as the call's error, so the call
// fails instead of decoding a silently truncated body.
var ErrResponseTooLarge = errors.New("uddi: response body exceeds the 32 MiB limit")

// bodyLimitTransport caps response bodies at limit bytes.
type bodyLimitTransport struct {
	base  http.RoundTripper
	limit int64
}

// RoundTrip implements http.RoundTripper.
func (t *bodyLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &limitedBody{rc: resp.Body, remaining: t.limit}
	return resp, nil
}

// limitedBody returns ErrResponseTooLarge once more than its limit has been read.
type limitedBody struct {
	rc        io.ReadCloser
	remaining int64
	tooLarge  bool
}

// Read implements io.Reader.
func (b *limitedBody) Read(p []byte) (int, error) {
	if b.tooLarge {
		return 0, ErrResponseTooLarge
	}
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining <= 0 {
		// The limit is reached: one more byte means the body is too large, EOF means it
		// was exactly the limit.
		var one [1]byte
		n, err := b.rc.Read(one[:])
		if n > 0 {
			b.tooLarge = true
			return 0, ErrResponseTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.rc.Read(p)
	b.remaining -= int64(n)
	return n, err
}

// Close implements io.Closer.
func (b *limitedBody) Close() error { return b.rc.Close() }
