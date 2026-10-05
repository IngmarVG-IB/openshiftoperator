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
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

func optedIn(target TargetRef, families ...IPFamily) Claim {
	c := Claim{Target: target, Eligible: true, OptIn: boolPtr(true), Hostname: target.Name + "." + testNS}
	if len(families) > 0 {
		c.FamilyPolicy = SingleStack
		if len(families) > 1 {
			c.FamilyPolicy = PreferDualStack
		}
		for _, f := range families {
			c.Slots = append(c.Slots, SlotRequest{Family: f})
		}
	}
	return c
}

func eipTarget(name string) TargetRef {
	return TargetRef{Kind: TargetEgressIP, Name: name, UID: types.UID(name + "-uid")}
}

func egressPolicy() *uddiv1alpha1.IPAllocationPolicy {
	p := rangePoolPolicy(boolPtr(true))
	p.Spec.Targets = []uddiv1alpha1.TargetKind{uddiv1alpha1.TargetKindService, uddiv1alpha1.TargetKindEgressIP}
	return p
}

func eipClaim(name string, count int, current ...string) Claim {
	return Claim{
		Target: eipTarget(name), Eligible: true, OptIn: boolPtr(true),
		Slots: make([]SlotRequest, count), CurrentAssignment: current,
	}
}

func eipAddress(name string, slot int, address string) AddressState {
	return AddressState{
		Name: SlotAddressName("lb", eipTarget(name), slot), TargetKind: TargetEgressIP, TargetName: name,
		Slot: slot, Ready: address != "", Address: address, Family: FamilyOf(address),
	}
}

func svcSlotAddress(service string, slot int, address string) AddressState {
	return AddressState{
		Name: SlotAddressName("lb", svcTargetRef(service), slot), Service: service, TargetKind: TargetService,
		TargetName: service, Slot: slot, Ready: address != "", Address: address, Family: FamilyOf(address),
	}
}

func skipReasonsOf(plan Plan) []SkipReason {
	out := make([]SkipReason, 0, len(plan.Skips))
	for _, s := range plan.Skips {
		out = append(out, s.Reason)
	}
	return out
}

func TestCompute_SingleStackIPv6FromIPv6AllocateFrom(t *testing.T) {
	p := rangePoolPolicy(boolPtr(true))
	pools := map[PoolRef]PoolState{lbPoolRef: {Ref: lbPoolRef, Ready: true, Family: IPv6, SpaceRef: uddiv1alpha1.ObjectRef{Name: "v6"}}}
	svc := lbService(svcWeb, corev1.IPFamilyPolicySingleStack, singleV6, map[string]string{"uddi.infoblox.com/allocate": "true"})

	plan := Compute(PlanInput{Policy: p, Claims: []Claim{ViewService(svc, AssignVia{}, "")}, Pools: pools})

	if len(plan.Skips) != 0 || len(plan.Ensure) != 1 {
		t.Fatalf("plan = %+v, want one ensured Address", plan)
	}
	d := plan.Ensure[0]
	if d.Name != addrLBWeb || d.Slot != 0 || d.Family != IPv6 || d.Spec.SpaceRef.Name != "v6" ||
		d.Spec.NextAvailableFrom == nil || d.Spec.NextAvailableFrom.RangeRef.Name != lbPoolRef.Name {
		t.Fatalf("Ensure[0] = %+v, want slot 0 lb-web from the IPv6 allocateFrom", d)
	}
}

