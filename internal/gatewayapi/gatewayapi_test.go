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

package gatewayapi

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
)

func TestGVKs(t *testing.T) {
	cases := []struct {
		name string
		got  schema.GroupVersionKind
		want schema.GroupVersionKind
	}{
		{name: "Gateway", got: GatewayGVK, want: schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"}},
		{name: "HTTPRoute", got: HTTPRouteGVK, want: schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("GVK = %v, want %v", tc.got, tc.want)
			}
		})
	}
}

// TestAvailable exercises Available's discovery-response handling against a fake apiserver,
// without needing a real cluster. The envtest-backed round trip in
// gatewayapi_envtest_test.go additionally proves the "true" case against the real CRDs.
func TestAvailable(t *testing.T) {
	const gatewaysResource = `{"name":"gateways","singularName":"gateway","namespaced":true,"kind":"Gateway","verbs":["get","list"]}`
	const gatewaysStatus = `{"name":"gateways/status","singularName":"","namespaced":true,"kind":"Gateway","verbs":["get","update"]}`
	const routesResource = `{"name":"httproutes","singularName":"httproute","namespaced":true,"kind":"HTTPRoute","verbs":["get","list"]}`
	list := func(resources ...string) string {
		body := `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"gateway.networking.k8s.io/v1","resources":[`
		for i, r := range resources {
			if i > 0 {
				body += ","
			}
			body += r
		}
		return body + `]}`
	}

	cases := []struct {
		name    string
		status  int
		body    string
		want    bool
		wantErr bool
	}{
		{name: "gateways and httproutes served", status: http.StatusOK, body: list(gatewaysResource, gatewaysStatus, routesResource), want: true},
		{name: "gateways only", status: http.StatusOK, body: list(gatewaysResource), want: false},
		{name: "httproutes only", status: http.StatusOK, body: list(routesResource), want: false},
		{name: "only the gateways status subresource", status: http.StatusOK, body: list(gatewaysStatus, routesResource), want: false},
		{
			name:   "group version not found",
			status: http.StatusNotFound,
			body: `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure",
				"message":"the server could not find the requested resource",
				"reason":"NotFound","details":{},"code":404}`,
			want: false,
		},
		{
			name:    "discovery failure is an error",
			status:  http.StatusInternalServerError,
			body:    `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"boom","reason":"InternalError","code":500}`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got, err := Available(&rest.Config{Host: srv.URL})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Available: want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Available: unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("Available = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAvailableBadConfig(t *testing.T) {
	// A config that fails to build a client (a bad TLS setup) surfaces the error rather
	// than reporting the Gateway API as merely absent.
	cfg := &rest.Config{
		Host: "https://127.0.0.1:0",
		TLSClientConfig: rest.TLSClientConfig{
			CertData: []byte("not-a-cert"),
		},
	}
	if _, err := Available(cfg); err == nil {
		t.Fatalf("Available: want error for an invalid client config, got nil")
	}
}

// -----------------------------------------------------------------------------------
// Accessors
// -----------------------------------------------------------------------------------

// object builds an unstructured Gateway API object for the accessor tests.
func object(kind, namespace, name string, spec, status map[string]interface{}) *unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       kind,
		"metadata":   map[string]interface{}{"namespace": namespace, "name": name},
	}
	if spec != nil {
		obj["spec"] = spec
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

func ref(fields ...string) map[string]interface{} {
	m := map[string]interface{}{}
	for i := 0; i+1 < len(fields); i += 2 {
		m[fields[i]] = fields[i+1]
	}
	return m
}

func cond(condType, status string) map[string]interface{} {
	return map[string]interface{}{"type": condType, "status": status, "reason": "test"}
}

func TestHostnames(t *testing.T) {
	cases := []struct {
		name  string
		route *unstructured.Unstructured
		want  []string
	}{
		{name: "nil route", route: nil, want: []string{}},
		{name: "no spec", route: object("HTTPRoute", "ns", "r", nil, nil), want: []string{}},
		{
			name:  "no hostnames",
			route: object("HTTPRoute", "ns", "r", map[string]interface{}{"parentRefs": []interface{}{}}, nil),
			want:  []string{},
		},
		{
			name: "hostnames as written, blanks and non-strings dropped",
			route: object("HTTPRoute", "ns", "r", map[string]interface{}{
				"hostnames": []interface{}{"App.Example.com", " *.apps.example.com ", "", int64(7), "b.example.com"},
			}, nil),
			want: []string{"App.Example.com", "*.apps.example.com", "b.example.com"},
		},
		{
			name:  "hostnames of the wrong type",
			route: object("HTTPRoute", "ns", "r", map[string]interface{}{"hostnames": "a.example.com"}, nil),
			want:  []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Hostnames(tc.route); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Hostnames() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestParentRefs(t *testing.T) {
	route := object("HTTPRoute", "team-a", "web", map[string]interface{}{
		"parentRefs": []interface{}{
			ref("name", "gw"),
			ref("group", "gateway.networking.k8s.io", "kind", "Gateway", "namespace", "infra", "name", "shared", "sectionName", "https"),
			ref("group", "", "kind", "Service", "name", "mesh-svc"),
			ref("namespace", "infra"), // no name: dropped
			"not-a-map",
		},
	}, nil)

	want := []ParentRef{
		{Namespace: "team-a", Name: "gw"},
		{Group: "gateway.networking.k8s.io", Kind: "Gateway", Namespace: "infra", Name: "shared", SectionName: "https"},
		{Kind: "Service", Namespace: "team-a", Name: "mesh-svc"},
	}
	if got := ParentRefs(route); !reflect.DeepEqual(got, want) {
		t.Errorf("ParentRefs() = %#v, want %#v", got, want)
	}
	if got := ParentRefs(nil); len(got) != 0 {
		t.Errorf("ParentRefs(nil) = %#v, want empty", got)
	}
	if got := ParentRefs(object("HTTPRoute", "ns", "r", nil, nil)); len(got) != 0 {
		t.Errorf("ParentRefs(no spec) = %#v, want empty", got)
	}
}

func TestParentRefIsGateway(t *testing.T) {
	cases := []struct {
		name string
		ref  ParentRef
		want bool
	}{
		{name: "defaults", ref: ParentRef{Name: "gw"}, want: true},
		{name: "explicit group and kind", ref: ParentRef{Group: "gateway.networking.k8s.io", Kind: "Gateway", Name: "gw"}, want: true},
		{name: "kind only", ref: ParentRef{Kind: "Gateway", Name: "gw"}, want: true},
		{name: "other group", ref: ParentRef{Group: "example.com", Kind: "Gateway", Name: "gw"}, want: false},
		{name: "other kind", ref: ParentRef{Group: "", Kind: "Service", Name: "svc"}, want: false},
		{name: "default group with other kind", ref: ParentRef{Group: "gateway.networking.k8s.io", Kind: "ListenerSet", Name: "ls"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ref.IsGateway(); got != tc.want {
				t.Errorf("IsGateway() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParentStatuses(t *testing.T) {
	route := object("HTTPRoute", "team-a", "web", nil, map[string]interface{}{
		"parents": []interface{}{
			map[string]interface{}{
				"parentRef":      ref("name", "gw"),
				"controllerName": "example.com/gateway-controller",
				"conditions": []interface{}{
					cond("ResolvedRefs", "True"),
					cond("Accepted", "True"),
				},
			},
			map[string]interface{}{
				"parentRef":      ref("group", "gateway.networking.k8s.io", "kind", "Gateway", "namespace", "infra", "name", "shared", "sectionName", "https"),
				"controllerName": "example.com/other",
				"conditions":     []interface{}{cond("Accepted", "False")},
			},
			map[string]interface{}{
				"parentRef":  ref("name", "no-accepted-condition"),
				"conditions": []interface{}{cond("ResolvedRefs", "True")},
			},
			map[string]interface{}{
				"parentRef": ref("name", "no-conditions"),
			},
			map[string]interface{}{
				"parentRef":  ref("name", "unknown-status"),
				"conditions": []interface{}{cond("Accepted", "Unknown")},
			},
			map[string]interface{}{"controllerName": "no-parent-ref"},
			"not-a-map",
		},
	})

	want := []ParentStatus{
		{ParentRef: ParentRef{Namespace: "team-a", Name: "gw"}, ControllerName: "example.com/gateway-controller", Accepted: true},
		{ParentRef: ParentRef{Group: "gateway.networking.k8s.io", Kind: "Gateway", Namespace: "infra", Name: "shared", SectionName: "https"}, ControllerName: "example.com/other"},
		{ParentRef: ParentRef{Namespace: "team-a", Name: "no-accepted-condition"}},
		{ParentRef: ParentRef{Namespace: "team-a", Name: "no-conditions"}},
		{ParentRef: ParentRef{Namespace: "team-a", Name: "unknown-status"}},
	}
	if got := ParentStatuses(route); !reflect.DeepEqual(got, want) {
		t.Errorf("ParentStatuses() = %#v, want %#v", got, want)
	}
	if got := ParentStatuses(nil); len(got) != 0 {
		t.Errorf("ParentStatuses(nil) = %#v, want empty", got)
	}
	if got := ParentStatuses(object("HTTPRoute", "ns", "r", nil, nil)); len(got) != 0 {
		t.Errorf("ParentStatuses(no status) = %#v, want empty", got)
	}
}

func TestAcceptedGateways(t *testing.T) {
	accepted := func(parentRef map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"parentRef": parentRef, "conditions": []interface{}{cond("Accepted", "True")}}
	}
	rejected := func(parentRef map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"parentRef": parentRef, "conditions": []interface{}{cond("Accepted", "False")}}
	}
	route := object("HTTPRoute", "team-a", "web", nil, map[string]interface{}{
		"parents": []interface{}{
			accepted(ref("namespace", "infra", "name", "zeta")),
			accepted(ref("name", "alpha", "sectionName", "http")),
			accepted(ref("name", "alpha", "sectionName", "https")), // same Gateway, other listener
			rejected(ref("name", "rejected")),
			accepted(ref("group", "", "kind", "Service", "name", "mesh-svc")),
			accepted(ref("group", "example.com", "kind", "Gateway", "name", "foreign")),
			accepted(ref("namespace", "infra", "name", "beta", "kind", "Gateway", "group", "gateway.networking.k8s.io")),
		},
	})

	want := []types.NamespacedName{
		{Namespace: "infra", Name: "beta"},
		{Namespace: "infra", Name: "zeta"},
		{Namespace: "team-a", Name: "alpha"},
	}
	if got := AcceptedGateways(route); !reflect.DeepEqual(got, want) {
		t.Errorf("AcceptedGateways() = %#v, want %#v", got, want)
	}
	if got := AcceptedGateways(nil); len(got) != 0 {
		t.Errorf("AcceptedGateways(nil) = %#v, want empty", got)
	}
}

func TestGatewayAddresses(t *testing.T) {
	gw := object("Gateway", "infra", "shared", nil, map[string]interface{}{
		"addresses": []interface{}{
			ref("type", "IPAddress", "value", "192.0.2.10"),
			ref("value", "2001:db8::10"), // missing type means IPAddress
			ref("type", "Hostname", "value", "lb.example.com"),
			ref("type", "NamedAddress", "value", "pool-1"),
			ref("type", "IPAddress", "value", ""), // dropped
			ref("type", "Hostname"),               // dropped
			"not-a-map",
		},
	})

	want := []Address{
		{Type: "IPAddress", Value: "192.0.2.10"},
		{Type: "IPAddress", Value: "2001:db8::10"},
		{Type: "Hostname", Value: "lb.example.com"},
		{Type: "NamedAddress", Value: "pool-1"},
	}
	if got := GatewayAddresses(gw); !reflect.DeepEqual(got, want) {
		t.Errorf("GatewayAddresses() = %#v, want %#v", got, want)
	}
	if got := GatewayAddresses(nil); len(got) != 0 {
		t.Errorf("GatewayAddresses(nil) = %#v, want empty", got)
	}
	if got := GatewayAddresses(object("Gateway", "infra", "shared", nil, nil)); len(got) != 0 {
		t.Errorf("GatewayAddresses(no status) = %#v, want empty", got)
	}
}
