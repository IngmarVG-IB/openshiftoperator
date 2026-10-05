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

// The envtest test lives in an external test package so it can drive the
// internal/ipalloc view and merge patches against the real API server without an import
// cycle (internal/ipalloc imports this package).
package egressip_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/IngmarVG-IB/openshiftoperator/internal/egressip"
	"github.com/IngmarVG-IB/openshiftoperator/internal/ipalloc"
)

// TestEgressIPCRDEnvtest proves that test/crds/k8s.ovn.org_egressips.yaml loads into a
// real envtest API server, that an EgressIP waiting for addresses (egressIPs: [], the
// allocate and count annotations) can be created and read back as unstructured data
// through egressip.New, that ipalloc.ViewEgressIP reads it as the expected claim, that
// ipalloc.EgressIPsPatch and ipalloc.ClearEgressIPsPatch are accepted as JSON merge
// patches (the clear keeps the required field as an empty list), and that Available
// reports the API once the CRD is installed.
func TestEgressIPCRDEnvtest(t *testing.T) {
	if testing.Short() {
		t.Skip("envtest starts a real kube-apiserver; skipped in -short mode")
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "test", "crds")},
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

	eip := egressip.New()
	eip.SetName("egress-prod")
	eip.SetLabels(map[string]string{"env": "prod"})
	eip.SetAnnotations(map[string]string{"uddi.infoblox.com/allocate": "true", "uddi.infoblox.com/count": "2"})
	if err := unstructured.SetNestedField(eip.Object, map[string]interface{}{
		"egressIPs":         []interface{}{},
		"namespaceSelector": map[string]interface{}{"matchLabels": map[string]interface{}{"env": "prod"}},
	}, "spec"); err != nil {
		t.Fatalf("SetNestedField spec: %v", err)
	}
	if err := k8sClient.Create(ctx, eip); err != nil {
		t.Fatalf("create EgressIP: %v", err)
	}

	got := egressip.New()
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(eip), got); err != nil {
		t.Fatalf("get EgressIP: %v", err)
	}
	if got.GetNamespace() != "" {
		t.Errorf("namespace = %q, want cluster-scoped", got.GetNamespace())
	}
	claim := ipalloc.ViewEgressIP(got, "")
	want := ipalloc.Claim{
		Target:   ipalloc.TargetRef{Kind: ipalloc.TargetEgressIP, Name: "egress-prod", UID: got.GetUID()},
		Eligible: true,
		OptIn:    claim.OptIn,
		Slots:    make([]ipalloc.SlotRequest, 2),
		Labels:   map[string]string{"env": "prod"},
	}
	if claim.OptIn == nil || !*claim.OptIn || !reflect.DeepEqual(claim, want) {
		t.Fatalf("ViewEgressIP() = %#v\nwant %#v", claim, want)
	}

	values := []string{"192.168.66.20", "192.168.66.21"}
	if err := k8sClient.Patch(ctx, got, client.RawPatch(types.MergePatchType, ipalloc.EgressIPsPatch(values))); err != nil {
		t.Fatalf("patch spec.egressIPs: %v", err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(eip), got); err != nil {
		t.Fatalf("get EgressIP: %v", err)
	}
	if ips, found, err := egressip.EgressIPs(got); err != nil || !found || !reflect.DeepEqual(ips, values) {
		t.Fatalf("EgressIPs() after set = %v, %v, %v; want %v", ips, found, err, values)
	}

	if err := k8sClient.Patch(ctx, got, client.RawPatch(types.MergePatchType, ipalloc.ClearEgressIPsPatch())); err != nil {
		t.Fatalf("clear spec.egressIPs: %v", err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(eip), got); err != nil {
		t.Fatalf("get EgressIP: %v", err)
	}
	if ips, found, err := egressip.EgressIPs(got); err != nil || !found || len(ips) != 0 {
		t.Fatalf("EgressIPs() after clear = %v, %v, %v; want an empty, present list", ips, found, err)
	}

	list := egressip.NewList()
	if err := k8sClient.List(ctx, list); err != nil || len(list.Items) != 1 {
		t.Fatalf("list EgressIPs = %d items, %v", len(list.Items), err)
	}

	available, err := egressip.Available(cfg)
	if err != nil || !available {
		t.Fatalf("Available = %v, %v; want true once the EgressIP CRD is installed", available, err)
	}
}

// firstEnvtestBinaryDir mirrors internal/nmstateapi's helper: KUBEBUILDER_ASSETS (set by
// make test) wins inside envtest itself; this fallback finds bin/k8s for IDE runs.
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
