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

package controller

import (
	"fmt"
	"reflect"
	"testing"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/ipalloc"
)

func TestKeepAllocatedFieldsPinsWhatTheLiveAddressAllocated(t *testing.T) {
	desired := uddiv1alpha1.AddressSpec{
		SpaceRef:          uddiv1alpha1.ObjectRef{Name: "new-space"},
		NextAvailableFrom: &uddiv1alpha1.NextAvailableFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: "new-pool"}},
		Comment:           "desired comment",
	}
	live := &uddiv1alpha1.Address{Spec: uddiv1alpha1.AddressSpec{
		SpaceRef:          uddiv1alpha1.ObjectRef{Name: "old-space"},
		NextAvailableFrom: &uddiv1alpha1.NextAvailableFrom{SubnetRef: &uddiv1alpha1.ObjectRef{Name: "old-pool"}},
		Comment:           "live comment",
	}}

	keepAllocatedFields(&desired, live)
	if desired.SpaceRef.Name != "old-space" || desired.Address != "" ||
		desired.NextAvailableFrom == nil || desired.NextAvailableFrom.SubnetRef == nil ||
		desired.NextAvailableFrom.SubnetRef.Name != "old-pool" || desired.NextAvailableFrom.RangeRef != nil {
		t.Errorf("desired = %+v", desired)
	}
	if desired.Comment != "desired comment" {
		t.Errorf("comment = %q, the other fields stay desired", desired.Comment)
	}
	desired.NextAvailableFrom.SubnetRef.Name = "changed"
	if live.Spec.NextAvailableFrom.SubnetRef.Name != "old-pool" {
		t.Error("keepAllocatedFields must copy nextAvailableFrom, not share it")
	}

	// A specific address replaces next-available allocation, and the other way round.
	desired = uddiv1alpha1.AddressSpec{Address: "10.0.0.9"}
	keepAllocatedFields(&desired, live)
	if desired.Address != "" || desired.NextAvailableFrom == nil {
		t.Errorf("address annotation over a next-available slot: %+v", desired)
	}
	pinned := &uddiv1alpha1.Address{Spec: uddiv1alpha1.AddressSpec{Address: "10.0.0.7"}}
	desired = uddiv1alpha1.AddressSpec{NextAvailableFrom: &uddiv1alpha1.NextAvailableFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: "p"}}}
	keepAllocatedFields(&desired, pinned)
	if desired.Address != "10.0.0.7" || desired.NextAvailableFrom != nil {
		t.Errorf("next-available over a pinned slot: %+v", desired)
	}
}

func TestPoolCIDRPrefersStatusAndFallsBackToSpec(t *testing.T) {
	for _, tc := range []struct {
		statusAddr string
		statusCIDR int32
		specAddr   string
		specCIDR   int32
		want       string
	}{
		{"10.1.0.0", 24, "10.2.0.0", 16, "10.1.0.0/24"},
		{"", 0, "10.2.0.0", 16, "10.2.0.0/16"},
		{"10.1.0.0", 0, "10.2.0.0", 16, "10.2.0.0/16"},
		{"", 0, "", 24, ""},
		{"2001:db8::", 64, "", 0, "2001:db8::/64"},
	} {
		if got := poolCIDR(tc.statusAddr, tc.statusCIDR, tc.specAddr, tc.specCIDR); got != tc.want {
			t.Errorf("poolCIDR(%q, %d, %q, %d) = %q, want %q", tc.statusAddr, tc.statusCIDR, tc.specAddr, tc.specCIDR, got, tc.want)
		}
	}
}

func TestWithoutEgressIPReleasesKeepsServiceReleases(t *testing.T) {
	svc := ipalloc.TargetRef{Kind: ipalloc.TargetService, Name: "web"}
	eip := ipalloc.TargetRef{Kind: ipalloc.TargetEgressIP, Name: "egress-a"}
	plan := ipalloc.Plan{
		Unassign: []ipalloc.Unassign{{Target: eip}, {Target: svc}},
		Release:  []ipalloc.Release{{Target: eip, AddressName: "e"}, {Target: svc, AddressName: "s"}, {Target: eip, AddressName: "e1"}},
	}
	withoutEgressIPReleases(&plan)
	if !reflect.DeepEqual(plan.Unassign, []ipalloc.Unassign{{Target: svc}}) {
		t.Errorf("Unassign = %+v", plan.Unassign)
	}
	if !reflect.DeepEqual(plan.Release, []ipalloc.Release{{Target: svc, AddressName: "s"}}) {
		t.Errorf("Release = %+v", plan.Release)
	}
}

func TestSkippedStatusNamesTargetsAndSortsByName(t *testing.T) {
	skips := planSkips([]ipalloc.Skip{
		{Target: ipalloc.TargetRef{Kind: ipalloc.TargetEgressIP, Name: "egress-a"}, Reason: ipalloc.SkipManuallyAssigned, Message: "m"},
		{Target: ipalloc.TargetRef{Kind: ipalloc.TargetService, Namespace: "ns", Name: "web"}, Reason: ipalloc.SkipNoPoolForFamily, Message: "n"},
	})
	skips = append(skips,
		policySkip{Name: "Range/pool", Reason: uddiv1alpha1.SkipReasonMetalLBPoolConflict, Message: "c"},
		policySkip{Name: "EgressIP", Reason: uddiv1alpha1.SkipReasonAPIUnavailable, Message: "a"},
	)
	got := toSkippedServices(skips)
	want := []uddiv1alpha1.SkippedService{
		{Service: "EgressIP", Reason: uddiv1alpha1.SkipReasonAPIUnavailable, Message: "a"},
		{Service: "EgressIP/egress-a", Reason: string(ipalloc.SkipManuallyAssigned), Message: "m"},
		{Service: "Range/pool", Reason: uddiv1alpha1.SkipReasonMetalLBPoolConflict, Message: "c"},
		{Service: "web", Reason: string(ipalloc.SkipNoPoolForFamily), Message: "n"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("skipped = %+v\nwant      %+v", got, want)
	}

	many := make([]policySkip, 0, maxSkipped+5)
	for i := range maxSkipped + 5 {
		many = append(many, policySkip{Name: fmt.Sprintf("svc-%03d", i), Reason: "R"})
	}
	if got := toSkippedServices(many); len(got) != maxSkipped || got[0].Service != "svc-000" {
		t.Errorf("capped skipped = %d entries starting %q", len(got), got[0].Service)
	}
	if got := exampleSkips(many, maxSkipExamples); !reflect.DeepEqual(got, []string{"svc-000", "svc-001", "svc-002"}) {
		t.Errorf("examples = %v", got)
	}
}
