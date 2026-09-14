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

package publish

import (
	"reflect"
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func int32p(n int32) *int32 { return &n }

func assertResult(t *testing.T, gotEps []Endpoint, gotSkips []Skip, wantEps []Endpoint, wantSkips []Skip) {
	t.Helper()
	if !reflect.DeepEqual(gotEps, wantEps) {
		t.Errorf("endpoints = %#v, want %#v", gotEps, wantEps)
	}
	if !reflect.DeepEqual(gotSkips, wantSkips) {
		t.Errorf("skips = %#v, want %#v", gotSkips, wantSkips)
	}
}

// -----------------------------------------------------------------------------------
// ParseTargets
// -----------------------------------------------------------------------------------

func TestParseTargets(t *testing.T) {
	cases := []struct {
		name    string
		csv     string
		want    []Target
		wantErr bool
	}{
		{name: "empty string", csv: "", want: nil},
		{name: "only commas", csv: " , ,,", want: nil},
		{name: "ipv4", csv: "192.0.2.10", want: []Target{{Type: TargetA, Value: "192.0.2.10"}}},
		{name: "ipv6", csv: "2001:db8::10", want: []Target{{Type: TargetAAAA, Value: "2001:db8::10"}}},
		{name: "hostname lower-cased with dot added", csv: "router.apps.example.com", want: []Target{{Type: TargetCNAME, Value: "router.apps.example.com."}}},
		{name: "hostname upper-case and trailing dot normalized", csv: "ROUTER.APPS.EXAMPLE.COM.", want: []Target{{Type: TargetCNAME, Value: "router.apps.example.com."}}},
		{
			name: "mixed list with blanks ignored",
			csv:  "10.0.0.1, ,2001:db8::1,, host.example.com",
			want: []Target{
				{Type: TargetA, Value: "10.0.0.1"},
				{Type: TargetAAAA, Value: "2001:db8::1"},
				{Type: TargetCNAME, Value: "host.example.com."},
			},
		},
		{name: "invalid name is an error", csv: "not_a_valid_host!", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTargets(tc.csv)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseTargets(%q): want error, got nil", tc.csv)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTargets(%q): unexpected error: %v", tc.csv, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseTargets(%q) = %#v, want %#v", tc.csv, got, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------------
// EndpointsFromRoute
// -----------------------------------------------------------------------------------

func admittedIngress(canonicalHostname string) routev1.RouteIngress {
	return routev1.RouteIngress{
		RouterCanonicalHostname: canonicalHostname,
		Conditions: []routev1.RouteIngressCondition{
			{Type: routev1.RouteAdmitted, Status: corev1.ConditionTrue},
		},
	}
}

func notAdmittedIngress() routev1.RouteIngress {
	return routev1.RouteIngress{
		Conditions: []routev1.RouteIngressCondition{
			{Type: routev1.RouteAdmitted, Status: corev1.ConditionFalse},
		},
	}
}

func TestEndpointsFromRoute(t *testing.T) {
	routeSrc := SourceRef{Kind: SourceRoute, Namespace: "team-a", Name: "web", UID: "web-uid"}

	cases := []struct {
		name        string
		host        string
		subdomain   string
		annotations map[string]string
		ingress     []routev1.RouteIngress
		opts        SourceOptions
		wantEps     []Endpoint
		wantSkips   []Skip
	}{
		{
			name:      "no host and no subdomain is unsupported",
			host:      "",
			subdomain: "",
			wantSkips: []Skip{{Source: routeSrc, Reason: SkipUnsupported, Message: "Route has neither spec.host nor spec.subdomain set"}},
		},
		{
			name:      "subdomain without host is unsupported",
			host:      "",
			subdomain: "frontend",
			wantSkips: []Skip{{Source: routeSrc, Reason: SkipUnsupported, Message: "Route uses spec.subdomain without spec.host, which is not supported"}},
		},
		{
			name:    "no ingress entries is not admitted",
			host:    "app.example.com",
			ingress: nil,
			wantSkips: []Skip{{
				Source: routeSrc, Hostname: "app.example.com.", Reason: SkipNotAdmitted,
				Message: "no router has admitted this route",
			}},
		},
		{
			name:    "admitted condition false is not admitted",
			host:    "app.example.com",
			ingress: []routev1.RouteIngress{notAdmittedIngress()},
			wantSkips: []Skip{{
				Source: routeSrc, Hostname: "app.example.com.", Reason: SkipNotAdmitted,
				Message: "no router has admitted this route",
			}},
		},
		{
			name:    "admitted with no canonical hostname, no override, no static addresses has no target",
			host:    "app.example.com",
			ingress: []routev1.RouteIngress{admittedIngress("")},
			wantSkips: []Skip{{
				Source: routeSrc, Hostname: "app.example.com.", Reason: SkipNoTarget,
				Message: "no target could be derived for the configured mode",
			}},
		},
		{
			name:    "admitted auto target is CNAME to router canonical hostname",
			host:    "app.example.com",
			ingress: []routev1.RouteIngress{admittedIngress("router-default.apps.example.com")},
			wantEps: []Endpoint{{
				Source: routeSrc, Hostname: "app.example.com.",
				Targets: []Target{{Type: TargetCNAME, Value: "router-default.apps.example.com."}},
			}},
		},
		{
			name:    "canonical hostname override replaces the router's canonical hostname",
			host:    "app.example.com",
			ingress: []routev1.RouteIngress{admittedIngress("router-default.apps.example.com")},
			opts:    SourceOptions{CanonicalHostname: "override.example.com"},
			wantEps: []Endpoint{{
				Source: routeSrc, Hostname: "app.example.com.",
				Targets: []Target{{Type: TargetCNAME, Value: "override.example.com."}},
			}},
		},
		{
			name:    "static addresses replace the auto CNAME with A/AAAA targets",
			host:    "app.example.com",
			ingress: []routev1.RouteIngress{admittedIngress("router-default.apps.example.com")},
			opts:    SourceOptions{StaticAddresses: []string{"192.0.2.5", "2001:db8::5"}},
			wantEps: []Endpoint{{
				Source: routeSrc, Hostname: "app.example.com.",
				Targets: []Target{
					{Type: TargetA, Value: "192.0.2.5"},
					{Type: TargetAAAA, Value: "2001:db8::5"},
				},
			}},
		},
		{
			name:    "mode CNAME filters out static addresses leaving no target",
			host:    "app.example.com",
			ingress: []routev1.RouteIngress{admittedIngress("router-default.apps.example.com")},
			opts:    SourceOptions{StaticAddresses: []string{"192.0.2.5"}, Mode: TargetModeCNAME},
			wantSkips: []Skip{{
				Source: routeSrc, Hostname: "app.example.com.", Reason: SkipNoTarget,
				Message: "no target could be derived for the configured mode",
			}},
		},
		{
			name:    "mode A filters out the auto CNAME leaving no target",
			host:    "app.example.com",
			ingress: []routev1.RouteIngress{admittedIngress("router-default.apps.example.com")},
			opts:    SourceOptions{Mode: TargetModeA},
			wantSkips: []Skip{{
				Source: routeSrc, Hostname: "app.example.com.", Reason: SkipNoTarget,
				Message: "no target could be derived for the configured mode",
			}},
		},
		{
			name:        "publish false excludes the whole source",
			host:        "app.example.com",
			ingress:     []routev1.RouteIngress{admittedIngress("router-default.apps.example.com")},
			annotations: map[string]string{"uddi.infoblox.com/publish": "false"},
			wantSkips:   []Skip{{Source: routeSrc, Reason: SkipExcluded, Message: "uddi.infoblox.com/publish is false"}},
		},
		{
			name:        "malformed publish annotation is invalid",
			host:        "app.example.com",
			annotations: map[string]string{"uddi.infoblox.com/publish": "maybe"},
			wantSkips: []Skip{{
				Source: routeSrc, Reason: SkipInvalidAnnotation,
				Message: `uddi.infoblox.com/publish: strconv.ParseBool: parsing "maybe": invalid syntax`,
			}},
		},
		{
			name:        "malformed ttl annotation is invalid",
			host:        "app.example.com",
			ingress:     []routev1.RouteIngress{admittedIngress("router-default.apps.example.com")},
			annotations: map[string]string{"uddi.infoblox.com/ttl": "-5"},
			wantSkips: []Skip{{
				Source: routeSrc, Reason: SkipInvalidAnnotation,
				Message: `uddi.infoblox.com/ttl: "-5" is not a positive integer`,
			}},
		},
		{
			name:        "malformed hostname annotation is invalid",
			host:        "app.example.com",
			annotations: map[string]string{"uddi.infoblox.com/hostname": "bad_host!"},
			wantSkips: []Skip{{
				Source: routeSrc, Reason: SkipInvalidAnnotation,
				Message: `uddi.infoblox.com/hostname: "bad_host!" is not a valid DNS name`,
			}},
		},
		{
			name:        "malformed target annotation is invalid",
			host:        "app.example.com",
			annotations: map[string]string{"uddi.infoblox.com/target": "bad_target!"},
			wantSkips: []Skip{{
				Source: routeSrc, Reason: SkipInvalidAnnotation,
				Message: `uddi.infoblox.com/target: "bad_target!" is not an IP address or a valid DNS name`,
			}},
		},
		{
			name:        "hostname annotation adds an extra hostname sharing the same target",
			host:        "app.example.com",
			ingress:     []routev1.RouteIngress{admittedIngress("router-default.apps.example.com")},
			annotations: map[string]string{"uddi.infoblox.com/hostname": "extra.example.com"},
			wantEps: []Endpoint{
				{Source: routeSrc, Hostname: "app.example.com.", Targets: []Target{{Type: TargetCNAME, Value: "router-default.apps.example.com."}}},
				{Source: routeSrc, Hostname: "extra.example.com.", Targets: []Target{{Type: TargetCNAME, Value: "router-default.apps.example.com."}}},
			},
		},
		{
			name:        "target annotation replaces the computed target",
			host:        "app.example.com",
			ingress:     []routev1.RouteIngress{admittedIngress("router-default.apps.example.com")},
			annotations: map[string]string{"uddi.infoblox.com/target": "203.0.113.9"},
			wantEps: []Endpoint{{
				Source: routeSrc, Hostname: "app.example.com.",
				Targets: []Target{{Type: TargetA, Value: "203.0.113.9"}},
			}},
		},
		{
			name:        "ttl annotation sets the endpoint TTL",
			host:        "app.example.com",
			ingress:     []routev1.RouteIngress{admittedIngress("router-default.apps.example.com")},
			annotations: map[string]string{"uddi.infoblox.com/ttl": "120"},
			wantEps: []Endpoint{{
				Source: routeSrc, Hostname: "app.example.com.",
				Targets: []Target{{Type: TargetCNAME, Value: "router-default.apps.example.com."}},
				TTL:     int32p(120),
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &routev1.Route{
				ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "web", UID: "web-uid", Annotations: tc.annotations},
				Spec:       routev1.RouteSpec{Host: tc.host, Subdomain: tc.subdomain},
				Status:     routev1.RouteStatus{Ingress: tc.ingress},
			}
			gotEps, gotSkips := EndpointsFromRoute(r, tc.opts)
			assertResult(t, gotEps, gotSkips, tc.wantEps, tc.wantSkips)
		})
	}
}

// -----------------------------------------------------------------------------------
// EndpointsFromIngress
// -----------------------------------------------------------------------------------

func TestEndpointsFromIngress(t *testing.T) {
	ingSrc := SourceRef{Kind: SourceIngress, Namespace: "team-a", Name: "web", UID: "ing-uid"}

	cases := []struct {
		name        string
		rules       []networkingv1.IngressRule
		lb          []networkingv1.IngressLoadBalancerIngress
		annotations map[string]string
		opts        SourceOptions
		wantEps     []Endpoint
		wantSkips   []Skip
	}{
		{
			name: "no rules and no annotation yields nothing",
		},
		{
			name: "empty host rules are skipped",
			rules: []networkingv1.IngressRule{
				{Host: ""},
				{Host: "app.example.com"},
			},
			lb: []networkingv1.IngressLoadBalancerIngress{{Hostname: "lb.example.com"}},
			wantEps: []Endpoint{{
				Source: ingSrc, Hostname: "app.example.com.",
				Targets: []Target{{Type: TargetCNAME, Value: "lb.example.com."}},
			}},
		},
		{
			name:  "load balancer IPs give one A/AAAA target per IP on a single endpoint",
			rules: []networkingv1.IngressRule{{Host: "app.example.com"}},
			lb: []networkingv1.IngressLoadBalancerIngress{
				{IP: "192.0.2.1"},
				{IP: "2001:db8::1"},
			},
			wantEps: []Endpoint{{
				Source: ingSrc, Hostname: "app.example.com.",
				Targets: []Target{
					{Type: TargetA, Value: "192.0.2.1"},
					{Type: TargetAAAA, Value: "2001:db8::1"},
				},
			}},
		},
		{
			name:  "load balancer hostname with no IPs gives one CNAME",
			rules: []networkingv1.IngressRule{{Host: "app.example.com"}},
			lb:    []networkingv1.IngressLoadBalancerIngress{{Hostname: "lb.example.com"}},
			wantEps: []Endpoint{{
				Source: ingSrc, Hostname: "app.example.com.",
				Targets: []Target{{Type: TargetCNAME, Value: "lb.example.com."}},
			}},
		},
		{
			name:  "empty load balancer status has no target",
			rules: []networkingv1.IngressRule{{Host: "app.example.com"}},
			wantSkips: []Skip{{
				Source: ingSrc, Hostname: "app.example.com.", Reason: SkipNoTarget,
				Message: "no target could be derived for the configured mode",
			}},
		},
		{
			name:  "mode CNAME filters out IP targets leaving no target",
			rules: []networkingv1.IngressRule{{Host: "app.example.com"}},
			lb:    []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.1"}},
			opts:  SourceOptions{Mode: TargetModeCNAME},
			wantSkips: []Skip{{
				Source: ingSrc, Hostname: "app.example.com.", Reason: SkipNoTarget,
				Message: "no target could be derived for the configured mode",
			}},
		},
		{
			name:        "hostname annotation adds an extra hostname",
			rules:       []networkingv1.IngressRule{{Host: "app.example.com"}},
			lb:          []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.1"}},
			annotations: map[string]string{"uddi.infoblox.com/hostname": "extra.example.com"},
			wantEps: []Endpoint{
				{Source: ingSrc, Hostname: "app.example.com.", Targets: []Target{{Type: TargetA, Value: "192.0.2.1"}}},
				{Source: ingSrc, Hostname: "extra.example.com.", Targets: []Target{{Type: TargetA, Value: "192.0.2.1"}}},
			},
		},
		{
			name:        "target annotation replaces the computed target",
			rules:       []networkingv1.IngressRule{{Host: "app.example.com"}},
			lb:          []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.1"}},
			annotations: map[string]string{"uddi.infoblox.com/target": "cname.example.com"},
			wantEps: []Endpoint{{
				Source: ingSrc, Hostname: "app.example.com.",
				Targets: []Target{{Type: TargetCNAME, Value: "cname.example.com."}},
			}},
		},
		{
			name:        "publish false excludes the whole source",
			rules:       []networkingv1.IngressRule{{Host: "app.example.com"}},
			annotations: map[string]string{"uddi.infoblox.com/publish": "false"},
			wantSkips:   []Skip{{Source: ingSrc, Reason: SkipExcluded, Message: "uddi.infoblox.com/publish is false"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "web", UID: "ing-uid", Annotations: tc.annotations},
				Spec:       networkingv1.IngressSpec{Rules: tc.rules},
				Status: networkingv1.IngressStatus{
					LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: tc.lb},
				},
			}
			gotEps, gotSkips := EndpointsFromIngress(i, tc.opts)
			assertResult(t, gotEps, gotSkips, tc.wantEps, tc.wantSkips)
		})
	}
}

// -----------------------------------------------------------------------------------
// EndpointsFromService
// -----------------------------------------------------------------------------------

func TestEndpointsFromService(t *testing.T) {
	svcSrc := SourceRef{Kind: SourceService, Namespace: "team-a", Name: "web", UID: "svc-uid"}

	cases := []struct {
		name        string
		svcType     corev1.ServiceType
		lb          []corev1.LoadBalancerIngress
		annotations map[string]string
		opts        SourceOptions
		wantEps     []Endpoint
		wantSkips   []Skip
	}{
		{
			name:      "not a LoadBalancer without a target annotation is skipped",
			svcType:   corev1.ServiceTypeClusterIP,
			wantSkips: []Skip{{Source: svcSrc, Reason: SkipNotLoadBalancer, Message: "Service is not type LoadBalancer"}},
		},
		{
			name:    "not a LoadBalancer but with a target annotation bypasses the type check",
			svcType: corev1.ServiceTypeClusterIP,
			annotations: map[string]string{
				"uddi.infoblox.com/hostname": "app.example.com",
				"uddi.infoblox.com/target":   "192.0.2.1",
			},
			wantEps: []Endpoint{{
				Source: svcSrc, Hostname: "app.example.com.",
				Targets: []Target{{Type: TargetA, Value: "192.0.2.1"}},
			}},
		},
		{
			name:      "LoadBalancer without a hostname annotation has no hostname",
			svcType:   corev1.ServiceTypeLoadBalancer,
			lb:        []corev1.LoadBalancerIngress{{IP: "192.0.2.1"}},
			wantSkips: []Skip{{Source: svcSrc, Reason: SkipNoHostname, Message: "no uddi.infoblox.com/hostname annotation"}},
		},
		{
			name:        "LoadBalancer IPs give A targets",
			svcType:     corev1.ServiceTypeLoadBalancer,
			lb:          []corev1.LoadBalancerIngress{{IP: "192.0.2.1"}, {IP: "192.0.2.2"}},
			annotations: map[string]string{"uddi.infoblox.com/hostname": "app.example.com"},
			wantEps: []Endpoint{{
				Source: svcSrc, Hostname: "app.example.com.",
				Targets: []Target{
					{Type: TargetA, Value: "192.0.2.1"},
					{Type: TargetA, Value: "192.0.2.2"},
				},
			}},
		},
		{
			name:        "LoadBalancer hostname with no IPs gives one CNAME",
			svcType:     corev1.ServiceTypeLoadBalancer,
			lb:          []corev1.LoadBalancerIngress{{Hostname: "lb.example.com"}},
			annotations: map[string]string{"uddi.infoblox.com/hostname": "app.example.com"},
			wantEps: []Endpoint{{
				Source: svcSrc, Hostname: "app.example.com.",
				Targets: []Target{{Type: TargetCNAME, Value: "lb.example.com."}},
			}},
		},
		{
			name:        "multiple hostnames in the annotation share the same target, sorted",
			svcType:     corev1.ServiceTypeLoadBalancer,
			lb:          []corev1.LoadBalancerIngress{{IP: "192.0.2.1"}},
			annotations: map[string]string{"uddi.infoblox.com/hostname": "zeta.example.com,alpha.example.com"},
			wantEps: []Endpoint{
				{Source: svcSrc, Hostname: "alpha.example.com.", Targets: []Target{{Type: TargetA, Value: "192.0.2.1"}}},
				{Source: svcSrc, Hostname: "zeta.example.com.", Targets: []Target{{Type: TargetA, Value: "192.0.2.1"}}},
			},
		},
		{
			name:        "mode A filters out the CNAME leaving no target",
			svcType:     corev1.ServiceTypeLoadBalancer,
			lb:          []corev1.LoadBalancerIngress{{Hostname: "lb.example.com"}},
			annotations: map[string]string{"uddi.infoblox.com/hostname": "app.example.com"},
			opts:        SourceOptions{Mode: TargetModeA},
			wantSkips: []Skip{{
				Source: svcSrc, Hostname: "app.example.com.", Reason: SkipNoTarget,
				Message: "no target could be derived for the configured mode",
			}},
		},
		{
			name:        "ttl annotation sets the endpoint TTL",
			svcType:     corev1.ServiceTypeLoadBalancer,
			lb:          []corev1.LoadBalancerIngress{{IP: "192.0.2.1"}},
			annotations: map[string]string{"uddi.infoblox.com/hostname": "app.example.com", "uddi.infoblox.com/ttl": "60"},
			wantEps: []Endpoint{{
				Source: svcSrc, Hostname: "app.example.com.",
				Targets: []Target{{Type: TargetA, Value: "192.0.2.1"}},
				TTL:     int32p(60),
			}},
		},
		{
			name:        "malformed hostname annotation is invalid, not NoHostname",
			svcType:     corev1.ServiceTypeLoadBalancer,
			annotations: map[string]string{"uddi.infoblox.com/hostname": "bad_host!"},
			wantSkips: []Skip{{
				Source: svcSrc, Reason: SkipInvalidAnnotation,
				Message: `uddi.infoblox.com/hostname: "bad_host!" is not a valid DNS name`,
			}},
		},
		{
			name:        "publish false excludes the whole source",
			svcType:     corev1.ServiceTypeLoadBalancer,
			annotations: map[string]string{"uddi.infoblox.com/publish": "false"},
			wantSkips:   []Skip{{Source: svcSrc, Reason: SkipExcluded, Message: "uddi.infoblox.com/publish is false"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "web", UID: "svc-uid", Annotations: tc.annotations},
				Spec:       corev1.ServiceSpec{Type: tc.svcType},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{Ingress: tc.lb},
				},
			}
			gotEps, gotSkips := EndpointsFromService(s, tc.opts)
			assertResult(t, gotEps, gotSkips, tc.wantEps, tc.wantSkips)
		})
	}
}
