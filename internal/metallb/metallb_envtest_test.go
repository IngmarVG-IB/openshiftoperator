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

package metallb

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const mirrorNamespace = "metallb-system"

// TestIPAddressPoolCRDEnvtest proves that test/crds/metallb.io_ipaddresspools.yaml loads
// into a real envtest apiserver, that Available flips from false to true when it does, and
// that pools built by Desired are accepted by the apiserver (a CIDR, a range, an IPv6 pool
// and a name at the 253-character limit), read back as Equal to what was built even though
// the apiserver defaults spec fields, found again with Selector and pruned with Stale. It
// also proves that Desired is a usable server-side apply configuration and that an apply
// leaves spec fields another manager owns alone, which is what Equal's contract relies on.
func TestIPAddressPoolCRDEnvtest(t *testing.T) {
	if testing.Short() {
		t.Skip("envtest starts a real kube-apiserver; skipped in -short mode")
	}

	e := startPoolEnv(t)

	subnet := desire(t, "lb", Source{PoolKind: "Subnet", PoolName: "net", CIDR: "10.20.0.0/24"})
	rangePool := desire(t, "lb", Source{PoolKind: "Range", PoolName: "r", Start: "10.20.1.10", End: "10.20.1.50"})
	v6Pool := desire(t, "lb", Source{PoolKind: "AddressBlock", PoolName: "v6", CIDR: "2001:db8:0:1::/64"})
	longPool := desire(t, "lb", Source{PoolKind: "Subnet", PoolName: strings.Repeat("a", 250), CIDR: "10.20.2.0/24"})
	otherPolicy := desire(t, "other", Source{PoolKind: "Subnet", PoolName: "net", CIDR: "10.30.0.0/24"})
	for _, pool := range []*unstructured.Unstructured{subnet, rangePool, v6Pool, longPool, otherPolicy} {
		e.create(t, pool)
	}
	// A pool somebody created by hand in the same namespace.
	userPool := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{"addresses": []interface{}{"192.168.100.0/24"}},
	}}
	userPool.SetGroupVersionKind(GVK)
	userPool.SetNamespace(mirrorNamespace)
	userPool.SetName("user-pool")
	e.create(t, userPool)

	t.Run("a created pool reads back equal to Desired", func(t *testing.T) {
		e.checkReadBack(t, subnet)
	})
	t.Run("every kind of pool round-trips", func(t *testing.T) {
		for _, want := range []*unstructured.Unstructured{rangePool, v6Pool, longPool, otherPolicy} {
			if got := e.get(t, want.GetName()); !Equal(want, got) {
				t.Errorf("Equal(desired, live) = false for %q\ndesired: %v\nlive:    %v", want.GetName(), want.Object, got.Object)
			}
		}
		if got := len(longPool.GetName()); got != 253 {
			t.Errorf("long pool name has length %d, want 253", got)
		}
	})
	t.Run("a changed CIDR is detected and updating it makes the pool equal again", func(t *testing.T) {
		e.checkChangedCIDR(t, subnet)
	})
	t.Run("server-side apply of Desired leaves other managers' fields alone", func(t *testing.T) {
		e.checkServerSideApply(t)
	})
	// Last, because it deletes pools.
	t.Run("Selector lists exactly the policy's pools and Stale prunes the rest", func(t *testing.T) {
		e.checkSelectorAndStale(t, []*unstructured.Unstructured{subnet, rangePool}, []*unstructured.Unstructured{v6Pool, longPool})
	})
}

// poolEnv is a client against an envtest apiserver that serves the IPAddressPool CRD.
type poolEnv struct {
	client client.Client
}