func TestCompute_PoolSelection(t *testing.T) {
	v4 := PoolState{Ref: lbPoolRef, Ready: true, Family: IPv4}
	v6 := PoolState{Ref: v6PoolRef, Ready: true, Family: IPv6}
	unknown := PoolState{Ref: lbPoolRef, Ready: true}
	notReady6 := PoolState{Ref: v6PoolRef}
	v4AsV6 := PoolState{Ref: v6PoolRef, Ready: true, Family: IPv4}
	override := PoolRef{Kind: PoolSubnet, Name: "override"}

	cases := []struct {
		name      string
		ipv6Pool  bool
		pools     map[PoolRef]PoolState
		claim     Claim
		wantPools []PoolRef // in slot order
		wantSkip  SkipReason
	}{
		{name: "IPv4 from allocateFrom", pools: map[PoolRef]PoolState{lbPoolRef: v4},
			claim: optedIn(svcTargetRef(svcWeb), IPv4), wantPools: []PoolRef{lbPoolRef}},
		{name: "IPv6 prefers allocateFromIPv6 over an IPv6 allocateFrom", ipv6Pool: true,
			pools: map[PoolRef]PoolState{lbPoolRef: {Ref: lbPoolRef, Ready: true, Family: IPv6}, v6PoolRef: v6},
			claim: optedIn(svcTargetRef(svcWeb), IPv6), wantPools: []PoolRef{v6PoolRef}},
		{name: "dual-stack uses both pools", ipv6Pool: true, pools: map[PoolRef]PoolState{lbPoolRef: v4, v6PoolRef: v6},
			claim: optedIn(svcTargetRef(svcWeb), IPv4, IPv6), wantPools: []PoolRef{lbPoolRef, v6PoolRef}},
		{name: "PreferDualStack drops a family without a pool", pools: map[PoolRef]PoolState{lbPoolRef: v4},
			claim: optedIn(svcTargetRef(svcWeb), IPv6, IPv4), wantPools: []PoolRef{lbPoolRef}},
		{name: "PreferDualStack with no pool for any family", pools: map[PoolRef]PoolState{lbPoolRef: {Ref: lbPoolRef, Ready: true, Family: "IPvX"}},
			claim: optedIn(svcTargetRef(svcWeb), IPv4, IPv6), wantSkip: SkipNoPoolForFamily},
		{name: "RequireDualStack without an IPv6 pool", pools: map[PoolRef]PoolState{lbPoolRef: v4},
			claim: func() Claim {
				c := optedIn(svcTargetRef(svcWeb), IPv4, IPv6)
				c.FamilyPolicy = RequireDualStack
				return c
			}(), wantSkip: SkipNoPoolForFamily},
		{name: "SingleStack IPv6 without an IPv6 pool", pools: map[PoolRef]PoolState{lbPoolRef: v4},
			claim: optedIn(svcTargetRef(svcWeb), IPv6), wantSkip: SkipNoPoolForFamily},
		{name: "unknown-family allocateFrom serves IPv4", pools: map[PoolRef]PoolState{lbPoolRef: unknown},
			claim: optedIn(svcTargetRef(svcWeb), IPv4), wantPools: []PoolRef{lbPoolRef}},
		{name: "unknown-family allocateFrom does not serve IPv6", pools: map[PoolRef]PoolState{lbPoolRef: unknown},
			claim: optedIn(svcTargetRef(svcWeb), IPv6), wantSkip: SkipNoPoolForFamily},
		{name: "allocateFromIPv6 not ready", ipv6Pool: true, pools: map[PoolRef]PoolState{lbPoolRef: v4, v6PoolRef: notReady6},
			claim: optedIn(svcTargetRef(svcWeb), IPv4, IPv6), wantSkip: SkipPoolNotReady},
		{name: "allocateFromIPv6 that is IPv4 falls back to an IPv6 allocateFrom", ipv6Pool: true,
			pools: map[PoolRef]PoolState{lbPoolRef: {Ref: lbPoolRef, Ready: true, Family: IPv6}, v6PoolRef: v4AsV6},
			claim: optedIn(svcTargetRef(svcWeb), IPv6), wantPools: []PoolRef{lbPoolRef}},
		{name: "override serves its own family, the other family uses the policy pool", ipv6Pool: true,
			pools: map[PoolRef]PoolState{lbPoolRef: v4, v6PoolRef: v6, override: {Ref: override, Ready: true, Family: IPv6}},
			claim: func() Claim {
				c := optedIn(svcTargetRef(svcWeb), IPv4, IPv6)
				c.Pool = &override
				return c
			}(), wantPools: []PoolRef{lbPoolRef, override}},
		{name: "override of a family the Service does not want", pools: map[PoolRef]PoolState{lbPoolRef: v4, override: {Ref: override, Ready: true, Family: IPv6}},
			claim: func() Claim {
				c := optedIn(svcTargetRef(svcWeb), IPv4)
				c.Pool = &override
				return c
			}(), wantSkip: SkipNoPoolForFamily},
		{name: "override not ready", pools: map[PoolRef]PoolState{lbPoolRef: v4},
			claim: func() Claim {
				c := optedIn(svcTargetRef(svcWeb), IPv4)
				c.Pool = &override
				return c
			}(), wantSkip: SkipPoolNotReady},
		{name: "unknown family value has no pool", pools: map[PoolRef]PoolState{lbPoolRef: v4},
			claim: func() Claim {
				c := optedIn(svcTargetRef(svcWeb))
				c.Slots = []SlotRequest{{Family: "IPvX"}}
				return c
			}(), wantSkip: SkipNoPoolForFamily},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := rangePoolPolicy(boolPtr(true))
			if c.ipv6Pool {
				withIPv6Pool(p)
			}
			plan := Compute(PlanInput{Policy: p, Claims: []Claim{c.claim}, Pools: c.pools})
			if c.wantSkip != "" {
				if len(plan.Skips) != 1 || plan.Skips[0].Reason != c.wantSkip || len(plan.Ensure) != 0 {
					t.Fatalf("plan = %+v, want only a %s skip", plan, c.wantSkip)
				}
				if plan.Skips[0].Message == "" {
					t.Fatalf("skip without a message: %+v", plan.Skips[0])
				}
				return
			}
			if len(plan.Skips) != 0 || len(plan.Ensure) != len(c.wantPools) {
				t.Fatalf("plan = %+v, want %d ensured", plan, len(c.wantPools))
			}
			for i, d := range plan.Ensure {
				got := d.Spec.NextAvailableFrom
				want := nextAvailableFromFor(c.wantPools[i])
				if !reflect.DeepEqual(got, want) || d.Slot != i {
					t.Fatalf("Ensure[%d] slot %d allocates from %+v, want slot %d from %+v", i, d.Slot, got, i, want)
				}
			}
		})
	}
}

