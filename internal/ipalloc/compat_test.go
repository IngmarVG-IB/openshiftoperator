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

// These tests pin the upgrade path from milestone 3: an Address the milestone 3 planner
// created (name "<policy>-<service>", only the policy and service labels) must be read as
// slot 0 of its Service and must never be released or re-created by the claims planner,
// whatever the Service does next. The milestone 3 Address and its spec are rebuilt here
// exactly as the milestone 3 planner and controller produced them.

import (
	"fmt"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

const (
	testNS     = "team-a"
	assignedV6 = "2001:db8::5"
)

var v6PoolRef = PoolRef{Kind: PoolRange, Name: "lb-pool-v6"}

// dualPools has the IPv4 Range lb-pool and the IPv6 Range lb-pool-v6, both ready.
func dualPools() map[PoolRef]PoolState {
	return map[PoolRef]PoolState{
		lbPoolRef: {Ref: lbPoolRef, Ready: true, SpaceRef: uddiv1alpha1.ObjectRef{Name: "corp"}, Family: IPv4},
		v6PoolRef: {Ref: v6PoolRef, Ready: true, SpaceRef: uddiv1alpha1.ObjectRef{Name: "corp"}, Family: IPv6},
	}
}

// v4OnlyPools has only the IPv4 Range lb-pool, with its family known.
func v4OnlyPools() map[PoolRef]PoolState {
	return map[PoolRef]PoolState{
		lbPoolRef: {Ref: lbPoolRef, Ready: true, SpaceRef: uddiv1alpha1.ObjectRef{Name: "corp"}, Family: IPv4},
	}
}

// withIPv6Pool adds allocateFromIPv6: rangeRef lb-pool-v6 to p.
func withIPv6Pool(p *uddiv1alpha1.IPAllocationPolicy) *uddiv1alpha1.IPAllocationPolicy {
	p.Spec.AllocateFromIPv6 = &uddiv1alpha1.AllocateFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: v6PoolRef.Name}}
	return p
}

// lbService builds a LoadBalancer Service as the API server stores it: spec.ipFamilies
// and spec.ipFamilyPolicy are always defaulted.
func lbService(name string, policy corev1.IPFamilyPolicy, families []corev1.IPFamily, ann map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: name, UID: types.UID(name + "-uid"), Annotations: ann},
		Spec: corev1.ServiceSpec{
			Type:           corev1.ServiceTypeLoadBalancer,
			IPFamilyPolicy: &policy,
			IPFamilies:     families,
		},
	}
}

var (
	singleV4 = []corev1.IPFamily{corev1.IPv4Protocol}
	singleV6 = []corev1.IPFamily{corev1.IPv6Protocol}
	dualV4V6 = []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol}
	dualV6V4 = []corev1.IPFamily{corev1.IPv6Protocol, corev1.IPv4Protocol}
)

// milestone3Spec is the AddressSpec the milestone 3 buildDesiredAddress produced for a
// Service allocated next-available from rangePoolPolicy's lb-pool.
func milestone3Spec(p *uddiv1alpha1.IPAllocationPolicy, service string) uddiv1alpha1.AddressSpec {
	tags := map[string]string{}
	for k, v := range p.Spec.Tags {
		tags[k] = v
	}
	tags[TagSource] = fmt.Sprintf("Service/%s/%s", p.Namespace, service)
	return uddiv1alpha1.AddressSpec{
		CommonSpec: uddiv1alpha1.CommonSpec{
			ConnectionRef: p.Spec.ConnectionRef,
			Tags:          tags,
			Lifecycle:     p.Spec.AddressLifecycle,
		},
		SpaceRef:          uddiv1alpha1.ObjectRef{Name: "corp"},
		Names:             []uddiv1alpha1.AddressName{{Name: service + "." + p.Namespace, Type: "user"}},
		Comment:           fmt.Sprintf("Allocated by IPAllocationPolicy %s/%s for Service %s", p.Namespace, p.Name, service),
		NextAvailableFrom: &uddiv1alpha1.NextAvailableFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: lbPoolRef.Name}},
	}
}

