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

package hostreg

import (
	"sort"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

// Literals repeated across many test cases below, factored out per the goconst linter.
const (
	nodeWorker1 = "worker-1"
	nodeAddr    = "10.0.0.5"
	nodeMAC     = "aa:bb:cc:dd:ee:ff"
	regWorker1  = "nodes-worker-1"
)

// hrPolicy builds a minimal HostRegistrationPolicy naming the IPSpace "corp", carrying
// only the fields Compute reads.
func hrPolicy(mode uddiv1alpha1.RegisterMode) *uddiv1alpha1.HostRegistrationPolicy {
	return &uddiv1alpha1.HostRegistrationPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "uddi-system", Name: "nodes"},
		Spec: uddiv1alpha1.HostRegistrationPolicySpec{
			ConnectionRef: uddiv1alpha1.ConnectionRef{Name: "default"},
			SpaceRef:      uddiv1alpha1.ObjectRef{Name: "corp"},
			AddressType:   uddiv1alpha1.AddressTypeInternalIP,
			Register:      uddiv1alpha1.Register{Mode: mode},
			Tags:          map[string]string{"role": "node"},
			Lifecycle:     uddiv1alpha1.Lifecycle{DeletionPolicy: uddiv1alpha1.DeletionPolicyDelete},
		},
	}
}

// readySpace is the IPSpace "corp" state with a status.id already assigned.
var readySpace = SpaceState{Name: "corp", Ready: true}

func TestRegistrationName(t *testing.T) {
	short := RegistrationName("nodes", nodeWorker1)
	if short != regWorker1 {
		t.Fatalf("RegistrationName(short) = %q, want %q", short, regWorker1)
	}

	longNode := strings.Repeat("n", 300)
	long := RegistrationName("nodes", longNode)
	if !strings.HasPrefix(long, "nodes-") {
		t.Fatalf("expected the policy prefix to survive, got %q", long)
	}
	suffix := strings.TrimPrefix(long, "nodes-")
	if len(suffix) != nameHashLength {
		t.Fatalf("expected a %d hex character suffix, got %d chars (%q)", nameHashLength, len(suffix), suffix)
	}
	if len(long) > maxNameLength {
		t.Fatalf("expected the hashed name to be short, got %d chars", len(long))
	}
	if again := RegistrationName("nodes", longNode); again != long {
		t.Fatalf("RegistrationName is not deterministic: %q != %q", again, long)
	}
	if other := RegistrationName("nodes", strings.Repeat("m", 300)); other == long {
		t.Fatalf("expected different nodes to hash to different names")
	}
}

func TestChooseKind(t *testing.T) {
	cases := []struct {
		name     string
		mode     RegisterMode
		mac      string
		wantKind RegistrationKind
		wantOK   bool
	}{
		{"address mode, no mac", RegisterModeAddress, "", KindAddress, true},
		{"address mode, with mac", RegisterModeAddress, nodeMAC, KindAddress, true},
		{"fixedaddress mode, with mac", RegisterModeFixedAddress, nodeMAC, KindFixedAddress, true},
		{"fixedaddress mode, no mac", RegisterModeFixedAddress, "", KindFixedAddress, false},
		{"auto, with mac", RegisterModeAuto, nodeMAC, KindFixedAddress, true},
		{"auto, no mac", RegisterModeAuto, "", KindAddress, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, ok := ChooseKind(c.mode, c.mac)
			if kind != c.wantKind || ok != c.wantOK {
				t.Errorf("ChooseKind(%s, %q) = (%s, %v), want (%s, %v)", c.mode, c.mac, kind, ok, c.wantKind, c.wantOK)
			}
		})
	}
}

