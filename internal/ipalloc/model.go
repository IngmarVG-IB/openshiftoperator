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

// Package ipalloc holds the pure logic behind IPAllocationPolicy: reading what a Service
// or an EgressIP asks for (a claim made of slots, one address each), deciding which
// Address CRs should exist, which targets get their addresses written back and which
// allocations must be released. Nothing here talks to Kubernetes or Universal DDI; the
// controller in internal/controller feeds it objects and applies the plan. This file is
// the shared contract between the views, the planner and the controller.
package ipalloc

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

// TargetKind is the kind of object a claim is made for. The values match
// IPAllocationPolicy.spec.targets.
type TargetKind string

const (
	// TargetService is a Service of type LoadBalancer: one slot per IP family.
	TargetService TargetKind = TargetKind(uddiv1alpha1.TargetKindService)
	// TargetEgressIP is an OVN-Kubernetes EgressIP: uddi.infoblox.com/count slots.
	TargetEgressIP TargetKind = TargetKind(uddiv1alpha1.TargetKindEgressIP)
)

// TargetRef identifies the object a claim, a skip or a generated Address is about.
type TargetRef struct {
	Kind TargetKind
	// Namespace is the Service's namespace (the policy's); empty for the cluster-scoped
	// EgressIP.
	Namespace string
	Name      string
	UID       types.UID
}

// String renders the reference the way status.skipped and events show it:
// "Service/web" or "EgressIP/egress-a".
func (t TargetRef) String() string { return fmt.Sprintf("%s/%s", t.Kind, t.Name) }

// IPFamily is an IP address family. The values match corev1.IPFamily.
type IPFamily string

const (
	IPv4 IPFamily = "IPv4"
	IPv6 IPFamily = "IPv6"
)

// FamilyPolicy is a Service's spec.ipFamilyPolicy. The values match
// corev1.IPFamilyPolicy; empty means unknown, which the planner treats as SingleStack.
type FamilyPolicy string

const (
	SingleStack      FamilyPolicy = "SingleStack"
	PreferDualStack  FamilyPolicy = "PreferDualStack"
	RequireDualStack FamilyPolicy = "RequireDualStack"
)

// PoolKind is the kind of object-layer CR an address is allocated from.
type PoolKind string

const (
	PoolSubnet       PoolKind = "Subnet"
	PoolRange        PoolKind = "Range"
	PoolAddressBlock PoolKind = "AddressBlock"
)

// PoolRef names a pool CR in the policy's namespace.
type PoolRef struct {
	Kind PoolKind
	Name string
}

// String renders "<kind>/<name>" in the lower-case form the allocate-from annotation uses.
func (p PoolRef) String() string {
	switch p.Kind {
	case PoolSubnet:
		return "subnet/" + p.Name
	case PoolRange:
		return "range/" + p.Name
	case PoolAddressBlock:
		return "addressblock/" + p.Name
	}
	return string(p.Kind) + "/" + p.Name
}

// AssignMode is IPAllocationPolicy.spec.assignVia.mode.
type AssignMode string

const (
	// AssignModeAnnotation writes the address into a Service annotation (MetalLB, Cilium,
	// kube-vip all read one). A dual-stack Service gets its addresses comma-separated.
	AssignModeAnnotation AssignMode = "Annotation"
	// AssignModeLoadBalancerIP writes spec.loadBalancerIP, deprecated upstream but still
	// honoured by most load balancer implementations. It carries one address, so only
	// one slot is allocated per Service.
	AssignModeLoadBalancerIP AssignMode = "LoadBalancerIP"
)

