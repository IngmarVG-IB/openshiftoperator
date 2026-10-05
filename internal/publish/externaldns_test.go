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

// The external-dns compatibility tests run every case against a Route, an Ingress and a
// Service, because the fallbacks live in the annotation parsing the three extractors share.

const (
	edHostnameKey    = "uddi.infoblox.com/hostname"
	edTTLKey         = "uddi.infoblox.com/ttl"
	edTargetKey      = "uddi.infoblox.com/target"
	edRouterHostname = "router-default.apps.example.com."
)

// edSource builds one source kind carrying the given annotations and runs its extractor.
type edSource struct {
	name string
	// baseAnnotations are always set so the source publishes app.example.com. without any
	// hostname annotation under test; a Service has no hostname of its own and gets the uddi
	// hostname annotation here.
	baseAnnotations map[string]string
	// ownHostnames are the hostnames the source publishes with no hostname annotation.
	ownHostnames []string
	// defaultTargets are the targets the source computes with no target annotation.
	defaultTargets []Target
	extract        func(annotations map[string]string, o SourceOptions) ([]Endpoint, []Skip)
}

func edSources() []edSource {
	return []edSource{
		{
			name:           "Route",
			ownHostnames:   []string{"app.example.com."},
			defaultTargets: []Target{{Type: TargetCNAME, Value: edRouterHostname}},
			extract: func(annotations map[string]string, o SourceOptions) ([]Endpoint, []Skip) {
				return EndpointsFromRoute(&routev1.Route{
					ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "web", Annotations: annotations},
					Spec:       routev1.RouteSpec{Host: "app.example.com"},
					Status:     routev1.RouteStatus{Ingress: []routev1.RouteIngress{admittedIngress("router-default.apps.example.com")}},
				}, o)
			},
		},
		{
			name:           "Ingress",
			ownHostnames:   []string{"app.example.com."},
			defaultTargets: []Target{{Type: TargetA, Value: "192.0.2.1"}},
			extract: func(annotations map[string]string, o SourceOptions) ([]Endpoint, []Skip) {
				return EndpointsFromIngress(&networkingv1.Ingress{
					ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "web", Annotations: annotations},
					Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "app.example.com"}}},
					Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{
						Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.1"}},
					}},
				}, o)
			},
		},
		{
			name:            "Service",
			baseAnnotations: map[string]string{edHostnameKey: "app.example.com"},
			ownHostnames:    nil, // a Service only has the hostname annotation
			defaultTargets:  []Target{{Type: TargetA, Value: "192.0.2.1"}},
			extract: func(annotations map[string]string, o SourceOptions) ([]Endpoint, []Skip) {
				return EndpointsFromService(&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "web", Annotations: annotations},
					Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
					Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{{IP: "192.0.2.1"}},
					}},
				}, o)
			},
		},
	}
}

// merged returns the source's base annotations overlaid with extra.
func (s edSource) merged(extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range s.baseAnnotations {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func edHostnames(eps []Endpoint) []string {
	out := make([]string, 0, len(eps))
	for _, ep := range eps {
		out = append(out, ep.Hostname)
	}
	return out
}

func TestExternalDNS_Hostname(t *testing.T) {
	for _, s := range edSources() {
		t.Run(s.name, func(t *testing.T) {
			cases := []struct {
				name      string
				ann       map[string]string
				external  bool
				wantHosts []string
				wantSkip  SkipReason
			}{
				{
					name:      "fallback used when only the external-dns hostname is set",
					ann:       map[string]string{ExternalDNSHostname: "ext.example.com"},
					external:  true,
					wantHosts: append(append([]string{}, s.ownHostnames...), "ext.example.com."),
				},
				{
					name:      "comma separated external-dns hostnames all publish",
					ann:       map[string]string{ExternalDNSHostname: "ext1.example.com, ext2.example.com"},
					external:  true,
					wantHosts: append(append([]string{}, s.ownHostnames...), "ext1.example.com.", "ext2.example.com."),
				},
				{
					name:      "uddi hostname wins when both are set",
					ann:       map[string]string{edHostnameKey: "uddi.example.com", ExternalDNSHostname: "ext.example.com"},
					external:  true,
					wantHosts: append(append([]string{}, s.ownHostnames...), "uddi.example.com."),
				},
				{
					name:      "compat off ignores the external-dns hostname",
					ann:       map[string]string{ExternalDNSHostname: "ext.example.com"},
					external:  false,
					wantHosts: s.ownHostnames,
					wantSkip:  edSkipIfNoHostnames(s.ownHostnames),
				},
				{
					name:     "an invalid external-dns hostname is InvalidAnnotation",
					ann:      map[string]string{ExternalDNSHostname: "bad_host!"},
					external: true,
					wantSkip: SkipInvalidAnnotation,
				},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					// A Service has no hostname of its own, so for it the hostname annotation is
					// the whole input and must not come from base annotations.
					eps, skips := s.extract(tc.ann, SourceOptions{ExternalDNS: tc.external})
					if tc.wantSkip != "" {
						if len(eps) != 0 || len(skips) != 1 || skips[0].Reason != tc.wantSkip {
							t.Fatalf("got endpoints %#v skips %#v, want one %s skip", eps, skips, tc.wantSkip)
						}
						return
					}
					if len(skips) != 0 {
						t.Fatalf("unexpected skips: %#v", skips)
					}
					if got := edHostnames(eps); !reflect.DeepEqual(got, tc.wantHosts) {
						t.Errorf("hostnames = %v, want %v", got, tc.wantHosts)
					}
				})
			}
		})
	}
}