func TestCompute_AutoWithMAC(t *testing.T) {
	policy := hrPolicy(uddiv1alpha1.RegisterModeAuto)
	v := NodeView{
		Ref:      NodeRef{Name: nodeWorker1},
		Address:  nodeAddr,
		MAC:      nodeMAC,
		Hostname: nodeWorker1,
	}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace})

	if len(plan.Skips) != 0 {
		t.Fatalf("expected no skips, got %+v", plan.Skips)
	}
	if len(plan.Ensure) != 1 {
		t.Fatalf("expected one ensured registration, got %d", len(plan.Ensure))
	}
	d := plan.Ensure[0]
	if d.Kind != KindFixedAddress {
		t.Fatalf("Kind = %s, want FixedAddress (a MAC is known)", d.Kind)
	}
	if d.Name != regWorker1 {
		t.Errorf("Name = %q, want %q", d.Name, regWorker1)
	}
	if d.FixedAddressSpec == nil {
		t.Fatalf("FixedAddressSpec is nil")
	}
	fa := d.FixedAddressSpec
	if fa.Address != nodeAddr {
		t.Errorf("Address = %q, want %q", fa.Address, nodeAddr)
	}
	if fa.MatchType != "mac" || fa.MatchValue != nodeMAC {
		t.Errorf("MatchType/MatchValue = %s/%s, want mac/%s", fa.MatchType, fa.MatchValue, nodeMAC)
	}
	if fa.Name != nodeWorker1 || fa.Hostname != nodeWorker1 {
		t.Errorf("Name/Hostname = %s/%s, want both %s", fa.Name, fa.Hostname, nodeWorker1)
	}
	if fa.SpaceRef.Name != "corp" {
		t.Errorf("SpaceRef = %+v, want corp", fa.SpaceRef)
	}
	wantComment := "Registered by HostRegistrationPolicy uddi-system/nodes for Node worker-1"
	if fa.Comment != wantComment {
		t.Errorf("Comment = %q, want %q", fa.Comment, wantComment)
	}
	if fa.Tags["role"] != "node" || fa.Tags[TagSource] != "Node/worker-1" {
		t.Errorf("Tags = %+v", fa.Tags)
	}
	if fa.Lifecycle != policy.Spec.Lifecycle {
		t.Errorf("Lifecycle = %+v, want %+v", fa.Lifecycle, policy.Spec.Lifecycle)
	}
	if d.AddressSpec != nil {
		t.Errorf("expected AddressSpec to be nil for a FixedAddress registration")
	}
	if d.Labels[LabelPolicy] != "nodes" || d.Labels[LabelNode] != nodeWorker1 {
		t.Errorf("Labels = %+v", d.Labels)
	}
	if plan.Registered != 0 || plan.Pending != 0 {
		t.Errorf("Registered/Pending = %d/%d, want 0/0 (no existing object)", plan.Registered, plan.Pending)
	}
	if len(plan.Release) != 0 {
		t.Errorf("expected no release, got %+v", plan.Release)
	}
}

func TestCompute_AutoWithoutMAC(t *testing.T) {
	policy := hrPolicy(uddiv1alpha1.RegisterModeAuto)
	v := NodeView{
		Ref:      NodeRef{Name: nodeWorker1},
		Address:  nodeAddr,
		Hostname: nodeWorker1,
		// MAC left empty.
	}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace})

	if len(plan.Ensure) != 1 {
		t.Fatalf("expected one ensured registration, got %d", len(plan.Ensure))
	}
	d := plan.Ensure[0]
	if d.Kind != KindAddress {
		t.Fatalf("Kind = %s, want Address (no MAC known)", d.Kind)
	}
	if d.AddressSpec == nil {
		t.Fatalf("AddressSpec is nil")
	}
	a := d.AddressSpec
	if a.HWAddr != "" {
		t.Errorf("HWAddr = %q, want empty when no MAC is known", a.HWAddr)
	}
	if len(a.Names) != 1 || a.Names[0].Name != nodeWorker1 || a.Names[0].Type != "user" {
		t.Errorf("Names = %+v", a.Names)
	}
	if d.FixedAddressSpec != nil {
		t.Errorf("expected FixedAddressSpec to be nil for an Address registration")
	}
}