// Well-known annotation keys load balancer implementations read the requested IP from.
// See knownkeys.go for the researched value formats, spec.loadBalancerIP support and doc
// citations behind these constants.
const (
	AnnotationMetalLB       = "metallb.io/loadBalancerIPs"
	AnnotationMetalLBLegacy = "metallb.universe.tf/loadBalancerIPs"
	// AnnotationCilium is Cilium LB-IPAM's current key, "lbipam.cilium.io/ips" (renamed from
	// the "io.cilium/lb-ipam-ips" used through Cilium 1.14; corrected here 2026-09-14 after
	// checking docs.cilium.io/en/stable/network/lb-ipam/ against the 1.14 archive — see
	// knownkeys.go).
	AnnotationCilium  = "lbipam.cilium.io/ips"
	AnnotationKubeVIP = "kube-vip.io/loadbalancerIPs"
	// DefaultAssignAnnotation is used when spec.assignVia.annotation is empty.
	DefaultAssignAnnotation = AnnotationMetalLB
)

// AssignVia is the resolved spec.assignVia.
type AssignVia struct {
	Mode       AssignMode
	Annotation string
}

// Key returns the annotation key to write in Annotation mode, applying the default.
func (a AssignVia) Key() string {
	if a.Annotation == "" {
		return DefaultAssignAnnotation
	}
	return a.Annotation
}

// DefaultAnnotationPrefix is the domain of the Service and EgressIP annotations the
// policy reads.
const DefaultAnnotationPrefix = "uddi.infoblox.com"

// Annotation keys relative to the prefix.
const (
	AnnotationAllocate     = "allocate"      // "true" opts in, "false" opts out
	AnnotationAllocateFrom = "allocate-from" // "subnet/<name>", "range/<name>" or "addressblock/<name>"
	AnnotationAddress      = "address"       // Service: specific IPs instead of next-available, one per family
	AnnotationHostname     = "hostname"      // Service: the name registered on the Addresses
	AnnotationCount        = "count"         // EgressIP: the number of addresses, 1 to 16 (default 1)
)

// EgressIP count bounds for the count annotation.
const (
	MinEgressIPCount     = 1
	MaxEgressIPCount     = 16
	DefaultEgressIPCount = 1
)

// Labels, annotations, tags and the field manager stamped on generated Addresses.
const (
	LabelPolicy = "uddi.infoblox.com/policy"
	// LabelService names the Service of a Service slot. A milestone 3 Address carries only
	// this label and LabelPolicy and is read as slot 0 of that Service.
	LabelService = "uddi.infoblox.com/service"
	// LabelTargetKind, LabelTargetName and LabelSlot identify the claim slot an Address
	// belongs to (milestone 7).
	LabelTargetKind = "uddi.infoblox.com/target-kind"
	LabelTargetName = "uddi.infoblox.com/target-name"
	LabelSlot       = "uddi.infoblox.com/slot"
	// AnnotationTargetName carries the full target name on an Address whose target name is
	// not a valid label value (an EgressIP name longer than 63 characters); the
	// LabelTargetName value is then a shortened, hashed form.
	AnnotationTargetName = "uddi.infoblox.com/target-name"
	// TagSource is the Universal DDI tag key naming the object an address was allocated
	// for: "Service/<namespace>/<name>" or "EgressIP/<name>".
	TagSource = "uddi_operator_source"
	// FieldManager is the server-side apply field manager for generated Addresses.
	FieldManager = "uddi-operator/ipallocationpolicy"
)

// SkipReason explains why a claim was not (or not yet) given its addresses. The values
// are the reason strings shown in IPAllocationPolicy.status.skipped and Skipped events.
type SkipReason string