func TestCompute_DualStackExplicitAddresses(t *testing.T) {
	p := withIPv6Pool(rangePoolPolicy(boolPtr(true)))
	svc := lbService(svcWeb, corev1.IPFamilyPolicyRequireDualStack, dualV4V6, map[string]string{
		"uddi.infoblox.com/allocate": "true",
		"uddi.infoblox.com/address":  "2001:db8::7, 10.0.10.7",
	})
	plan := Compute(PlanInput{Policy: p, Claims: []Claim{ViewService(svc, AssignVia{}, "")}, Pools: dualPools()})
	if len(plan.Ensure) != 2 {
		t.Fatalf("Ensure = %+v", plan.Ensure)
	}
	if plan.Ensure[0].Spec.Address != "10.0.10.7" || plan.Ensure[0].Spec.NextAvailableFrom != nil {
		t.Errorf("slot 0 = %+v, want the IPv4 address", plan.Ensure[0].Spec)
	}
	if plan.Ensure[1].Spec.Address != "2001:db8::7" || plan.Ensure[1].Spec.NextAvailableFrom != nil {
		t.Errorf("slot 1 = %+v, want the IPv6 address", plan.Ensure[1].Spec)
	}
	if !strings.HasSuffix(plan.Ensure[1].Spec.Comment, "for Service web slot 1") {
		t.Errorf("slot 1 comment = %q", plan.Ensure[1].Spec.Comment)
	}
}

func TestCompute_AssignmentWaitsForEverySlot(t *testing.T) {
	p := withIPv6Pool(rangePoolPolicy(boolPtr(true)))
	claim := optedIn(svcTargetRef(svcWeb), IPv4, IPv6)

	pending := svcSlotAddress(svcWeb, 1, "")
	pending.Reason, pending.Message = "APIError", "no free address"
	plan := Compute(PlanInput{Policy: p, Claims: []Claim{claim}, Pools: dualPools(),
		Existing: []AddressState{svcSlotAddress(svcWeb, 0, assignedIP), pending}})
	if len(plan.Assign) != 0 || plan.Assigned != 0 {
		t.Fatalf("Assign = %+v, want nothing until slot 1 is Ready", plan.Assign)
	}
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipAddressPending || plan.Skips[0].Message != "slot 1: APIError: no free address" {
		t.Fatalf("Skips = %+v, want AddressPending naming slot 1", plan.Skips)
	}

	plan = Compute(PlanInput{Policy: p, Claims: []Claim{claim}, Pools: dualPools(),
		Existing: []AddressState{svcSlotAddress(svcWeb, 0, assignedIP), svcSlotAddress(svcWeb, 1, assignedV6)}})
	if len(plan.Assign) != 1 || !reflect.DeepEqual(plan.Assign[0].Values, []string{assignedIP, assignedV6}) {
		t.Fatalf("Assign = %+v, want both addresses in slot order", plan.Assign)
	}
}

func TestCompute_ManuallyAssignedDualStack(t *testing.T) {
	p := withIPv6Pool(rangePoolPolicy(boolPtr(true)))
	existing := []AddressState{svcSlotAddress(svcWeb, 0, assignedIP), svcSlotAddress(svcWeb, 1, assignedV6)}

	foreign := optedIn(svcTargetRef(svcWeb), IPv4, IPv6)
	foreign.CurrentAssignment = []string{assignedIP, "2001:db8::99"}
	plan := Compute(PlanInput{Policy: p, Claims: []Claim{foreign}, Pools: dualPools(), Existing: existing})
	want := DefaultAssignAnnotation + " is already 10.0.10.5,2001:db8::99"
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipManuallyAssigned || plan.Skips[0].Message != want {
		t.Fatalf("Skips = %+v, want ManuallyAssigned %q", plan.Skips, want)
	}
	if len(plan.Ensure) != 0 || len(plan.Release) != 0 {
		t.Fatalf("plan = %+v, want the claim frozen", plan)
	}

	partial := optedIn(svcTargetRef(svcWeb), IPv4, IPv6)
	partial.CurrentAssignment = []string{assignedIP}
	plan = Compute(PlanInput{Policy: p, Claims: []Claim{partial}, Pools: dualPools(), Existing: existing})
	if len(plan.Skips) != 0 || len(plan.Assign) != 1 {
		t.Fatalf("plan = %+v, want a subset of our own addresses to be completed, not ManuallyAssigned", plan)
	}
}