func TestCompute_HWAddrSetWhenKnown(t *testing.T) {
	policy := hrPolicy(uddiv1alpha1.RegisterModeAddress) // Address mode: hwaddr set when known, never a FixedAddress.
	v := NodeView{
		Ref:      NodeRef{Name: nodeWorker1},
		Address:  nodeAddr,
		MAC:      nodeMAC,
		Hostname: nodeWorker1,
	}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace})

	if len(plan.Ensure) != 1 {
		t.Fatalf("expected one ensured registration, got %d", len(plan.Ensure))
	}
	d := plan.Ensure[0]
	if d.Kind != KindAddress {
		t.Fatalf("Kind = %s, want Address (mode Address always registers an Address)", d.Kind)
	}
	if d.AddressSpec.HWAddr != nodeMAC {
		t.Errorf("HWAddr = %q, want %q", d.AddressSpec.HWAddr, nodeMAC)
	}
}

func TestCompute_FixedAddressModeNoMAC(t *testing.T) {
	policy := hrPolicy(uddiv1alpha1.RegisterModeFixedAddress)
	v := NodeView{
		Ref:      NodeRef{Name: nodeWorker1},
		Address:  nodeAddr,
		Hostname: nodeWorker1,
		// MAC left empty: FixedAddress mode cannot register this Node.
	}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace})

	if len(plan.Ensure) != 0 {
		t.Fatalf("expected no ensured registration, got %d", len(plan.Ensure))
	}
	want := "mode FixedAddress needs a MAC; no annotation, NodeNetworkState or Metal3 BareMetalHost provides one"
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipNoMAC || plan.Skips[0].Message != want {
		t.Fatalf("expected one NoMAC skip %q, got %+v", want, plan.Skips)
	}
}

func TestCompute_NoAddressSkip(t *testing.T) {
	policy := hrPolicy(uddiv1alpha1.RegisterModeAuto)
	v := NodeView{
		Ref: NodeRef{Name: nodeWorker1},
		// Address left empty.
	}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace})

	if len(plan.Ensure) != 0 {
		t.Fatalf("expected no ensured registration, got %d", len(plan.Ensure))
	}
	want := "node has no InternalIP address"
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipNoAddress || plan.Skips[0].Message != want {
		t.Fatalf("expected one NoAddress skip %q, got %+v", want, plan.Skips)
	}
}

func TestCompute_SpaceNotReady(t *testing.T) {
	policy := hrPolicy(uddiv1alpha1.RegisterModeAuto)
	v := NodeView{
		Ref:      NodeRef{Name: nodeWorker1},
		Address:  nodeAddr,
		Hostname: nodeWorker1,
	}
	existing := []RegistrationState{{Name: regWorker1, Kind: KindAddress, Node: nodeWorker1, Ready: true}}
	notReady := SpaceState{Name: "corp", Ready: false}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: notReady, Existing: existing})

	if len(plan.Ensure) != 0 {
		t.Fatalf("expected no ensured registration while the space is not ready, got %d", len(plan.Ensure))
	}
	want := "space corp has no status.id yet"
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipSpaceNotReady || plan.Skips[0].Message != want {
		t.Fatalf("expected one SpaceNotReady skip %q, got %+v", want, plan.Skips)
	}
	if len(plan.Release) != 0 {
		t.Fatalf("expected the existing registration left alone while the space recovers, got %+v", plan.Release)
	}
}

func TestCompute_InvalidAnnotationFrozen(t *testing.T) {
	policy := hrPolicy(uddiv1alpha1.RegisterModeAuto)
	v := NodeView{
		Ref:     NodeRef{Name: nodeWorker1},
		Invalid: `invalid hostname annotation "??"`,
	}
	existing := []RegistrationState{{Name: regWorker1, Kind: KindAddress, Node: nodeWorker1, Ready: true}}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace, Existing: existing})

	if len(plan.Ensure) != 0 {
		t.Fatalf("expected no ensured registration, got %d", len(plan.Ensure))
	}
	if len(plan.Release) != 0 {
		t.Fatalf("expected the existing registration to be left alone (frozen), got %+v", plan.Release)
	}
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipInvalidAnnotation || plan.Skips[0].Message != v.Invalid {
		t.Fatalf("expected one InvalidAnnotation skip carrying the view's message, got %+v", plan.Skips)
	}
}

