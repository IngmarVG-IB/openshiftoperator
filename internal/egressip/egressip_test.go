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

package egressip

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

func TestGVK(t *testing.T) {
	if GVK.Group != "k8s.ovn.org" || GVK.Version != "v1" || GVK.Kind != "EgressIP" {
		t.Errorf("GVK = %v", GVK)
	}
	if ListGVK.GroupVersion() != GVK.GroupVersion() || ListGVK.Kind != "EgressIPList" {
		t.Errorf("ListGVK = %v", ListGVK)
	}
	if got := New().GroupVersionKind(); got != GVK {
		t.Errorf("New() GVK = %v", got)
	}
	if got := NewList().GroupVersionKind(); got != ListGVK {
		t.Errorf("NewList() GVK = %v", got)
	}
}

// TestAvailable exercises Available's discovery-response handling against a fake
// apiserver. The envtest round trip in egressip_envtest_test.go proves the "true" case
// against the CRD fixture.
func TestAvailable(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    bool
		wantErr bool
	}{
		{
			name:   "group version served with egressips",
			status: http.StatusOK,
			body: `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"k8s.ovn.org/v1","resources":[
				{"name":"egressips","singularName":"egressip","namespaced":false,"kind":"EgressIP","verbs":["get","list","patch","watch"]}
			]}`,
			want: true,
		},
		{
			name:   "group version served without egressips",
			status: http.StatusOK,
			body: `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"k8s.ovn.org/v1","resources":[
				{"name":"egressfirewalls","singularName":"egressfirewall","namespaced":true,"kind":"EgressFirewall","verbs":["get"]}
			]}`,
			want: false,
		},
		{
			name:   "group version not found",
			status: http.StatusNotFound,
			body: `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure",
				"message":"the server could not find the requested resource","reason":"NotFound","details":{},"code":404}`,
			want: false,
		},
		{
			name:    "server error",
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
	cfg := &rest.Config{
		Host:            "https://127.0.0.1:0",
		TLSClientConfig: rest.TLSClientConfig{CertData: []byte("not-a-cert")},
	}
	if _, err := Available(cfg); err == nil {
		t.Fatalf("Available: want error for an invalid client config, got nil")
	}
}

func TestEgressIPs(t *testing.T) {
	withSpec := func(spec interface{}) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{"spec": spec}}
	}
	cases := []struct {
		name      string
		obj       *unstructured.Unstructured
		want      []string
		wantFound bool
		wantErr   bool
	}{
		{name: "nil object"},
		{name: "no spec", obj: &unstructured.Unstructured{Object: map[string]interface{}{}}},
		{name: "spec without egressIPs", obj: withSpec(map[string]interface{}{})},
		{name: "null egressIPs", obj: withSpec(map[string]interface{}{"egressIPs": nil})},
		{name: "empty list", obj: withSpec(map[string]interface{}{"egressIPs": []interface{}{}}), wantFound: true},
		{
			name:      "addresses trimmed, empties dropped, order kept",
			obj:       withSpec(map[string]interface{}{"egressIPs": []interface{}{" 10.0.0.6", "", "10.0.0.5 "}}),
			want:      []string{"10.0.0.6", "10.0.0.5"},
			wantFound: true,
		},
		{name: "not a list", obj: withSpec(map[string]interface{}{"egressIPs": "10.0.0.5"}), wantFound: true, wantErr: true},
		{name: "not strings", obj: withSpec(map[string]interface{}{"egressIPs": []interface{}{true}}), wantFound: true, wantErr: true},
		{name: "spec is not a map", obj: withSpec("x"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found, err := EgressIPs(tc.obj)
			if (err != nil) != tc.wantErr {
				t.Fatalf("EgressIPs() error = %v, wantErr %v", err, tc.wantErr)
			}
			if found != tc.wantFound || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("EgressIPs() = %#v, %v; want %#v, %v", got, found, tc.want, tc.wantFound)
			}
		})
	}
}