func TestCompute_LoadBalancerIPModeAllocatesOneSlot(t *testing.T) {
	p := withIPv6Pool(rangePoolPolicy(boolPtr(true)))
	p.Spec.AssignVia.Mode = uddiv1alpha1.AssignModeLoadBalancerIP

	// Switched from Annotation mode: slot 1 is released, slot 0 assigned alone.
	claim := optedIn(svcTargetRef(svcWeb), IPv4, IPv6)
	plan := Compute(PlanInput{Policy: p, Claims: []Claim{claim}, Pools: dualPools(),
		Existing: []AddressState{svcSlotAddress(svcWeb, 0, assignedIP), svcSlotAddress(svcWeb, 1, assignedV6)}})
	if len(plan.Ensure) != 1 || plan.Ensure[0].Slot != 0 || plan.Ensure[0].Family != IPv4 {
		t.Fatalf("Ensure = %+v, want slot 0 only", plan.Ensure)
	}
	if len(plan.Release) != 1 || plan.Release[0].Slot != 1 || len(plan.Unassign) != 0 {
		t.Fatalf("Release/Unassign = %+v / %+v, want slot 1 released", plan.Release, plan.Unassign)
	}
	if len(plan.Assign) != 1 || plan.Assign[0].Value() != assignedIP {
		t.Fatalf("Assign = %+v, want the single slot 0 address", plan.Assign)
	}

	// An IPv6-primary Service holding an IPv4 address keeps it.
	v6First := optedIn(svcTargetRef(svcWeb), IPv6, IPv4)
	v6First.CurrentAssignment = []string{assignedIP}
	plan = Compute(PlanInput{Policy: p, Claims: []Claim{v6First}, Pools: dualPools(),
		Existing: []AddressState{{Name: addrLBWeb, Service: svcWeb, Ready: true, Address: assignedIP, Family: IPv4}}})
	if len(plan.Ensure) != 1 || plan.Ensure[0].Name != addrLBWeb || plan.Ensure[0].Family != IPv4 || len(plan.Release) != 0 {
		t.Fatalf("plan = %+v, want the held IPv4 address kept", plan)
	}
	if plan.Assigned != 1 {
		t.Fatalf("Assigned = %d, want 1", plan.Assigned)
	}
}

func TestCompute_ReleaseWholeDualStackClaim(t *testing.T) {
	p := withIPv6Pool(rangePoolPolicy(boolPtr(true)))
	existing := []AddressState{svcSlotAddress(svcWeb, 1, assignedV6), svcSlotAddress(svcWeb, 0, assignedIP)}

	plan := Compute(PlanInput{Policy: p, Pools: dualPools(), Existing: existing})
	if len(plan.Release) != 2 || plan.Release[0].Slot != 0 || plan.Release[1].Slot != 1 {
		t.Fatalf("Release = %+v, want both slots in slot order", plan.Release)
	}
	if plan.Release[0].Target != svcTargetRef(svcWeb) {
		t.Fatalf("Release target = %+v", plan.Release[0].Target)
	}
	if len(plan.Unassign) != 1 || !reflect.DeepEqual(plan.Unassign[0].Addresses, []string{assignedIP, assignedV6}) {
		t.Fatalf("Unassign = %+v, want one entry holding both addresses", plan.Unassign)
	}
}

func TestUnassignApplies(t *testing.T) {
	u := Unassign{Addresses: []string{assignedIP, assignedV6}}
	cases := []struct {
		current []string
		want    bool
	}{
		{nil, false},
		{[]string{assignedIP}, true},
		{[]string{assignedV6, assignedIP}, true},
		{[]string{assignedIP, "192.0.2.1"}, false},
		{[]string{"192.0.2.1"}, false},
	}
	for _, c := range cases {
		if got := u.Applies(c.current); got != c.want {
			t.Errorf("Applies(%v) = %v, want %v", c.current, got, c.want)
		}
	}
}

