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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition reasons for IPAllocationPolicy.status.conditions. See DESIGN.md section 4.7.
const (
	// ReasonAssigned means assigned == allocated and no PoolNotReady or
	// AddressPending skip remains.
	ReasonAssigned = "Assigned"
	// ReasonAddressesPending means the desired Addresses exist but some are not
	// Ready (assigned) yet.
	ReasonAddressesPending = "AddressesPending"
	// ReasonPoolNotReady means the policy pool itself has no status.id yet.
	ReasonPoolNotReady = "PoolNotReady"
)

// Skip reasons IPAllocationPolicy.status.skipped carries besides the claim skip reasons of
// internal/ipalloc (ipalloc.SkipReason). None of them changes the Ready condition. See
// DESIGN.md section 4.7, milestone 7 extensions.
const (
	// SkipReasonAPIUnavailable means spec.targets lists EgressIP but the cluster does not
	// serve k8s.ovn.org/v1 EgressIP (entry "EgressIP"), or spec.metallb.mirrorPools is true
	// but the cluster does not serve metallb.io/v1beta1 IPAddressPool (entry
	// "IPAddressPool"). That target, or the mirroring, is inert.
	SkipReasonAPIUnavailable = "APIUnavailable"
	// SkipReasonMetalLBPoolInvalid means a pool the policy uses cannot be mirrored into a
	// MetalLB IPAddressPool, for example a Subnet whose address is not known yet (entry
	// "<pool kind>/<pool name>").
	SkipReasonMetalLBPoolInvalid = "MetalLBPoolInvalid"
	// SkipReasonMetalLBPoolConflict means the IPAddressPool a pool would be mirrored into
	// already exists without this policy's labels; it is left alone (entry
	// "<pool kind>/<pool name>").
	SkipReasonMetalLBPoolConflict = "MetalLBPoolConflict"
)

// AssignMode selects how an allocated address is written back onto a Service.
// +kubebuilder:validation:Enum=Annotation;LoadBalancerIP
type AssignMode string

const (
	// AssignModeAnnotation writes the address into the Service annotation named by
	// AssignVia.Annotation (MetalLB, Cilium and kube-vip all read one).
	AssignModeAnnotation AssignMode = "Annotation"
	// AssignModeLoadBalancerIP writes spec.loadBalancerIP, deprecated upstream but
	// still honoured by most load balancer implementations.
	AssignModeLoadBalancerIP AssignMode = "LoadBalancerIP"
)

// AssignVia selects how an IPAllocationPolicy writes an allocated address back onto
// the Service it was allocated for.
type AssignVia struct {
	// Mode selects whether the address is written to Annotation or to
	// spec.loadBalancerIP.
	// +kubebuilder:default=Annotation
	// +optional
	Mode AssignMode `json:"mode,omitempty"`

	// Annotation is the Service annotation key written in Annotation mode. Defaults
	// to metallb.io/loadBalancerIPs; other implementations read
	// io.cilium/lb-ipam-ips, kube-vip.io/loadbalancerIPs or the legacy
	// metallb.universe.tf/loadBalancerIPs.
	// +kubebuilder:default=metallb.io/loadBalancerIPs
	// +kubebuilder:validation:Pattern=`^(([A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?\.)*[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`
	// +kubebuilder:validation:MaxLength=317
	// +optional
	Annotation string `json:"annotation,omitempty"`
}

// AllocateFrom selects the pool CR, in the policy's namespace, that generated
// Addresses draw from unless a Service overrides it with the allocate-from
// annotation. Exactly one of subnetRef, rangeRef or addressBlockRef is set, and it
// must name a CR in this namespace: a Universal DDI id is not accepted here, since
// the pool has to exist as a CR for its spaceRef to be read.
// +kubebuilder:validation:XValidation:rule="[has(self.subnetRef), has(self.rangeRef), has(self.addressBlockRef)].filter(x, x).size() == 1",message="exactly one of subnetRef, rangeRef or addressBlockRef must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.subnetRef) || (has(self.subnetRef.name) && size(self.subnetRef.name) > 0)",message="subnetRef must set name"
// +kubebuilder:validation:XValidation:rule="!has(self.rangeRef) || (has(self.rangeRef.name) && size(self.rangeRef.name) > 0)",message="rangeRef must set name"
// +kubebuilder:validation:XValidation:rule="!has(self.addressBlockRef) || (has(self.addressBlockRef.name) && size(self.addressBlockRef.name) > 0)",message="addressBlockRef must set name"
type AllocateFrom struct {
	// SubnetRef names a Subnet CR in this namespace.
	// +optional
	SubnetRef *ObjectRef `json:"subnetRef,omitempty"`

	// RangeRef names a Range CR in this namespace.
	// +optional
	RangeRef *ObjectRef `json:"rangeRef,omitempty"`

	// AddressBlockRef names an AddressBlock CR in this namespace.
	// +optional
	AddressBlockRef *ObjectRef `json:"addressBlockRef,omitempty"`
}

// TargetKind names a kind of object an IPAllocationPolicy allocates addresses for.
// +kubebuilder:validation:Enum=Service;EgressIP
type TargetKind string

const (
	// TargetKindService allocates for Services of type LoadBalancer, one address per IP
	// family.
	TargetKindService TargetKind = "Service"
	// TargetKindEgressIP allocates for OVN-Kubernetes EgressIPs (k8s.ovn.org/v1), the
	// number of addresses each one asks for with the uddi.infoblox.com/count annotation.
	TargetKindEgressIP TargetKind = "EgressIP"
)

// DefaultMetalLBNamespace is the namespace mirrored MetalLB IPAddressPools are created in
// when spec.metallb.namespace is empty.
const DefaultMetalLBNamespace = "metallb-system"

