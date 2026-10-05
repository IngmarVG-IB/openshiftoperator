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

package ipalloc

import (
	"sort"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

func boolPtr(b bool) *bool { return &b }

// Literals repeated across many test cases below, factored out per the goconst linter.
const (
	svcWeb     = "web"
	addrLBWeb  = "lb-web"
	assignedIP = "10.0.10.5"
)

// lbPoolRef is the pool named by rangePoolPolicy's spec.allocateFrom.
var lbPoolRef = PoolRef{Kind: PoolRange, Name: "lb-pool"}

// rangePoolPolicy builds a minimal IPAllocationPolicy allocating from the Range
// "lb-pool", carrying only the fields Compute reads.
func rangePoolPolicy(require *bool) *uddiv1alpha1.IPAllocationPolicy {
	return &uddiv1alpha1.IPAllocationPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "lb"},
		Spec: uddiv1alpha1.IPAllocationPolicySpec{
			ConnectionRef:     uddiv1alpha1.ConnectionRef{Name: "default"},
			RequireAnnotation: require,
			AllocateFrom:      uddiv1alpha1.AllocateFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: "lb-pool"}},
			Tags:              map[string]string{"team": "a"},
			AddressLifecycle:  uddiv1alpha1.Lifecycle{DeletionPolicy: uddiv1alpha1.DeletionPolicyDelete},
		},
	}
}

// readyPools returns a Pools map with lbPoolRef ready, its spaceRef "corp".
func readyPools() map[PoolRef]PoolState {
	return map[PoolRef]PoolState{
		lbPoolRef: {Ref: lbPoolRef, Ready: true, SpaceRef: uddiv1alpha1.ObjectRef{Name: "corp"}},
	}
}

func TestAddressName(t *testing.T) {
	short := AddressName("lb", svcWeb)
	if short != addrLBWeb {
		t.Fatalf("AddressName(short) = %q, want lb-web", short)
	}

	longService := strings.Repeat("s", 300)
	long := AddressName("lb", longService)
	if !strings.HasPrefix(long, "lb-") {
		t.Fatalf("expected the policy prefix to survive, got %q", long)
	}
	suffix := strings.TrimPrefix(long, "lb-")
	if len(suffix) != nameHashLength {
		t.Fatalf("expected a %d hex character suffix, got %d chars (%q)", nameHashLength, len(suffix), suffix)
	}
	if len(long) > maxNameLength {
		t.Fatalf("expected the hashed name to be short, got %d chars", len(long))
	}
	if again := AddressName("lb", longService); again != long {
		t.Fatalf("AddressName is not deterministic: %q != %q", again, long)
	}
	if other := AddressName("lb", strings.Repeat("t", 300)); other == long {
		t.Fatalf("expected different services to hash to different names")
	}
}

func TestDefaultHostname(t *testing.T) {
	got := DefaultHostname(TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb})
	if got != "web.team-a" {
		t.Fatalf("DefaultHostname = %q, want web.team-a", got)
	}
}