// milestone3Address is an Address exactly as the milestone 3 controller left it: name
// "<policy>-<service>", only the policy and service labels, Ready with assignedIP.
func milestone3Address(p *uddiv1alpha1.IPAllocationPolicy, service string) *uddiv1alpha1.Address {
	return &uddiv1alpha1.Address{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: p.Namespace,
			Name:      p.Name + "-" + service,
			Labels:    map[string]string{LabelPolicy: p.Name, LabelService: service},
		},
		Spec: milestone3Spec(p, service),
		Status: uddiv1alpha1.AddressStatus{
			ObjectStatus: uddiv1alpha1.ObjectStatus{
				ID:         "ipam/address/1",
				Conditions: []metav1.Condition{{Type: uddiv1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Synced"}},
			},
			Address: assignedIP,
		},
	}
}

// newSlotAddress is an Address as the claims planner creates it for slot of a Service,
// Ready with address.
func newSlotAddress(t *testing.T, plan Plan, slot int, address string) AddressState {
	t.Helper()
	for _, d := range plan.Ensure {
		if d.Slot == slot {
			a := &uddiv1alpha1.Address{
				ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: d.Name, Labels: d.Labels, Annotations: d.Annotations},
				Spec:       d.Spec,
				Status: uddiv1alpha1.AddressStatus{
					ObjectStatus: uddiv1alpha1.ObjectStatus{
						Conditions: []metav1.Condition{{Type: uddiv1alpha1.ConditionReady, Status: metav1.ConditionTrue}},
					},
					Address: address,
				},
			}
			return AddressStateOf(a)
		}
	}
	t.Fatalf("no slot %d in plan.Ensure %+v", slot, plan.Ensure)
	return AddressState{}
}

// assertSlotZeroUntouched checks the plan keeps the milestone 3 Address exactly: ensured
// under its own name with the milestone 3 spec (so server-side apply changes nothing
// Universal DDI sees), its labels a superset of the milestone 3 ones, and never released.
func assertSlotZeroUntouched(t *testing.T, plan Plan, p *uddiv1alpha1.IPAllocationPolicy, service string) {
	t.Helper()
	var slot0 *DesiredAddress
	for i := range plan.Ensure {
		if plan.Ensure[i].Target.Name == service && plan.Ensure[i].Slot == 0 {
			slot0 = &plan.Ensure[i]
		}
	}
	if slot0 == nil {
		t.Fatalf("slot 0 of %s not ensured; Ensure = %+v, Skips = %+v", service, plan.Ensure, plan.Skips)
	}
	if slot0.Name != p.Name+"-"+service {
		t.Errorf("slot 0 name = %q, want the milestone 3 name %q", slot0.Name, p.Name+"-"+service)
	}
	if !reflect.DeepEqual(slot0.Spec, milestone3Spec(p, service)) {
		t.Errorf("slot 0 spec changed from milestone 3:\n got %+v\nwant %+v", slot0.Spec, milestone3Spec(p, service))
	}
	if slot0.Labels[LabelPolicy] != p.Name || slot0.Labels[LabelService] != service {
		t.Errorf("slot 0 lost its milestone 3 labels: %v", slot0.Labels)
	}
	if slot0.Labels[LabelTargetKind] != "Service" || slot0.Labels[LabelTargetName] != service || slot0.Labels[LabelSlot] != "0" {
		t.Errorf("slot 0 labels = %v, want target-kind Service, target-name %s, slot 0", slot0.Labels, service)
	}
	for _, r := range plan.Release {
		if r.AddressName == p.Name+"-"+service {
			t.Fatalf("the milestone 3 Address is released: %+v", r)
		}
	}
	for _, u := range plan.Unassign {
		if u.Target.Name == service {
			t.Fatalf("the Service's assignment is cleared: %+v", u)
		}
	}
}

func TestCompat_MilestoneThreeAddressReadAsSlotZero(t *testing.T) {
	p := rangePoolPolicy(boolPtr(true))
	st := AddressStateOf(milestone3Address(p, svcWeb))

	ref, slot, ok := st.Target(testNS)
	if !ok || slot != 0 || ref != (TargetRef{Kind: TargetService, Namespace: testNS, Name: svcWeb}) {
		t.Fatalf("Target() = %+v, %d, %v; want slot 0 of Service web", ref, slot, ok)
	}
	if st.Family != IPv4 || st.Pool == nil || *st.Pool != lbPoolRef || !st.Ready || st.Address != assignedIP {
		t.Fatalf("AddressStateOf = %+v", st)
	}
}