func TestCompute_EgressIP(t *testing.T) {
	p := egressPolicy()
	p.Spec.RequireAnnotation = boolPtr(false)

	plan := Compute(PlanInput{Policy: p, Claims: []Claim{eipClaim("egress-a", 2)}, Pools: v4OnlyPools()})
	if len(plan.Skips) != 0 || len(plan.Ensure) != 2 || plan.Allocated != 1 {
		t.Fatalf("plan = %+v, want two slots", plan)
	}
	d := plan.Ensure[1]
	if d.Name != "lb-egressip-egress-a-1" || d.Slot != 1 || d.Target != eipTarget("egress-a") || d.Family != "" {
		t.Fatalf("Ensure[1] = %+v", d)
	}
	wantLabels := map[string]string{LabelPolicy: "lb", LabelTargetKind: "EgressIP", LabelTargetName: "egress-a", LabelSlot: "1"}
	if !reflect.DeepEqual(d.Labels, wantLabels) || d.Annotations != nil {
		t.Fatalf("labels/annotations = %v / %v, want %v and none", d.Labels, d.Annotations, wantLabels)
	}
	if d.Spec.Tags[TagSource] != "EgressIP/egress-a" || d.Spec.Tags["team"] != "a" ||
		d.Spec.Comment != "Allocated by IPAllocationPolicy team-a/lb for EgressIP egress-a slot 1" || d.Spec.Names != nil {
		t.Fatalf("spec = %+v", d.Spec)
	}

	// Without the allocate annotation an EgressIP is never allocated, requireAnnotation or not.
	unannotated := eipClaim("egress-b", 1)
	unannotated.OptIn = nil
	plan = Compute(PlanInput{Policy: p, Claims: []Claim{unannotated}, Pools: v4OnlyPools()})
	if len(plan.Ensure) != 0 || len(plan.Skips) != 0 {
		t.Fatalf("plan = %+v, want an EgressIP without the annotation ignored", plan)
	}
}

func TestCompute_EgressIPAssignmentAndCount(t *testing.T) {
	p := egressPolicy()
	a0, a1, a2 := "10.0.10.20", "10.0.10.21", "10.0.10.22"
	all := []AddressState{eipAddress("egress-a", 0, a0), eipAddress("egress-a", 1, a1), eipAddress("egress-a", 2, a2)}

	// Every slot Ready: spec.egressIPs is written in slot order.
	plan := Compute(PlanInput{Policy: p, Claims: []Claim{eipClaim("egress-a", 3)}, Pools: v4OnlyPools(), Existing: all})
	if len(plan.Assign) != 1 || !reflect.DeepEqual(plan.Assign[0].Values, []string{a0, a1, a2}) {
		t.Fatalf("Assign = %+v", plan.Assign)
	}

	// Lowering the count releases the highest slot and narrows spec.egressIPs.
	plan = Compute(PlanInput{Policy: p, Claims: []Claim{eipClaim("egress-a", 2, a0, a1, a2)}, Pools: v4OnlyPools(), Existing: all})
	if len(plan.Skips) != 0 {
		t.Fatalf("Skips = %+v; the released slot's address is still ours, not ManuallyAssigned", plan.Skips)
	}
	if len(plan.Release) != 1 || plan.Release[0].Slot != 2 || plan.Release[0].Address != a2 || len(plan.Unassign) != 0 {
		t.Fatalf("Release/Unassign = %+v / %+v, want slot 2 released only", plan.Release, plan.Unassign)
	}
	if len(plan.Assign) != 1 || !reflect.DeepEqual(plan.Assign[0].Values, []string{a0, a1}) {
		t.Fatalf("Assign = %+v, want the first two", plan.Assign)
	}

	// A foreign address in spec.egressIPs is ManuallyAssigned and freezes the claim.
	plan = Compute(PlanInput{Policy: p, Claims: []Claim{eipClaim("egress-a", 2, "192.0.2.1")}, Pools: v4OnlyPools(), Existing: all})
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipManuallyAssigned ||
		plan.Skips[0].Message != "spec.egressIPs is already 192.0.2.1" || len(plan.Release) != 0 {
		t.Fatalf("plan = %+v, want ManuallyAssigned and nothing released", plan)
	}

	// Opting out releases every slot and clears spec.egressIPs if it holds only ours.
	out := eipClaim("egress-a", 2, a0, a1)
	out.OptIn = boolPtr(false)
	plan = Compute(PlanInput{Policy: p, Claims: []Claim{out}, Pools: v4OnlyPools(), Existing: all})
	if len(plan.Release) != 3 || len(plan.Unassign) != 1 || plan.Unassign[0].Target != eipTarget("egress-a") {
		t.Fatalf("Release/Unassign = %+v / %+v", plan.Release, plan.Unassign)
	}
	if !plan.Unassign[0].Applies([]string{a0, a1}) || plan.Unassign[0].Applies([]string{a0, "192.0.2.1"}) {
		t.Fatalf("Unassign %+v applies to the wrong values", plan.Unassign[0])
	}

	// A vanished EgressIP releases with a cluster-scoped target reference.
	plan = Compute(PlanInput{Policy: p, Pools: v4OnlyPools(), Existing: all[:1]})
	if len(plan.Release) != 1 || plan.Release[0].Target != (TargetRef{Kind: TargetEgressIP, Name: "egress-a"}) {
		t.Fatalf("Release = %+v", plan.Release)
	}
}

