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
	"context"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestNodeNetworkStateCRDEnvtest proves that test/crds/nmstate.io_nodenetworkstates.yaml
// (see its README entry for provenance) loads into a real envtest apiserver, that a
// NodeNetworkState can be created and have its status.currentState written through the
// status subresource, that CurrentState reads it back correctly, and that Available
// reports the NMState API as present once the CRD is installed. This is the practical
// half of DESIGN.md 4.7's "NodeNetworkState is read as unstructured data; the operator
// takes no dependency on the NMState Go module" behaviour: unlike routeapi's envtest test,
// there is no generated Go type here at all, only *unstructured.Unstructured built
// directly from GVK. The discovery response parsing itself is unit-tested against a fake
// apiserver in TestAvailable.
func TestNodeNetworkStateCRDEnvtest(t *testing.T) {
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

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	ctx := context.Background()

	// NodeNetworkState is cluster-scoped and named after the Node it reports on; no
	// namespace, and no dependency on the NMState Go module (see GVK's doc comment) — the
	// object is built directly as unstructured data the way the controller will read it.
	nns := &unstructured.Unstructured{}
	nns.SetGroupVersionKind(GVK)
	nns.SetName("node01")
	if err := k8sClient.Create(ctx, nns); err != nil {
		t.Fatalf("create NodeNetworkState: %v", err)
	}

	// currentState mirrors the shape documented at
	// https://raw.githubusercontent.com/nmstate/kubernetes-nmstate/main/docs/content/user-guide/101-reporting.md,
	// written through the status subresource the way the real NMState handler does.
	currentState := map[string]interface{}{
		"interfaces": []interface{}{
			map[string]interface{}{
				"name":        "eth0",
				"type":        "ethernet",
				"state":       "up",
				"mac-address": "52:55:00:D1:55:01",
				"ipv4": map[string]interface{}{
					"address": []interface{}{
						map[string]interface{}{"ip": "192.168.66.101", "prefix-length": int64(24)},
					},
					"enabled": true,
				},
			},
		},
	}
	if err := unstructured.SetNestedMap(nns.Object, currentState, "status", "currentState"); err != nil {
		t.Fatalf("SetNestedMap status.currentState: %v", err)
	}
	if err := k8sClient.Status().Update(ctx, nns); err != nil {
		t.Fatalf("update NodeNetworkState status: %v", err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(GVK)
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(nns), got); err != nil {
		t.Fatalf("get NodeNetworkState: %v", err)
	}

	readBack, ok := CurrentState(got)
	if !ok {
		t.Fatalf("CurrentState() ok = false, want true")
	}
	interfaces, _ := readBack["interfaces"].([]interface{})
	if len(interfaces) != 1 {
		t.Fatalf("CurrentState() interfaces = %#v, want exactly one entry", readBack["interfaces"])
	}
	iface, _ := interfaces[0].(map[string]interface{})
	if got, want := iface["mac-address"], "52:55:00:D1:55:01"; got != want {
		t.Errorf("CurrentState() interfaces[0][\"mac-address\"] = %v, want %v", got, want)
	}

	available, err := Available(cfg)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if !available {
		t.Errorf("Available = false, want true once the NodeNetworkState CRD is installed")
	}
}

// firstEnvtestBinaryDir mirrors getFirstFoundEnvTestBinaryDir in
// internal/controller/suite_test.go (and internal/routeapi/routeapi_envtest_test.go): it
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