// TestCompat_SingleStackServiceUnchanged reconciles a milestone 3 Service, single-stack
// IPv4 as the API server defaults it, with the same policy: the plan is the milestone 3
// plan, nothing allocated, assigned or released anew.
func TestCompat_SingleStackServiceUnchanged(t *testing.T) {
	for _, name := range []string{svcWeb, "api-gateway"} {
		for _, pools := range []map[PoolRef]PoolState{readyPools(), v4OnlyPools()} {
			p := rangePoolPolicy(boolPtr(true))
			svc := lbService(name, corev1.IPFamilyPolicySingleStack, singleV4, map[string]string{
				"uddi.infoblox.com/allocate": "true",
				AnnotationMetalLB:            assignedIP,
			})
			in := PlanInput{
				Policy:   p,
				Claims:   []Claim{ViewService(svc, AssignVia{Mode: AssignModeAnnotation}, "")},
				Pools:    pools,
				Existing: []AddressState{AddressStateOf(milestone3Address(p, name))},
			}
			plan := Compute(in)

			assertSlotZeroUntouched(t, plan, p, name)
			if len(plan.Ensure) != 1 || len(plan.Assign) != 0 || len(plan.Release) != 0 || len(plan.Unassign) != 0 || len(plan.Skips) != 0 {
				t.Fatalf("plan = %+v, want exactly the one milestone 3 Address ensured", plan)
			}
			if plan.Allocated != 1 || plan.Assigned != 1 {
				t.Fatalf("Allocated/Assigned = %d/%d, want 1/1", plan.Allocated, plan.Assigned)
			}
		}
	}
}

// TestCompat_ServiceBecomesDualStack takes a milestone 3 Service to PreferDualStack with
// an IPv6 pool configured: slot 1 is added next to the untouched slot 0, and the
// assignment becomes both addresses once slot 1 is Ready.
func TestCompat_ServiceBecomesDualStack(t *testing.T) {
	p := withIPv6Pool(rangePoolPolicy(boolPtr(true)))
	legacy := AddressStateOf(milestone3Address(p, svcWeb))
	via := AssignVia{Mode: AssignModeAnnotation}
	svc := lbService(svcWeb, corev1.IPFamilyPolicyPreferDualStack, dualV4V6, map[string]string{
		"uddi.infoblox.com/allocate": "true",
		AnnotationMetalLB:            assignedIP,
	})

	// Round 1: slot 1 does not exist yet.
	plan := Compute(PlanInput{Policy: p, Claims: []Claim{ViewService(svc, via, "")}, Pools: dualPools(), Existing: []AddressState{legacy}})
	assertSlotZeroUntouched(t, plan, p, svcWeb)
	if len(plan.Ensure) != 2 || len(plan.Skips) != 0 || len(plan.Assign) != 0 || len(plan.Release) != 0 {
		t.Fatalf("round 1 plan = %+v, want slot 0 and slot 1 ensured and nothing else", plan)
	}
	slot1 := plan.Ensure[1]
	if slot1.Name != "lb-web-1" || slot1.Slot != 1 || slot1.Family != IPv6 {
		t.Fatalf("slot 1 = %+v, want lb-web-1, IPv6", slot1)
	}
	if slot1.Spec.NextAvailableFrom == nil || slot1.Spec.NextAvailableFrom.RangeRef == nil ||
		slot1.Spec.NextAvailableFrom.RangeRef.Name != v6PoolRef.Name {
		t.Fatalf("slot 1 allocates from %+v, want the IPv6 range", slot1.Spec.NextAvailableFrom)
	}
	if slot1.Labels[LabelSlot] != "1" || slot1.Labels[LabelService] != svcWeb {
		t.Fatalf("slot 1 labels = %v", slot1.Labels)
	}
	if plan.Allocated != 1 || plan.Assigned != 0 {
		t.Fatalf("round 1 Allocated/Assigned = %d/%d, want 1/0 (slot 1 still pending)", plan.Allocated, plan.Assigned)
	}

	// Round 2: slot 1 is Ready, so both addresses are written in slot order.
	existing := []AddressState{legacy, newSlotAddress(t, plan, 1, assignedV6)}
	plan = Compute(PlanInput{Policy: p, Claims: []Claim{ViewService(svc, via, "")}, Pools: dualPools(), Existing: existing})
	assertSlotZeroUntouched(t, plan, p, svcWeb)
	if len(plan.Assign) != 1 || plan.Assign[0].Value() != assignedIP+","+assignedV6 {
		t.Fatalf("round 2 Assign = %+v, want %s,%s", plan.Assign, assignedIP, assignedV6)
	}

	// Round 3: the annotation carries both, so the claim counts as assigned.
	svc.Annotations[AnnotationMetalLB] = assignedIP + "," + assignedV6
	plan = Compute(PlanInput{Policy: p, Claims: []Claim{ViewService(svc, via, "")}, Pools: dualPools(), Existing: existing})
	assertSlotZeroUntouched(t, plan, p, svcWeb)
	if len(plan.Assign) != 0 || plan.Assigned != 1 || len(plan.Skips) != 0 || len(plan.Release) != 0 {
		t.Fatalf("round 3 plan = %+v, want assigned and nothing to do", plan)
	}

	// Round 4: back to SingleStack releases slot 1 only and narrows the assignment.
	single := lbService(svcWeb, corev1.IPFamilyPolicySingleStack, singleV4, svc.Annotations)
	plan = Compute(PlanInput{Policy: p, Claims: []Claim{ViewService(single, via, "")}, Pools: dualPools(), Existing: existing})
	assertSlotZeroUntouched(t, plan, p, svcWeb)
	if len(plan.Release) != 1 || plan.Release[0].AddressName != "lb-web-1" || plan.Release[0].Slot != 1 {
		t.Fatalf("round 4 Release = %+v, want only lb-web-1", plan.Release)
	}
	if len(plan.Assign) != 1 || plan.Assign[0].Value() != assignedIP || len(plan.Unassign) != 0 {
		t.Fatalf("round 4 Assign/Unassign = %+v / %+v, want the assignment narrowed to slot 0", plan.Assign, plan.Unassign)
	}
}