// edSkipIfNoHostnames is the skip reason a source that publishes nothing of its own gives.
func edSkipIfNoHostnames(own []string) SkipReason {
	if len(own) == 0 {
		return SkipNoHostname
	}
	return ""
}

func TestExternalDNS_InvalidHostnameMessageNamesTheKey(t *testing.T) {
	for _, s := range edSources() {
		t.Run(s.name, func(t *testing.T) {
			_, skips := s.extract(map[string]string{ExternalDNSHostname: "bad_host!"}, SourceOptions{ExternalDNS: true})
			want := `external-dns.alpha.kubernetes.io/hostname: "bad_host!" is not a valid DNS name`
			if len(skips) != 1 || skips[0].Message != want {
				t.Fatalf("skips = %#v, want one with message %q", skips, want)
			}
		})
	}
}

func TestExternalDNS_TTL(t *testing.T) {
	for _, s := range edSources() {
		t.Run(s.name, func(t *testing.T) {
			cases := []struct {
				name     string
				ann      map[string]string
				external bool
				wantTTL  *int32
				wantMsg  string
			}{
				{
					name:     "fallback used when only the external-dns ttl is set",
					ann:      map[string]string{ExternalDNSTTL: "120"},
					external: true,
					wantTTL:  int32p(120),
				},
				{
					name:     "external-dns ttl as a duration",
					ann:      map[string]string{ExternalDNSTTL: "5m"},
					external: true,
					wantTTL:  int32p(300),
				},
				{
					name:     "uddi ttl wins when both are set",
					ann:      map[string]string{edTTLKey: "60", ExternalDNSTTL: "120"},
					external: true,
					wantTTL:  int32p(60),
				},
				{
					name:     "a malformed uddi ttl is not rescued by the external-dns one",
					ann:      map[string]string{edTTLKey: "soon", ExternalDNSTTL: "120"},
					external: true,
					wantMsg:  `uddi.infoblox.com/ttl: "soon" is not an integer`,
				},
				{
					name:     "compat off ignores the external-dns ttl",
					ann:      map[string]string{ExternalDNSTTL: "120"},
					external: false,
					wantTTL:  nil,
				},
				{
					name:     "an invalid external-dns ttl is InvalidAnnotation naming its key",
					ann:      map[string]string{ExternalDNSTTL: "soon"},
					external: true,
					wantMsg:  `external-dns.alpha.kubernetes.io/ttl: "soon" is neither an integer number of seconds nor a duration`,
				},
				{
					name:     "a zero external-dns ttl is InvalidAnnotation",
					ann:      map[string]string{ExternalDNSTTL: "0"},
					external: true,
					wantMsg:  `external-dns.alpha.kubernetes.io/ttl: "0" is not a positive integer`,
				},
				{
					name:     "a sub-second external-dns duration is InvalidAnnotation",
					ann:      map[string]string{ExternalDNSTTL: "500ms"},
					external: true,
					wantMsg:  `external-dns.alpha.kubernetes.io/ttl: "500ms" is not between 1 second and 2147483647 seconds`,
				},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					eps, skips := s.extract(s.merged(tc.ann), SourceOptions{ExternalDNS: tc.external})
					if tc.wantMsg != "" {
						if len(eps) != 0 || len(skips) != 1 || skips[0].Reason != SkipInvalidAnnotation || skips[0].Message != tc.wantMsg {
							t.Fatalf("got endpoints %#v skips %#v, want one InvalidAnnotation skip %q", eps, skips, tc.wantMsg)
						}
						return
					}
					if len(skips) != 0 || len(eps) == 0 {
						t.Fatalf("got endpoints %#v skips %#v, want endpoints and no skips", eps, skips)
					}
					for _, ep := range eps {
						if !reflect.DeepEqual(ep.TTL, tc.wantTTL) {
							t.Errorf("TTL = %v, want %v", edDerefTTL(ep.TTL), edDerefTTL(tc.wantTTL))
						}
					}
				})
			}
		})
	}
}

