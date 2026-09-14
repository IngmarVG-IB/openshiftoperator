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

// DHCPUtilization mirrors the read-only Universal DDI DHCP utilisation block, which is
// reported separately from the general address Utilization on kinds that serve DHCP
// (Subnet, Range).
type DHCPUtilization struct {
	// DHCPFree is the free address count within the DHCP ranges.
	// +optional
	DHCPFree string `json:"dhcpFree,omitempty"`
	// DHCPTotal is the total address count within the DHCP ranges.
	// +optional
	DHCPTotal string `json:"dhcpTotal,omitempty"`
	// DHCPUsed is the used address count within the DHCP ranges.
	// +optional
	DHCPUsed string `json:"dhcpUsed,omitempty"`
	// DHCPUtilization is the utilization percentage within the DHCP ranges.
	// +optional
	DHCPUtilization int64 `json:"dhcpUtilization,omitempty"`
}

// SubnetSpec describes a Universal DDI subnet.
// Natural key for adoption: address plus cidr plus space.
// +kubebuilder:validation:XValidation:rule="has(self.address) || has(self.parentRef)",message="parentRef is required when address is omitted"
type SubnetSpec struct {
	CommonSpec `json:",inline"`

	// SpaceRef names the IPSpace CR or a Universal DDI ip space id.
	SpaceRef ObjectRef `json:"spaceRef"`

	// Address of the subnet, for example "10.0.10.0". Omit to allocate the next available
	// subnet from parentRef. Immutable.
	// +kubebuilder:validation:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="self == oldSelf.orValue(self)",message="address is immutable",optionalOldSelf=true
	// +optional
	Address string `json:"address,omitempty"`

	// CIDR prefix length. Immutable.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=128
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="cidr is immutable"
	CIDR int32 `json:"cidr"`

	// ParentRef names the parent AddressBlock CR or a Universal DDI address block id.
	// Required when address is omitted. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf.orValue(self)",message="parentRef is immutable",optionalOldSelf=true
	// +optional
	ParentRef *ObjectRef `json:"parentRef,omitempty"`

	// Name of the subnet in Universal DDI.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Name string `json:"name,omitempty"`

	// Comment stored on the Universal DDI object.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Comment string `json:"comment,omitempty"`

	// DHCPHost is a Universal DDI DHCP service host id, for example "dhcp/host/<uuid>".
	// +kubebuilder:validation:MaxLength=256
	// +optional
	DHCPHost string `json:"dhcpHost,omitempty"`

	// DHCPOptions applied to everything in the subnet.
	// +listType=atomic
	// +optional
	DHCPOptions []DHCPOption `json:"dhcpOptions,omitempty"`

	// DisableDHCP stops Universal DDI serving DHCP from this subnet.
	// +optional
	DisableDHCP bool `json:"disableDhcp,omitempty"`
}

// SubnetStatus reports the observed state of the subnet.
type SubnetStatus struct {
	ObjectStatus `json:",inline"`

	// Address observed in Universal DDI. For next-available subnets this is the allocated
	// network address.
	// +optional
	Address string `json:"address,omitempty"`

	// CIDR observed in Universal DDI.
	// +optional
	CIDR int32 `json:"cidr,omitempty"`

	// Space is the Universal DDI ip space id.
	// +optional
	Space string `json:"space,omitempty"`

	// Utilization reported by Universal DDI.
	// +optional
	Utilization *Utilization `json:"utilization,omitempty"`

	// DHCPUtilization reported by Universal DDI for the DHCP ranges within this subnet.
	// +optional
	DHCPUtilization *DHCPUtilization `json:"dhcpUtilization,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=uddisubnet
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.status.address`
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Subnet is a Universal DDI subnet.
type Subnet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SubnetSpec   `json:"spec,omitempty"`
	Status SubnetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SubnetList contains a list of Subnet.
type SubnetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Subnet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Subnet{}, &SubnetList{})
}