func TestCompute_OptOutReleasesExisting(t *testing.T) {
	policy := hrPolicy(uddiv1alpha1.RegisterModeAuto)
	v := NodeView{
		Ref:    NodeRef{Name: nodeWorker1},
		OptOut: true,
	}
	existing := []RegistrationState{{Name: regWorker1, Kind: KindAddress, Node: nodeWorker1, Ready: true}}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace, Existing: existing})

	if len(plan.Ensure) != 0 {
		t.Fatalf("expected no ensured registration for an opted-out Node, got %d", len(plan.Ensure))
	}
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipExcluded {
		t.Fatalf("expected one Excluded skip, got %+v", plan.Skips)
	}
	if len(plan.Release) != 1 || plan.Release[0].Name != regWorker1 || plan.Release[0].Kind != KindAddress || plan.Release[0].Node.Name != nodeWorker1 {
		t.Fatalf("expected the existing registration released on opt-out, got %+v", plan.Release)
	}
}

func TestCompute_ReleaseOnNodeDisappearance(t *testing.T) {
	policy := hrPolicy(uddiv1alpha1.RegisterModeAuto)
	existing := []RegistrationState{{Name: "nodes-gone", Kind: KindAddress, Node: "gone", Ready: true}}

	plan := Compute(PlanInput{Policy: policy, Nodes: nil, Space: readySpace, Existing: existing})

	if len(plan.Release) != 1 {
		t.Fatalf("expected one release, got %+v", plan.Release)
	}
	r := plan.Release[0]
	if r.Name != "nodes-gone" || r.Kind != KindAddress {
		t.Fatalf("Release = %+v, want Name nodes-gone Kind Address", r)
	}
	if r.Node.Name != "gone" {
		t.Fatalf("Release.Node = %+v, want Name gone", r.Node)
	}
}

func TestCompute_KindChangeReleaseAndEnsure(t *testing.T) {
	// Auto mode; the Node previously had a MAC (registered as a FixedAddress) but no
	// longer does, so the chosen kind changes to Address: the old FixedAddress must be
	// released while the new Address is ensured, in the same plan.
	policy := hrPolicy(uddiv1alpha1.RegisterModeAuto)
	v := NodeView{
		Ref:      NodeRef{Name: nodeWorker1},
		Address:  nodeAddr,
		Hostname: nodeWorker1,
		// MAC left empty this round.
	}
	existing := []RegistrationState{{Name: regWorker1, Kind: KindFixedAddress, Node: nodeWorker1, Ready: true}}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace, Existing: existing})

	if len(plan.Ensure) != 1 || plan.Ensure[0].Kind != KindAddress {
		t.Fatalf("expected one ensured Address registration, got %+v", plan.Ensure)
	}
	if len(plan.Release) != 1 || plan.Release[0].Kind != KindFixedAddress || plan.Release[0].Name != regWorker1 {
		t.Fatalf("expected the old FixedAddress released, got %+v", plan.Release)
	}
	// The stale FixedAddress is a different kind than what's ensured now, so it must
	// not be counted as already Registered.
	if plan.Registered != 0 {
		t.Fatalf("Registered = %d, want 0 (the existing object is the wrong kind)", plan.Registered)
	}
}

func TestCompute_RegisteredWhenExistingReady(t *testing.T) {
	policy := hrPolicy(uddiv1alpha1.RegisterModeAuto)
	v := NodeView{
		Ref:      NodeRef{Name: nodeWorker1},
		Address:  nodeAddr,
		MAC:      nodeMAC,
		Hostname: nodeWorker1,
	}
	existing := []RegistrationState{{Name: regWorker1, Kind: KindFixedAddress, Node: nodeWorker1, Ready: true}}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace, Existing: existing})

	if plan.Registered != 1 || plan.Pending != 0 {
		t.Fatalf("Registered/Pending = %d/%d, want 1/0", plan.Registered, plan.Pending)
	}
	if len(plan.Skips) != 0 {
		t.Fatalf("expected no skips, got %+v", plan.Skips)
	}
	if len(plan.Release) != 0 {
		t.Fatalf("expected no release, got %+v", plan.Release)
	}
}