// TestCompat_DualStackWithoutIPv6Pool: a PreferDualStack Service under a milestone 3
// policy (no allocateFromIPv6, IPv4 allocateFrom) keeps exactly its one IPv4 address.
func TestCompat_DualStackWithoutIPv6Pool(t *testing.T) {
	p := rangePoolPolicy(boolPtr(true))
	svc := lbService(svcWeb, corev1.IPFamilyPolicyPreferDualStack, dualV4V6, map[string]string{
		"uddi.infoblox.com/allocate": "true",
		AnnotationMetalLB:            assignedIP,
	})
	plan := Compute(PlanInput{
		Policy:   p,
		Claims:   []Claim{ViewService(svc, AssignVia{Mode: AssignModeAnnotation}, "")},
		Pools:    v4OnlyPools(),
		Existing: []AddressState{AddressStateOf(milestone3Address(p, svcWeb))},
	})
	assertSlotZeroUntouched(t, plan, p, svcWeb)
	if len(plan.Ensure) != 1 || plan.Assigned != 1 || len(plan.Skips) != 0 {
		t.Fatalf("plan = %+v, want the IPv6 family dropped silently", plan)
	}
}

// TestCompat_IPv6PrimaryKeepsItsIPv4Slot: an IPv6-primary dual-stack Service that got its
// IPv4 address from milestone 3 (the IPv6 family had no pool) keeps it at slot 0 when an
// IPv6 pool is added; the IPv6 address becomes slot 1 instead of displacing it.
func TestCompat_IPv6PrimaryKeepsItsIPv4Slot(t *testing.T) {
	p := withIPv6Pool(rangePoolPolicy(boolPtr(true)))
	svc := lbService(svcWeb, corev1.IPFamilyPolicyPreferDualStack, dualV6V4, map[string]string{
		"uddi.infoblox.com/allocate": "true",
		AnnotationMetalLB:            assignedIP,
	})
	legacy := AddressStateOf(milestone3Address(p, svcWeb))
	plan := Compute(PlanInput{
		Policy:   p,
		Claims:   []Claim{ViewService(svc, AssignVia{Mode: AssignModeAnnotation}, "")},
		Pools:    dualPools(),
		Existing: []AddressState{legacy},
	})
	assertSlotZeroUntouched(t, plan, p, svcWeb)
	if len(plan.Ensure) != 2 || plan.Ensure[1].Family != IPv6 || plan.Ensure[1].Name != "lb-web-1" {
		t.Fatalf("Ensure = %+v, want the IPv6 address as slot 1", plan.Ensure)
	}

	existing := []AddressState{legacy, newSlotAddress(t, plan, 1, assignedV6)}
	plan = Compute(PlanInput{
		Policy:   p,
		Claims:   []Claim{ViewService(svc, AssignVia{Mode: AssignModeAnnotation}, "")},
		Pools:    dualPools(),
		Existing: existing,
	})
	if len(plan.Assign) != 1 || plan.Assign[0].Value() != assignedIP+","+assignedV6 {
		t.Fatalf("Assign = %+v, want slot order %s,%s", plan.Assign, assignedIP, assignedV6)
	}
}

