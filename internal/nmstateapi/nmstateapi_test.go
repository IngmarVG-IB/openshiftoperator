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

package nmstateapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

func TestGVK(t *testing.T) {
	if got, want := GVK.Group, "nmstate.io"; got != want {
		t.Errorf("GVK.Group = %q, want %q", got, want)
	}
	if got, want := GVK.Version, "v1beta1"; got != want {
		t.Errorf("GVK.Version = %q, want %q", got, want)
	}
	if got, want := GVK.Kind, "NodeNetworkState"; got != want {
		t.Errorf("GVK.Kind = %q, want %q", got, want)
	}
}

// TestAvailable exercises Available's discovery-response handling against a fake
// apiserver, without needing a real cluster. The envtest-backed round trip in
// nmstateapi_envtest_test.go additionally proves the "true" case against the real CRD.
func TestAvailable(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    bool
		wantErr bool
	}{
		{
			name:   "group version served with nodenetworkstates resource",
			status: http.StatusOK,
			body: `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"nmstate.io/v1beta1","resources":[
				{"name":"nodenetworkstates","singularName":"nodenetworkstate","namespaced":false,"kind":"NodeNetworkState","verbs":["get","list","create","update","delete","watch"]}
			]}`,
			want: true,
		},
		{
			name:   "group version served without a nodenetworkstates resource",
			status: http.StatusOK,
			body: `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"nmstate.io/v1beta1","resources":[
				{"name":"nodenetworkconfigurationpolicies","singularName":"nodenetworkconfigurationpolicy","namespaced":false,"kind":"NodeNetworkConfigurationPolicy","verbs":["get"]}
			]}`,
			want: false,
		},
		{
			name:   "group version not found",
			status: http.StatusNotFound,
			body: `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure",
				"message":"the server could not find the requested resource",
				"reason":"NotFound","details":{},"code":404}`,
			want: false,
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
	// than reporting the NMState API as merely absent.
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
// CurrentState
// -----------------------------------------------------------------------------------

func TestCurrentState(t *testing.T) {
	withCurrentState := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nmstate.io/v1beta1",
		"kind":       "NodeNetworkState",
		"metadata":   map[string]interface{}{"name": "node01"},
		"status": map[string]interface{}{
			"currentState": map[string]interface{}{
				"interfaces": []interface{}{
					map[string]interface{}{"name": "eth0", "type": "ethernet"},
				},
			},
		},
	}}

	noStatus := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nmstate.io/v1beta1",
		"kind":       "NodeNetworkState",
		"metadata":   map[string]interface{}{"name": "node01"},
	}}

	statusWithoutCurrentState := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{"lastSuccessfulUpdateTime": "2026-09-15T00:00:00Z"},
	}}

	currentStateWrongType := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{"currentState": "not-a-map"},
	}}

	cases := []struct {
		name   string
		obj    *unstructured.Unstructured
		wantOK bool
	}{
		{name: "status.currentState present", obj: withCurrentState, wantOK: true},
		{name: "nil object", obj: nil, wantOK: false},
		{name: "no status at all", obj: noStatus, wantOK: false},
		{name: "status present without currentState", obj: statusWithoutCurrentState, wantOK: false},
		{name: "currentState is not a map", obj: currentStateWrongType, wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := CurrentState(tc.obj)
			if ok != tc.wantOK {
				t.Fatalf("CurrentState() ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if _, found := m["interfaces"]; !found {
				t.Errorf("CurrentState() = %#v, want an \"interfaces\" key", m)
			}
		})
	}
}