// MetalLBMirror configures mirroring of the pools an IPAllocationPolicy uses into MetalLB
// IPAddressPools, so MetalLB announces the addresses the policy assigns.
type MetalLBMirror struct {
	// MirrorPools, when true, mirrors every pool this policy uses into a
	// metallb.io/v1beta1 IPAddressPool with autoAssign false.
	// +optional
	MirrorPools bool `json:"mirrorPools,omitempty"`

	// Namespace is where the mirrored IPAddressPools are created. Defaults to
	// metallb-system.
	// +kubebuilder:default=metallb-system
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// IPAllocationPolicySpec selects which Services and EgressIPs get Universal DDI IPAM
// addresses allocated and written back. See DESIGN.md section 4.7 for the full pipeline.
type IPAllocationPolicySpec struct {
	// ConnectionRef names the Connection used by every Address this policy
	// generates. Empty means the Connection called "default" in this namespace.
	// +optional
	ConnectionRef ConnectionRef `json:"connectionRef,omitempty"`

	// Targets selects which kinds of objects claim addresses: Service (type
	// LoadBalancer Services in this namespace) and EgressIP (cluster-scoped
	// OVN-Kubernetes EgressIPs). Defaults to Service only. Addresses already allocated
	// for a kind that is removed from this list are left in place.
	// +kubebuilder:default={Service}
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=2
	// +listType=set
	// +optional
	Targets []TargetKind `json:"targets,omitempty"`

	// Selector selects the Services in this namespace, and the EgressIPs, this policy
	// considers. Empty selects every one.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// RequireAnnotation, when true (the default), only allocates for Services that
	// carry the uddi.infoblox.com/allocate: "true" annotation. When false, every
	// selected type: LoadBalancer Service is allocated for unless it opts out with
	// uddi.infoblox.com/allocate: "false". EgressIPs always need the annotation.
	// +kubebuilder:default=true
	// +optional
	RequireAnnotation *bool `json:"requireAnnotation,omitempty"`

	// AllocateFrom selects the pool this policy allocates Addresses from. IPv4 slots
	// use it, and IPv6 slots too when it is an IPv6 pool and allocateFromIPv6 is unset.
	AllocateFrom AllocateFrom `json:"allocateFrom"`

	// AllocateFromIPv6 selects the pool IPv6 slots (the IPv6 address of a dual-stack
	// Service) are allocated from. Optional: without it IPv6 slots use allocateFrom
	// when that is an IPv6 pool.
	// +optional
	AllocateFromIPv6 *AllocateFrom `json:"allocateFromIPv6,omitempty"`

	// AssignVia selects how the allocated address is written back onto a Service.
	// +optional
	AssignVia AssignVia `json:"assignVia,omitempty"`

	// MetalLB configures mirroring of this policy's pools into MetalLB IPAddressPools.
	// +optional
	MetalLB *MetalLBMirror `json:"metallb,omitempty"`

	// Tags are merged into every generated Address's spec.tags.
	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// AddressLifecycle is copied onto every generated Address's spec.lifecycle.
	// +optional
	AddressLifecycle Lifecycle `json:"addressLifecycle,omitempty"`
}

// SkippedService is one Service or EgressIP that did not get its addresses, or did not
// get them assigned, or one pool that could not be mirrored into MetalLB, and why. See
// internal/ipalloc.Skip.
type SkippedService struct {
	// Service names what was skipped: the name of a Service; "EgressIP/<name>" for an
	// EgressIP; "<pool kind>/<pool name>" (for example "Range/lb-pool") for a pool that
	// cannot be mirrored into MetalLB; or "EgressIP" or "IPAddressPool" alone when the
	// cluster does not serve that API. At most "AddressBlock/" plus a 253 character name.
	// +kubebuilder:validation:MaxLength=266
	// +optional
	Service string `json:"service,omitempty"`

	// Reason is a short machine-readable skip reason, for example PoolNotReady.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message is a human-readable explanation.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	Message string `json:"message,omitempty"`
}

// IPAllocationPolicyStatus reports the observed state of an IPAllocationPolicy.
type IPAllocationPolicyStatus struct {
	// ObservedGeneration is the metadata.generation the status was last computed
	// from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions hold Ready.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Allocated is the number of Services and EgressIPs that have their Address CRs.
	// +optional
	Allocated int32 `json:"allocated,omitempty"`

	// Assigned is, of those, the number whose assignment field carries every Ready
	// address.
	// +optional
	Assigned int32 `json:"assigned,omitempty"`

	// Skipped lists Services and EgressIPs not allocated or not assigned, and pools not
	// mirrored into MetalLB, and why. Bounded and sorted by service then reason.
	// +kubebuilder:validation:MaxItems=50
	// +listType=atomic
	// +optional
	Skipped []SkippedService `json:"skipped,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=uddiipalloc
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 63",message="the policy name becomes a label value on every generated Address, so it is limited to 63 characters"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Allocated",type=integer,JSONPath=`.status.allocated`
// +kubebuilder:printcolumn:name="Assigned",type=integer,JSONPath=`.status.assigned`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// IPAllocationPolicy allocates Universal DDI IPAM addresses for LoadBalancer
// Services, one per IP family, and OVN-Kubernetes EgressIPs, and writes them back as a
// Service annotation, spec.loadBalancerIP or the EgressIP's spec.egressIPs.
type IPAllocationPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IPAllocationPolicySpec   `json:"spec,omitempty"`
	Status IPAllocationPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// IPAllocationPolicyList contains a list of IPAllocationPolicy.
type IPAllocationPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IPAllocationPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&IPAllocationPolicy{}, &IPAllocationPolicyList{})
}