func TestPolicyPool(t *testing.T) {
	cases := []struct {
		name string
		af   uddiv1alpha1.AllocateFrom
		want PoolRef
	}{
		{"subnet", uddiv1alpha1.AllocateFrom{SubnetRef: &uddiv1alpha1.ObjectRef{Name: "s1"}}, PoolRef{Kind: PoolSubnet, Name: "s1"}},
		{"range", uddiv1alpha1.AllocateFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: "r1"}}, PoolRef{Kind: PoolRange, Name: "r1"}},
		{"addressblock", uddiv1alpha1.AllocateFrom{AddressBlockRef: &uddiv1alpha1.ObjectRef{Name: "b1"}}, PoolRef{Kind: PoolAddressBlock, Name: "b1"}},
		{"none set", uddiv1alpha1.AllocateFrom{}, PoolRef{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &uddiv1alpha1.IPAllocationPolicy{Spec: uddiv1alpha1.IPAllocationPolicySpec{AllocateFrom: c.af}}
			if got := PolicyPool(p); got != c.want {
				t.Errorf("PolicyPool = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestWantsAddress(t *testing.T) {
	cases := []struct {
		name    string
		require *bool
		optIn   *bool
		want    bool
	}{
		{"require true (explicit), no annotation", boolPtr(true), nil, false},
		{"require true (explicit), opted in", boolPtr(true), boolPtr(true), true},
		{"require true (explicit), opted out", boolPtr(true), boolPtr(false), false},
		{"require false, no annotation", boolPtr(false), nil, true},
		{"require false, opted out", boolPtr(false), boolPtr(false), false},
		{"require false, opted in", boolPtr(false), boolPtr(true), true},
		{"require nil defaults to true, no annotation", nil, nil, false},
		{"require nil defaults to true, opted in", nil, boolPtr(true), true},
		{"require nil defaults to true, opted out", nil, boolPtr(false), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &uddiv1alpha1.IPAllocationPolicy{Spec: uddiv1alpha1.IPAllocationPolicySpec{RequireAnnotation: c.require}}
			v := Claim{OptIn: c.optIn}
			if got := WantsAddress(p, v); got != c.want {
				t.Errorf("WantsAddress = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCompute_EnsureNextAvailable(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true))
	v := Claim{
		Target:   TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb},
		Eligible: true,
		OptIn:    boolPtr(true),
		Hostname: "web.team-a",
	}

	plan := Compute(PlanInput{Policy: policy, Claims: []Claim{v}, Pools: readyPools()})

	if len(plan.Skips) != 0 {
		t.Fatalf("expected no skips, got %+v", plan.Skips)
	}
	if len(plan.Ensure) != 1 {
		t.Fatalf("expected one ensured Address, got %d", len(plan.Ensure))
	}
	d := plan.Ensure[0]
	if d.Name != addrLBWeb {
		t.Errorf("Name = %q, want lb-web", d.Name)
	}
	if d.Spec.SpaceRef.Name != "corp" {
		t.Errorf("SpaceRef = %+v, want corp", d.Spec.SpaceRef)
	}
	if d.Spec.NextAvailableFrom == nil || d.Spec.NextAvailableFrom.RangeRef == nil || d.Spec.NextAvailableFrom.RangeRef.Name != "lb-pool" {
		t.Errorf("NextAvailableFrom = %+v, want rangeRef lb-pool", d.Spec.NextAvailableFrom)
	}
	if d.Spec.Address != "" {
		t.Errorf("Address = %q, want empty (next-available)", d.Spec.Address)
	}
	if len(d.Spec.Names) != 1 || d.Spec.Names[0].Name != "web.team-a" || d.Spec.Names[0].Type != "user" {
		t.Errorf("Names = %+v", d.Spec.Names)
	}
	wantComment := "Allocated by IPAllocationPolicy team-a/lb for Service web"
	if d.Spec.Comment != wantComment {
		t.Errorf("Comment = %q, want %q", d.Spec.Comment, wantComment)
	}
	if d.Spec.Tags["team"] != "a" || d.Spec.Tags[TagSource] != "Service/team-a/web" {
		t.Errorf("Tags = %+v", d.Spec.Tags)
	}
	if d.Labels[LabelPolicy] != "lb" || d.Labels[LabelService] != svcWeb {
		t.Errorf("Labels = %+v", d.Labels)
	}
	if d.Spec.Lifecycle != policy.Spec.AddressLifecycle {
		t.Errorf("Lifecycle = %+v, want %+v", d.Spec.Lifecycle, policy.Spec.AddressLifecycle)
	}
	if plan.Allocated != 1 {
		t.Errorf("Allocated = %d, want 1", plan.Allocated)
	}
	if plan.Assigned != 0 {
		t.Errorf("Assigned = %d, want 0 (no existing Address yet)", plan.Assigned)
	}
	if len(plan.Assign) != 0 || len(plan.Release) != 0 {
		t.Errorf("expected no Assign/Release, got %+v / %+v", plan.Assign, plan.Release)
	}
}

func TestCompute_ExplicitAddress(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true))
	v := Claim{
		Target:   TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb},
		Eligible: true,
		OptIn:    boolPtr(true),
		Slots:    []SlotRequest{{Address: "10.0.10.7"}},
		Hostname: "web.team-a",
	}

	plan := Compute(PlanInput{Policy: policy, Claims: []Claim{v}, Pools: readyPools()})

	if len(plan.Ensure) != 1 {
		t.Fatalf("expected one ensured Address, got %d", len(plan.Ensure))
	}
	d := plan.Ensure[0]
	if d.Spec.Address != "10.0.10.7" {
		t.Errorf("Address = %q, want 10.0.10.7", d.Spec.Address)
	}
	if d.Spec.NextAvailableFrom != nil {
		t.Errorf("NextAvailableFrom = %+v, want nil when address is explicit", d.Spec.NextAvailableFrom)
	}
}

func TestCompute_PoolOverrideViaAnnotation(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true))
	override := PoolRef{Kind: PoolSubnet, Name: "other-subnet"}
	v := Claim{
		Target:   TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb},
		Eligible: true,
		OptIn:    boolPtr(true),
		Pool:     &override,
		Hostname: "web.team-a",
	}
	pools := readyPools()
	pools[override] = PoolState{Ref: override, Ready: true, SpaceRef: uddiv1alpha1.ObjectRef{Name: "other-space"}}

	plan := Compute(PlanInput{Policy: policy, Claims: []Claim{v}, Pools: pools})

	if len(plan.Ensure) != 1 {
		t.Fatalf("expected one ensured Address, got %d", len(plan.Ensure))
	}
	d := plan.Ensure[0]
	if d.Spec.SpaceRef.Name != "other-space" {
		t.Errorf("SpaceRef = %+v, want other-space (from the overriding pool)", d.Spec.SpaceRef)
	}
	if d.Spec.NextAvailableFrom == nil || d.Spec.NextAvailableFrom.SubnetRef == nil || d.Spec.NextAvailableFrom.SubnetRef.Name != "other-subnet" {
		t.Errorf("NextAvailableFrom = %+v, want subnetRef other-subnet", d.Spec.NextAvailableFrom)
	}
}

