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
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// DefaultAllowedCSPHost is the only Cloud Services Portal host a Resolver with an empty
// AllowedHosts list accepts.
const DefaultAllowedCSPHost = "csp.infoblox.com"

var (
	// ErrInvalidEndpoint means spec.cspURL is not https://host[:port] with at most a
	// trailing slash. The wrapped message names at most the offending host.
	ErrInvalidEndpoint = errors.New("connection: invalid cspURL")
	// ErrEndpointNotAllowed means the spec.cspURL host is not on the operator's allowlist.
	ErrEndpointNotAllowed = errors.New("connection: cspURL host is not allowed")
)

// hostnamePattern is the host part of the CRD's spec.cspURL pattern.
var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

// maxHostInError caps how much of a host name goes into an error, and so into a
// condition message.
const maxHostInError = 253

// ValidateCSPURL applies the CRD's spec.cspURL rules: https, a host name, an optional
// port and nothing else but an optional single trailing slash. It returns the host in
// lower case. The API key is sent as a bearer token to this URL and the SDK builds
// request URLs by appending to it, so userinfo, a path, a query or a fragment would
// redirect or neutralise the request path.
func ValidateCSPURL(raw string) (string, error) {
	invalid := func(why, host string) error {
		if host == "" {
			return fmt.Errorf("%w: %s", ErrInvalidEndpoint, why)
		}
		if len(host) > maxHostInError {
			host = host[:maxHostInError]
		}
		return fmt.Errorf("%w: %s (host %q)", ErrInvalidEndpoint, why, host)
	}
	if !strings.HasPrefix(raw, "https://") {
		return "", invalid("must start with https://", "")
	}
	// url.Parse silently accepts an empty query ("?") or fragment ("#"), and a backslash
	// or whitespace is never part of a host name.
	if strings.ContainsAny(raw, "?#@\\ \t\r\n") {
		return "", invalid("must not contain userinfo, a query or a fragment", "")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", invalid("not a URL", "")
	}
	host := u.Hostname()
	switch {
	case u.Scheme != "https" || u.Opaque != "":
		return "", invalid("must start with https://", "")
	case u.User != nil:
		return "", invalid("must not contain userinfo", host)
	case host == "" || !hostnamePattern.MatchString(host):
		return "", invalid("must name a host", host)
	case u.Path != "" && u.Path != "/", u.RawPath != "":
		return "", invalid("must not contain a path", host)
	}
	if p := u.Port(); p != "" || strings.HasSuffix(u.Host, ":") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", invalid("port must be 1-65535", host)
		}
	}
	return strings.ToLower(host), nil
}

// ParseAllowedHosts turns a comma-separated --allowed-csp-hosts value into a host list:
// trimmed, lower case, without ports, deduplicated, empty entries dropped.
func ParseAllowedHosts(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		h := strings.ToLower(strings.TrimSpace(part))
		if hp, _, err := net.SplitHostPort(h); err == nil {
			h = hp
		}
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// checkEndpoint validates a Connection's cspURL and checks its host against the
// Resolver's allowlist. An empty allowlist means DefaultAllowedCSPHost only, never
// "anything".
func (r *Resolver) checkEndpoint(raw string) error {
	host, err := ValidateCSPURL(raw)
	if err != nil {
		return err
	}
	allowed := r.AllowedHosts
	if len(allowed) == 0 {
		allowed = []string{DefaultAllowedCSPHost}
	}
	for _, a := range allowed {
		if strings.EqualFold(a, host) {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not in --allowed-csp-hosts", ErrEndpointNotAllowed, host)
}
