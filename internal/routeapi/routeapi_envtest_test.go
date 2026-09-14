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
	"context"
	"os"
	"path/filepath"
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestRouteCRDEnvtest proves that test/crds/route.openshift.io_routes.yaml (see its
// README for provenance) loads into a real envtest apiserver, that AddToScheme lets a
// controller-runtime client round-trip a Route through it, and that Available reports
// the Route API as present once the CRD is installed. This is the practical half of
// DESIGN.md 4.7's "the Route API is registered from github.com/openshift/api/route/v1
// and its presence is checked once with discovery at startup" behaviour; the discovery
// response parsing itself (including the "no Route API here" case) is unit-tested
// against a fake apiserver in TestAvailable.
func TestRouteCRDEnvtest(t *testing.T) {
	if testing.Short() {
		t.Skip("envtest starts a real kube-apiserver; skipped in -short mode")
	}

	env := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "test", "crds"),
			filepath.Join("..", "..", "config", "crd", "bases"),
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

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgoscheme.AddToScheme: %v", err)
	}
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "routeapi-test-"}}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns.Name, Name: "web"},
		Spec: routev1.RouteSpec{
			Host: "app.example.com",
			To:   routev1.RouteTargetReference{Kind: "Service", Name: "web"},
		},
	}
	if err := k8sClient.Create(ctx, route); err != nil {
		t.Fatalf("create route: %v", err)
	}

	got := &routev1.Route{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(route), got); err != nil {
		t.Fatalf("get route: %v", err)
	}
	if got.Spec.Host != "app.example.com" {
		t.Errorf("got.Spec.Host = %q, want %q", got.Spec.Host, "app.example.com")
	}
	if got.Spec.To.Name != "web" {
		t.Errorf("got.Spec.To.Name = %q, want %q", got.Spec.To.Name, "web")
	}
	if got.UID == "" {
		t.Errorf("got.UID is empty; the apiserver did not assign one")
	}

	available, err := Available(cfg)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if !available {
		t.Errorf("Available = false, want true once the Route CRD is installed")
	}
}

// firstEnvtestBinaryDir mirrors getFirstFoundEnvTestBinaryDir in
// internal/controller/suite_test.go: it locates the envtest binaries so `go test` finds
// them the same way `make test` does (via the KUBEBUILDER_ASSETS environment variable,
// which envtest.Environment honours on its own when BinaryAssetsDirectory is left unset)
// and, as a fallback for running the test directly from an IDE, the first version
// directory under bin/k8s.
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
