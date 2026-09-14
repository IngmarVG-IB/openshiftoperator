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

// ExclusionRange carves a hole out of a Range.
type ExclusionRange struct {
	// Start address of the exclusion, inclusive.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=45
	Start string `json:"start"`

	// End address of the exclusion, inclusive.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=45
	End string `json:"end"`

	// Comment stored on the Universal DDI object.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Comment string `json:"comment,omitempty"`
}

// RangeSpec describes a Universal DDI address range. The parent subnet is derived by
// Universal DDI from start and end.
// Natural key for adoption: start plus end plus space.
type RangeSpec struct {
	CommonSpec `json:",inline"`

	// SpaceRef names the IPSpace CR or a Universal DDI ip space id.
	SpaceRef ObjectRef `json:"spaceRef"`

	// Start address of the range, inclusive. Immutable.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="start is immutable"
	Start string `json:"start"`

	// End address of the range, inclusive. Immutable.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="end is immutable"
	End string `json:"end"`

	// Name of the range in Universal DDI.
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

	// DHCPOptions applied to leases from this range.
	// +listType=atomic
	// +optional
	DHCPOptions []DHCPOption `json:"dhcpOptions,omitempty"`

	// ExclusionRanges carve holes out of the range.
	// +listType=atomic
	// +optional
	ExclusionRanges []ExclusionRange `json:"exclusionRanges,omitempty"`

	// DisableDHCP stops Universal DDI serving DHCP from this range.
	// +optional
	DisableDHCP bool `json:"disableDhcp,omitempty"`
}

// RangeStatus reports the observed state of the range.
type RangeStatus struct {
	ObjectStatus `json:",inline"`

	// Start observed in Universal DDI.
	// +optional
	Start string `json:"start,omitempty"`

	// End observed in Universal DDI.
	// +optional
	End string `json:"end,omitempty"`

	// Space is the Universal DDI ip space id.
	// +optional
	Space string `json:"space,omitempty"`

	// Parent is the Universal DDI subnet id derived from start and end.
	// +optional
	Parent string `json:"parent,omitempty"`

	// Utilization reported by Universal DDI.
	// +optional
	Utilization *Utilization `json:"utilization,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=uddirange
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Start",type=string,JSONPath=`.spec.start`
// +kubebuilder:printcolumn:name="End",type=string,JSONPath=`.spec.end`
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Range is a Universal DDI address range.
type Range struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RangeSpec   `json:"spec,omitempty"`
	Status RangeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RangeList contains a list of Range.
type RangeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Range `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Range{}, &RangeList{})
}
