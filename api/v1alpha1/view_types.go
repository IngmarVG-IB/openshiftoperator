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

// ViewSpec describes a Universal DDI DNS view. Natural key for adoption: name.
type ViewSpec struct {
	CommonSpec `json:",inline"`

	// Name of the DNS view in Universal DDI. Defaults to metadata.name.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Name string `json:"name,omitempty"`

	// IPSpaces bound to the view for reverse lookups. Each entry names an IPSpace CR
	// in this namespace or a Universal DDI ip space id.
	// +listType=atomic
	// +optional
	IPSpaces []ObjectRef `json:"ipSpaces,omitempty"`

	// Comment stored on the Universal DDI object.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Comment string `json:"comment,omitempty"`

	// Disabled turns the view off in Universal DDI.
	// +optional
	Disabled bool `json:"disabled,omitempty"`
}

// ViewStatus reports the observed state of the DNS view.
type ViewStatus struct {
	ObjectStatus `json:",inline"`

	// Name is the view name observed in Universal DDI.
	// +optional
	Name string `json:"name,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=uddiview
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// View is a Universal DDI DNS view.
type View struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ViewSpec   `json:"spec,omitempty"`
	Status ViewStatus `json:"status,omitempty"`
}

// ViewName returns the effective Universal DDI view name.
func (v *View) ViewName() string {
	if v.Spec.Name != "" {
		return v.Spec.Name
	}
	return v.Name
}

// +kubebuilder:object:root=true

// ViewList contains a list of View.
type ViewList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []View `json:"items"`
}

func init() {
	SchemeBuilder.Register(&View{}, &ViewList{})
}