// startPoolEnv starts envtest without any CRD, proves MetalLB is reported absent, installs
// the fixture CRD, proves it is reported present and creates the mirror namespace.
func startPoolEnv(t *testing.T) *poolEnv {
	t.Helper()

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

	available, err := Available(cfg)
	if err != nil {
		t.Fatalf("Available before the CRD is installed: %v", err)
	}
	if available {
		t.Fatalf("Available = true before the IPAddressPool CRD is installed, want false")
	}
	installPoolCRD(t, cfg)

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgoscheme.AddToScheme: %v", err)
	}
	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: mirrorNamespace}}
	if err := k8sClient.Create(context.Background(), ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	return &poolEnv{client: k8sClient}
}

func installPoolCRD(t *testing.T, cfg *rest.Config) {
	t.Helper()
	if _, err := envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{
		Paths:              []string{filepath.Join("..", "..", "test", "crds", "metallb.io_ipaddresspools.yaml")},
		ErrorIfPathMissing: true,
	}); err != nil {
		t.Fatalf("install the IPAddressPool CRD: %v", err)
	}
	err := wait.PollUntilContextTimeout(context.Background(), 100*time.Millisecond, 30*time.Second, true,
		func(context.Context) (bool, error) { return Available(cfg) })
	if err != nil {
		t.Fatalf("Available did not become true once the IPAddressPool CRD is installed: %v", err)
	}
}

// desire builds the pool of the policy apps/<policy> in the mirror namespace.
func desire(t *testing.T, policy string, src Source) *unstructured.Unstructured {
	t.Helper()
	pool, err := Desired("apps", policy, mirrorNamespace, src)
	if err != nil {
		t.Fatalf("Desired(apps/%s, %s/%s): %v", policy, src.PoolKind, src.PoolName, err)
	}
	return pool
}

func names(pools []unstructured.Unstructured) []string {
	out := make([]string, 0, len(pools))
	for i := range pools {
		out = append(out, pools[i].GetName())
	}
	return out
}

func (e *poolEnv) create(t *testing.T, pool *unstructured.Unstructured) {
	t.Helper()
	// Create writes the server's response back into the object, so create a copy and keep
	// the built object as the reference.
	if err := e.client.Create(context.Background(), pool.DeepCopy()); err != nil {
		t.Fatalf("create IPAddressPool %q: %v", pool.GetName(), err)
	}
}

func (e *poolEnv) get(t *testing.T, name string) *unstructured.Unstructured {
	t.Helper()
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(GVK)
	if err := e.client.Get(context.Background(), client.ObjectKey{Namespace: mirrorNamespace, Name: name}, got); err != nil {
		t.Fatalf("get IPAddressPool %q: %v", name, err)
	}
	return got
}

func (e *poolEnv) list(t *testing.T, opts ...client.ListOption) []unstructured.Unstructured {
	t.Helper()
	pools := &unstructured.UnstructuredList{}
	pools.SetGroupVersionKind(ListGVK)
	opts = append([]client.ListOption{client.InNamespace(mirrorNamespace)}, opts...)
	if err := e.client.List(context.Background(), pools, opts...); err != nil {
		t.Fatalf("list IPAddressPools: %v", err)
	}
	return pools.Items
}

// apply server-side applies a copy of pool the way the controller is expected to.
func (e *poolEnv) apply(t *testing.T, pool *unstructured.Unstructured) {
	t.Helper()
	cfg := client.ApplyConfigurationFromUnstructured(pool.DeepCopy())
	if err := e.client.Apply(context.Background(), cfg, client.FieldOwner("uddi-operator"), client.ForceOwnership); err != nil {
		t.Fatalf("apply IPAddressPool %q: %v", pool.GetName(), err)
	}
}

