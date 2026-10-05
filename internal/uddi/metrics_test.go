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
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

func TestClassifyRequest(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantDomain string
		wantMethod string
	}{
		{"create view", http.MethodPost, "/api/ddi/v1/dns/view", "dns", "POST view"},
		{"patch auth zone by id", http.MethodPatch, "/api/ddi/v1/dns/auth_zone/00000000-0000-4000-8000-000000000001", "dns", "PATCH auth_zone"},
		{"create record", http.MethodPost, "/api/ddi/v1/dns/record", "dns", "POST record"},
		{"create address", http.MethodPost, "/api/ddi/v1/ipam/address", "ipam", "POST address"},
		{"delete address block", http.MethodDelete, "/api/ddi/v1/ipam/address_block/00000000-0000-4000-8000-000000000002", "ipam", "DELETE address_block"},
		{"list subnets", http.MethodGet, "/api/ddi/v1/ipam/subnet", "ipam", "GET subnet"},
		{"dedicated next-available endpoint", http.MethodPost, "/api/ddi/v1/ipam/subnet/00000000-0000-4000-8000-000000000003/nextavailableip", "ipam", "POST subnet"},
		{"create fixed address", http.MethodPost, "/api/ddi/v1/dhcp/fixed_address", "dhcp", "POST fixed_address"},
		{"unrecognised host path", http.MethodGet, "/v2/session", "other", "GET unknown"},
		{"bare domain with no resource", http.MethodGet, "/api/ddi/v1/ipam", "ipam", "GET unknown"},
		{"empty method defaults to GET", "", "/api/ddi/v1/dns/view", "dns", "GET view"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.invalid"+tt.path, nil)
			req.Method = tt.method
			domain, method := classifyRequest(req)
			if domain != tt.wantDomain || method != tt.wantMethod {
				t.Fatalf("classifyRequest(%s %s) = (%q, %q), want (%q, %q)",
					tt.method, tt.path, domain, method, tt.wantDomain, tt.wantMethod)
			}
		})
	}
}

// errTransport is an http.RoundTripper that always fails, standing in for a network or
// transport-level failure (as opposed to an HTTP error status).
type errTransport struct{ err error }

func (t errTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }

func TestMetricsTransportRecordsSuccessAndFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ddi/v1/ipam/address_block" {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	const connLabel = "test-metrics-success"
	client := &http.Client{Transport: &metricsTransport{base: http.DefaultTransport, connection: connLabel}}

	// A successful create: code "201", domain "ipam", method "POST address".
	resp, err := client.Post(srv.URL+"/api/ddi/v1/ipam/address", "application/json", nil)
	if err != nil {
		t.Fatalf("POST address: %v", err)
	}
	_ = resp.Body.Close()

	if got := testutil.ToFloat64(apiRequestsTotal.WithLabelValues(connLabel, "ipam", "POST address", "201")); got != 1 {
		t.Fatalf("apiRequestsTotal{code=201} = %v, want 1", got)
	}
	if n := observationCount(t, connLabel, "ipam", "POST address"); n != 1 {
		t.Fatalf("apiRequestDuration sample count = %d, want 1", n)
	}

	// A conflicting create: code "409", same domain/method series, a second observation.
	resp, err = client.Post(srv.URL+"/api/ddi/v1/ipam/address_block", "application/json", nil)
	if err != nil {
		t.Fatalf("POST address_block: %v", err)
	}
	_ = resp.Body.Close()

	if got := testutil.ToFloat64(apiRequestsTotal.WithLabelValues(connLabel, "ipam", "POST address_block", "409")); got != 1 {
		t.Fatalf("apiRequestsTotal{code=409} = %v, want 1", got)
	}

	// A transport failure never reaches the server: code "error".
	failing := &http.Client{Transport: &metricsTransport{
		base:       errTransport{err: errors.New("boom")},
		connection: connLabel,
	}}
	if _, err := failing.Get("http://example.invalid/api/ddi/v1/dns/view"); err == nil {
		t.Fatal("expected the failing transport to return an error")
	}
	if got := testutil.ToFloat64(apiRequestsTotal.WithLabelValues(connLabel, "dns", "GET view", "error")); got != 1 {
		t.Fatalf("apiRequestsTotal{code=error} = %v, want 1", got)
	}
	if n := observationCount(t, connLabel, "dns", "GET view"); n != 1 {
		t.Fatalf("apiRequestDuration sample count = %d, want 1", n)
	}
}

func TestMetricsTransportDefaultsToDefaultTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// base is nil: RoundTrip must fall back to http.DefaultTransport rather than panic.
	rt := &metricsTransport{connection: "test-metrics-default-transport"}
	req := httptest.NewRequest(http.MethodGet, srv.URL+"/api/ddi/v1/dns/view", nil)
	req.RequestURI = ""
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// TestRetryMetricCountsEachRetryByCause records retries directly on the counter through
// the transport and checks the series it produces, with the labels derived the same way as
// the request metrics.
func TestRetryMetricCountsEachRetryByCause(t *testing.T) {
	const connLabel = "test-retry-metric"
	srv := newScriptedServer(t,
		reply{http.StatusTooManyRequests, ""}, reply{http.StatusBadGateway, ""}, reply{http.StatusOK, ""})
	rt, _ := newTestRetry(connLabel)

	if status, err := send(t, rt, http.MethodPatch, srv.URL+"/api/ddi/v1/ipam/subnet/00000000-0000-4000-8000-000000000003", []byte(`{}`)); err != nil || status != http.StatusOK {
		t.Fatalf("send = (%d, %v), want 200", status, err)
	}

	// One retry was caused by a 429 and one by a 502; the final 200 is not a retry.
	for code, want := range map[string]float64{"429": 1, "502": 1, "200": 0} {
		if got := testutil.ToFloat64(apiRetriesTotal.WithLabelValues(connLabel, "ipam", "PATCH subnet", code)); got != want {
			t.Errorf("retries{ipam, PATCH subnet, %s} = %v, want %v", code, got, want)
		}
	}
}

// TestNewMetricsAreRegisteredOnTheControllerRuntimeRegistry pins the names and label sets
// of the retry counter and the rate-limit wait histogram, which DESIGN.md section 9, the
// alerts and the dashboards depend on, and checks they pass the Prometheus naming lint.
func TestNewMetricsAreRegisteredOnTheControllerRuntimeRegistry(t *testing.T) {
	const connLabel = "test-registry"
	apiRetriesTotal.WithLabelValues(connLabel, "ipam", "PATCH subnet", "429").Inc()
	apiRateLimitWait.WithLabelValues(connLabel).Observe(0)

	families, err := ctrlmetrics.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	want := map[string][]string{
		"uddi_operator_api_retries_total":          {"code", "connection", "domain", "method"},
		"uddi_operator_api_ratelimit_wait_seconds": {"connection"},
	}
	for name, labels := range want {
		var found *dto.Metric
		for _, fam := range families {
			if fam.GetName() != name {
				continue
			}
			for _, m := range fam.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "connection" && lp.GetValue() == connLabel {
						found = m
					}
				}
			}
		}
		if found == nil {
			t.Errorf("%s with connection=%q is not on controller-runtime's registry", name, connLabel)
			continue
		}
		got := make([]string, 0, len(found.GetLabel()))
		for _, lp := range found.GetLabel() {
			got = append(got, lp.GetName())
		}
		slices.Sort(got)
		if !slices.Equal(got, labels) {
			t.Errorf("%s labels = %v, want %v", name, got, labels)
		}
	}

	if problems, err := testutil.CollectAndLint(apiRetriesTotal, "uddi_operator_api_retries_total"); err != nil || len(problems) != 0 {
		t.Errorf("lint of the retry counter: %v %v", problems, err)
	}
	if problems, err := testutil.CollectAndLint(apiRateLimitWait, "uddi_operator_api_ratelimit_wait_seconds"); err != nil || len(problems) != 0 {
		t.Errorf("lint of the rate-limit wait histogram: %v %v", problems, err)
	}
}

// observationCount returns the number of observations recorded so far for the given
// label combination on apiRequestDuration.
func observationCount(t *testing.T, connection, domain, method string) uint64 {
	t.Helper()
	hist, ok := apiRequestDuration.WithLabelValues(connection, domain, method).(prometheus.Histogram)
	if !ok {
		t.Fatalf("apiRequestDuration.WithLabelValues(%s, %s, %s) is not a prometheus.Histogram", connection, domain, method)
	}
	m := &dto.Metric{}
	if err := hist.Write(m); err != nil {
		t.Fatalf("writing histogram metric: %v", err)
	}
	return m.GetHistogram().GetSampleCount()
}
