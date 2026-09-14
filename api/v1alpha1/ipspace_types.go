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

// Utilization mirrors the read-only Universal DDI utilisation block.
type Utilization struct {
	// AbandonUtilization percentage.
	// +optional
	AbandonUtilization int64 `json:"abandonUtilization,omitempty"`
	// Abandoned address count.
	// +optional
	Abandoned string `json:"abandoned,omitempty"`
	// Dynamic address count.
	// +optional
	Dynamic string `json:"dynamic,omitempty"`
	// Free address count.
	// +optional
	Free string `json:"free,omitempty"`
	// Static address count.
	// +optional
	Static string `json:"static,omitempty"`
	// Total address count.
	// +optional
	Total string `json:"total,omitempty"`
	// Used address count.
	// +optional
	Used string `json:"used,omitempty"`
	// Utilization percentage.
	// +optional
	Utilization int64 `json:"utilization,omitempty"`
}

// IPSpaceSpec describes a Universal DDI IP space. Natural key for adoption: name.
type IPSpaceSpec struct {
	CommonSpec `json:",inline"`

	// Name of the IP space in Universal DDI. Defaults to metadata.name.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Name string `json:"name,omitempty"`

	// Comment stored on the Universal DDI object.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Comment string `json:"comment,omitempty"`

	// DHCPOptions applied to everything in the space.
	// +listType=atomic
	// +optional
	DHCPOptions []DHCPOption `json:"dhcpOptions,omitempty"`
}

// IPSpaceStatus reports the observed state of the IP space.
type IPSpaceStatus struct {
	ObjectStatus `json:",inline"`

	// Name observed in Universal DDI.
	// +optional
	Name string `json:"name,omitempty"`

	// Utilization reported by Universal DDI.
	// +optional
	Utilization *Utilization `json:"utilization,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=uddispace
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// IPSpace is a Universal DDI IP space.
type IPSpace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IPSpaceSpec   `json:"spec,omitempty"`
	Status IPSpaceStatus `json:"status,omitempty"`
}

// SpaceName returns the effective Universal DDI IP space name.
func (s *IPSpace) SpaceName() string {
	if s.Spec.Name != "" {
		return s.Spec.Name
	}
	return s.Name
}

// +kubebuilder:object:root=true

// IPSpaceList contains a list of IPSpace.
type IPSpaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IPSpace `json:"items"`
}

func init() {
	SchemeBuilder.Register(&IPSpace{}, &IPSpaceList{})
}
