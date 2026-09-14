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

// RecordType is a DNS resource record type.
// +kubebuilder:validation:Enum=A;AAAA;CNAME;TXT;PTR;SRV;MX;NS;CAA
type RecordType string

// ARData is the rdata of an A record.
type ARData struct {
	// Address is an IPv4 address.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=45
	Address string `json:"address"`
}

// AAAARData is the rdata of an AAAA record.
type AAAARData struct {
	// Address is an IPv6 address.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=45
	Address string `json:"address"`
}

// CNAMERData is the rdata of a CNAME record.
type CNAMERData struct {
	// CNAME is the canonical name, trailing dot included.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	CNAME string `json:"cname"`
}

// TXTRData is the rdata of a TXT record.
type TXTRData struct {
	// Text is the character string.
	// +kubebuilder:validation:MaxLength=4096
	Text string `json:"text"`
}

// PTRRData is the rdata of a PTR record.
type PTRRData struct {
	// DName is the target domain name, trailing dot included.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	DName string `json:"dname"`
}

// SRVRData is the rdata of an SRV record.
type SRVRData struct {
	// Priority of the target host.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	Priority int32 `json:"priority"`

	// Weight for records with the same priority.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=0
	// +optional
	Weight int32 `json:"weight,omitempty"`

	// Port on the target host.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	// Target host, trailing dot included.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Target string `json:"target"`
}

// MXRData is the rdata of an MX record.
type MXRData struct {
	// Preference of this exchange.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	Preference int32 `json:"preference"`

	// Exchange host, trailing dot included.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Exchange string `json:"exchange"`
}

// NSRData is the rdata of an NS record.
type NSRData struct {
	// DName is the nameserver name, trailing dot included.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	DName string `json:"dname"`
}

// CAARData is the rdata of a CAA record.
type CAARData struct {
	// Flags byte.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=255
	// +kubebuilder:default=0
	// +optional
	Flags int32 `json:"flags,omitempty"`

	// Tag, for example "issue", "issuewild" or "iodef".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Tag string `json:"tag"`

	// Value for the tag.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Value string `json:"value"`
}

// RecordRData holds exactly one typed rdata block, matching spec.type.
// +kubebuilder:validation:XValidation:rule="[has(self.a), has(self.aaaa), has(self.cname), has(self.txt), has(self.ptr), has(self.srv), has(self.mx), has(self.ns), has(self.caa)].filter(x, x).size() == 1",message="exactly one rdata block must be set"
type RecordRData struct {
	// A rdata.
	// +optional
	A *ARData `json:"a,omitempty"`
	// AAAA rdata.
	// +optional
	AAAA *AAAARData `json:"aaaa,omitempty"`
	// CNAME rdata.
	// +optional
	CNAME *CNAMERData `json:"cname,omitempty"`
	// TXT rdata.
	// +optional
	TXT *TXTRData `json:"txt,omitempty"`
	// PTR rdata.
	// +optional
	PTR *PTRRData `json:"ptr,omitempty"`
	// SRV rdata.
	// +optional
	SRV *SRVRData `json:"srv,omitempty"`
	// MX rdata.
	// +optional
	MX *MXRData `json:"mx,omitempty"`
	// NS rdata.
	// +optional
	NS *NSRData `json:"ns,omitempty"`
	// CAA rdata.
	// +optional
	CAA *CAARData `json:"caa,omitempty"`
}

// RecordSpec describes a Universal DDI DNS resource record. Universal DDI stores one
// rdata per record object, so two A records for the same name are two CRs.
// Natural key for adoption: zone plus name plus type plus rdata.
// +kubebuilder:validation:XValidation:rule="(self.type == 'A' && has(self.rdata.a)) || (self.type == 'AAAA' && has(self.rdata.aaaa)) || (self.type == 'CNAME' && has(self.rdata.cname)) || (self.type == 'TXT' && has(self.rdata.txt)) || (self.type == 'PTR' && has(self.rdata.ptr)) || (self.type == 'SRV' && has(self.rdata.srv)) || (self.type == 'MX' && has(self.rdata.mx)) || (self.type == 'NS' && has(self.rdata.ns)) || (self.type == 'CAA' && has(self.rdata.caa))",message="rdata must contain the block matching spec.type"
// +kubebuilder:validation:XValidation:rule="!has(self.createPtr) || !self.createPtr || self.type == 'A' || self.type == 'AAAA'",message="createPtr is only valid for A and AAAA records"
type RecordSpec struct {
	CommonSpec `json:",inline"`

	// ZoneRef names the AuthZone CR or a Universal DDI auth zone id. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="zoneRef is immutable"
	ZoneRef ObjectRef `json:"zoneRef"`

	// Name of the record inside the zone. "@" or the empty string means the apex.
	// Immutable.
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:XValidation:rule="self == oldSelf.orValue(self)",message="name is immutable",optionalOldSelf=true
	// +optional
	Name string `json:"name,omitempty"`

	// Type of the resource record. Immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="type is immutable"
	Type RecordType `json:"type"`

	// RData holds exactly one block, matching type.
	RData RecordRData `json:"rdata"`

	// TTL in seconds. Omitted means inherit from the zone.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2147483647
	// +optional
	TTL *int32 `json:"ttl,omitempty"`

	// CreatePtr asks the API to create the matching PTR record. A and AAAA only.
	// +optional
	CreatePtr *bool `json:"createPtr,omitempty"`

	// Comment stored on the Universal DDI object.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Comment string `json:"comment,omitempty"`

	// Disabled turns the record off in Universal DDI.
	// +optional
	Disabled bool `json:"disabled,omitempty"`
}

// RecordStatus reports the observed state of the record.
type RecordStatus struct {
	ObjectStatus `json:",inline"`

	// AbsoluteName is the fully qualified name observed in Universal DDI.
	// +optional
	AbsoluteName string `json:"absoluteName,omitempty"`

	// Source reported by Universal DDI. Only STATIC records are adopted, updated or deleted.
	// +listType=atomic
	// +optional
	Source []string `json:"source,omitempty"`

	// Zone is the Universal DDI auth zone id the record lives in.
	// +optional
	Zone string `json:"zone,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=uddirecord
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.status.absoluteName`
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Record is a Universal DDI DNS resource record.
type Record struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RecordSpec   `json:"spec,omitempty"`
	Status RecordStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RecordList contains a list of Record.
type RecordList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Record `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Record{}, &RecordList{})
}
