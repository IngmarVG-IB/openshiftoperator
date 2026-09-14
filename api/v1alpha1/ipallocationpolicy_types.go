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

// IPAllocationPolicySpec selects which Services get a Universal DDI IPAM address
// allocated and written back. See DESIGN.md section 4.7 for the full pipeline.
type IPAllocationPolicySpec struct {
	// ConnectionRef names the Connection used by every Address this policy
	// generates. Empty means the Connection called "default" in this namespace.
	// +optional
	ConnectionRef ConnectionRef `json:"connectionRef,omitempty"`

	// Selector selects the Services in this namespace this policy considers. Empty
	// selects every Service in the namespace.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// RequireAnnotation, when true (the default), only allocates for Services that
	// carry the uddi.infoblox.com/allocate: "true" annotation. When false, every
	// selected type: LoadBalancer Service is allocated for unless it opts out with
	// uddi.infoblox.com/allocate: "false".
	// +kubebuilder:default=true
	// +optional
	RequireAnnotation *bool `json:"requireAnnotation,omitempty"`

	// AllocateFrom selects the pool this policy allocates Addresses from.
	AllocateFrom AllocateFrom `json:"allocateFrom"`

	// AssignVia selects how the allocated address is written back onto a Service.
	// +optional
	AssignVia AssignVia `json:"assignVia,omitempty"`

	// Tags are merged into every generated Address's spec.tags.
	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// AddressLifecycle is copied onto every generated Address's spec.lifecycle.
	// +optional
	AddressLifecycle Lifecycle `json:"addressLifecycle,omitempty"`
}

// SkippedService is one Service that did not get an address, or did not get its
// address assigned, and why. See internal/ipalloc.Skip.
type SkippedService struct {
	// Service is the name of the Service that was skipped.
	// +kubebuilder:validation:MaxLength=253
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

	// Allocated is the number of Services that have an Address CR.
	// +optional
	Allocated int32 `json:"allocated,omitempty"`

	// Assigned is, of those, the number whose assignment field carries the Ready
	// address.
	// +optional
	Assigned int32 `json:"assigned,omitempty"`

	// Skipped lists Services not allocated or not assigned, and why. Bounded and
	// sorted by service then reason.
	// +kubebuilder:validation:MaxItems=50
	// +listType=atomic
	// +optional
	Skipped []SkippedService `json:"skipped,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=uddiipalloc
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Allocated",type=integer,JSONPath=`.status.allocated`
// +kubebuilder:printcolumn:name="Assigned",type=integer,JSONPath=`.status.assigned`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// IPAllocationPolicy allocates Universal DDI IPAM addresses for LoadBalancer
// Services and writes them back as a Service annotation or spec.loadBalancerIP.
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