func TestCompute_TargetsGateClaimKinds(t *testing.T) {
	existingEIP := eipAddress("egress-a", 0, "10.0.10.20")
	existingSvc := svcSlotAddress(svcWeb, 0, assignedIP)

	// Default targets: EgressIP claims are ignored and EgressIP Addresses left alone.
	p := rangePoolPolicy(boolPtr(true))
	plan := Compute(PlanInput{Policy: p, Claims: []Claim{eipClaim("egress-a", 1)}, Pools: v4OnlyPools(),
		Existing: []AddressState{existingEIP}})
	if len(plan.Ensure) != 0 || len(plan.Release) != 0 || len(plan.Unassign) != 0 {
		t.Fatalf("plan = %+v, want an EgressIP untouched under the default targets", plan)
	}

	// targets [EgressIP]: Service Addresses are left in place, not released.
	p.Spec.Targets = []uddiv1alpha1.TargetKind{uddiv1alpha1.TargetKindEgressIP}
	plan = Compute(PlanInput{Policy: p, Claims: []Claim{optedIn(svcTargetRef(svcWeb))}, Pools: v4OnlyPools(),
		Existing: []AddressState{existingSvc}})
	if len(plan.Ensure) != 0 || len(plan.Release) != 0 || len(plan.Unassign) != 0 || len(plan.Skips) != 0 {
		t.Fatalf("plan = %+v, want Service Addresses left in place", plan)
	}
}

func TestCompute_NameConflicts(t *testing.T) {
	p := withIPv6Pool(rangePoolPolicy(boolPtr(true)))

	// Both new in one pass: slot 0 of web-1 wins its milestone 3 name over slot 1 of web.
	plan := Compute(PlanInput{Policy: p, Pools: dualPools(), Claims: []Claim{
		optedIn(svcTargetRef(svcWeb), IPv4, IPv6),
		optedIn(svcTargetRef("web-1"), IPv4),
	}})
	if len(plan.Ensure) != 1 || plan.Ensure[0].Target.Name != "web-1" || plan.Ensure[0].Name != "lb-web-1" {
		t.Fatalf("Ensure = %+v, want only web-1's slot 0", plan.Ensure)
	}
	if !reflect.DeepEqual(skipReasonsOf(plan), []SkipReason{SkipNameConflict}) || plan.Skips[0].Target.Name != svcWeb {
		t.Fatalf("Skips = %+v, want NameConflict for web", plan.Skips)
	}

	// web's slot 1 exists first: the new web-1 is the one skipped, web is untouched.
	existing := []AddressState{svcSlotAddress(svcWeb, 0, assignedIP), svcSlotAddress(svcWeb, 1, assignedV6)}
	plan = Compute(PlanInput{Policy: p, Pools: dualPools(), Existing: existing, Claims: []Claim{
		optedIn(svcTargetRef(svcWeb), IPv4, IPv6),
		optedIn(svcTargetRef("web-1"), IPv4),
	}})
	if len(plan.Ensure) != 2 || plan.Ensure[0].Target.Name != svcWeb || plan.Ensure[1].Name != "lb-web-1" || plan.Ensure[1].Target.Name != svcWeb {
		t.Fatalf("Ensure = %+v, want web's two slots", plan.Ensure)
	}
	if len(plan.Skips) != 1 || plan.Skips[0].Target.Name != "web-1" || plan.Skips[0].Reason != SkipNameConflict ||
		!strings.Contains(plan.Skips[0].Message, "slot 1 of Service/web") || len(plan.Release) != 0 {
		t.Fatalf("plan = %+v, want NameConflict for web-1 and nothing released", plan)
	}

	// An Address without target labels holding the name is never taken over.
	stray := AddressState{Name: addrLBWeb, Address: "10.0.10.99", Ready: true}
	plan = Compute(PlanInput{Policy: p, Pools: dualPools(), Existing: []AddressState{stray},
		Claims: []Claim{optedIn(svcTargetRef(svcWeb), IPv4)}})
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipNameConflict || len(plan.Release) != 0 || len(plan.Ensure) != 0 {
		t.Fatalf("plan = %+v, want NameConflict and the unlabelled Address left alone", plan)
	}

	// A Service named like an EgressIP slot: the Service's slot 0 name wins.
	ep := egressPolicy()
	plan = Compute(PlanInput{Policy: ep, Pools: v4OnlyPools(), Claims: []Claim{
		eipClaim("x", 1), optedIn(svcTargetRef("egressip-x-0")),
	}})
	if len(plan.Ensure) != 1 || plan.Ensure[0].Target.Kind != TargetService || len(plan.Skips) != 1 ||
		plan.Skips[0].Target.Kind != TargetEgressIP || plan.Skips[0].Reason != SkipNameConflict {
		t.Fatalf("plan = %+v, want the Service to keep lb-egressip-x-0", plan)
	}

	// Two new non-slot-0 names collide: the claim planned first keeps it.
	withIPv6Pool(ep)
	plan = Compute(PlanInput{Policy: ep, Pools: dualPools(), Claims: []Claim{
		optedIn(svcTargetRef("egressip-a"), IPv4, IPv6), eipClaim("a", 2),
	}})
	if len(plan.Ensure) != 2 || plan.Ensure[1].Name != "lb-egressip-a-1" || plan.Ensure[1].Target.Kind != TargetEgressIP {
		t.Fatalf("Ensure = %+v, want the EgressIP's two slots", plan.Ensure)
	}
	if len(plan.Skips) != 1 || plan.Skips[0].Target.Name != "egressip-a" ||
		!strings.Contains(plan.Skips[0].Message, "already planned for another target") {
		t.Fatalf("Skips = %+v", plan.Skips)
	}
}

