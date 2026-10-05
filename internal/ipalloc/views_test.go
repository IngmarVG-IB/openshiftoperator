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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/labelvalue"
)

func TestViewServiceFamilies(t *testing.T) {
	webRef := TargetRef{Kind: TargetService, Namespace: testNS, Name: "web", UID: "web-uid"}
	allocate := map[string]string{"uddi.infoblox.com/allocate": "true"}
	with := func(extra map[string]string) map[string]string {
		out := map[string]string{"uddi.infoblox.com/allocate": "true"}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	cases := []struct {
		name string
		svc  *corev1.Service
		want Claim
	}{
		{
			name: "single-stack IPv4",
			svc:  lbService("web", corev1.IPFamilyPolicySingleStack, singleV4, allocate),
			want: Claim{Target: webRef, Eligible: true, OptIn: boolPtr(true), FamilyPolicy: SingleStack,
				Slots: []SlotRequest{{Family: IPv4}}, Hostname: "web.team-a"},
		},
		{
			name: "PreferDualStack keeps both families in order",
			svc:  lbService("web", corev1.IPFamilyPolicyPreferDualStack, dualV6V4, allocate),
			want: Claim{Target: webRef, Eligible: true, OptIn: boolPtr(true), FamilyPolicy: PreferDualStack,
				Slots: []SlotRequest{{Family: IPv6}, {Family: IPv4}}, Hostname: "web.team-a"},
		},
		{
			name: "SingleStack keeps only the first family",
			svc:  lbService("web", corev1.IPFamilyPolicySingleStack, dualV4V6, allocate),
			want: Claim{Target: webRef, Eligible: true, OptIn: boolPtr(true), FamilyPolicy: SingleStack,
				Slots: []SlotRequest{{Family: IPv4}}, Hostname: "web.team-a"},
		},
		{
			name: "an unset policy keeps only the first family",
			svc: func() *corev1.Service {
				s := lbService("web", "", dualV4V6, allocate)
				s.Spec.IPFamilyPolicy = nil
				return s
			}(),
			want: Claim{Target: webRef, Eligible: true, OptIn: boolPtr(true),
				Slots: []SlotRequest{{Family: IPv4}}, Hostname: "web.team-a"},
		},
		{
			name: "unknown family values are ignored",
			svc:  lbService("web", corev1.IPFamilyPolicySingleStack, []corev1.IPFamily{"IPvX"}, allocate),
			want: Claim{Target: webRef, Eligible: true, OptIn: boolPtr(true), FamilyPolicy: SingleStack, Hostname: "web.team-a"},
		},
		{
			name: "one address per family, placed by family",
			svc: lbService("web", corev1.IPFamilyPolicyRequireDualStack, dualV4V6,
				with(map[string]string{"uddi.infoblox.com/address": " 2001:DB8::7 ,10.0.10.7"})),
			want: Claim{Target: webRef, Eligible: true, OptIn: boolPtr(true), FamilyPolicy: RequireDualStack,
				Slots: []SlotRequest{{Family: IPv4, Address: "10.0.10.7"}, {Family: IPv6, Address: "2001:db8::7"}}, Hostname: "web.team-a"},
		},
		{
			name: "an address for only one of two families",
			svc: lbService("web", corev1.IPFamilyPolicyPreferDualStack, dualV4V6,
				with(map[string]string{"uddi.infoblox.com/address": "2001:db8::7"})),
			want: Claim{Target: webRef, Eligible: true, OptIn: boolPtr(true), FamilyPolicy: PreferDualStack,
				Slots: []SlotRequest{{Family: IPv4}, {Family: IPv6, Address: "2001:db8::7"}}, Hostname: "web.team-a"},
		},
		{
			name: "labels and a dual-stack current assignment",
			svc: func() *corev1.Service {
				s := lbService("web", corev1.IPFamilyPolicyPreferDualStack, dualV4V6,
					with(map[string]string{AnnotationMetalLB: "10.0.10.5, 2001:db8::5,"}))
				s.Labels = map[string]string{"lb": "uddi"}
				return s
			}(),
			want: Claim{Target: webRef, Eligible: true, OptIn: boolPtr(true), FamilyPolicy: PreferDualStack,
				Slots: []SlotRequest{{Family: IPv4}, {Family: IPv6}}, Hostname: "web.team-a",
				CurrentAssignment: []string{"10.0.10.5", "2001:db8::5"}, Labels: map[string]string{"lb": "uddi"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ViewService(tc.svc, AssignVia{Mode: AssignModeAnnotation}, "")
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ViewService() = %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

func TestViewServiceAddressValidation(t *testing.T) {
	cases := []struct {
		name     string
		families []corev1.IPFamily
		policy   corev1.IPFamilyPolicy
		value    string
		want     string
	}{
		{"empty value", singleV4, corev1.IPFamilyPolicySingleStack, " , ",
			`uddi.infoblox.com/address: " , " not a valid IPv4 or IPv6 address`},
		{"one bad entry of two", dualV4V6, corev1.IPFamilyPolicyRequireDualStack, "10.0.10.7,bogus",
			`uddi.infoblox.com/address: "10.0.10.7,bogus" "bogus" is not a valid IPv4 or IPv6 address`},
		{"two IPv4 addresses", dualV4V6, corev1.IPFamilyPolicyRequireDualStack, "10.0.10.7,10.0.10.8",
			`uddi.infoblox.com/address: "10.0.10.7,10.0.10.8" has more than one IPv4 address`},
		{"a family the Service does not want", singleV4, corev1.IPFamilyPolicySingleStack, "2001:db8::7",
			`uddi.infoblox.com/address: "2001:db8::7" 2001:db8::7 is IPv6 but the Service wants IPv4`},
		{"two addresses without spec.ipFamilies", nil, "", "10.0.10.7,2001:db8::7",
			`uddi.infoblox.com/address: "10.0.10.7,2001:db8::7" takes a single address on a Service without spec.ipFamilies`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := lbService("web", tc.policy, tc.families, map[string]string{"uddi.infoblox.com/address": tc.value})
			if tc.policy == "" {
				s.Spec.IPFamilyPolicy = nil
			}
			got := ViewService(s, AssignVia{}, "")
			if got.Invalid != tc.want {
				t.Errorf("Invalid = %q\nwant %q", got.Invalid, tc.want)
			}
		})
	}
}

// egressIPObject builds the EgressIP "egress-a" as unstructured data.
func egressIPObject(ann, lbl map[string]string, ips []interface{}) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "k8s.ovn.org/v1",
		"kind":       "EgressIP",
		"metadata":   map[string]interface{}{"name": "egress-a", "uid": "egress-a-uid"},
		"spec":       map[string]interface{}{"namespaceSelector": map[string]interface{}{}},
	}}
	obj.SetAnnotations(ann)
	obj.SetLabels(lbl)
	if ips != nil {
		obj.Object["spec"].(map[string]interface{})["egressIPs"] = ips
	}
	return obj
}

func TestViewEgressIP(t *testing.T) {
	ref := TargetRef{Kind: TargetEgressIP, Name: "egress-a", UID: "egress-a-uid"}
	pool := PoolRef{Kind: PoolSubnet, Name: "nodes"}
	cases := []struct {
		name   string
		obj    *unstructured.Unstructured
		prefix string
		want   Claim
	}{
		{
			name: "defaults: no annotations, one slot",
			obj:  egressIPObject(nil, nil, []interface{}{}),
			want: Claim{Target: ref, Eligible: true, Slots: make([]SlotRequest, 1)},
		},
		{
			name: "allocate, count, allocate-from, labels and current addresses",
			obj: egressIPObject(
				map[string]string{"uddi.infoblox.com/allocate": "true", "uddi.infoblox.com/count": " 3 ", "uddi.infoblox.com/allocate-from": "subnet/nodes"},
				map[string]string{"env": "prod"}, []interface{}{"10.0.0.5", " 10.0.0.6 ", ""}),
			want: Claim{Target: ref, Eligible: true, OptIn: boolPtr(true), Pool: &pool, Slots: make([]SlotRequest, 3),
				CurrentAssignment: []string{"10.0.0.5", "10.0.0.6"}, Labels: map[string]string{"env": "prod"}},
		},
		{
			name:   "custom prefix",
			obj:    egressIPObject(map[string]string{"example.com/allocate": "false", "uddi.infoblox.com/allocate": "true"}, nil, nil),
			prefix: "example.com",
			want:   Claim{Target: ref, Eligible: true, OptIn: boolPtr(false), Slots: make([]SlotRequest, 1)},
		},
		{
			name: "invalid allocate",
			obj:  egressIPObject(map[string]string{"uddi.infoblox.com/allocate": "yes"}, nil, nil),
			want: Claim{Target: ref, Eligible: true, Invalid: `uddi.infoblox.com/allocate: "yes" must be "true" or "false"`},
		},
		{
			name: "invalid allocate-from",
			obj:  egressIPObject(map[string]string{"uddi.infoblox.com/allocate-from": "vlan/x"}, nil, nil),
			want: Claim{Target: ref, Eligible: true, Invalid: `uddi.infoblox.com/allocate-from: "vlan/x" "vlan" is not subnet, range or addressblock`},
		},
		{
			name: "count zero",
			obj:  egressIPObject(map[string]string{"uddi.infoblox.com/count": "0"}, nil, nil),
			want: Claim{Target: ref, Eligible: true, Invalid: `uddi.infoblox.com/count: "0" must be an integer from 1 to 16`},
		},
		{
			name: "count above 16",
			obj:  egressIPObject(map[string]string{"uddi.infoblox.com/count": "17"}, nil, nil),
			want: Claim{Target: ref, Eligible: true, Invalid: `uddi.infoblox.com/count: "17" must be an integer from 1 to 16`},
		},
		{
			name: "count not a number",
			obj:  egressIPObject(map[string]string{"uddi.infoblox.com/count": "two"}, nil, nil),
			want: Claim{Target: ref, Eligible: true, Invalid: `uddi.infoblox.com/count: "two" must be an integer from 1 to 16`},
		},
		{
			name: "malformed spec.egressIPs",
			obj:  egressIPObject(nil, nil, []interface{}{int64(5)}),
			want: Claim{Target: ref, Eligible: true, Invalid: "spec.egressIPs[0] is int64, not a string"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ViewEgressIP(tc.obj, tc.prefix)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ViewEgressIP() = %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

func TestEgressIPsPatch(t *testing.T) {
	if got, want := string(EgressIPsPatch([]string{"10.0.0.5", "10.0.0.6"})), `{"spec":{"egressIPs":["10.0.0.5","10.0.0.6"]}}`; got != want {
		t.Errorf("EgressIPsPatch() = %s, want %s", got, want)
	}
	if got, want := string(ClearEgressIPsPatch()), `{"spec":{"egressIPs":[]}}`; got != want {
		t.Errorf("ClearEgressIPsPatch() = %s, want %s", got, want)
	}
}

func TestAssignmentHelpersCommaLists(t *testing.T) {
	s := svc("ns", "s", map[string]string{AnnotationMetalLB: " 10.0.0.1 , 2001:db8::1 "})
	via := AssignVia{Mode: AssignModeAnnotation}
	if !AssignmentMatches(s, via, "10.0.0.1,2001:db8::1") {
		t.Error("AssignmentMatches() = false for the same list with spaces")
	}
	if AssignmentMatches(s, via, "2001:db8::1,10.0.0.1") {
		t.Error("AssignmentMatches() = true for a reordered list")
	}
	if got := CurrentAssignmentValues(s, via); !reflect.DeepEqual(got, []string{"10.0.0.1", "2001:db8::1"}) {
		t.Errorf("CurrentAssignmentValues() = %v", got)
	}
	if got := SplitAssignment(" , "); got != nil {
		t.Errorf("SplitAssignment(blank) = %#v, want nil", got)
	}
	a := Assignment{Values: []string{"10.0.0.1", "2001:db8::1"}}
	if got := string(AssignmentPatch(via, a.Value())); got != `{"metadata":{"annotations":{"metallb.io/loadBalancerIPs":"10.0.0.1,2001:db8::1"}}}` {
		t.Errorf("AssignmentPatch(dual-stack) = %s", got)
	}
}

func TestFamilyOf(t *testing.T) {
	cases := map[string]IPFamily{
		"10.0.0.1":         IPv4,
		" 10.0.0.0 ":       IPv4,
		"10.0.0.0/24":      IPv4,
		"2001:db8::":       IPv6,
		"2001:db8::/64":    IPv6,
		"::ffff:10.0.0.1":  IPv4,
		"":                 "",
		"not-an-address":   "",
		"10.0.0.0/33":      "",
		"fe80::1%eth0":     IPv6,
		"192.168.001.0001": "",
	}
	for in, want := range cases {
		if got := FamilyOf(in); got != want {
			t.Errorf("FamilyOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPoolStates(t *testing.T) {
	space := uddiv1alpha1.ObjectRef{Name: "corp"}
	subnet := &uddiv1alpha1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Name: "s"},
		Spec:       uddiv1alpha1.SubnetSpec{SpaceRef: space},
		Status:     uddiv1alpha1.SubnetStatus{ObjectStatus: uddiv1alpha1.ObjectStatus{ID: "ipam/subnet/1"}, Address: "2001:db8::"},
	}
	if got := SubnetPoolState(subnet); got != (PoolState{Ref: PoolRef{Kind: PoolSubnet, Name: "s"}, Ready: true, SpaceRef: space, Family: IPv6}) {
		t.Errorf("SubnetPoolState(status address) = %+v", got)
	}
	subnet.Status = uddiv1alpha1.SubnetStatus{}
	subnet.Spec.Address = "10.0.0.0"
	if got := SubnetPoolState(subnet); got.Ready || got.Family != IPv4 {
		t.Errorf("SubnetPoolState(spec address, no id) = %+v", got)
	}

	rg := &uddiv1alpha1.Range{
		ObjectMeta: metav1.ObjectMeta{Name: "r"},
		Spec:       uddiv1alpha1.RangeSpec{SpaceRef: space, Start: "10.0.10.100", End: "10.0.10.199"},
		Status:     uddiv1alpha1.RangeStatus{ObjectStatus: uddiv1alpha1.ObjectStatus{ID: "ipam/range/1"}},
	}
	if got := RangePoolState(rg); got != (PoolState{Ref: PoolRef{Kind: PoolRange, Name: "r"}, Ready: true, SpaceRef: space, Family: IPv4}) {
		t.Errorf("RangePoolState = %+v", got)
	}
	rg.Spec.Start = ""
	rg.Status.Start = "2001:db8::10"
	if got := RangePoolState(rg); got.Family != IPv6 {
		t.Errorf("RangePoolState(status start) = %+v", got)
	}

	block := &uddiv1alpha1.AddressBlock{
		ObjectMeta: metav1.ObjectMeta{Name: "b"},
		Spec:       uddiv1alpha1.AddressBlockSpec{SpaceRef: space, Address: "10.0.0.0"},
		Status:     uddiv1alpha1.AddressBlockStatus{Address: "not-an-address"},
	}
	if got := AddressBlockPoolState(block); got != (PoolState{Ref: PoolRef{Kind: PoolAddressBlock, Name: "b"}, SpaceRef: space, Family: IPv4}) {
		t.Errorf("AddressBlockPoolState = %+v", got)
	}
	block.Spec.Address = ""
	block.Status.Address = ""
	if got := AddressBlockPoolState(block); got.Family != "" {
		t.Errorf("AddressBlockPoolState(no address) family = %q, want unknown", got.Family)
	}
}

func TestAddressStateOf(t *testing.T) {
	ready := []metav1.Condition{{Type: uddiv1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Synced"}}
	notReady := []metav1.Condition{{Type: uddiv1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: "APIError", Message: "boom"}}
	addr := func(lbl, ann map[string]string, spec uddiv1alpha1.AddressSpec, status string, conds []metav1.Condition) *uddiv1alpha1.Address {
		return &uddiv1alpha1.Address{
			ObjectMeta: metav1.ObjectMeta{Name: "a", Labels: lbl, Annotations: ann},
			Spec:       spec,
			Status:     uddiv1alpha1.AddressStatus{ObjectStatus: uddiv1alpha1.ObjectStatus{Conditions: conds}, Address: status},
		}
	}
	nextSubnet := uddiv1alpha1.AddressSpec{NextAvailableFrom: &uddiv1alpha1.NextAvailableFrom{SubnetRef: &uddiv1alpha1.ObjectRef{Name: "s"}}}
	nextBlock := uddiv1alpha1.AddressSpec{NextAvailableFrom: &uddiv1alpha1.NextAvailableFrom{AddressBlockRef: &uddiv1alpha1.ObjectRef{Name: "b"}}}
	nextByID := uddiv1alpha1.AddressSpec{NextAvailableFrom: &uddiv1alpha1.NextAvailableFrom{RangeRef: &uddiv1alpha1.ObjectRef{ID: "ipam/range/1"}}}
	subnetRef := PoolRef{Kind: PoolSubnet, Name: "s"}
	blockRef := PoolRef{Kind: PoolAddressBlock, Name: "b"}

	cases := []struct {
		name string
		a    *uddiv1alpha1.Address
		want AddressState
	}{
		{
			name: "milestone 3 labels",
			a:    addr(map[string]string{LabelPolicy: "lb", LabelService: "web"}, nil, nextSubnet, "10.0.0.5", ready),
			want: AddressState{Name: "a", Service: "web", Ready: true, Address: "10.0.0.5", Family: IPv4, Pool: &subnetRef},
		},
		{
			name: "milestone 7 labels, not ready, family from spec.address",
			a: addr(map[string]string{LabelService: "web", LabelTargetKind: "Service", LabelTargetName: "web", LabelSlot: "1"}, nil,
				uddiv1alpha1.AddressSpec{Address: "2001:db8::7"}, "", notReady),
			want: AddressState{Name: "a", Service: "web", TargetKind: TargetService, TargetName: "web", Slot: 1,
				Reason: "APIError", Message: "boom", Family: IPv6},
		},
		{
			name: "EgressIP with the full name in the annotation",
			a: addr(map[string]string{LabelTargetKind: "EgressIP", LabelTargetName: "short-0123456789", LabelSlot: "2"},
				map[string]string{AnnotationTargetName: "the-full-name"}, nextBlock, "", nil),
			want: AddressState{Name: "a", TargetKind: TargetEgressIP, TargetName: "the-full-name", Slot: 2, Pool: &blockRef},
		},
		{
			name: "unreadable slot label",
			a:    addr(map[string]string{LabelTargetKind: "Service", LabelTargetName: "web", LabelSlot: "one"}, nil, nextByID, "", nil),
			want: AddressState{Name: "a", TargetKind: TargetService, TargetName: "web", Slot: -1},
		},
		{
			name: "negative slot label",
			a:    addr(map[string]string{LabelTargetKind: "Service", LabelTargetName: "web", LabelSlot: "-2"}, nil, uddiv1alpha1.AddressSpec{}, "", nil),
			want: AddressState{Name: "a", TargetKind: TargetService, TargetName: "web", Slot: -1},
		},
		{
			name: "empty target-kind falls back to milestone 3",
			a:    addr(map[string]string{LabelService: "web", LabelTargetKind: ""}, nil, uddiv1alpha1.AddressSpec{}, "", nil),
			want: AddressState{Name: "a", Service: "web"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AddressStateOf(tc.a); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("AddressStateOf() = %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestAddressStateTarget(t *testing.T) {
	cases := []struct {
		name     string
		st       AddressState
		wantRef  TargetRef
		wantSlot int
		wantOK   bool
	}{
		{"milestone 3", AddressState{Service: "web"}, TargetRef{Kind: TargetService, Namespace: testNS, Name: "web"}, 0, true},
		{"no labels", AddressState{}, TargetRef{}, 0, false},
		{"Service slot", AddressState{TargetKind: TargetService, TargetName: "web", Slot: 1}, TargetRef{Kind: TargetService, Namespace: testNS, Name: "web"}, 1, true},
		{"Service without name", AddressState{TargetKind: TargetService, Service: "web"}, TargetRef{}, 0, false},
		{"EgressIP slot", AddressState{TargetKind: TargetEgressIP, TargetName: "e", Slot: 3}, TargetRef{Kind: TargetEgressIP, Name: "e"}, 3, true},
		{"EgressIP bad slot", AddressState{TargetKind: TargetEgressIP, TargetName: "e", Slot: -1}, TargetRef{}, 0, false},
		{"unknown kind", AddressState{TargetKind: "Pod", TargetName: "p"}, TargetRef{}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, slot, ok := tc.st.Target(testNS)
			if ref != tc.wantRef || slot != tc.wantSlot || ok != tc.wantOK {
				t.Errorf("Target() = %+v, %d, %v; want %+v, %d, %v", ref, slot, ok, tc.wantRef, tc.wantSlot, tc.wantOK)
			}
		})
	}
}

func TestSlotAddressName(t *testing.T) {
	web := TargetRef{Kind: TargetService, Name: "web"}
	if got := SlotAddressName("lb", web, 0); got != "lb-web" {
		t.Errorf("Service slot 0 = %q", got)
	}
	if got := SlotAddressName("lb", web, 1); got != "lb-web-1" {
		t.Errorf("Service slot 1 = %q", got)
	}
	if got := SlotAddressName("lb", TargetRef{Kind: TargetEgressIP, Name: "e"}, 4); got != "lb-egressip-e-4" {
		t.Errorf("EgressIP slot 4 = %q", got)
	}

	longName := strings.Repeat("s", 300)
	longSvc := TargetRef{Kind: TargetService, Name: longName}
	if got := SlotAddressName("lb", longSvc, 0); got != AddressName("lb", longName) {
		t.Errorf("long Service slot 0 = %q, want AddressName's %q", got, AddressName("lb", longName))
	}
	slot1 := SlotAddressName("lb", longSvc, 1)
	if slot1 != AddressName("lb", longName)+"-1" || len(slot1) > maxNameLength {
		t.Errorf("long Service slot 1 = %q", slot1)
	}
	longEIP := SlotAddressName("lb", TargetRef{Kind: TargetEgressIP, Name: longName}, 2)
	if !strings.HasPrefix(longEIP, "lb-egressip-") || !strings.HasSuffix(longEIP, "-2") || len(longEIP) != len("lb-egressip-")+nameHashLength+2 {
		t.Errorf("long EgressIP slot = %q", longEIP)
	}
}

func TestTargetNameLabelValue(t *testing.T) {
	if got := TargetNameLabelValue("egress-a"); got != "egress-a" {
		t.Errorf("short name = %q", got)
	}
	long := strings.Repeat("a", 51) + "." + strings.Repeat("b", 100)
	got := TargetNameLabelValue(long)
	if errs := validation.IsValidLabelValue(got); len(errs) != 0 || got == long || !strings.HasPrefix(got, strings.Repeat("a", 51)+"-") {
		t.Errorf("long name label = %q (%v)", got, errs)
	}
	if again := TargetNameLabelValue(long); again != got {
		t.Errorf("not deterministic: %q != %q", again, got)
	}
	// Every policy shortens long names the same way.
	if want := labelvalue.For(long); got != want {
		t.Errorf("TargetNameLabelValue = %q, labelvalue.For = %q", got, want)
	}

	// The planner stamps the full name in an annotation and AddressStateOf reads it back.
	p := egressPolicy()
	plan := Compute(PlanInput{Policy: p, Pools: v4OnlyPools(), Claims: []Claim{eipClaim(long, 1)}})
	if len(plan.Ensure) != 1 {
		t.Fatalf("Ensure = %+v", plan.Ensure)
	}
	d := plan.Ensure[0]
	if d.Labels[LabelTargetName] != got || d.Annotations[AnnotationTargetName] != long {
		t.Fatalf("labels/annotations = %v / %v", d.Labels, d.Annotations)
	}
	st := AddressStateOf(&uddiv1alpha1.Address{ObjectMeta: metav1.ObjectMeta{Name: d.Name, Labels: d.Labels, Annotations: d.Annotations}})
	if ref, slot, ok := st.Target(testNS); !ok || ref.Name != long || slot != 0 {
		t.Fatalf("Target() = %+v, %d, %v", ref, slot, ok)
	}
}

func TestPolicyHelpers(t *testing.T) {
	p := rangePoolPolicy(boolPtr(true))
	if _, ok := PolicyPoolIPv6(p); ok {
		t.Error("PolicyPoolIPv6 ok without allocateFromIPv6")
	}
	p.Spec.AllocateFromIPv6 = &uddiv1alpha1.AllocateFrom{}
	if _, ok := PolicyPoolIPv6(p); ok {
		t.Error("PolicyPoolIPv6 ok with an empty allocateFromIPv6")
	}
	withIPv6Pool(p)
	if ref, ok := PolicyPoolIPv6(p); !ok || ref != v6PoolRef {
		t.Errorf("PolicyPoolIPv6 = %+v, %v", ref, ok)
	}

	if !TargetEnabled(p, TargetService) || TargetEnabled(p, TargetEgressIP) {
		t.Error("default targets must be Service only")
	}
	p.Spec.Targets = []uddiv1alpha1.TargetKind{uddiv1alpha1.TargetKindEgressIP}
	if TargetEnabled(p, TargetService) || !TargetEnabled(p, TargetEgressIP) {
		t.Error("targets [EgressIP] must enable only EgressIP")
	}

	override := PoolRef{Kind: PoolSubnet, Name: "o"}
	old := PoolRef{Kind: PoolAddressBlock, Name: "old"}
	got := PoolRefs(p, []Claim{{Pool: &override}, {}, {Pool: &override}}, []AddressState{{Pool: &old}, {}, {Pool: &lbPoolRef}})
	want := []PoolRef{old, lbPoolRef, v6PoolRef, override}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PoolRefs = %+v, want %+v", got, want)
	}
}

func TestClaimSelectedAndStrings(t *testing.T) {
	c := Claim{Labels: map[string]string{"env": "prod"}}
	sel, err := labels.Parse("env=prod")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := labels.Parse("env=dev")
	if !c.Selected(nil) || !c.Selected(sel) || c.Selected(other) {
		t.Error("Selected() mismatch")
	}
	if got := (TargetRef{Kind: TargetEgressIP, Name: "e"}).String(); got != "EgressIP/e" {
		t.Errorf("String() = %q", got)
	}
	if got := (PoolRef{Kind: "Other", Name: "x"}).String(); got != "Other/x" {
		t.Errorf("PoolRef.String() = %q", got)
	}
	if nextAvailableFromFor(PoolRef{Kind: "Other"}) != nil {
		t.Error("nextAvailableFromFor(unknown kind) must be nil")
	}
}
