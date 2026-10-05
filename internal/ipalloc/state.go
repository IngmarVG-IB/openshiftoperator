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

// This file turns object-layer CRs into the planner's PoolState and AddressState: pool
// family detection (DESIGN.md 4.7, milestone 7 "Pool family") and reading which claim
// slot an existing Address belongs to, including the milestone 3 label rule.
package ipalloc

import (
	"net/netip"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

// FamilyOf returns the IP family of an address (or of a CIDR's address part); empty when
// s is neither. An IPv4-mapped IPv6 address counts as IPv4.
func FamilyOf(s string) IPFamily {
	s = strings.TrimSpace(s)
	addr, err := netip.ParseAddr(s)
	if err != nil {
		prefix, perr := netip.ParsePrefix(s)
		if perr != nil {
			return ""
		}
		addr = prefix.Addr()
	}
	if addr.Unmap().Is4() {
		return IPv4
	}
	return IPv6
}

// firstFamily returns the family of the first of candidates that parses.
func firstFamily(candidates ...string) IPFamily {
	for _, c := range candidates {
		if f := FamilyOf(c); f != "" {
			return f
		}
	}
	return ""
}

// SubnetPoolState builds the PoolState of a Subnet CR: Ready once it has a status.id, its
// family from status.address, falling back to spec.address.
func SubnetPoolState(s *uddiv1alpha1.Subnet) PoolState {
	return PoolState{
		Ref:      PoolRef{Kind: PoolSubnet, Name: s.Name},
		Ready:    s.Status.ID != "",
		SpaceRef: s.Spec.SpaceRef,
		Family:   firstFamily(s.Status.Address, s.Spec.Address),
	}
}

// RangePoolState builds the PoolState of a Range CR: Ready once it has a status.id, its
// family from spec.start (status.start when spec.start does not parse).
func RangePoolState(r *uddiv1alpha1.Range) PoolState {
	return PoolState{
		Ref:      PoolRef{Kind: PoolRange, Name: r.Name},
		Ready:    r.Status.ID != "",
		SpaceRef: r.Spec.SpaceRef,
		Family:   firstFamily(r.Spec.Start, r.Status.Start),
	}
}

// AddressBlockPoolState builds the PoolState of an AddressBlock CR: Ready once it has a
// status.id, its family from status.address, falling back to spec.address.
func AddressBlockPoolState(b *uddiv1alpha1.AddressBlock) PoolState {
	return PoolState{
		Ref:      PoolRef{Kind: PoolAddressBlock, Name: b.Name},
		Ready:    b.Status.ID != "",
		SpaceRef: b.Spec.SpaceRef,
		Family:   firstFamily(b.Status.Address, b.Spec.Address),
	}
}

// AddressStateOf reads an existing generated Address into the AddressState the planner
// needs: its labels (LabelService, and LabelTargetKind, LabelTargetName or
// AnnotationTargetName, and LabelSlot when present), its Ready condition, status.address,
// and the family and pool it is allocated from.
func AddressStateOf(a *uddiv1alpha1.Address) AddressState {
	st := AddressState{
		Name:    a.Name,
		Service: a.Labels[LabelService],
		Address: a.Status.Address,
		Family:  firstFamily(a.Status.Address, a.Spec.Address),
	}
	if kind, ok := a.Labels[LabelTargetKind]; ok && kind != "" {
		st.TargetKind = TargetKind(kind)
		st.TargetName = a.Labels[LabelTargetName]
		if full := a.Annotations[AnnotationTargetName]; full != "" {
			st.TargetName = full
		}
		slot, err := strconv.Atoi(a.Labels[LabelSlot])
		if err != nil || slot < 0 {
			slot = -1
		}
		st.Slot = slot
	}
	if cond := meta.FindStatusCondition(a.Status.Conditions, uddiv1alpha1.ConditionReady); cond != nil {
		st.Ready = cond.Status == metav1.ConditionTrue
		if !st.Ready {
			st.Reason = cond.Reason
			st.Message = cond.Message
		}
	}
	if nf := a.Spec.NextAvailableFrom; nf != nil {
		var ref PoolRef
		switch {
		case nf.SubnetRef != nil:
			ref = PoolRef{Kind: PoolSubnet, Name: nf.SubnetRef.Name}
		case nf.RangeRef != nil:
			ref = PoolRef{Kind: PoolRange, Name: nf.RangeRef.Name}
		case nf.AddressBlockRef != nil:
			ref = PoolRef{Kind: PoolAddressBlock, Name: nf.AddressBlockRef.Name}
		}
		if ref.Name != "" {
			st.Pool = &ref
		}
	}
	return st
}