func TestCompute_UnattributedAndStrayAddresses(t *testing.T) {
	p := rangePoolPolicy(boolPtr(true))
	unlabelled := AddressState{Name: "lb-mystery", Ready: true, Address: "10.0.10.50"}
	badSlot := AddressState{Name: "lb-bad", TargetKind: TargetService, TargetName: svcWeb, Slot: -1}
	badKind := AddressState{Name: "lb-kind", TargetKind: "Pod", TargetName: svcWeb}
	plan := Compute(PlanInput{Policy: p, Pools: v4OnlyPools(), Existing: []AddressState{unlabelled, badSlot, badKind}})
	if len(plan.Release) != 0 || len(plan.Unassign) != 0 {
		t.Fatalf("plan = %+v, want Addresses without a readable target left alone", plan)
	}

	// Two Addresses claim slot 0 of web: the one with the derived name is kept, the
	// other released without clearing the assignment.
	dup := svcSlotAddress(svcWeb, 0, "10.0.10.6")
	dup.Name = "lb-web-old"
	claim := optedIn(svcTargetRef(svcWeb), IPv4)
	plan = Compute(PlanInput{Policy: p, Pools: v4OnlyPools(), Claims: []Claim{claim},
		Existing: []AddressState{svcSlotAddress(svcWeb, 0, assignedIP), dup}})
	if len(plan.Ensure) != 1 || plan.Ensure[0].Name != addrLBWeb {
		t.Fatalf("Ensure = %+v", plan.Ensure)
	}
	if len(plan.Release) != 1 || plan.Release[0].AddressName != "lb-web-old" || len(plan.Unassign) != 0 {
		t.Fatalf("Release/Unassign = %+v / %+v", plan.Release, plan.Unassign)
	}

	// Only the non-derived Address exists for slot 0: its name is kept.
	plan = Compute(PlanInput{Policy: p, Pools: v4OnlyPools(), Claims: []Claim{claim}, Existing: []AddressState{dup}})
	if len(plan.Ensure) != 1 || plan.Ensure[0].Name != "lb-web-old" || len(plan.Release) != 0 {
		t.Fatalf("plan = %+v, want the existing name reused", plan)
	}
}

