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

// The two immutability rules on AddressBlockSpec live on the spec rather than on the
// fields themselves so they also catch adding or removing a field, which a rule on the
// field cannot see without optionalOldSelf (Kubernetes 1.30 and newer; the OpenShift
// 4.16 floor is Kubernetes 1.29). An address that starts empty for next-available
// allocation must stay empty, and a set address must stay equal.

// AddressBlockSpec describes a Universal DDI address block.
// Natural key for adoption: address plus cidr plus space.
// +kubebuilder:validation:XValidation:rule="has(self.address) || has(self.parentRef)",message="parentRef is required when address is omitted"
// +kubebuilder:validation:XValidation:rule="(!has(oldSelf.address) && !has(self.address)) || (has(oldSelf.address) && has(self.address) && oldSelf.address == self.address)",message="address is immutable: a block allocated from parentRef must keep spec.address empty, and a set address must not change"
// +kubebuilder:validation:XValidation:rule="(!has(oldSelf.parentRef) && !has(self.parentRef)) || (has(oldSelf.parentRef) && has(self.parentRef) && oldSelf.parentRef == self.parentRef)",message="parentRef is immutable: it may not be added, removed or changed after creation"
type AddressBlockSpec struct {
	CommonSpec `json:",inline"`

	// SpaceRef names the IPSpace CR or a Universal DDI ip space id.
	SpaceRef ObjectRef `json:"spaceRef"`

	// Address of the block, for example "10.0.0.0". Omit to allocate the next available
	// block from parentRef. Immutable.
	// +kubebuilder:validation:MaxLength=45
	// +optional
	Address string `json:"address,omitempty"`

	// CIDR prefix length. Immutable.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=128
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="cidr is immutable"
	CIDR int32 `json:"cidr"`

	// ParentRef names the parent AddressBlock CR or a Universal DDI address block id.
	// Required when address is omitted. Immutable.
	// +optional
	ParentRef *ObjectRef `json:"parentRef,omitempty"`

	// Name of the block in Universal DDI.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Name string `json:"name,omitempty"`

	// Comment stored on the Universal DDI object.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Comment string `json:"comment,omitempty"`

	// DHCPOptions applied to everything in the block.
	// +listType=atomic
	// +optional
	DHCPOptions []DHCPOption `json:"dhcpOptions,omitempty"`
}

// AddressBlockStatus reports the observed state of the address block.
type AddressBlockStatus struct {
	ObjectStatus `json:",inline"`

	// Address observed in Universal DDI. For next-available blocks this is the allocated
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
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=uddiblock
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.status.address`
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AddressBlock is a Universal DDI address block.
type AddressBlock struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AddressBlockSpec   `json:"spec,omitempty"`
	Status AddressBlockStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AddressBlockList contains a list of AddressBlock.
type AddressBlockList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AddressBlock `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AddressBlock{}, &AddressBlockList{})
}