func (e *poolEnv) checkReadBack(t *testing.T, want *unstructured.Unstructured) {
	t.Helper()
	got := e.get(t, want.GetName())
	if !Equal(want, got) {
		t.Errorf("Equal(desired, live) = false\ndesired: %v\nlive:    %v", want.Object, got.Object)
	}
	if autoAssign, found, err := unstructured.NestedBool(got.Object, "spec", "autoAssign"); err != nil || !found || autoAssign {
		t.Errorf("live spec.autoAssign = %v (found=%v, err=%v), want an explicit false", autoAssign, found, err)
	}
	// The apiserver defaulted a field Desired does not set; Equal has to look past it.
	if _, found, _ := unstructured.NestedBool(got.Object, "spec", "avoidBuggyIPs"); !found {
		t.Errorf("live spec.avoidBuggyIPs was not defaulted; the fixture no longer exercises server-side defaulting")
	}
	addresses, _, _ := unstructured.NestedStringSlice(got.Object, "spec", "addresses")
	if wantAddresses := []string{"10.20.0.0/24"}; !reflect.DeepEqual(addresses, wantAddresses) {
		t.Errorf("live spec.addresses = %v, want %v", addresses, wantAddresses)
	}
	if !OwnedBy(got, "apps", "lb") {
		t.Errorf("live pool is not OwnedBy its policy; labels = %v", got.GetLabels())
	}
	namespaces, _, _ := unstructured.NestedStringSlice(got.Object, "spec", "serviceAllocation", "namespaces")
	if !reflect.DeepEqual(namespaces, []string{"apps"}) {
		t.Errorf("live spec.serviceAllocation.namespaces = %v, want [apps]", namespaces)
	}
}

func (e *poolEnv) checkChangedCIDR(t *testing.T, subnet *unstructured.Unstructured) {
	t.Helper()
	changed := desire(t, "lb", Source{PoolKind: "Subnet", PoolName: "net", CIDR: "10.21.0.0/24"})
	live := e.get(t, subnet.GetName())
	if Equal(changed, live) {
		t.Fatalf("Equal(changed, live) = true for a different CIDR")
	}
	live.SetLabels(changed.GetLabels())
	if err := unstructured.SetNestedSlice(live.Object, []interface{}{"10.21.0.0/24"}, "spec", "addresses"); err != nil {
		t.Fatalf("SetNestedSlice: %v", err)
	}
	if err := e.client.Update(context.Background(), live); err != nil {
		t.Fatalf("update IPAddressPool: %v", err)
	}
	if got := e.get(t, subnet.GetName()); !Equal(changed, got) {
		t.Errorf("Equal(changed, live) = false after the update\nlive: %v", got.Object)
	}
}

func (e *poolEnv) checkServerSideApply(t *testing.T) {
	t.Helper()
	first := desire(t, "ssa", Source{PoolKind: "Subnet", PoolName: "net", CIDR: "10.40.0.0/24"})
	e.apply(t, first)
	live := e.get(t, first.GetName())
	if !Equal(first, live) {
		t.Fatalf("Equal(desired, live) = false after apply\nlive: %v", live.Object)
	}

	// Somebody else tunes the pool.
	tuned := live.DeepCopy()
	if err := unstructured.SetNestedField(tuned.Object, int64(5), "spec", "serviceAllocation", "priority"); err != nil {
		t.Fatalf("SetNestedField: %v", err)
	}
	if err := e.client.Patch(context.Background(), tuned, client.MergeFrom(live), client.FieldOwner("someone-else")); err != nil {
		t.Fatalf("tune the pool: %v", err)
	}
	if got := e.get(t, first.GetName()); !Equal(first, got) {
		t.Errorf("another manager's serviceAllocation made Equal false")
	}

	// The pool's CIDR changes and the controller applies the new Desired.
	second := desire(t, "ssa", Source{PoolKind: "Subnet", PoolName: "net", CIDR: "10.41.0.0/24"})
	if Equal(second, e.get(t, first.GetName())) {
		t.Fatalf("Equal(second, live) = true before the second apply")
	}
	e.apply(t, second)
	got := e.get(t, first.GetName())
	if !Equal(second, got) {
		t.Errorf("Equal(second, live) = false after the second apply\nlive: %v", got.Object)
	}
	if priority, found, _ := unstructured.NestedInt64(got.Object, "spec", "serviceAllocation", "priority"); !found || priority != 5 {
		t.Errorf("applying Desired dropped spec.serviceAllocation.priority (found=%v, value=%d)", found, priority)
	}

	// Somebody widens the pool to another namespace; the next apply narrows it again.
	widened := got.DeepCopy()
	if err := unstructured.SetNestedStringSlice(widened.Object, []string{"apps", "other"}, "spec", "serviceAllocation", "namespaces"); err != nil {
		t.Fatalf("SetNestedStringSlice: %v", err)
	}
	if err := e.client.Patch(context.Background(), widened, client.MergeFrom(got), client.FieldOwner("someone-else")); err != nil {
		t.Fatalf("widen the pool: %v", err)
	}
	if Equal(second, e.get(t, first.GetName())) {
		t.Fatalf("Equal(second, live) = true for a pool widened to another namespace")
	}
	e.apply(t, second)
	got = e.get(t, first.GetName())
	if namespaces, _, _ := unstructured.NestedStringSlice(got.Object, "spec", "serviceAllocation", "namespaces"); !reflect.DeepEqual(namespaces, []string{"apps"}) {
		t.Errorf("applying Desired left spec.serviceAllocation.namespaces = %v, want [apps]", namespaces)
	}
}