func TestCompute_InvalidAnnotationSkipped(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true))
	v := Claim{
		Target:  TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb},
		Invalid: `invalid allocate-from value "bogus"`,
	}
	existing := []AddressState{{Name: addrLBWeb, Service: svcWeb, Ready: true, Address: assignedIP}}

	plan := Compute(PlanInput{Policy: policy, Claims: []Claim{v}, Pools: readyPools(), Existing: existing})

	if len(plan.Ensure) != 0 {
		t.Fatalf("expected no ensured Address, got %d", len(plan.Ensure))
	}
	if len(plan.Release) != 0 {
		t.Fatalf("expected the existing Address to be left alone (frozen), got %+v", plan.Release)
	}
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipInvalidAnnotation || plan.Skips[0].Message != v.Invalid {
		t.Fatalf("expected one InvalidAnnotation skip carrying the view's message, got %+v", plan.Skips)
	}
}

// With requireAnnotation false every Service in the namespace is a claim; a ClusterIP
// Service without the allocate annotation must not be reported, or status.skipped and the
// Skipped events would fill with every ordinary Service.
func TestCompute_NonLoadBalancerWithoutAnnotationIsIgnored(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(false))
	plain := Claim{Target: TargetRef{Kind: TargetService, Namespace: "team-a", Name: "plain"}, Eligible: false}
	formerLB := Claim{Target: TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb}, Eligible: false}
	existing := []AddressState{{Name: addrLBWeb, Service: svcWeb, Ready: true, Address: assignedIP}}

	plan := Compute(PlanInput{Policy: policy, Claims: []Claim{plain, formerLB}, Pools: readyPools(), Existing: existing})

	if len(plan.Skips) != 0 {
		t.Fatalf("expected no skips for Services that did not ask for an address, got %+v", plan.Skips)
	}
	if len(plan.Ensure) != 0 {
		t.Fatalf("expected nothing ensured, got %+v", plan.Ensure)
	}
	if len(plan.Release) != 1 || plan.Release[0].AddressName != addrLBWeb {
		t.Fatalf("expected the Address of the Service that stopped being a LoadBalancer to be released, got %+v", plan.Release)
	}
}

func TestCompute_NotLoadBalancer(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true))
	v := Claim{
		Target:   TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb},
		Eligible: false,
		OptIn:    boolPtr(true),
	}
	existing := []AddressState{{Name: addrLBWeb, Service: svcWeb, Ready: true, Address: assignedIP}}

	plan := Compute(PlanInput{Policy: policy, Claims: []Claim{v}, Pools: readyPools(), Existing: existing})

	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipNotLoadBalancer {
		t.Fatalf("expected one NotLoadBalancer skip, got %+v", plan.Skips)
	}
	if len(plan.Release) != 1 || plan.Release[0].AddressName != addrLBWeb || plan.Release[0].Address != assignedIP {
		t.Fatalf("expected the stale Address to be released, got %+v", plan.Release)
	}
}