const (
	SkipNotLoadBalancer   SkipReason = "NotLoadBalancer"   // opted in but not type LoadBalancer
	SkipInvalidAnnotation SkipReason = "InvalidAnnotation" // a malformed annotation or spec.egressIPs value
	SkipPoolNotReady      SkipReason = "PoolNotReady"      // a pool CR is missing or has no status.id
	SkipManuallyAssigned  SkipReason = "ManuallyAssigned"  // the assignment field holds an address no slot holds
	SkipAddressPending    SkipReason = "AddressPending"    // an Address CR exists but is not Ready yet
	// SkipNoPoolForFamily: a wanted IP family has no pool (RequireDualStack or
	// SingleStack), the allocate-from pool serves no wanted family, or PreferDualStack
	// would drop a family the claim already holds an address of.
	SkipNoPoolForFamily SkipReason = "NoPoolForFamily"
	// SkipDualStackUnsupported: assignVia.mode LoadBalancerIP carries one address, so a
	// RequireDualStack Service cannot be served.
	SkipDualStackUnsupported SkipReason = "DualStackUnsupported"
	// SkipNameConflict: the Address name a new slot would get already belongs to another
	// target's slot (for example slot 1 of Service web and slot 0 of Service web-1 are
	// both "<policy>-web-1"). The existing Address is never taken over.
	SkipNameConflict SkipReason = "NameConflict"
)

// Skip records one claim that was not assigned and why.
type Skip struct {
	Target  TargetRef
	Reason  SkipReason
	Message string
}

// SlotRequest is one address a claim asks for, before the planner picks its pool and
// slot number.
type SlotRequest struct {
	// Family is the IP family the slot must have. Empty means any family: the slot uses
	// the allocate-from pool or allocateFrom whatever its family (EgressIP slots, and a
	// Service whose spec.ipFamilies is unset).
	Family IPFamily
	// Address is a specific address to reserve instead of next-available; empty for
	// next-available. Its family always equals Family when Family is set.
	Address string
}

// Claim is everything the planner needs to know about one Service or EgressIP. It is
// built by ViewService or ViewEgressIP.
type Claim struct {
	Target TargetRef
	// Eligible is false for a Service that is not of type LoadBalancer; always true for an
	// EgressIP.
	Eligible bool
	// OptIn is nil when the allocate annotation is absent, otherwise its parsed value.
	OptIn *bool
	// Pool overrides the policy pool when the allocate-from annotation is set. For a
	// Service it replaces the policy pool of its own IP family only.
	Pool *PoolRef
	// FamilyPolicy is the Service's spec.ipFamilyPolicy; empty for an EgressIP or when
	// unknown.
	FamilyPolicy FamilyPolicy
	// Slots are the requested slots in request order: for a Service one per wanted IP
	// family (spec.ipFamilies, only the first for SingleStack), for an EgressIP count
	// family-agnostic slots. Empty means one slot of any family (the milestone 3 shape).
	Slots []SlotRequest
	// Hostname is the name registered on the Addresses; empty registers no name (EgressIP).
	Hostname string
	// CurrentAssignment is what the assignment field holds now, in order: the Service
	// annotation split on commas, spec.loadBalancerIP, or spec.egressIPs.
	CurrentAssignment []string
	// Labels are the target's labels, for matching spec.selector.
	Labels map[string]string
	// Invalid carries the message of an InvalidAnnotation skip; empty when the claim is
	// valid.
	Invalid string
}

// Selected reports whether the claim's target matches sel. A nil sel selects everything.
func (c Claim) Selected(sel labels.Selector) bool {
	return sel == nil || sel.Matches(labels.Set(c.Labels))
}

// PoolState is a pool CR as the planner sees it.
type PoolState struct {
	Ref      PoolRef
	Ready    bool                   // the CR exists and has a status.id
	SpaceRef uddiv1alpha1.ObjectRef // the pool's spec.spaceRef, copied to generated Addresses
	// Family is the pool's IP family (see SubnetPoolState, RangePoolState and
	// AddressBlockPoolState); empty when unknown.
	Family IPFamily
}

