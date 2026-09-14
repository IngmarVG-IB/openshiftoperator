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

// AddressName is one entry of the Universal DDI address names list. Both fields are
// required by the API. Type is an open string; "user" is the only value confirmed to be
// writable, so it is the default and there is no enum.
type AddressName struct {
	// Name of the address.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Name string `json:"name"`

	// Type of the name, for example "user".
	// +kubebuilder:default=user
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Type string `json:"type"`
}

// NextAvailableFrom selects the parent the address is allocated from. Exactly one ref
// is set. Allocation happens in a single tagged create using the Universal DDI
// "<parent id>/nextavailableip" address form.
// +kubebuilder:validation:XValidation:rule="[has(self.subnetRef), has(self.rangeRef), has(self.addressBlockRef)].filter(x, x).size() == 1",message="exactly one of subnetRef, rangeRef or addressBlockRef must be set"
type NextAvailableFrom struct {
	// SubnetRef names a Subnet CR or a Universal DDI subnet id.
	// +optional
	SubnetRef *ObjectRef `json:"subnetRef,omitempty"`

	// RangeRef names a Range CR or a Universal DDI range id.
	// +optional
	RangeRef *ObjectRef `json:"rangeRef,omitempty"`

	// AddressBlockRef names an AddressBlock CR or a Universal DDI address block id.
	// +optional
	AddressBlockRef *ObjectRef `json:"addressBlockRef,omitempty"`
}

// AddressSpec describes a Universal DDI address reservation.
// Natural key for adoption: address plus space.
// +kubebuilder:validation:XValidation:rule="has(self.address) != has(self.nextAvailableFrom)",message="exactly one of address or nextAvailableFrom must be set"
type AddressSpec struct {
	CommonSpec `json:",inline"`

	// SpaceRef names the IPSpace CR or a Universal DDI ip space id.
	SpaceRef ObjectRef `json:"spaceRef"`

	// Address to reserve, for example "10.0.10.7". Omit to allocate the next available
	// address from nextAvailableFrom. Immutable.
	// +kubebuilder:validation:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="self == oldSelf.orValue(self)",message="address is immutable",optionalOldSelf=true
	// +optional
	Address string `json:"address,omitempty"`

	// NextAvailableFrom selects the parent to allocate from. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf.orValue(self)",message="nextAvailableFrom is immutable",optionalOldSelf=true
	// +optional
	NextAvailableFrom *NextAvailableFrom `json:"nextAvailableFrom,omitempty"`

	// Names attached to the address.
	// +listType=atomic
	// +optional
	Names []AddressName `json:"names,omitempty"`

	// HWAddr is the MAC address associated with the reservation.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	HWAddr string `json:"hwaddr,omitempty"`

	// Interface name associated with the reservation.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Interface string `json:"interface,omitempty"`

	// Comment stored on the Universal DDI object.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Comment string `json:"comment,omitempty"`
}

// AddressStatus reports the observed state of the address. status.address is the
// allocated address and the value consumers read.
type AddressStatus struct {
	ObjectStatus `json:",inline"`

	// Address allocated or reserved in Universal DDI.
	// +optional
	Address string `json:"address,omitempty"`

	// Space is the Universal DDI ip space id.
	// +optional
	Space string `json:"space,omitempty"`

	// State reported by Universal DDI, for example "used" or "free".
	// +optional
	State string `json:"state,omitempty"`

	// Parent is the Universal DDI subnet or range id containing the address.
	// +optional
	Parent string `json:"parent,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=uddiaddr
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.status.address`
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Address is a Universal DDI address reservation.
type Address struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AddressSpec   `json:"spec,omitempty"`
	Status AddressStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AddressList contains a list of Address.
type AddressList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Address `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Address{}, &AddressList{})
}
