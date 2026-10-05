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
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// hrRoute builds an HTTPRoute in team-a with the given spec.hostnames and status.parents.
func hrRoute(annotations map[string]string, hostnames []string, parents ...map[string]interface{}) *unstructured.Unstructured {
	route := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata":   map[string]interface{}{"namespace": "team-a", "name": "web", "uid": "web-uid"},
	}}
	route.SetAnnotations(annotations)
	if hostnames != nil {
		list := make([]interface{}, 0, len(hostnames))
		for _, h := range hostnames {
			list = append(list, h)
		}
		route.Object["spec"] = map[string]interface{}{"hostnames": list}
	}
	if parents != nil {
		list := make([]interface{}, 0, len(parents))
		for _, p := range parents {
			list = append(list, p)
		}
		route.Object["status"] = map[string]interface{}{"parents": list}
	}
	return route
}

// hrParent builds one status.parents entry. namespace may be empty (the route's namespace).
func hrParent(namespace, name, accepted string) map[string]interface{} {
	parentRef := map[string]interface{}{"name": name}
	if namespace != "" {
		parentRef["namespace"] = namespace
	}
	return map[string]interface{}{
		"parentRef":      parentRef,
		"controllerName": "example.com/gateway-controller",
		"conditions": []interface{}{
			map[string]interface{}{"type": "Accepted", "status": accepted, "reason": "Test"},
		},
	}
}

// hrParentOfKind builds an accepted status.parents entry for a parent that may not be a Gateway.
func hrParentOfKind(group, kind, name string) map[string]interface{} {
	p := hrParent("", name, "True")
	ref, _ := p["parentRef"].(map[string]interface{})
	ref["group"] = group
	ref["kind"] = kind
	return p
}

// hrGateway builds a Gateway whose status.addresses are the given {type, value} pairs; an
// empty type leaves the field out.
func hrGateway(namespace, name string, addresses ...[2]string) *unstructured.Unstructured {
	list := make([]interface{}, 0, len(addresses))
	for _, a := range addresses {
		entry := map[string]interface{}{"value": a[1]}
		if a[0] != "" {
			entry["type"] = a[0]
		}
		list = append(list, entry)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "Gateway",
		"metadata":   map[string]interface{}{"namespace": namespace, "name": name},
		"status":     map[string]interface{}{"addresses": list},
	}}
}

func hrGateways(gws ...*unstructured.Unstructured) map[types.NamespacedName]*unstructured.Unstructured {
	m := make(map[types.NamespacedName]*unstructured.Unstructured, len(gws))
	for _, gw := range gws {
		m[types.NamespacedName{Namespace: gw.GetNamespace(), Name: gw.GetName()}] = gw
	}
	return m
}

