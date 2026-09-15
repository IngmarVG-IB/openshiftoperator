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

// Condition reasons for HostRegistrationPolicy.status.conditions. See DESIGN.md
// section 4.7.
const (
	// ReasonRegistered means nothing is pending and no SpaceNotReady skip remains.
	ReasonRegistered = "Registered"
	// ReasonRegistrationsPending means the desired objects exist but some are not
	// Ready yet.
	ReasonRegistrationsPending = "RegistrationsPending"
	// ReasonSpaceNotReady means the policy's IPSpace CR is missing or has no status.id.
	ReasonSpaceNotReady = "SpaceNotReady"
)

// AddressType selects which Node address is registered: the first
// status.addresses entry of that type.
// +kubebuilder:validation:Enum=InternalIP;ExternalIP
type AddressType string

const (
	// AddressTypeInternalIP registers the Node's InternalIP.
	AddressTypeInternalIP AddressType = "InternalIP"
	// AddressTypeExternalIP registers the Node's ExternalIP.
	AddressTypeExternalIP AddressType = "ExternalIP"
)

// RegisterMode selects which kind of object represents a registered Node.
// +kubebuilder:validation:Enum=Auto;Address;FixedAddress
type RegisterMode string

const (
	// RegisterModeAuto registers a FixedAddress when a MAC is known and an Address
	// otherwise.
	RegisterModeAuto RegisterMode = "Auto"
	// RegisterModeAddress always registers a named IPAM Address (hwaddr set when a
	// MAC is known).
	RegisterModeAddress RegisterMode = "Address"
	// RegisterModeFixedAddress always registers a DHCP FixedAddress and needs a MAC.
	RegisterModeFixedAddress RegisterMode = "FixedAddress"
)

// Register controls which kind of object HostRegistrationPolicy creates for each
// selected Node.
type Register struct {
	// Mode selects Address, FixedAddress, or Auto (FixedAddress when a MAC is known,
	// Address otherwise).
	// +kubebuilder:default=Auto
	// +optional
	Mode RegisterMode `json:"mode,omitempty"`

	// DHCPOptions are copied to every generated FixedAddress. Ignored in Address mode.
	// +listType=atomic
	// +optional
	DHCPOptions []DHCPOption `json:"dhcpOptions,omitempty"`
}

// NMStateSource selects whether, and from which interface, a Node's MAC is read from
// NodeNetworkState.
type NMStateSource struct {
	// Enabled reads NodeNetworkState when the nmstate.io/v1beta1 API is present on the
	// cluster.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Interface names the NodeNetworkState interface to take the MAC from. Empty means
	// the interface whose addresses include the registered address.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Interface string `json:"interface,omitempty"`
}

// MACSource selects where a Node's MAC address comes from: first the annotation, then
// (when enabled and available) NodeNetworkState.
type MACSource struct {
	// NodeAnnotation is the fully qualified annotation key holding the MAC. Defaults
	// to uddi.infoblox.com/mac.
	// +kubebuilder:default=uddi.infoblox.com/mac
	// +kubebuilder:validation:Pattern=`^(([A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?\.)*[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`
	// +kubebuilder:validation:MaxLength=317
	// +optional
	NodeAnnotation string `json:"nodeAnnotation,omitempty"`

	// NMState controls reading NodeNetworkState for the MAC.
	// +optional
	NMState NMStateSource `json:"nmstate,omitempty"`
}

// HostRegistrationPolicySpec selects which Nodes get registered in Universal DDI as an
// Address or a DHCP FixedAddress. See DESIGN.md section 4.7 for the full pipeline.
type HostRegistrationPolicySpec struct {
	// ConnectionRef names the Connection used by every generated object. Empty means
	// the Connection called "default" in this namespace.
	// +optional
	ConnectionRef ConnectionRef `json:"connectionRef,omitempty"`

	// Selector selects the Nodes this policy considers. Empty selects every Node.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// SpaceRef names the IPSpace CR, in this namespace, that holds the Node addresses.
	// Only a name is accepted (not a raw Universal DDI id): the IPSpace CR must exist
	// so its status.id can be watched for readiness.
	// +kubebuilder:validation:XValidation:rule="size(self.name) > 0",message="spaceRef must set name"
	SpaceRef ObjectRef `json:"spaceRef"`

	// AddressType selects which Node address is registered.
	// +kubebuilder:default=InternalIP
	// +optional
	AddressType AddressType `json:"addressType,omitempty"`

	// Register controls which kind of object is created for each Node.
	// +optional
	Register Register `json:"register,omitempty"`

	// MACSource selects where a Node's MAC address comes from.
	// +optional
	MACSource MACSource `json:"macSource,omitempty"`

	// Tags are merged into every generated object's spec.tags.
	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// Lifecycle is copied onto every generated object's spec.lifecycle.
	// +optional
	Lifecycle Lifecycle `json:"lifecycle,omitempty"`
}

// SkippedNode is one Node that was not registered, or not registered as desired, and
// why. See internal/hostreg.Skip.
type SkippedNode struct {
	// Node is the name of the Node that was skipped.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Node string `json:"node,omitempty"`

	// Reason is a short machine-readable skip reason, for example NoMAC.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message is a human-readable explanation.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	Message string `json:"message,omitempty"`
}

// HostRegistrationPolicyStatus reports the observed state of a HostRegistrationPolicy.
type HostRegistrationPolicyStatus struct {
	// ObservedGeneration is the metadata.generation the status was last computed
	// from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions hold Ready.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Registered is the number of Nodes whose registration object is Ready.
	// +optional
	Registered int32 `json:"registered,omitempty"`

	// Pending is the number of registration objects that exist but are not Ready.
	// +optional
	Pending int32 `json:"pending,omitempty"`

	// Skipped lists Nodes not registered, and why. Bounded and sorted by node then
	// reason.
	// +kubebuilder:validation:MaxItems=50
	// +listType=atomic
	// +optional
	Skipped []SkippedNode `json:"skipped,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=uddihostreg
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Registered",type=integer,JSONPath=`.status.registered`
// +kubebuilder:printcolumn:name="Pending",type=integer,JSONPath=`.status.pending`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HostRegistrationPolicy registers selected Nodes in Universal DDI as an IPAM Address
// or a DHCP FixedAddress.
type HostRegistrationPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HostRegistrationPolicySpec   `json:"spec,omitempty"`
	Status HostRegistrationPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HostRegistrationPolicyList contains a list of HostRegistrationPolicy.
type HostRegistrationPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HostRegistrationPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HostRegistrationPolicy{}, &HostRegistrationPolicyList{})
}
