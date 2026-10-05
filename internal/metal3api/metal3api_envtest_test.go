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

package metal3api

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestBareMetalHostCRDEnvtest proves that test/crds/metal3.io_baremetalhosts.yaml loads into
// a real envtest apiserver, that a BareMetalHost can be created and have status.hardware
// written through the status subresource, that listing it as unstructured data and reading
// it through Index gives the MACs the controller needs, and that Available reports the
// Metal3 API as present once the CRD is installed. The discovery response parsing is
// unit-tested against a fake apiserver in TestAvailable.
func TestBareMetalHostCRDEnvtest(t *testing.T) {
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

	// BareMetalHost is namespaced; the OpenShift platform keeps them in openshift-machine-api.
	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(DefaultNamespace)
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace %s: %v", DefaultNamespace, err)
	}

	host := &unstructured.Unstructured{}
	host.SetGroupVersionKind(GVK)
	host.SetNamespace(DefaultNamespace)
	host.SetName("worker-0")
	if err := unstructured.SetNestedField(host.Object, "52:55:00:d1:55:01", "spec", "bootMACAddress"); err != nil {
		t.Fatalf("set spec.bootMACAddress: %v", err)
	}
	if err := k8sClient.Create(ctx, host); err != nil {
		t.Fatalf("create BareMetalHost: %v", err)
	}

	// status.hardware as the baremetal-operator writes it after inspection.
	hardware := map[string]interface{}{
		"hostname": "worker-0.example.com",
		"nics": []interface{}{
			map[string]interface{}{"name": "eno1", "mac": "52:55:00:D1:55:02", "ip": "192.168.66.101"},
			map[string]interface{}{"name": "eno1", "mac": "52:55:00:D1:55:02", "ip": "2001:db8::101"},
			map[string]interface{}{"name": "eno2", "mac": "52:55:00:D1:55:03", "ip": "10.20.0.101"},
		},
	}
	if err := unstructured.SetNestedMap(host.Object, hardware, "status", "hardware"); err != nil {
		t.Fatalf("set status.hardware: %v", err)
	}
	if err := k8sClient.Status().Update(ctx, host); err != nil {
		t.Fatalf("update BareMetalHost status: %v", err)
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(ListGVK)
	if err := k8sClient.List(ctx, list, client.InNamespace(DefaultNamespace)); err != nil {
		t.Fatalf("list BareMetalHosts: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("listed %d BareMetalHosts, want 1", len(list.Items))
	}

	idx := NewIndex(list.Items)

	mac, hostName, ok := idx.MACForNode("192.168.66.101", "something-else")
	if !ok || mac != "52:55:00:d1:55:02" || hostName != "worker-0" {
		t.Errorf("MACForNode by NIC IP = (%q, %q, %v), want (52:55:00:d1:55:02, worker-0, true)", mac, hostName, ok)
	}
	mac, _, ok = idx.MACForNode("172.16.0.1", "worker-0.example.com")
	if !ok || mac != "52:55:00:d1:55:01" {
		t.Errorf("MACForNode by hostname = (%q, %v), want the bootMACAddress 52:55:00:d1:55:01", mac, ok)
	}
	mac, _, ok = idx.MACForIP(netip.MustParseAddr("10.20.0.101"))
	if !ok || mac != "52:55:00:d1:55:03" {
		t.Errorf("MACForIP = (%q, %v), want 52:55:00:d1:55:03", mac, ok)
	}
	if _, _, ok := idx.MACForNode("172.16.0.1", "unknown"); ok {
		t.Errorf("MACForNode answered for a Node no host knows")
	}

	available, err := Available(cfg)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if !available {
		t.Errorf("Available = false, want true once the BareMetalHost CRD is installed")
	}
}

// firstEnvtestBinaryDir mirrors the helper of the same name in internal/nmstateapi's
// envtest test: it locates the envtest binaries for running the test directly, when
// KUBEBUILDER_ASSETS is not set.
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
