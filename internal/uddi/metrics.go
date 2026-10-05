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
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Metric names, per DESIGN.md section 9. The object and policy gauges from the same
// section come from the collector in internal/metrics, not from here.
const (
	metricAPIRequestsTotal   = "uddi_operator_api_requests_total"
	metricAPIRequestDuration = "uddi_operator_api_request_duration_seconds"
	metricAPIRetriesTotal    = "uddi_operator_api_retries_total"
	metricAPIRateLimitWait   = "uddi_operator_api_ratelimit_wait_seconds"
)

// Label names shared by the API metrics.
const (
	labelConnection = "connection"
	labelDomain     = "domain"
	labelMethod     = "method"
	labelCode       = "code"
)

var (
	// apiRequestsTotal counts every Universal DDI API request, labeled by connection,
	// domain, method and result code. Every label is drawn from a small, fixed set (see
	// classifyRequest and metricsTransport.RoundTrip): never an object id, address, name,
	// tag value or the API key, so cardinality stays bounded regardless of how many
	// objects the operator manages.
	apiRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: metricAPIRequestsTotal,
		Help: "Total Universal DDI API requests, labeled by connection, domain, method and result code.",
	}, []string{labelConnection, labelDomain, labelMethod, labelCode})

	// apiRequestDuration observes the latency of the underlying HTTP round trip to
	// Universal DDI. It sits inside the client-side rate limiter in the transport chain
	// (see newHTTPClient), so the observed duration never counts time spent waiting for a
	// rate-limit token or backing off between retries.
	apiRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    metricAPIRequestDuration,
		Help:    "Universal DDI API request duration in seconds, labeled by connection, domain and method.",
		Buckets: prometheus.DefBuckets,
	}, []string{labelConnection, labelDomain, labelMethod})

	// apiRetriesTotal counts the retries retryTransport performed, with the status code
	// of the response that caused each one. The labels are derived exactly as for
	// apiRequestsTotal (classifyRequest), so a retry series lines up with the request
	// series it belongs to. Every attempt, the first included, is also counted in
	// apiRequestsTotal; this counter says how many of them were retries.
	apiRetriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: metricAPIRetriesTotal,
		Help: "Total Universal DDI API request retries, labeled by connection, domain, method and the status code that caused the retry.",
	}, []string{labelConnection, labelDomain, labelMethod, labelCode})

	// apiRateLimitWait observes how long each request waited for a token from the
	// client-side rate limiter, zero waits included, so the bucket counts add up to the
	// number of rate-limited requests and a rising tail shows the limiter saturating.
	apiRateLimitWait = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    metricAPIRateLimitWait,
		Help:    "Time Universal DDI API requests spent waiting on the client-side rate limiter, in seconds, labeled by connection.",
		Buckets: rateLimitWaitBuckets,
	}, []string{labelConnection})
)

// rateLimitWaitBuckets span an uncontended limiter (a few microseconds, below the first
// bucket) to a queue that approaches the 30 second request timeout.
var rateLimitWaitBuckets = []float64{0.001, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

// init registers the API metrics on controller-runtime's default metrics registry, the
// same one the manager serves (cmd/main.go wires up metrics/server against it). This
// mirrors how controller-runtime's own reconcile metrics register themselves.
func init() {
	metrics.Registry.MustRegister(apiRequestsTotal, apiRequestDuration, apiRetriesTotal, apiRateLimitWait)
}

// metricsTransport is an http.RoundTripper that records apiRequestsTotal and
// apiRequestDuration for every request it forwards to base. connection is the label
// value supplied by Config.ConnectionLabel; it is the only per-instance state this
// transport carries, so it never has anything object-specific to leak into a label.
type metricsTransport struct {
	base       http.RoundTripper
	connection string
}

// RoundTrip implements http.RoundTripper.
func (t *metricsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	domain, method := classifyRequest(req)

	start := time.Now()
	resp, err := base.RoundTrip(req)
	apiRequestDuration.WithLabelValues(t.connection, domain, method).Observe(time.Since(start).Seconds())

	code := "error"
	if err == nil && resp != nil {
		code = strconv.Itoa(resp.StatusCode)
	}
	apiRequestsTotal.WithLabelValues(t.connection, domain, method, code).Inc()

	return resp, err
}

// classifyRequest derives the domain and method metric labels from a request's HTTP verb
// and path alone, never from a header, query parameter or body: those are where ids,
// addresses, names, tags and the API key live.
//
// Every Universal DDI REST path the SDK generates has the shape
// "/api/ddi/v1/<domain>/<resource>[/<id>][/<suffix>]", for example
// "/api/ddi/v1/dns/auth_zone/<uuid>" (GetAuthZone/UpdateAuthZone/DeleteAuthZone) or
// "/api/ddi/v1/ipam/subnet/<uuid>/nextavailableip" (the dedicated next-available
// endpoint, which the operator does not call but which still classifies cleanly). domain
// is "dns", "ipam" or "dhcp"; method is "<HTTP verb> <resource>", for example
// "PATCH auth_zone" or "POST address". Any other shape -- a different host, a future API
// family, a CSP auth or session endpoint -- classifies as domain "other" with an
// "unknown" resource, which keeps both labels bounded no matter what turns up on the
// wire.
func classifyRequest(req *http.Request) (domain, method string) {
	verb := req.Method
	if verb == "" {
		verb = http.MethodGet
	}

	const other = "other"
	const unknown = "unknown"
	const prefix = "/api/ddi/v1/"

	path := req.URL.Path
	if !strings.HasPrefix(path, prefix) {
		return other, verb + " " + unknown
	}
	segments := strings.Split(strings.TrimPrefix(path, prefix), "/")
	switch segments[0] {
	case "dns", "ipam", "dhcp":
		domain = segments[0]
	default:
		return other, verb + " " + unknown
	}

	resource := unknown
	if len(segments) > 1 && segments[1] != "" {
		resource = segments[1]
	}
	return domain, verb + " " + resource
}