// AddressState is an existing generated Address CR as the planner sees it. Build it with
// AddressStateOf.
type AddressState struct {
	Name string
	// Service is the LabelService label.
	Service string
	// TargetKind, TargetName and Slot come from the LabelTargetKind, LabelTargetName (or
	// AnnotationTargetName) and LabelSlot labels. An empty TargetKind is a milestone 3
	// Address: slot 0 of the Service named by Service. A Slot below zero marks an
	// unreadable slot label; such an Address is never touched.
	TargetKind TargetKind
	TargetName string
	Slot       int
	Ready      bool
	Address    string // status.address, empty until allocated
	// Reason and Message come from the Address's Ready condition when it is not Ready.
	Reason  string
	Message string
	// Family is the family of status.address, or of spec.address before allocation;
	// empty when neither is known.
	Family IPFamily
	// Pool is the pool CR spec.nextAvailableFrom names, nil for a specific address. The
	// planner reads the family from it when Family is empty.
	Pool *PoolRef
}

// Target resolves which claim slot the Address belongs to, applying the milestone 3 rule:
// an Address without LabelTargetKind is slot 0 of the Service named by LabelService.
// namespace is the policy's namespace (where Services live). ok is false when the labels
// do not identify a target.
func (a AddressState) Target(namespace string) (ref TargetRef, slot int, ok bool) {
	switch a.TargetKind {
	case "":
		if a.Service == "" {
			return TargetRef{}, 0, false
		}
		return TargetRef{Kind: TargetService, Namespace: namespace, Name: a.Service}, 0, true
	case TargetService:
		if a.TargetName == "" || a.Slot < 0 {
			return TargetRef{}, 0, false
		}
		return TargetRef{Kind: TargetService, Namespace: namespace, Name: a.TargetName}, a.Slot, true
	case TargetEgressIP:
		if a.TargetName == "" || a.Slot < 0 {
			return TargetRef{}, 0, false
		}
		return TargetRef{Kind: TargetEgressIP, Name: a.TargetName}, a.Slot, true
	default:
		return TargetRef{}, 0, false
	}
}

// DesiredAddress is one Address CR the policy wants to exist: one slot of one claim.
type DesiredAddress struct {
	Name   string
	Target TargetRef
	Slot   int
	// Family is the slot's IP family; empty for a family-agnostic slot.
	Family      IPFamily
	Labels      map[string]string
	Annotations map[string]string
	Spec        uddiv1alpha1.AddressSpec
}

// Assignment writes a claim's addresses into its target: Values are the slot addresses
// in slot order. It is planned only when every slot of the claim is Ready.
type Assignment struct {
	Target TargetRef
	Values []string
}

// Value renders Values the way a Service annotation carries them: comma-joined, in slot
// order. In LoadBalancerIP mode a Service claim has a single slot, so Value is that one
// address.
func (a Assignment) Value() string { return strings.Join(a.Values, ",") }

// Unassign clears a target's assignment field when its whole claim is released, but only
// if the field still holds nothing but addresses the claim's Addresses hold (Addresses).
// Apply it before the matching Releases.
type Unassign struct {
	Target    TargetRef
	Addresses []string
}

// Applies reports whether a target whose assignment field currently holds current
// should be cleared: current is non-empty and every entry is one of u.Addresses.
func (u Unassign) Applies(current []string) bool {
	if len(current) == 0 {
		return false
	}
	ours := make(map[string]bool, len(u.Addresses))
	for _, a := range u.Addresses {
		ours[a] = true
	}
	for _, c := range current {
		if !ours[c] {
			return false
		}
	}
	return true
}

// Release deletes one Address CR (its finalizer releases the IP in Universal DDI): a slot
// no longer wanted because the claim went away, opted out, stopped being a LoadBalancer,
// lowered its count or dropped an IP family.
type Release struct {
	Target      TargetRef
	Slot        int
	AddressName string
	// Address is the Address's status.address, for events; empty if never allocated.
	Address string
}

// Plan is the planner's output for one reconcile. The controller applies it in the
// order Ensure, Assign, Unassign, Release.
type Plan struct {
	Ensure   []DesiredAddress
	Assign   []Assignment
	Unassign []Unassign
	Release  []Release
	Skips    []Skip
	// Allocated counts claims whose Addresses are ensured; Assigned counts, of those, the
	// claims whose assignment field already carries every slot's Ready address.
	Allocated int32
	Assigned  int32
}