// TestCompat_FrozenNotReleased covers the milestone 7 skips a milestone 3 Service can now
// hit; each must freeze the claim (keep the Address and the assignment) rather than
// release it.
func TestCompat_FrozenNotReleased(t *testing.T) {
	cases := []struct {
		name   string
		policy func() *uddiv1alpha1.IPAllocationPolicy
		svc    *corev1.Service
		pools  map[PoolRef]PoolState
		reason SkipReason
	}{
		{
			name:   "RequireDualStack without an IPv6 pool",
			policy: func() *uddiv1alpha1.IPAllocationPolicy { return rangePoolPolicy(boolPtr(true)) },
			svc: lbService(svcWeb, corev1.IPFamilyPolicyRequireDualStack, dualV4V6,
				map[string]string{"uddi.infoblox.com/allocate": "true", AnnotationMetalLB: assignedIP}),
			pools:  v4OnlyPools(),
			reason: SkipNoPoolForFamily,
		},
		{
			name: "RequireDualStack in LoadBalancerIP mode",
			policy: func() *uddiv1alpha1.IPAllocationPolicy {
				p := withIPv6Pool(rangePoolPolicy(boolPtr(true)))
				p.Spec.AssignVia.Mode = uddiv1alpha1.AssignModeLoadBalancerIP
				return p
			},
			svc: func() *corev1.Service {
				s := lbService(svcWeb, corev1.IPFamilyPolicyRequireDualStack, dualV4V6,
					map[string]string{"uddi.infoblox.com/allocate": "true"})
				s.Spec.LoadBalancerIP = assignedIP
				return s
			}(),
			pools:  dualPools(),
			reason: SkipDualStackUnsupported,
		},
		{
			name:   "IPv4 Service whose allocateFrom is IPv6 (a milestone 3 misconfiguration)",
			policy: func() *uddiv1alpha1.IPAllocationPolicy { return rangePoolPolicy(boolPtr(true)) },
			svc: lbService(svcWeb, corev1.IPFamilyPolicySingleStack, singleV4,
				map[string]string{"uddi.infoblox.com/allocate": "true", AnnotationMetalLB: assignedIP}),
			pools: map[PoolRef]PoolState{
				lbPoolRef: {Ref: lbPoolRef, Ready: true, SpaceRef: uddiv1alpha1.ObjectRef{Name: "corp"}, Family: IPv6},
			},
			reason: SkipNoPoolForFamily,
		},
		{
			name: "PreferDualStack whose IPv6 pool was removed after slot 1 was allocated",
			policy: func() *uddiv1alpha1.IPAllocationPolicy {
				return rangePoolPolicy(boolPtr(true))
			},
			svc: lbService(svcWeb, corev1.IPFamilyPolicyPreferDualStack, dualV4V6,
				map[string]string{"uddi.infoblox.com/allocate": "true", AnnotationMetalLB: assignedIP + "," + assignedV6}),
			pools:  v4OnlyPools(),
			reason: SkipNoPoolForFamily,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.policy()
			via := AssignVia{Mode: AssignMode(p.Spec.AssignVia.Mode)}
			existing := []AddressState{AddressStateOf(milestone3Address(p, svcWeb))}
			if c.reason == SkipNoPoolForFamily && c.svc.Spec.IPFamilyPolicy != nil &&
				*c.svc.Spec.IPFamilyPolicy == corev1.IPFamilyPolicyPreferDualStack {
				existing = append(existing, AddressState{
					Name: "lb-web-1", TargetKind: TargetService, TargetName: svcWeb, Slot: 1,
					Ready: true, Address: assignedV6, Family: IPv6,
				})
			}
			plan := Compute(PlanInput{Policy: p, Claims: []Claim{ViewService(c.svc, via, "")}, Pools: c.pools, Existing: existing})
			if len(plan.Skips) != 1 || plan.Skips[0].Reason != c.reason {
				t.Fatalf("Skips = %+v, want one %s", plan.Skips, c.reason)
			}
			if len(plan.Release) != 0 || len(plan.Unassign) != 0 || len(plan.Ensure) != 0 || len(plan.Assign) != 0 {
				t.Fatalf("plan = %+v, want the claim frozen", plan)
			}
		})
	}
}