func edDerefTTL(p *int32) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestExternalDNS_Target(t *testing.T) {
	for _, s := range edSources() {
		t.Run(s.name, func(t *testing.T) {
			cases := []struct {
				name        string
				ann         map[string]string
				external    bool
				wantTargets []Target
				wantMsg     string
			}{
				{
					name:        "fallback used when only the external-dns target is set (address)",
					ann:         map[string]string{ExternalDNSTarget: "203.0.113.9"},
					external:    true,
					wantTargets: []Target{{Type: TargetA, Value: "203.0.113.9"}},
				},
				{
					name:        "fallback used for a name, which becomes a CNAME",
					ann:         map[string]string{ExternalDNSTarget: "Alias.Example.com"},
					external:    true,
					wantTargets: []Target{{Type: TargetCNAME, Value: "alias.example.com."}},
				},
				{
					name:        "dual-stack external-dns target gives A and AAAA",
					ann:         map[string]string{ExternalDNSTarget: "203.0.113.9,2001:db8::9"},
					external:    true,
					wantTargets: []Target{{Type: TargetA, Value: "203.0.113.9"}, {Type: TargetAAAA, Value: "2001:db8::9"}},
				},
				{
					name:        "uddi target wins when both are set",
					ann:         map[string]string{edTargetKey: "198.51.100.1", ExternalDNSTarget: "203.0.113.9"},
					external:    true,
					wantTargets: []Target{{Type: TargetA, Value: "198.51.100.1"}},
				},
				{
					name:        "compat off ignores the external-dns target",
					ann:         map[string]string{ExternalDNSTarget: "203.0.113.9"},
					external:    false,
					wantTargets: s.defaultTargets,
				},
				{
					name:     "an invalid external-dns target is InvalidAnnotation naming its key",
					ann:      map[string]string{ExternalDNSTarget: "bad_target!"},
					external: true,
					wantMsg:  `external-dns.alpha.kubernetes.io/target: "bad_target!" is not an IP address or a valid DNS name`,
				},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					eps, skips := s.extract(s.merged(tc.ann), SourceOptions{ExternalDNS: tc.external})
					if tc.wantMsg != "" {
						if len(eps) != 0 || len(skips) != 1 || skips[0].Reason != SkipInvalidAnnotation || skips[0].Message != tc.wantMsg {
							t.Fatalf("got endpoints %#v skips %#v, want one InvalidAnnotation skip %q", eps, skips, tc.wantMsg)
						}
						return
					}
					if len(skips) != 0 || len(eps) == 0 {
						t.Fatalf("got endpoints %#v skips %#v, want endpoints and no skips", eps, skips)
					}
					for _, ep := range eps {
						if !reflect.DeepEqual(ep.Targets, tc.wantTargets) {
							t.Errorf("targets = %#v, want %#v", ep.Targets, tc.wantTargets)
						}
					}
				})
			}
		})
	}
}

// TestExternalDNS_PrefixOverride proves the fallback keys do not move with a custom
// annotation prefix: only the uddi keys are renamed.
func TestExternalDNS_PrefixOverride(t *testing.T) {
	for _, s := range edSources() {
		t.Run(s.name, func(t *testing.T) {
			o := SourceOptions{ExternalDNS: true, AnnotationPrefix: "dns.example.org"}
			ann := map[string]string{
				"dns.example.org/hostname": "custom.example.com",
				edHostnameKey:              "ignored.example.com",
				ExternalDNSHostname:        "ext.example.com",
			}
			eps, skips := s.extract(ann, o)
			if len(skips) != 0 {
				t.Fatalf("unexpected skips: %#v", skips)
			}
			want := append(append([]string{}, s.ownHostnames...), "custom.example.com.")
			if got := edHostnames(eps); !reflect.DeepEqual(got, want) {
				t.Errorf("hostnames = %v, want %v", got, want)
			}
		})
	}
}

// TestWildcardHostnameAnnotation covers the wildcard form the hostname annotation now
// accepts, and that a wildcard is never accepted as a target.
func TestWildcardHostnameAnnotation(t *testing.T) {
	got, err := parseHostnamesAnnotation("*.apps.example.com, Plain.example.com")
	if err != nil {
		t.Fatalf("parseHostnamesAnnotation: %v", err)
	}
	want := []string{"*.apps.example.com.", "plain.example.com."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hostnames = %v, want %v", got, want)
	}

	for _, bad := range []string{"*", "*.", "*.*.example.com", "foo.*.example.com", "**.example.com"} {
		if _, err := parseHostnamesAnnotation(bad); err == nil {
			t.Errorf("parseHostnamesAnnotation(%q): want error, got nil", bad)
		}
	}
	if _, err := ParseTargets("*.example.com"); err == nil {
		t.Errorf("ParseTargets(*.example.com): want error, a wildcard cannot be a CNAME target")
	}
}