func TestCompute_PendingMessage(t *testing.T) {
	policy := hrPolicy(uddiv1alpha1.RegisterModeAuto)
	v := NodeView{
		Ref:      NodeRef{Name: nodeWorker1},
		Address:  nodeAddr,
		MAC:      nodeMAC,
		Hostname: nodeWorker1,
	}
	existing := []RegistrationState{{
		Name:    regWorker1,
		Kind:    KindFixedAddress,
		Node:    nodeWorker1,
		Ready:   false,
		Reason:  "APIError",
		Message: "conflict reserving address",
	}}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace, Existing: existing})

	if len(plan.Ensure) != 1 {
		t.Fatalf("expected the registration to still be ensured, got %d", len(plan.Ensure))
	}
	if plan.Pending != 1 || plan.Registered != 0 {
		t.Fatalf("Registered/Pending = %d/%d, want 0/1", plan.Registered, plan.Pending)
	}
	want := "APIError: conflict reserving address"
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipRegistrationPending || plan.Skips[0].Message != want {
		t.Fatalf("expected one RegistrationPending skip %q, got %+v", want, plan.Skips)
	}
}

func TestCompute_DHCPOptionsCopied(t *testing.T) {
	policy := hrPolicy(uddiv1alpha1.RegisterModeFixedAddress)
	policy.Spec.Register.DHCPOptions = []uddiv1alpha1.DHCPOption{
		{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionCode: "dhcp/option_code/1", OptionValue: "1440"},
	}
	v := NodeView{
		Ref:      NodeRef{Name: nodeWorker1},
		Address:  nodeAddr,
		MAC:      nodeMAC,
		Hostname: nodeWorker1,
	}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace})

	if len(plan.Ensure) != 1 || plan.Ensure[0].FixedAddressSpec == nil {
		t.Fatalf("expected one ensured FixedAddress registration, got %+v", plan.Ensure)
	}
	got := plan.Ensure[0].FixedAddressSpec.DHCPOptions
	if len(got) != 1 || got[0].OptionCode != "dhcp/option_code/1" || got[0].OptionValue != "1440" {
		t.Fatalf("DHCPOptions = %+v, want the policy's register.dhcpOptions copied over", got)
	}
}

func TestCompute_SortedAndDisjointOutputs(t *testing.T) {
	policy := hrPolicy(uddiv1alpha1.RegisterModeAuto)
	nodes := []NodeView{
		{Ref: NodeRef{Name: "c"}, Address: nodeAddr, Hostname: "c"},
		{Ref: NodeRef{Name: "a"}, Address: nodeAddr, Hostname: "a"},
		{Ref: NodeRef{Name: "b"}, Address: nodeAddr, Hostname: "b"},
	}

	plan := Compute(PlanInput{Policy: policy, Nodes: nodes, Space: readySpace})

	if len(plan.Ensure) != 3 {
		t.Fatalf("expected 3 ensured registrations, got %d", len(plan.Ensure))
	}
	names := make([]string, len(plan.Ensure))
	ensured := make(map[string]bool, len(plan.Ensure))
	for i, d := range plan.Ensure {
		names[i] = d.Node.Name
		ensured[d.Node.Name] = true
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("expected Ensure sorted by Node name, got %v", names)
	}
	for _, r := range plan.Release {
		if ensured[r.Node.Name] {
			t.Fatalf("Node %q appears in both Ensure and Release", r.Node.Name)
		}
	}
}

func TestCompute_SkipsSortedByNodeThenReason(t *testing.T) {
	policy := hrPolicy(uddiv1alpha1.RegisterModeAuto)
	nodes := []NodeView{
		{Ref: NodeRef{Name: "b"}}, // no address -> NoAddress
		{Ref: NodeRef{Name: "a"}}, // no address -> NoAddress
	}

	plan := Compute(PlanInput{Policy: policy, Nodes: nodes, Space: readySpace})

	if len(plan.Skips) != 2 {
		t.Fatalf("expected 2 skips, got %d", len(plan.Skips))
	}
	if plan.Skips[0].Node.Name != "a" || plan.Skips[1].Node.Name != "b" {
		t.Fatalf("expected skips sorted by Node name, got %+v", plan.Skips)
	}
}