// TestCompat_FamilyPathMatchesLegacyPath runs the milestone 3 scenarios twice, once with
// the family-agnostic claim the milestone 3 tests use and once with the single-stack IPv4
// claim ViewService builds from a defaulted Service, and expects identical plans apart
// from the slot family.
func TestCompat_FamilyPathMatchesLegacyPath(t *testing.T) {
	type scenario struct {
		name     string
		policy   *uddiv1alpha1.IPAllocationPolicy
		claim    Claim
		existing []AddressState
	}
	base := func() Claim {
		return Claim{Target: svcTargetRef(svcWeb), Eligible: true, OptIn: boolPtr(true), Hostname: "web.team-a"}
	}
	lbIP := rangePoolPolicy(boolPtr(true))
	lbIP.Spec.AssignVia.Mode = uddiv1alpha1.AssignModeLoadBalancerIP
	scenarios := []scenario{
		{name: "new allocation", policy: rangePoolPolicy(boolPtr(true)), claim: base()},
		{name: "explicit address", policy: rangePoolPolicy(boolPtr(true)), claim: func() Claim {
			c := base()
			c.Slots = []SlotRequest{{Address: "10.0.10.7"}}
			return c
		}()},
		{name: "needs assignment", policy: rangePoolPolicy(boolPtr(true)), claim: base(),
			existing: []AddressState{{Name: addrLBWeb, Service: svcWeb, Ready: true, Address: assignedIP}}},
		{name: "assigned", policy: rangePoolPolicy(boolPtr(true)), claim: func() Claim {
			c := base()
			c.CurrentAssignment = []string{assignedIP}
			return c
		}(), existing: []AddressState{{Name: addrLBWeb, Service: svcWeb, Ready: true, Address: assignedIP}}},
		{name: "pending", policy: rangePoolPolicy(boolPtr(true)), claim: base(),
			existing: []AddressState{{Name: addrLBWeb, Service: svcWeb, Reason: "APIError", Message: "boom"}}},
		{name: "manually assigned", policy: rangePoolPolicy(boolPtr(true)), claim: func() Claim {
			c := base()
			c.CurrentAssignment = []string{"10.0.10.9"}
			return c
		}(), existing: []AddressState{{Name: addrLBWeb, Service: svcWeb, Ready: true, Address: assignedIP}}},
		{name: "LoadBalancerIP mode", policy: lbIP, claim: base(),
			existing: []AddressState{{Name: addrLBWeb, Service: svcWeb, Ready: true, Address: assignedIP}}},
		{name: "opt out", policy: rangePoolPolicy(boolPtr(true)), claim: func() Claim {
			c := base()
			c.OptIn = boolPtr(false)
			return c
		}(), existing: []AddressState{{Name: addrLBWeb, Service: svcWeb, Ready: true, Address: assignedIP}}},
	}
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			legacy := Compute(PlanInput{Policy: s.policy, Claims: []Claim{s.claim}, Pools: v4OnlyPools(), Existing: s.existing})

			typed := s.claim
			typed.FamilyPolicy = SingleStack
			addr := ""
			if len(s.claim.Slots) > 0 {
				addr = s.claim.Slots[0].Address
			}
			typed.Slots = []SlotRequest{{Family: IPv4, Address: addr}}
			withFamilies := Compute(PlanInput{Policy: s.policy, Claims: []Claim{typed}, Pools: v4OnlyPools(), Existing: s.existing})
			for i := range withFamilies.Ensure {
				if withFamilies.Ensure[i].Family != IPv4 {
					t.Fatalf("slot family = %q, want IPv4", withFamilies.Ensure[i].Family)
				}
				withFamilies.Ensure[i].Family = ""
			}
			if !reflect.DeepEqual(legacy, withFamilies) {
				t.Fatalf("plans differ:\nlegacy   %+v\nfamilies %+v", legacy, withFamilies)
			}
		})
	}
}

func svcTargetRef(name string) TargetRef {
	return TargetRef{Kind: TargetService, Namespace: testNS, Name: name}
}
