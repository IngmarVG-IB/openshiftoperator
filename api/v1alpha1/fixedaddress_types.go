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

// FixedAddressMatchType selects how a DHCP client is matched to the reservation.
// relay_hex and relay_text are IPv4 only, duid is IPv6 only.
// +kubebuilder:validation:Enum=mac;client_text;client_hex;relay_text;relay_hex;duid
type FixedAddressMatchType string

// FixedAddressSpec describes a Universal DDI DHCP fixed address.
// Natural key for adoption: address plus ip space.
type FixedAddressSpec struct {
	CommonSpec `json:",inline"`

	// SpaceRef names the IPSpace CR or a Universal DDI ip space id.
	SpaceRef ObjectRef `json:"spaceRef"`

	// Address to reserve, for example "10.0.10.50". Immutable.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="address is immutable"
	Address string `json:"address"`

	// MatchType selects how the DHCP client is identified. Immutable.
	// +kubebuilder:default=mac
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="matchType is immutable"
	MatchType FixedAddressMatchType `json:"matchType"`

	// MatchValue is the client identifier, for example a MAC address.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	MatchValue string `json:"matchValue"`

	// Name of the reservation in Universal DDI.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Name string `json:"name,omitempty"`

	// Hostname handed to the client.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Hostname string `json:"hostname,omitempty"`

	// Comment stored on the Universal DDI object.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Comment string `json:"comment,omitempty"`

	// DisableDHCP turns the reservation into an exclusion without deleting it.
	// +optional
	DisableDHCP bool `json:"disableDhcp,omitempty"`

	// DHCPOptions applied to this reservation.
	// +listType=atomic
	// +optional
	DHCPOptions []DHCPOption `json:"dhcpOptions,omitempty"`
}

// FixedAddressStatus reports the observed state of the fixed address.
type FixedAddressStatus struct {
	ObjectStatus `json:",inline"`

	// Address observed in Universal DDI.
	// +optional
	Address string `json:"address,omitempty"`

	// IPSpace is the Universal DDI ip space id.
	// +optional
	IPSpace string `json:"ipSpace,omitempty"`

	// Parent is the Universal DDI subnet id containing the address.
	// +optional
	Parent string `json:"parent,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=uddifixed
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.spec.address`
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FixedAddress is a Universal DDI DHCP fixed address reservation.
type FixedAddress struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FixedAddressSpec   `json:"spec,omitempty"`
	Status FixedAddressStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FixedAddressList contains a list of FixedAddress.
type FixedAddressList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FixedAddress `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FixedAddress{}, &FixedAddressList{})
}