func TestCompute_PoolNotReady(t *testing.T) {
	cases := []struct {
		name  string
		pools map[PoolRef]PoolState
	}{
		{"missing from the pool map", map[PoolRef]PoolState{}},
		{"present but not ready", map[PoolRef]PoolState{lbPoolRef: {Ref: lbPoolRef, Ready: false}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			policy := rangePoolPolicy(boolPtr(true))
			v := Claim{
				Target:   TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb},
				Eligible: true,
				OptIn:    boolPtr(true),
			}
			existing := []AddressState{{Name: addrLBWeb, Service: svcWeb, Ready: true, Address: assignedIP}}

			plan := Compute(PlanInput{Policy: policy, Claims: []Claim{v}, Pools: c.pools, Existing: existing})

			if len(plan.Ensure) != 0 {
				t.Fatalf("expected no ensured Address while the pool is not ready, got %d", len(plan.Ensure))
			}
			if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipPoolNotReady {
				t.Fatalf("expected one PoolNotReady skip, got %+v", plan.Skips)
			}
			if len(plan.Release) != 0 {
				t.Fatalf("expected the existing Address left alone while the pool recovers, got %+v", plan.Release)
			}
		})
	}
}

func TestCompute_ManuallyAssignedForeignValue(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true))
	policy.Spec.AssignVia = uddiv1alpha1.AssignVia{Mode: uddiv1alpha1.AssignModeAnnotation, Annotation: "metallb.io/loadBalancerIPs"}
	v := Claim{
		Target:            TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb},
		Eligible:          true,
		OptIn:             boolPtr(true),
		CurrentAssignment: []string{"10.0.10.9"},
	}

	// No existing Address at all: any non-empty CurrentAssignment is foreign.
	plan := Compute(PlanInput{Policy: policy, Claims: []Claim{v}, Pools: readyPools()})

	if len(plan.Ensure) != 0 {
		t.Fatalf("expected nothing ensured for a manually assigned Service, got %d", len(plan.Ensure))
	}
	want := "metallb.io/loadBalancerIPs is already 10.0.10.9"
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipManuallyAssigned || plan.Skips[0].Message != want {
		t.Fatalf("expected one ManuallyAssigned skip %q, got %+v", want, plan.Skips)
	}
}

func TestCompute_ManuallyAssignedLoadBalancerIPMode(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true))
	policy.Spec.AssignVia = uddiv1alpha1.AssignVia{Mode: uddiv1alpha1.AssignModeLoadBalancerIP}
	v := Claim{
		Target:            TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb},
		Eligible:          true,
		OptIn:             boolPtr(true),
		CurrentAssignment: []string{"10.0.10.9"},
	}
	// A different existing address: 10.0.10.9 still doesn't match it, so it is foreign.
	existing := []AddressState{{Name: addrLBWeb, Service: svcWeb, Ready: true, Address: assignedIP}}

	plan := Compute(PlanInput{Policy: policy, Claims: []Claim{v}, Pools: readyPools(), Existing: existing})

	want := "spec.loadBalancerIP is already 10.0.10.9"
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipManuallyAssigned || plan.Skips[0].Message != want {
		t.Fatalf("expected one ManuallyAssigned skip %q, got %+v", want, plan.Skips)
	}
	// The Service still wants an address (it did not opt out), so its existing
	// Address must not be released just because the assignment field is foreign.
	if len(plan.Release) != 0 {
		t.Fatalf("expected no release for a manually assigned Service, got %+v", plan.Release)
	}
}

func TestCompute_ManuallyAssignedDefaultAnnotation(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true)) // AssignVia left at its zero value.
	v := Claim{
		Target:            TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb},
		Eligible:          true,
		OptIn:             boolPtr(true),
		CurrentAssignment: []string{"10.0.10.9"},
	}

	plan := Compute(PlanInput{Policy: policy, Claims: []Claim{v}, Pools: readyPools()})

	want := DefaultAssignAnnotation + " is already 10.0.10.9"
	if len(plan.Skips) != 1 || plan.Skips[0].Message != want {
		t.Fatalf("expected the default annotation key in the message %q, got %+v", want, plan.Skips)
	}
}

