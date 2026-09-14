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

// AuthZonePrimaryType selects who serves the zone.
// +kubebuilder:validation:Enum=cloud;external
type AuthZonePrimaryType string

const (
	// AuthZonePrimaryCloud is served by Universal DDI DNS hosts.
	AuthZonePrimaryCloud AuthZonePrimaryType = "cloud"
	// AuthZonePrimaryExternal is served by external primaries.
	AuthZonePrimaryExternal AuthZonePrimaryType = "external"
)

// ExternalPrimaryType distinguishes a real external primary from an NSG placeholder.
// +kubebuilder:validation:Enum=primary;nsg
type ExternalPrimaryType string

// InternalSecondary is a Universal DDI DNS host acting as an internal secondary.
type InternalSecondary struct {
	// Host is a Universal DDI DNS host id, for example "dns/host/<uuid>".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Host string `json:"host"`
}

// ExternalPrimary is a nameserver outside Universal DDI. TSIG key material is not
// modelled in v1alpha1; only the tsigEnabled flag is carried through.
type ExternalPrimary struct {
	// Type of the external primary.
	// +kubebuilder:default=primary
	Type ExternalPrimaryType `json:"type"`

	// Address of the nameserver.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Address string `json:"address,omitempty"`

	// FQDN of the nameserver, trailing dot included.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	FQDN string `json:"fqdn,omitempty"`

	// NSG is a nameserver group id this primary belongs to. Only meaningful when type
	// is "nsg".
	// +kubebuilder:validation:MaxLength=256
	// +optional
	NSG string `json:"nsg,omitempty"`

	// TSIGEnabled turns on TSIG for transfers from this primary.
	// +optional
	TSIGEnabled bool `json:"tsigEnabled,omitempty"`
}

// ExternalSecondary is a secondary nameserver outside Universal DDI. TSIG key material
// is not modelled in v1alpha1.
type ExternalSecondary struct {
	// Address of the nameserver.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Address string `json:"address"`

	// FQDN of the nameserver, trailing dot included.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	FQDN string `json:"fqdn"`

	// Stealth keeps the secondary out of the zone's NS records.
	// +optional
	Stealth bool `json:"stealth,omitempty"`

	// TSIGEnabled turns on TSIG for transfers to this secondary.
	// +optional
	TSIGEnabled bool `json:"tsigEnabled,omitempty"`
}

// AuthZoneSpec describes a Universal DDI authoritative zone.
// Natural key for adoption: fqdn plus view.
//
// Nameserver assignment is mid-migration in the API. The unified "nameservers" model
// (API version v2) does not exist in universal-ddi-go-client v0.4.0, so v1alpha1
// exposes only the legacy fields the SDK actually carries. The API rejects an NSG
// combined with internal secondaries, and rejects more than one NSG on a zone, so both
// are enforced here.
// +kubebuilder:validation:XValidation:rule="!(has(self.nsgs) && size(self.nsgs) > 0 && has(self.internalSecondaries) && size(self.internalSecondaries) > 0)",message="nsgs and internalSecondaries cannot both be set"
type AuthZoneSpec struct {
	CommonSpec `json:",inline"`

	// FQDN of the zone, trailing dot included. Immutable.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="fqdn is immutable"
	FQDN string `json:"fqdn"`

	// ViewRef names the View CR or a Universal DDI view id. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="viewRef is immutable"
	ViewRef ObjectRef `json:"viewRef"`

	// PrimaryType selects who serves the zone. Immutable.
	// +kubebuilder:default=cloud
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="primaryType is immutable"
	PrimaryType AuthZonePrimaryType `json:"primaryType"`

	// NSGs are nameserver group ids. The API rejects more than one NSG per zone.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=1
	// +optional
	NSGs []string `json:"nsgs,omitempty"`

	// InternalSecondaries are Universal DDI DNS hosts serving the zone (primaryType cloud).
	// +listType=atomic
	// +optional
	InternalSecondaries []InternalSecondary `json:"internalSecondaries,omitempty"`

	// ExternalPrimaries are nameservers outside Universal DDI (primaryType external).
	// +listType=atomic
	// +optional
	ExternalPrimaries []ExternalPrimary `json:"externalPrimaries,omitempty"`

	// ExternalSecondaries are secondary nameservers outside Universal DDI.
	// +listType=atomic
	// +optional
	ExternalSecondaries []ExternalSecondary `json:"externalSecondaries,omitempty"`

	// Comment stored on the Universal DDI object.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Comment string `json:"comment,omitempty"`

	// Disabled turns the zone off in Universal DDI.
	// +optional
	Disabled bool `json:"disabled,omitempty"`
}

// AuthZoneStatus reports the observed state of the zone.
type AuthZoneStatus struct {
	ObjectStatus `json:",inline"`

	// FQDN observed in Universal DDI, in protocol form.
	// +optional
	FQDN string `json:"fqdn,omitempty"`

	// View is the Universal DDI view id the zone lives in.
	// +optional
	View string `json:"view,omitempty"`

	// Warnings reported by the API on the last create or update, as "<name>: <message>"
	// lines.
	// +listType=atomic
	// +optional
	Warnings []string `json:"warnings,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=uddizone
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="FQDN",type=string,JSONPath=`.spec.fqdn`
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AuthZone is a Universal DDI authoritative DNS zone.
type AuthZone struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AuthZoneSpec   `json:"spec,omitempty"`
	Status AuthZoneStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AuthZoneList contains a list of AuthZone.
type AuthZoneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AuthZone `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AuthZone{}, &AuthZoneList{})
}
