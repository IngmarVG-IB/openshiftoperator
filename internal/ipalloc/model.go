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
// asks for, deciding which Address CRs should exist, which Services get their address
// written back and which allocations must be released. Nothing here talks to Kubernetes
// or Universal DDI; the controller in internal/controller feeds it objects and applies the
// plan. This file is the shared contract between the Service view, the planner and the
// controller.
package ipalloc

import (
	"fmt"

	"k8s.io/apimachinery/pkg/types"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

// ServiceRef identifies the Service a decision is about.
type ServiceRef struct {
	Namespace string
	Name      string
	UID       types.UID
}

// String renders the reference the way status.skipped and events show it: "Service/name".
func (s ServiceRef) String() string { return fmt.Sprintf("Service/%s", s.Name) }

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
	// kube-vip all read one).
	AssignModeAnnotation AssignMode = "Annotation"
	// AssignModeLoadBalancerIP writes spec.loadBalancerIP, deprecated upstream but still
	// honoured by most load balancer implementations.
	AssignModeLoadBalancerIP AssignMode = "LoadBalancerIP"
)

// Well-known annotation keys load balancer implementations read the requested IP from.
const (
	AnnotationMetalLB       = "metallb.io/loadBalancerIPs"
	AnnotationMetalLBLegacy = "metallb.universe.tf/loadBalancerIPs"
	AnnotationCilium        = "io.cilium/lb-ipam-ips"
	AnnotationKubeVIP       = "kube-vip.io/loadbalancerIPs"
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

// DefaultAnnotationPrefix is the domain of the Service annotations the policy reads.
const DefaultAnnotationPrefix = "uddi.infoblox.com"

// Service annotation keys relative to the prefix.
const (
	AnnotationAllocate     = "allocate"      // "true" opts in, "false" opts out
	AnnotationAllocateFrom = "allocate-from" // "subnet/<name>", "range/<name>" or "addressblock/<name>"
	AnnotationAddress      = "address"       // a specific IP instead of next-available
	AnnotationHostname     = "hostname"      // the name registered on the Address
)

// Labels, tags and the field manager stamped on generated Addresses.
const (
	LabelPolicy  = "uddi.infoblox.com/policy"
	LabelService = "uddi.infoblox.com/service"
	// TagSource is the Universal DDI tag key naming the Service an address was allocated for.
	TagSource = "uddi_operator_source"
	// FieldManager is the server-side apply field manager for generated Addresses.
	FieldManager = "uddi-operator/ipallocationpolicy"
)

// SkipReason explains why a Service was not (or not yet) given an address. The values are
// the reason strings shown in IPAllocationPolicy.status.skipped and Skipped events.
type SkipReason string

const (
	SkipNotLoadBalancer   SkipReason = "NotLoadBalancer"   // opted in but not type LoadBalancer
	SkipInvalidAnnotation SkipReason = "InvalidAnnotation" // a malformed allocate-from / address / hostname value
	SkipPoolNotReady      SkipReason = "PoolNotReady"      // the pool CR is missing or has no status.id
	SkipManuallyAssigned  SkipReason = "ManuallyAssigned"  // the assignment field already holds a foreign value
	SkipAddressPending    SkipReason = "AddressPending"    // the Address CR exists but is not Ready yet
)

// Skip records one Service that was not assigned and why.
type Skip struct {
	Service ServiceRef
	Reason  SkipReason
	Message string
}

// ServiceView is everything the planner needs to know about one Service. It is built by
// ViewService from a corev1.Service and the policy's AssignVia.
type ServiceView struct {
	Ref            ServiceRef
	IsLoadBalancer bool
	// OptIn is nil when the allocate annotation is absent, otherwise its parsed value.
	OptIn *bool
	// Pool overrides the policy pool when the allocate-from annotation is set.
	Pool *PoolRef
	// ExplicitAddress is the address annotation, validated as an IP; empty for next-available.
	ExplicitAddress string
	// Hostname is the hostname annotation, or the default "<service>.<namespace>".
	Hostname string
	// CurrentAssignment is the value the assignment field currently holds ("" when unset).
	CurrentAssignment string
	// Invalid carries the message of an InvalidAnnotation skip; empty when the view is valid.
	Invalid string
}

// PoolState is a pool CR as the planner sees it.
type PoolState struct {
	Ref      PoolRef
	Ready    bool                   // the CR exists and has a status.id
	SpaceRef uddiv1alpha1.ObjectRef // the pool's spec.spaceRef, copied to generated Addresses
}

// AddressState is an existing generated Address CR as the planner sees it.
type AddressState struct {
	Name    string
	Service string // from the LabelService label
	Ready   bool
	Address string // status.address, empty until allocated
	// Reason and Message come from the Address's Ready condition when it is not Ready.
	Reason  string
	Message string
}

// DesiredAddress is one Address CR the policy wants to exist.
type DesiredAddress struct {
	Name        string
	Service     ServiceRef
	Labels      map[string]string
	Annotations map[string]string
	Spec        uddiv1alpha1.AddressSpec
}

// Assignment writes an allocated address into a Service.
type Assignment struct {
	Service ServiceRef
	Value   string
}

// Release undoes an allocation: clear the assignment field if it still equals ClearValue,
// then delete the Address CR.
type Release struct {
	Service     ServiceRef
	AddressName string
	ClearValue  string
}

// Plan is the planner's output for one reconcile.
type Plan struct {
	Ensure    []DesiredAddress
	Assign    []Assignment
	Release   []Release
	Skips     []Skip
	Allocated int32
	Assigned  int32
}