func TestCompute_FamilyStablePlacement(t *testing.T) {
	p := withIPv6Pool(rangePoolPolicy(boolPtr(true)))

	// An IPv6 Address at slot 0 that is still being allocated (family from its pool) keeps
	// slot 0; the IPv4 family goes to slot 1.
	pendingV6 := AddressState{Name: addrLBWeb, TargetKind: TargetService, TargetName: svcWeb, Pool: &v6PoolRef}
	plan := Compute(PlanInput{Policy: p, Pools: dualPools(), Existing: []AddressState{pendingV6},
		Claims: []Claim{optedIn(svcTargetRef(svcWeb), IPv4, IPv6)}})
	if len(plan.Ensure) != 2 || plan.Ensure[0].Family != IPv6 || plan.Ensure[0].Name != addrLBWeb ||
		plan.Ensure[1].Family != IPv4 || plan.Ensure[1].Name != "lb-web-1" {
		t.Fatalf("Ensure = %+v, want IPv6 kept at slot 0 and IPv4 at slot 1", plan.Ensure)
	}

	// A slot held by a family no longer wanted is skipped over until it is released.
	single := optedIn(svcTargetRef(svcWeb), IPv4)
	plan = Compute(PlanInput{Policy: p, Pools: dualPools(),
		Existing: []AddressState{svcSlotAddress(svcWeb, 0, assignedV6)}, Claims: []Claim{single}})
	if len(plan.Ensure) != 1 || plan.Ensure[0].Slot != 1 || plan.Ensure[0].Name != "lb-web-1" {
		t.Fatalf("Ensure = %+v, want IPv4 at slot 1 beside the IPv6 slot 0", plan.Ensure)
	}
	if len(plan.Release) != 1 || plan.Release[0].Slot != 0 {
		t.Fatalf("Release = %+v, want the IPv6 slot released", plan.Release)
	}

	// An existing slot of unknown family takes the family landing on its number.
	unknown := AddressState{Name: addrLBWeb, TargetKind: TargetService, TargetName: svcWeb}
	plan = Compute(PlanInput{Policy: p, Pools: dualPools(), Existing: []AddressState{unknown},
		Claims: []Claim{optedIn(svcTargetRef(svcWeb), IPv4, IPv6)}})
	if len(plan.Ensure) != 2 || plan.Ensure[0].Name != addrLBWeb || plan.Ensure[0].Family != IPv4 {
		t.Fatalf("Ensure = %+v", plan.Ensure)
	}

	// Two Addresses of one family: the second's number is never reused for another family.
	dupV4 := svcSlotAddress(svcWeb, 1, "10.0.10.6")
	plan = Compute(PlanInput{Policy: p, Pools: dualPools(),
		Existing: []AddressState{svcSlotAddress(svcWeb, 0, assignedIP), dupV4},
		Claims:   []Claim{optedIn(svcTargetRef(svcWeb), IPv4, IPv6)}})
	if len(plan.Ensure) != 2 || plan.Ensure[1].Slot != 2 || plan.Ensure[1].Family != IPv6 {
		t.Fatalf("Ensure = %+v, want IPv6 at slot 2", plan.Ensure)
	}
	if len(plan.Release) != 1 || plan.Release[0].Slot != 1 {
		t.Fatalf("Release = %+v, want the duplicate IPv4 slot released", plan.Release)
	}
}

func TestCompute_DuplicateClaimsAndDeterminism(t *testing.T) {
	p := egressPolicy()
	claims := []Claim{
		eipClaim("e2", 2), optedIn(svcTargetRef("b"), IPv4), eipClaim("e1", 1),
		optedIn(svcTargetRef("a"), IPv4), optedIn(svcTargetRef("b"), IPv4),
	}
	existing := []AddressState{svcSlotAddress("gone", 0, "10.0.10.9"), eipAddress("e3", 0, "10.0.10.8")}
	first := Compute(PlanInput{Policy: p, Claims: claims, Pools: v4OnlyPools(), Existing: existing})

	reversed := make([]Claim, len(claims))
	for i := range claims {
		reversed[len(claims)-1-i] = claims[i]
	}
	second := Compute(PlanInput{Policy: p, Claims: reversed, Pools: v4OnlyPools(),
		Existing: []AddressState{existing[1], existing[0]}})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("plans depend on input order:\n%+v\n%+v", first, second)
	}
	order := make([]string, 0, len(first.Ensure))
	for _, d := range first.Ensure {
		order = append(order, d.Name)
	}
	want := []string{"lb-egressip-e1-0", "lb-egressip-e2-0", "lb-egressip-e2-1", "lb-a", "lb-b"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("Ensure order = %v, want %v", order, want)
	}
	if first.Allocated != 4 || len(first.Release) != 2 || len(first.Unassign) != 2 {
		t.Fatalf("Allocated/Release/Unassign = %d/%+v/%+v", first.Allocated, first.Release, first.Unassign)
	}
}

func TestCompute_SkipsSortedByKindNameReason(t *testing.T) {
	p := egressPolicy()
	bad := eipClaim("e", 1)
	bad.Invalid = "bad"
	plan := Compute(PlanInput{Policy: p, Pools: map[PoolRef]PoolState{}, Claims: []Claim{
		optedIn(svcTargetRef("b")), bad, optedIn(svcTargetRef("a")),
	}})
	got := make([]string, 0, len(plan.Skips))
	for _, s := range plan.Skips {
		got = append(got, s.Target.String()+":"+string(s.Reason))
	}
	want := []string{"EgressIP/e:InvalidAnnotation", "Service/a:PoolNotReady", "Service/b:PoolNotReady"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Skips = %v, want %v", got, want)
	}
}