func TestEndpointsFromHTTPRoute(t *testing.T) {
	src := SourceRef{Kind: SourceHTTPRoute, Namespace: "team-a", Name: "web", UID: "web-uid"}

	ipv4 := [2]string{"IPAddress", "192.0.2.10"}
	ipv6 := [2]string{"IPAddress", "2001:db8::10"}
	hostAddr := [2]string{"Hostname", "LB.Example.com"}

	var (
		a    = Target{Type: TargetA, Value: "192.0.2.10"}
		aaaa = Target{Type: TargetAAAA, Value: "2001:db8::10"}
		cn   = Target{Type: TargetCNAME, Value: "lb.example.com."}
	)

	notAdmitted := func(hostnames ...string) []Skip {
		skips := make([]Skip, 0, len(hostnames))
		for _, h := range hostnames {
			skips = append(skips, Skip{Source: src, Hostname: h, Reason: SkipNotAdmitted, Message: "no parent Gateway has accepted this route"})
		}
		return skips
	}
	noTarget := func(hostnames ...string) []Skip {
		skips := make([]Skip, 0, len(hostnames))
		for _, h := range hostnames {
			skips = append(skips, Skip{Source: src, Hostname: h, Reason: SkipNoTarget, Message: "no target could be derived for the configured mode"})
		}
		return skips
	}
	endpoint := func(hostname string, ttl *int32, targets ...Target) Endpoint {
		return Endpoint{Source: src, Hostname: hostname, Targets: targets, TTL: ttl}
	}
	noHostname := Skip{Source: src, Reason: SkipNoHostname, Message: "no spec.hostnames and no uddi.infoblox.com/hostname annotation"}

	cases := []struct {
		name        string
		annotations map[string]string
		hostnames   []string
		parents     []map[string]interface{}
		gateways    map[types.NamespacedName]*unstructured.Unstructured
		opts        SourceOptions
		wantEps     []Endpoint
		wantSkips   []Skip
	}{
		// Source-level skips.
		{
			name:        "publish false is excluded",
			annotations: map[string]string{"uddi.infoblox.com/publish": "false"},
			hostnames:   []string{"app.example.com"},
			parents:     []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:    hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantSkips:   []Skip{{Source: src, Reason: SkipExcluded, Message: "uddi.infoblox.com/publish is false"}},
		},
		{
			name:        "malformed publish annotation is invalid",
			annotations: map[string]string{"uddi.infoblox.com/publish": "maybe"},
			hostnames:   []string{"app.example.com"},
			wantSkips: []Skip{{
				Source: src, Reason: SkipInvalidAnnotation,
				Message: `uddi.infoblox.com/publish: strconv.ParseBool: parsing "maybe": invalid syntax`,
			}},
		},
		{
			name:      "no hostnames and no annotation",
			hostnames: nil,
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantSkips: []Skip{noHostname},
		},
		{
			name:      "empty hostnames list",
			hostnames: []string{},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantSkips: []Skip{noHostname},
		},

		// Acceptance.
		{
			name:      "accepted parent in the route namespace",
			hostnames: []string{"app.example.com"},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantEps:   []Endpoint{endpoint("app.example.com.", nil, a)},
		},
		{
			name:      "accepted parent in another namespace",
			hostnames: []string{"app.example.com"},
			parents:   []map[string]interface{}{hrParent("infra", "shared", "True")},
			gateways:  hrGateways(hrGateway("infra", "shared", ipv4)),
			wantEps:   []Endpoint{endpoint("app.example.com.", nil, a)},
		},
		{
			name:      "parent namespace defaults to the route namespace, not the Gateway map",
			hostnames: []string{"app.example.com"},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("infra", "gw", ipv4)),
			wantSkips: noTarget("app.example.com."),
		},
		{
			name:      "parent not accepted",
			hostnames: []string{"app.example.com"},
			parents:   []map[string]interface{}{hrParent("", "gw", "False")},
			gateways:  hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantSkips: notAdmitted("app.example.com."),
		},
		{
			name:      "accepted status Unknown is not accepted",
			hostnames: []string{"app.example.com"},
			parents:   []map[string]interface{}{hrParent("", "gw", "Unknown")},
			gateways:  hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantSkips: notAdmitted("app.example.com."),
		},
		{
			name:      "no status parents at all",
			hostnames: []string{"app.example.com"},
			gateways:  hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantSkips: notAdmitted("app.example.com."),
		},
		{
			name:      "not admitted skips every hostname",
			hostnames: []string{"b.example.com", "a.example.com"},
			parents:   []map[string]interface{}{hrParent("", "gw", "False")},
			gateways:  hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantSkips: notAdmitted("a.example.com.", "b.example.com."),
		},
		{
			name:      "two parents, only the accepted one contributes",
			hostnames: []string{"app.example.com"},
			parents: []map[string]interface{}{
				hrParent("", "rejected", "False"),
				hrParent("", "accepted", "True"),
			},
			gateways: hrGateways(
				hrGateway("team-a", "rejected", [2]string{"IPAddress", "198.51.100.1"}),
				hrGateway("team-a", "accepted", ipv4),
			),
			wantEps: []Endpoint{endpoint("app.example.com.", nil, a)},
		},
		{
			name:      "an accepted parent that is not a Gateway does not admit the route",
			hostnames: []string{"app.example.com"},
			parents:   []map[string]interface{}{hrParentOfKind("", "Service", "svc")},
			gateways:  hrGateways(hrGateway("team-a", "svc", ipv4)),
			wantSkips: notAdmitted("app.example.com."),
		},
		{
			name:      "explicit Gateway group and kind are accepted",
			hostnames: []string{"app.example.com"},
			parents:   []map[string]interface{}{hrParentOfKind("gateway.networking.k8s.io", "Gateway", "gw")},
			gateways:  hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantEps:   []Endpoint{endpoint("app.example.com.", nil, a)},
		},

		// Targets.
		{
			name:      "IPv4 and IPv6 addresses give A and AAAA",
			hostnames: []string{"app.example.com"},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw", ipv6, ipv4)),
			wantEps:   []Endpoint{endpoint("app.example.com.", nil, a, aaaa)},
		},
		{
			name:      "an address without a type is an IPAddress",
			hostnames: []string{"app.example.com"},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw", [2]string{"", "192.0.2.10"})),
			wantEps:   []Endpoint{endpoint("app.example.com.", nil, a)},
		},
		{
			name:      "hostname address gives a normalized CNAME",
			hostnames: []string{"app.example.com"},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw", hostAddr)),
			wantEps:   []Endpoint{endpoint("app.example.com.", nil, cn)},
		},
		{
			name:      "mixed addresses are sorted by type then value",
			hostnames: []string{"app.example.com"},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways: hrGateways(hrGateway("team-a", "gw",
				hostAddr, ipv6, [2]string{"IPAddress", "192.0.2.9"}, ipv4)),
			wantEps: []Endpoint{endpoint("app.example.com.", nil,
				Target{Type: TargetA, Value: "192.0.2.10"},
				Target{Type: TargetA, Value: "192.0.2.9"},
				aaaa, cn)},
		},
		{
			name:      "addresses of two accepted Gateways are unioned and deduplicated",
			hostnames: []string{"app.example.com"},
			parents: []map[string]interface{}{
				hrParent("infra", "zone-b", "True"),
				hrParent("", "zone-a", "True"),
			},
			gateways: hrGateways(
				hrGateway("infra", "zone-b", ipv4, [2]string{"IPAddress", "198.51.100.7"}),
				hrGateway("team-a", "zone-a", ipv4, ipv6),
			),
			wantEps: []Endpoint{endpoint("app.example.com.", nil,
				a, Target{Type: TargetA, Value: "198.51.100.7"}, aaaa)},
		},
		{
			name:      "one Gateway attached through two listeners is counted once",
			hostnames: []string{"app.example.com"},
			parents: []map[string]interface{}{
				hrParent("", "gw", "True"),
				hrParent("", "gw", "True"),
			},
			gateways: hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantEps:  []Endpoint{endpoint("app.example.com.", nil, a)},
		},
		{
			name:      "an accepted Gateway missing from the map contributes nothing",
			hostnames: []string{"app.example.com"},
			parents: []map[string]interface{}{
				hrParent("", "missing", "True"),
				hrParent("", "present", "True"),
			},
			gateways: hrGateways(hrGateway("team-a", "present", ipv4)),
			wantEps:  []Endpoint{endpoint("app.example.com.", nil, a)},
		},
		{
			name:      "the only accepted Gateway missing from the map is NoTarget",
			hostnames: []string{"app.example.com"},
			parents:   []map[string]interface{}{hrParent("", "missing", "True")},
			gateways:  hrGateways(),
			wantSkips: noTarget("app.example.com."),
		},
		{
			name:      "nil Gateway map is NoTarget",
			hostnames: []string{"app.example.com"},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  nil,
			wantSkips: noTarget("app.example.com."),
		},
		{
			name:      "Gateway without addresses is NoTarget",
			hostnames: []string{"app.example.com"},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw")),
			wantSkips: noTarget("app.example.com."),
		},
		{
			name:      "NamedAddress and unparsable addresses are ignored",
			hostnames: []string{"app.example.com"},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways: hrGateways(hrGateway("team-a", "gw",
				[2]string{"NamedAddress", "pool-1"},
				[2]string{"example.com/custom", "10.1.1.1"},
				[2]string{"IPAddress", "not-an-ip"},
				[2]string{"IPAddress", "lb.example.com"},
				[2]string{"Hostname", "192.0.2.99"},
				[2]string{"Hostname", "bad_host!"},
				ipv4,
			)),
			wantEps: []Endpoint{endpoint("app.example.com.", nil, a)},
		},

		// Hostnames.
		{
			name:      "wildcard hostname is kept and lower-cased",
			hostnames: []string{"*.Apps.Example.com"},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantEps:   []Endpoint{endpoint("*.apps.example.com.", nil, a)},
		},
		{
			name:      "hostnames are sorted and the annotation adds more without duplicates",
			hostnames: []string{"B.example.com", "*.apps.example.com"},
			annotations: map[string]string{
				"uddi.infoblox.com/hostname": "a.example.com, b.example.com",
			},
			parents:  []map[string]interface{}{hrParent("", "gw", "True")},
			gateways: hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantEps: []Endpoint{
				endpoint("*.apps.example.com.", nil, a),
				endpoint("a.example.com.", nil, a),
				endpoint("b.example.com.", nil, a),
			},
		},
		{
			name:        "hostname annotation alone is enough",
			hostnames:   nil,
			annotations: map[string]string{"uddi.infoblox.com/hostname": "app.example.com"},
			parents:     []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:    hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantEps:     []Endpoint{endpoint("app.example.com.", nil, a)},
		},
		{
			name:        "malformed hostname annotation is invalid",
			hostnames:   []string{"app.example.com"},
			annotations: map[string]string{"uddi.infoblox.com/hostname": "bad_host!"},
			parents:     []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:    hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantSkips: []Skip{{
				Source: src, Reason: SkipInvalidAnnotation,
				Message: `uddi.infoblox.com/hostname: "bad_host!" is not a valid DNS name`,
			}},
		},
		{
			name:      "an invalid spec hostname is skipped next to a valid one",
			hostnames: []string{"app.example.com", "bad_host!"},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantEps:   []Endpoint{endpoint("app.example.com.", nil, a)},
			wantSkips: []Skip{{
				Source: src, Hostname: "bad_host!.", Reason: SkipUnsupported,
				Message: `spec.hostnames entry "bad_host!" is not a DNS name or a *. wildcard`,
			}},
		},
		{
			name:      "only invalid spec hostnames",
			hostnames: []string{"*"},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantSkips: []Skip{{
				Source: src, Hostname: "*.", Reason: SkipUnsupported,
				Message: `spec.hostnames entry "*" is not a DNS name or a *. wildcard`,
			}},
		},

		// Annotations.
		{
			name:        "ttl annotation is carried on every endpoint",
			hostnames:   []string{"a.example.com", "b.example.com"},
			annotations: map[string]string{"uddi.infoblox.com/ttl": "120"},
			parents:     []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:    hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantEps: []Endpoint{
				endpoint("a.example.com.", int32p(120), a),
				endpoint("b.example.com.", int32p(120), a),
			},
		},
		{
			name:        "malformed ttl annotation is invalid",
			hostnames:   []string{"app.example.com"},
			annotations: map[string]string{"uddi.infoblox.com/ttl": "0"},
			parents:     []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:    hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantSkips: []Skip{{
				Source: src, Reason: SkipInvalidAnnotation,
				Message: `uddi.infoblox.com/ttl: "0" is not a positive integer`,
			}},
		},
		{
			name:        "target annotation replaces the Gateway addresses",
			hostnames:   []string{"app.example.com"},
			annotations: map[string]string{"uddi.infoblox.com/target": "203.0.113.5, edge.example.net"},
			parents:     []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:    hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantEps: []Endpoint{endpoint("app.example.com.", nil,
				Target{Type: TargetA, Value: "203.0.113.5"},
				Target{Type: TargetCNAME, Value: "edge.example.net."})},
		},
		{
			name:        "target annotation works when the Gateway is missing",
			hostnames:   []string{"app.example.com"},
			annotations: map[string]string{"uddi.infoblox.com/target": "203.0.113.5"},
			parents:     []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:    hrGateways(),
			wantEps:     []Endpoint{endpoint("app.example.com.", nil, Target{Type: TargetA, Value: "203.0.113.5"})},
		},
		{
			name:        "target annotation does not bypass admission",
			hostnames:   []string{"app.example.com"},
			annotations: map[string]string{"uddi.infoblox.com/target": "203.0.113.5"},
			parents:     []map[string]interface{}{hrParent("", "gw", "False")},
			gateways:    hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantSkips:   notAdmitted("app.example.com."),
		},
		{
			name:        "empty target annotation leaves no target",
			hostnames:   []string{"app.example.com"},
			annotations: map[string]string{"uddi.infoblox.com/target": ""},
			parents:     []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:    hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantSkips:   noTarget("app.example.com."),
		},
		{
			name:        "custom annotation prefix",
			hostnames:   []string{"app.example.com"},
			annotations: map[string]string{"dns.example.org/ttl": "60", "uddi.infoblox.com/ttl": "999"},
			opts:        SourceOptions{AnnotationPrefix: "dns.example.org"},
			parents:     []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:    hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantEps:     []Endpoint{endpoint("app.example.com.", int32p(60), a)},
		},

		// Mode filtering.
		{
			name:      "mode A keeps address targets",
			hostnames: []string{"app.example.com"},
			opts:      SourceOptions{Mode: TargetModeA},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw", hostAddr, ipv6, ipv4)),
			wantEps:   []Endpoint{endpoint("app.example.com.", nil, a, aaaa)},
		},
		{
			name:      "mode A with only a hostname address is NoTarget",
			hostnames: []string{"app.example.com"},
			opts:      SourceOptions{Mode: TargetModeA},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw", hostAddr)),
			wantSkips: noTarget("app.example.com."),
		},
		{
			name:      "mode CNAME keeps hostname targets",
			hostnames: []string{"app.example.com"},
			opts:      SourceOptions{Mode: TargetModeCNAME},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw", ipv4, hostAddr)),
			wantEps:   []Endpoint{endpoint("app.example.com.", nil, cn)},
		},
		{
			name:      "mode CNAME with only IP addresses is NoTarget",
			hostnames: []string{"app.example.com", "b.example.com"},
			opts:      SourceOptions{Mode: TargetModeCNAME},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw", ipv4, ipv6)),
			wantSkips: noTarget("app.example.com.", "b.example.com."),
		},
		{
			name:        "mode CNAME applies to the target annotation too",
			hostnames:   []string{"app.example.com"},
			annotations: map[string]string{"uddi.infoblox.com/target": "203.0.113.5"},
			opts:        SourceOptions{Mode: TargetModeCNAME},
			parents:     []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:    hrGateways(hrGateway("team-a", "gw", hostAddr)),
			wantSkips:   noTarget("app.example.com."),
		},
		{
			name:      "mode Auto keeps everything",
			hostnames: []string{"app.example.com"},
			opts:      SourceOptions{Mode: TargetModeAuto},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw", hostAddr, ipv4)),
			wantEps:   []Endpoint{endpoint("app.example.com.", nil, a, cn)},
		},
		{
			name:      "static target addresses are not used for HTTPRoutes",
			hostnames: []string{"app.example.com"},
			opts:      SourceOptions{StaticAddresses: []string{"203.0.113.50"}},
			parents:   []map[string]interface{}{hrParent("", "gw", "True")},
			gateways:  hrGateways(hrGateway("team-a", "gw", ipv4)),
			wantEps:   []Endpoint{endpoint("app.example.com.", nil, a)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route := hrRoute(tc.annotations, tc.hostnames, tc.parents...)
			gotEps, gotSkips := EndpointsFromHTTPRoute(route, tc.gateways, tc.opts)
			assertResult(t, gotEps, gotSkips, tc.wantEps, tc.wantSkips)
		})
	}
}
