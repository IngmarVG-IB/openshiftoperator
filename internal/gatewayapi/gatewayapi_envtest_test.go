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
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestGatewayAPICRDsEnvtest proves that the hand-written fixtures
// test/crds/gateway.networking.k8s.io_gateways.yaml and ..._httproutes.yaml load into a real
// envtest apiserver, that a Gateway and an HTTPRoute can be created and have their status
// written through the status subresource, that the accessors read the stored objects back
// correctly, and that Available reports the Gateway API as present once both CRDs are
// installed (and as absent before). There is no generated Go type here at all, only
// *unstructured.Unstructured built from the GVKs, which is how the controller reads them.
func TestGatewayAPICRDsEnvtest(t *testing.T) {
	if testing.Short() {
		t.Skip("envtest starts a real kube-apiserver; skipped in -short mode")
	}

	crdDir := filepath.Join("..", "..", "test", "crds")
	env := &envtest.Environment{
		// Only the two Gateway API fixtures, so the test does not depend on the other
		// fixtures in the directory.
		CRDDirectoryPaths: []string{
			filepath.Join(crdDir, "gateway.networking.k8s.io_gateways.yaml"),
			filepath.Join(crdDir, "gateway.networking.k8s.io_httproutes.yaml"),
		},
		ErrorIfCRDPathMissing: true,
	}
	if dir := firstEnvtestBinaryDir(); dir != "" {
		env.BinaryAssetsDirectory = dir
	}

	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("envtest Start: %v", err)
	}
	t.Cleanup(func() {
		if stopErr := env.Stop(); stopErr != nil {
			t.Errorf("envtest Stop: %v", stopErr)
		}
	})

	available, err := Available(cfg)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if !available {
		t.Errorf("Available = false, want true once the Gateway and HTTPRoute CRDs are installed")
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgoscheme.AddToScheme: %v", err)
	}
	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	ctx := context.Background()

	if err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "gw-infra"}}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	// A Gateway in another namespace, as a platform team would run it, reporting one address
	// of each kind. The IPv6 entry leaves out type, which the real CRD defaults to IPAddress.
	gw := &unstructured.Unstructured{}
	gw.SetGroupVersionKind(GatewayGVK)
	gw.SetNamespace("gw-infra")
	gw.SetName("shared")
	if err := unstructured.SetNestedField(gw.Object, "example", "spec", "gatewayClassName"); err != nil {
		t.Fatalf("set Gateway spec: %v", err)
	}
	if err := k8sClient.Create(ctx, gw); err != nil {
		t.Fatalf("create Gateway: %v", err)
	}
	if err := unstructured.SetNestedSlice(gw.Object, []interface{}{
		map[string]interface{}{"type": "IPAddress", "value": "192.0.2.10"},
		map[string]interface{}{"value": "2001:db8::10"},
		map[string]interface{}{"type": "Hostname", "value": "lb.example.com"},
	}, "status", "addresses"); err != nil {
		t.Fatalf("set Gateway status.addresses: %v", err)
	}
	if err := k8sClient.Status().Update(ctx, gw); err != nil {
		t.Fatalf("update Gateway status: %v", err)
	}

	// An HTTPRoute attached to that Gateway (a cross-namespace parentRef) and to a second
	// parent that has not accepted it.
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(HTTPRouteGVK)
	route.SetNamespace("default")
	route.SetName("web")
	if err := unstructured.SetNestedStringSlice(route.Object, []string{"web.example.com", "*.apps.example.com"}, "spec", "hostnames"); err != nil {
		t.Fatalf("set HTTPRoute hostnames: %v", err)
	}
	if err := unstructured.SetNestedSlice(route.Object, []interface{}{
		map[string]interface{}{"name": "shared", "namespace": "gw-infra", "sectionName": "https"},
		map[string]interface{}{"name": "local"},
	}, "spec", "parentRefs"); err != nil {
		t.Fatalf("set HTTPRoute parentRefs: %v", err)
	}
	if err := k8sClient.Create(ctx, route); err != nil {
		t.Fatalf("create HTTPRoute: %v", err)
	}
	if err := unstructured.SetNestedSlice(route.Object, []interface{}{
		map[string]interface{}{
			"parentRef": map[string]interface{}{
				"group": "gateway.networking.k8s.io", "kind": "Gateway",
				"name": "shared", "namespace": "gw-infra", "sectionName": "https",
			},
			"controllerName": "example.com/gateway-controller",
			"conditions": []interface{}{
				map[string]interface{}{"type": "Accepted", "status": "True", "reason": "Accepted", "message": "", "lastTransitionTime": "2026-10-04T00:00:00Z"},
			},
		},
		map[string]interface{}{
			"parentRef":      map[string]interface{}{"name": "local"},
			"controllerName": "example.com/gateway-controller",
			"conditions": []interface{}{
				map[string]interface{}{"type": "Accepted", "status": "False", "reason": "NotAllowedByListeners", "message": "", "lastTransitionTime": "2026-10-04T00:00:00Z"},
			},
		},
	}, "status", "parents"); err != nil {
		t.Fatalf("set HTTPRoute status.parents: %v", err)
	}
	if err := k8sClient.Status().Update(ctx, route); err != nil {
		t.Fatalf("update HTTPRoute status: %v", err)
	}

	gotGW := &unstructured.Unstructured{}
	gotGW.SetGroupVersionKind(GatewayGVK)
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(gw), gotGW); err != nil {
		t.Fatalf("get Gateway: %v", err)
	}
	gotRoute := &unstructured.Unstructured{}
	gotRoute.SetGroupVersionKind(HTTPRouteGVK)
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(route), gotRoute); err != nil {
		t.Fatalf("get HTTPRoute: %v", err)
	}

	wantAddresses := []Address{
		{Type: AddressTypeIPAddress, Value: "192.0.2.10"},
		{Type: AddressTypeIPAddress, Value: "2001:db8::10"},
		{Type: AddressTypeHostname, Value: "lb.example.com"},
	}
	if got := GatewayAddresses(gotGW); !reflect.DeepEqual(got, wantAddresses) {
		t.Errorf("GatewayAddresses() = %#v, want %#v", got, wantAddresses)
	}

	if got, want := Hostnames(gotRoute), []string{"web.example.com", "*.apps.example.com"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Hostnames() = %#v, want %#v", got, want)
	}

	wantRefs := []ParentRef{
		{Namespace: "gw-infra", Name: "shared", SectionName: "https"},
		{Namespace: "default", Name: "local"},
	}
	if got := ParentRefs(gotRoute); !reflect.DeepEqual(got, wantRefs) {
		t.Errorf("ParentRefs() = %#v, want %#v", got, wantRefs)
	}

	wantStatuses := []ParentStatus{
		{
			ParentRef:      ParentRef{Group: Group, Kind: "Gateway", Namespace: "gw-infra", Name: "shared", SectionName: "https"},
			ControllerName: "example.com/gateway-controller",
			Accepted:       true,
		},
		{
			ParentRef:      ParentRef{Namespace: "default", Name: "local"},
			ControllerName: "example.com/gateway-controller",
		},
	}
	if got := ParentStatuses(gotRoute); !reflect.DeepEqual(got, wantStatuses) {
		t.Errorf("ParentStatuses() = %#v, want %#v", got, wantStatuses)
	}

	wantAccepted := []types.NamespacedName{{Namespace: "gw-infra", Name: "shared"}}
	if got := AcceptedGateways(gotRoute); !reflect.DeepEqual(got, wantAccepted) {
		t.Errorf("AcceptedGateways() = %#v, want %#v", got, wantAccepted)
	}
}