// checkSelectorAndStale expects the pools of apps/lb to be keep plus drop, alongside the
// pool of another policy, the user's pool and the pool the apply check created.
func (e *poolEnv) checkSelectorAndStale(t *testing.T, keep, drop []*unstructured.Unstructured) {
	t.Helper()
	selector := client.MatchingLabelsSelector{Selector: Selector("apps", "lb")}

	mine := e.list(t, selector)
	if got, want := len(mine), len(keep)+len(drop); got != want {
		t.Fatalf("listed %d pools of apps/lb (%v), want %d", got, names(mine), want)
	}
	desired := map[string]bool{}
	for _, pool := range keep {
		desired[pool.GetName()] = true
	}
	wantStale := make([]string, 0, len(drop))
	for _, pool := range drop {
		wantStale = append(wantStale, pool.GetName())
	}
	slices.Sort(wantStale)

	got := Stale("apps", "lb", mine, desired)
	if !reflect.DeepEqual(got, wantStale) {
		t.Errorf("Stale over the selected list = %v, want %v", got, wantStale)
	}
	// Listing without the selector must not change the answer: other policies' pools and
	// the user's pool are never stale for apps/lb.
	all := e.list(t)
	if len(all) != len(mine)+3 {
		t.Fatalf("listed %d pools in total (%v), want %d", len(all), names(all), len(mine)+3)
	}
	if got := Stale("apps", "lb", all, desired); !reflect.DeepEqual(got, wantStale) {
		t.Errorf("Stale over the unfiltered list = %v, want %v", got, wantStale)
	}

	for _, name := range got {
		stale := &unstructured.Unstructured{}
		stale.SetGroupVersionKind(GVK)
		stale.SetNamespace(mirrorNamespace)
		stale.SetName(name)
		if err := e.client.Delete(context.Background(), stale); err != nil {
			t.Fatalf("delete stale pool %q: %v", name, err)
		}
	}
	remaining := e.list(t, selector)
	if len(remaining) != len(keep) || len(Stale("apps", "lb", remaining, desired)) != 0 {
		t.Errorf("after pruning, apps/lb still has %v", names(remaining))
	}
	if left := e.list(t); len(left) != len(all)-len(drop) {
		t.Errorf("pruning touched pools of other owners: %v", names(left))
	}
}

// firstEnvtestBinaryDir mirrors getFirstFoundEnvTestBinaryDir in
// internal/controller/suite_test.go (and internal/nmstateapi's envtest test): it locates
// the envtest binaries so `go test` finds them the same way `make test` does (via the
// KUBEBUILDER_ASSETS environment variable, which envtest.Environment honours on its own
// when BinaryAssetsDirectory is left unset) and, as a fallback for running the test
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