func TestCompute_AssignedByUs(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true))
	v := Claim{
		Target:            TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb},
		Eligible:          true,
		OptIn:             boolPtr(true),
		CurrentAssignment: []string{assignedIP},
		Hostname:          "web.team-a",
	}
	existing := []AddressState{{Name: addrLBWeb, Service: svcWeb, Ready: true, Address: assignedIP}}

	plan := Compute(PlanInput{Policy: policy, Claims: []Claim{v}, Pools: readyPools(), Existing: existing})

	if len(plan.Ensure) != 1 {
		t.Fatalf("expected the Address to still be ensured (idempotent), got %d", len(plan.Ensure))
	}
	if len(plan.Skips) != 0 {
		t.Fatalf("expected no skips, got %+v", plan.Skips)
	}
	if len(plan.Assign) != 0 {
		t.Fatalf("expected no Assign entries (the value is already ours), got %+v", plan.Assign)
	}
	if plan.Assigned != 1 {
		t.Fatalf("Assigned = %d, want 1", plan.Assigned)
	}
	if plan.Allocated != 1 {
		t.Fatalf("Allocated = %d, want 1", plan.Allocated)
	}
}

func TestCompute_NeedsAssignment(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true))
	v := Claim{
		Target:   TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb},
		Eligible: true,
		OptIn:    boolPtr(true),
		Hostname: "web.team-a",
		// CurrentAssignment left empty: the Address is Ready but not written back yet.
	}
	existing := []AddressState{{Name: addrLBWeb, Service: svcWeb, Ready: true, Address: assignedIP}}

	plan := Compute(PlanInput{Policy: policy, Claims: []Claim{v}, Pools: readyPools(), Existing: existing})

	if len(plan.Assign) != 1 || plan.Assign[0].Value() != assignedIP || plan.Assign[0].Target.Name != svcWeb {
		t.Fatalf("expected an Assignment writing 10.0.10.5 to web, got %+v", plan.Assign)
	}
	if plan.Assigned != 0 {
		t.Fatalf("Assigned = %d, want 0 (not written back yet)", plan.Assigned)
	}
}

func TestCompute_AddressPending(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true))
	v := Claim{
		Target:   TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb},
		Eligible: true,
		OptIn:    boolPtr(true),
		Hostname: "web.team-a",
	}
	existing := []AddressState{{
		Name:    addrLBWeb,
		Service: svcWeb,
		Ready:   false,
		Reason:  "APIError",
		Message: "conflict allocating address",
	}}

	plan := Compute(PlanInput{Policy: policy, Claims: []Claim{v}, Pools: readyPools(), Existing: existing})

	if len(plan.Ensure) != 1 {
		t.Fatalf("expected the Address to still be ensured, got %d", len(plan.Ensure))
	}
	want := "APIError: conflict allocating address"
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipAddressPending || plan.Skips[0].Message != want {
		t.Fatalf("expected one AddressPending skip %q, got %+v", want, plan.Skips)
	}
	if plan.Assigned != 0 || len(plan.Assign) != 0 {
		t.Fatalf("expected no assignment while the Address is pending")
	}
}

func TestCompute_ReleaseOnOptOut(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true))
	v := Claim{
		Target:   TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb},
		Eligible: true,
		OptIn:    boolPtr(false),
	}
	existing := []AddressState{{Name: addrLBWeb, Service: svcWeb, Ready: true, Address: assignedIP}}

	plan := Compute(PlanInput{Policy: policy, Claims: []Claim{v}, Pools: readyPools(), Existing: existing})

	if len(plan.Skips) != 0 {
		t.Fatalf("expected no skip for an explicit opt-out, got %+v", plan.Skips)
	}
	if len(plan.Release) != 1 || plan.Release[0].AddressName != addrLBWeb || plan.Release[0].Address != assignedIP || plan.Release[0].Target.Name != svcWeb {
		t.Fatalf("expected the Address released on opt-out, got %+v", plan.Release)
	}
}