// TestAvailableTransitionsEnvtest proves Available against a real apiserver as the Gateway
// API arrives: false with no error while the group is not served at all (the apiserver
// answers 404), still false with only the Gateway CRD installed, true once HTTPRoute is
// installed as well.
func TestAvailableTransitionsEnvtest(t *testing.T) {
	if testing.Short() {
		t.Skip("envtest starts a real kube-apiserver; skipped in -short mode")
	}

	env := &envtest.Environment{}
	if dir := firstEnvtestBinaryDir(); dir != "" {
		env.BinaryAssetsDirectory = dir
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("envtest Start: %v", err)
	}
	t.Cleanup(func() {
		if stopErr := env.Stop(); stopErr != nil {
			t.Errorf("envtest Stop: %v", stopErr)
		}
	})

	crdDir := filepath.Join("..", "..", "test", "crds")
	steps := []struct {
		name string
		crd  string // fixture installed before the check; empty for none
		want bool
	}{
		{name: "no Gateway API", want: false},
		{name: "Gateway CRD only", crd: "gateway.networking.k8s.io_gateways.yaml", want: false},
		{name: "Gateway and HTTPRoute CRDs", crd: "gateway.networking.k8s.io_httproutes.yaml", want: true},
	}
	for _, step := range steps {
		if step.crd != "" {
			_, err := envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{
				Paths:              []string{filepath.Join(crdDir, step.crd)},
				ErrorIfPathMissing: true,
			})
			if err != nil {
				t.Fatalf("%s: install %s: %v", step.name, step.crd, err)
			}
		}
		got, err := Available(cfg)
		if err != nil {
			t.Fatalf("%s: Available: %v", step.name, err)
		}
		if got != step.want {
			t.Errorf("%s: Available = %v, want %v", step.name, got, step.want)
		}
	}
}

// firstEnvtestBinaryDir mirrors getFirstFoundEnvTestBinaryDir in
// internal/controller/suite_test.go (and internal/nmstateapi/nmstateapi_envtest_test.go): it
// locates the envtest binaries so `go test` finds them the same way `make test` does (via
// the KUBEBUILDER_ASSETS environment variable, which envtest.Environment honours on its
// own when BinaryAssetsDirectory is left unset) and, as a fallback for running the test
// directly from an IDE, the first version directory under bin/k8s.
func firstEnvtestBinaryDir() string {
	base := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(base, e.Name())
		}
	}
	return ""
}
