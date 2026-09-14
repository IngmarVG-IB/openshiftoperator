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
	"fmt"
	"net/http"
	"strings"
)

// maxBodyLen caps how much of an API error body is kept. Bodies end up in conditions
// and events, so they must stay short.
const maxBodyLen = 512

// APIError is the single error type every Universal DDI call funnels into. It never
// carries the API key: only the domain, the logical method, the HTTP status and a
// truncated response body.
type APIError struct {
	// Domain is the SDK package the call belongs to: "dnsconfig", "dnsdata", "ipam"
	// or "dhcp".
	Domain string
	// Method is the logical operation, for example "ListViews".
	Method string
	// StatusCode is the HTTP status, or 0 when the call never reached the server.
	StatusCode int
	// Body is the truncated response body.
	Body string
	// Err is the underlying transport or SDK error.
	Err error
}

// Error implements error.
func (e *APIError) Error() string {
	var b strings.Builder
	b.WriteString(e.Domain)
	b.WriteByte('.')
	b.WriteString(e.Method)
	if e.StatusCode != 0 {
		fmt.Fprintf(&b, ": http %d", e.StatusCode)
	}
	if e.Body != "" {
		b.WriteString(": ")
		b.WriteString(e.Body)
	} else if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap exposes the underlying error.
func (e *APIError) Unwrap() error { return e.Err }

// IsNotFound reports whether the object does not exist.
func (e *APIError) IsNotFound() bool { return e.StatusCode == http.StatusNotFound }

// IsConflict reports whether the request collided with existing state. Universal DDI
// is inconsistent about 409 versus 400 for duplicate creates, so both count.
func (e *APIError) IsConflict() bool {
	return e.StatusCode == http.StatusConflict || e.StatusCode == http.StatusBadRequest
}

// IsUnauthorized reports whether the API key was rejected.
func (e *APIError) IsUnauthorized() bool {
	return e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden
}

// IsNotFound reports whether err is an APIError with a 404 status.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.IsNotFound()
}

// IsConflict reports whether err is an APIError that collided with existing state.
func IsConflict(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.IsConflict()
}

// IsUnauthorized reports whether err is an APIError with a 401 or 403 status.
func IsUnauthorized(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.IsUnauthorized()
}

// NewAPIError builds an APIError with an explicit status and body. The fake uses it to
// produce errors indistinguishable from the real client's.
func NewAPIError(domain, method string, status int, body string) *APIError {
	return &APIError{Domain: domain, Method: method, StatusCode: status, Body: truncate(body)}
}

// wrap turns an SDK (result, *http.Response, error) triple into an APIError. It returns
// nil when err is nil.
func wrap(domain, method string, resp *http.Response, err error) error {
	if err == nil {
		return nil
	}
	out := &APIError{Domain: domain, Method: method, Err: err}
	if resp != nil {
		out.StatusCode = resp.StatusCode
	}
	// The SDK returns an unexported GenericOpenAPIError; the body is reachable through
	// a structural assertion.
	var be interface{ Body() []byte }
	if errors.As(err, &be) {
		out.Body = truncate(string(be.Body()))
	}
	if out.Body == "" {
		out.Body = truncate(err.Error())
	}
	return out
}

func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxBodyLen {
		return s
	}
	return s[:maxBodyLen] + "..."
}