func TestCompute_ReleaseOnServiceDisappearance(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true))
	existing := []AddressState{{Name: "lb-gone", Service: "gone", Ready: true, Address: "10.0.10.8"}}

	plan := Compute(PlanInput{Policy: policy, Claims: nil, Pools: readyPools(), Existing: existing})

	if len(plan.Release) != 1 {
		t.Fatalf("expected one release, got %+v", plan.Release)
	}
	r := plan.Release[0]
	if r.AddressName != "lb-gone" || r.Address != "10.0.10.8" {
		t.Fatalf("Release = %+v, want AddressName lb-gone Address 10.0.10.8", r)
	}
	if r.Target.Name != "gone" || r.Target.Namespace != "team-a" {
		t.Fatalf("Release.Target = %+v, want name gone in namespace team-a", r.Target)
	}
}

func TestCompute_ReleaseWhenAnnotationMissingUnderRequireAnnotation(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true)) // requireAnnotation: true
	v := Claim{
		Target:   TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb},
		Eligible: true,
		// OptIn nil: the allocate annotation is absent, which requireAnnotation
		// treats as opted out.
	}
	existing := []AddressState{{Name: addrLBWeb, Service: svcWeb, Ready: true, Address: assignedIP}}

	plan := Compute(PlanInput{Policy: policy, Claims: []Claim{v}, Pools: readyPools(), Existing: existing})

	if len(plan.Ensure) != 0 {
		t.Fatalf("expected nothing ensured without the allocate annotation, got %d", len(plan.Ensure))
	}
	if len(plan.Release) != 1 || plan.Release[0].AddressName != addrLBWeb {
		t.Fatalf("expected the stale Address released once the annotation disappears, got %+v", plan.Release)
	}
}

func TestCompute_RequireAnnotationFalseDefaultsIn(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(false))
	v := Claim{
		Target:   TargetRef{Kind: TargetService, Namespace: "team-a", Name: svcWeb},
		Eligible: true,
		Hostname: "web.team-a",
		// OptIn nil: with requireAnnotation false every LoadBalancer Service is
		// allocated for by default.
	}

	plan := Compute(PlanInput{Policy: policy, Claims: []Claim{v}, Pools: readyPools()})

	if len(plan.Ensure) != 1 {
		t.Fatalf("expected an Address ensured by default when requireAnnotation is false, got %d", len(plan.Ensure))
	}
}

func TestCompute_SortedAndDisjointOutputs(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true))
	services := []Claim{
		{Target: TargetRef{Kind: TargetService, Namespace: "team-a", Name: "c"}, Eligible: true, OptIn: boolPtr(true), Hostname: "c.team-a"},
		{Target: TargetRef{Kind: TargetService, Namespace: "team-a", Name: "a"}, Eligible: true, OptIn: boolPtr(true), Hostname: "a.team-a"},
		{Target: TargetRef{Kind: TargetService, Namespace: "team-a", Name: "b"}, Eligible: true, OptIn: boolPtr(true), Hostname: "b.team-a"},
	}

	plan := Compute(PlanInput{Policy: policy, Claims: services, Pools: readyPools()})

	if len(plan.Ensure) != 3 {
		t.Fatalf("expected 3 ensured Addresses, got %d", len(plan.Ensure))
	}
	names := make([]string, len(plan.Ensure))
	ensured := make(map[string]bool, len(plan.Ensure))
	for i, d := range plan.Ensure {
		names[i] = d.Target.Name
		ensured[d.Target.Name] = true
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("expected Ensure sorted by Service name, got %v", names)
	}
	for _, r := range plan.Release {
		if ensured[r.Target.Name] {
			t.Fatalf("Service %q appears in both Ensure and Release", r.Target.Name)
		}
	}
}

func TestCompute_SkipsSortedByServiceThenReason(t *testing.T) {
	policy := rangePoolPolicy(boolPtr(true))
	services := []Claim{
		{Target: TargetRef{Kind: TargetService, Namespace: "team-a", Name: "b"}, Eligible: false, OptIn: boolPtr(true)},
		{Target: TargetRef{Kind: TargetService, Namespace: "team-a", Name: "a"}, Eligible: false, OptIn: boolPtr(true)},
	}

	plan := Compute(PlanInput{Policy: policy, Claims: services, Pools: readyPools()})

	if len(plan.Skips) != 2 {
		t.Fatalf("expected 2 skips, got %d", len(plan.Skips))
	}
	if plan.Skips[0].Target.Name != "a" || plan.Skips[1].Target.Name != "b" {
		t.Fatalf("expected skips sorted by Service name, got %+v", plan.Skips)
	}
}
