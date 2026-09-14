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

package routeapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
)

func TestAddToScheme(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if !s.Recognizes(routev1.GroupVersion.WithKind("Route")) {
		t.Errorf("scheme does not recognize Route after AddToScheme")
	}
	if !s.Recognizes(routev1.GroupVersion.WithKind("RouteList")) {
		t.Errorf("scheme does not recognize RouteList after AddToScheme")
	}
}

// TestAvailable exercises Available's discovery-response handling against a fake
// apiserver, without needing a real cluster. The envtest-backed round trip in
// routeapi_envtest_test.go additionally proves the "true" case against the real CRD.
func TestAvailable(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    bool
		wantErr bool
	}{
		{
			name:   "group version served with routes resource",
			status: http.StatusOK,
			body: `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"route.openshift.io/v1","resources":[
				{"name":"routes","singularName":"route","namespaced":true,"kind":"Route","verbs":["get","list","create","update","delete","watch"]}
			]}`,
			want: true,
		},
		{
			name:   "group version served without a routes resource",
			status: http.StatusOK,
			body: `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"route.openshift.io/v1","resources":[
				{"name":"routestatuses","singularName":"routestatus","namespaced":true,"kind":"RouteStatus","verbs":["get"]}
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
	// than reporting the Route API as merely absent.
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
