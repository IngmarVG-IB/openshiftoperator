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

// Condition reasons for DNSPublishPolicy.status.conditions. See DESIGN.md section 4.7.
const (
	// ReasonPublished means every desired Record is Ready and no ZoneNotReady skip
	// remains.
	ReasonPublished = "Published"
	// ReasonRecordsNotReady means the desired Records exist but some are not Ready yet.
	ReasonRecordsNotReady = "RecordsNotReady"
	// ReasonZonesNotReady means no zoneRef resolves to a Ready AuthZone.
	ReasonZonesNotReady = "ZonesNotReady"
)

// ConditionZonesResolved is the second DNSPublishPolicy condition type. It is True when
// every zoneRef names an existing AuthZone CR, and False with reason ZoneNotFound and
// the missing names otherwise. It never changes Ready: a mistyped zoneRef next to
// working ones is reported without taking the policy out of service.
const ConditionZonesResolved = "ZonesResolved"

// Reasons for the ZonesResolved condition.
const (
	// ReasonZonesFound means every zoneRef names an existing AuthZone CR.
	ReasonZonesFound = "ZonesFound"
	// ReasonZoneNotFound means at least one zoneRef names no existing AuthZone CR. It is
	// also the reason of the Warning event emitted alongside.
	ReasonZoneNotFound = "ZoneNotFound"
)

// TargetMode selects how DNSPublishPolicy resolves the DNS record type for the Records
// it generates.
// +kubebuilder:validation:Enum=Auto;CNAME;A
type TargetMode string

const (
	// TargetModeAuto picks CNAME for Routes and for load balancers that expose a
	// hostname, and A/AAAA for load balancers that expose IPs or when target.addresses
	// is set.
	TargetModeAuto TargetMode = "Auto"
	// TargetModeCNAME keeps only CNAME targets.
	TargetModeCNAME TargetMode = "CNAME"
	// TargetModeA keeps only A and AAAA targets.
	TargetModeA TargetMode = "A"
)

// SourceKind names a kind of Kubernetes object DNSPublishPolicy can publish from.
// +kubebuilder:validation:Enum=Route;Ingress;Service;Node;DNSRecord;HTTPRoute
type SourceKind string

const (
	// SourceKindRoute publishes OpenShift Routes.
	SourceKindRoute SourceKind = "Route"
	// SourceKindIngress publishes Ingresses.
	SourceKindIngress SourceKind = "Ingress"
	// SourceKindService publishes Services of type LoadBalancer.
	SourceKindService SourceKind = "Service"
	// SourceKindNode publishes cluster-scoped Nodes: the Node name when it contains a dot,
	// plus the hostname annotation, pointing at the Node addresses of nodeAddressType.
	SourceKindNode SourceKind = "Node"
	// SourceKindDNSRecord publishes OpenShift ingress operator DNSRecords
	// (ingress.operator.openshift.io/v1) from dnsRecordNamespace, usually the default
	// router wildcard.
	SourceKindDNSRecord SourceKind = "DNSRecord"
	// SourceKindHTTPRoute publishes Gateway API HTTPRoutes (gateway.networking.k8s.io/v1).
	SourceKindHTTPRoute SourceKind = "HTTPRoute"
)

// NodeAddressType selects which Node addresses the Node source publishes.
// +kubebuilder:validation:Enum=InternalIP;ExternalIP
type NodeAddressType string

const (
	// NodeAddressTypeInternalIP publishes the Node's InternalIP addresses.
	NodeAddressTypeInternalIP NodeAddressType = "InternalIP"
	// NodeAddressTypeExternalIP publishes the Node's ExternalIP addresses.
	NodeAddressTypeExternalIP NodeAddressType = "ExternalIP"
)

// DefaultDNSRecordNamespace is the namespace the DNSRecord source reads when
// dnsRecordNamespace is unset: where the OpenShift ingress operator creates its records.
const DefaultDNSRecordNamespace = "openshift-ingress-operator"

// DNSPublishCompatibility switches on interoperability with other DNS publishers.
type DNSPublishCompatibility struct {
	// ExternalDNS also reads the external-dns.alpha.kubernetes.io/hostname, /ttl and
	// /target annotations as fallbacks on every source kind. The uddi.infoblox.com
	// annotation wins when both are set, so existing external-dns users can move without
	// relabelling.
	// +optional
	ExternalDNS bool `json:"externalDNS,omitempty"`
}

// DNSPublishTarget selects the DNS record type generated for hostnames a
// DNSPublishPolicy matches.
type DNSPublishTarget struct {
	// Mode selects CNAME, A/AAAA, or lets the source decide (Auto).
	// +kubebuilder:default=Auto
	// +optional
	Mode TargetMode `json:"mode,omitempty"`

	// Addresses are static A/AAAA targets: used by mode A, and by mode Auto for Routes.
	// Each entry must be a literal IPv4 or IPv6 address.
	// +kubebuilder:validation:items:Pattern=`^((25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)(\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)){3}|(([0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,7}:|([0-9a-fA-F]{1,4}:){1,6}:[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,5}(:[0-9a-fA-F]{1,4}){1,2}|([0-9a-fA-F]{1,4}:){1,4}(:[0-9a-fA-F]{1,4}){1,3}|([0-9a-fA-F]{1,4}:){1,3}(:[0-9a-fA-F]{1,4}){1,4}|([0-9a-fA-F]{1,4}:){1,2}(:[0-9a-fA-F]{1,4}){1,5}|[0-9a-fA-F]{1,4}:((:[0-9a-fA-F]{1,4}){1,6})|:((:[0-9a-fA-F]{1,4}){1,7}|:)))$`
	// +listType=atomic
	// +optional
	Addresses []string `json:"addresses,omitempty"`

	// CanonicalHostname overrides the admitted Route's routerCanonicalHostname.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	CanonicalHostname string `json:"canonicalHostname,omitempty"`
}

// DNSPublishPolicySpec describes which OpenShift objects a policy publishes to
// Universal DDI DNS. Selected source objects are turned into Records in the matching
// zoneRefs; see DESIGN.md section 4.7 for the full pipeline.
type DNSPublishPolicySpec struct {
	// ConnectionRef names the Connection used by every Record this policy generates.
	// Empty means the Connection called "default" in this namespace.
	// +optional
	ConnectionRef ConnectionRef `json:"connectionRef,omitempty"`

	// Sources selects which kinds of objects this policy publishes. Defaults to Route,
	// Ingress and Service; Service means type LoadBalancer. Node, DNSRecord and HTTPRoute
	// are opt-in.
	// +kubebuilder:default={Route,Ingress,Service}
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	// +optional
	Sources []SourceKind `json:"sources,omitempty"`

	// Selector selects the source objects in this namespace. Empty selects every
	// object of the configured source kinds.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// ZoneRefs are the AuthZone CRs in this namespace considered for the generated
	// Records. Only name is supported here; a Universal DDI zone id is not accepted.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:XValidation:rule="self.all(z, has(z.name) && z.name != '')",message="every zoneRefs entry must set name"
	// +listType=atomic
	ZoneRefs []ObjectRef `json:"zoneRefs"`

	// Target selects the DNS record type generated for matched hostnames.
	// +optional
	Target DNSPublishTarget `json:"target,omitempty"`

	// TTL is the default TTL in seconds for generated Records, used when a source does
	// not carry its own ttl override.
	// +kubebuilder:default=300
	// +kubebuilder:validation:Minimum=1
	// +optional
	TTL *int32 `json:"ttl,omitempty"`

	// Tags are merged into every generated Record's spec.tags.
	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// RecordLifecycle is copied onto every generated Record's spec.lifecycle.
	// +optional
	RecordLifecycle Lifecycle `json:"recordLifecycle,omitempty"`

	// CreatePtr sets createPtr on every A and AAAA Record this policy generates, so the
	// matching PTR record is created too. CNAME Records never carry it.
	// +optional
	CreatePtr bool `json:"createPtr,omitempty"`

	// Compatibility switches on interoperability with other DNS publishers.
	// +optional
	Compatibility DNSPublishCompatibility `json:"compatibility,omitempty"`

	// NodeAddressType selects which Node addresses the Node source publishes.
	// +kubebuilder:default=InternalIP
	// +optional
	NodeAddressType NodeAddressType `json:"nodeAddressType,omitempty"`

	// DNSRecordNamespace is the namespace the DNSRecord source reads, matched by selector.
	// Defaults to the namespace the OpenShift ingress operator creates its records in.
	// +kubebuilder:default=openshift-ingress-operator
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	DNSRecordNamespace string `json:"dnsRecordNamespace,omitempty"`
}

// SkippedSource is one hostname (or a whole source, when Hostname is empty) that a
// DNSPublishPolicy did not publish, and why.
type SkippedSource struct {
	// Source identifying the object, rendered as "Kind/name".
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Source string `json:"source,omitempty"`

	// Hostname that was skipped. Empty means the whole source was skipped.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Hostname string `json:"hostname,omitempty"`

	// Reason is a short machine-readable skip reason, for example NoMatchingZone.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message is a human-readable explanation.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	Message string `json:"message,omitempty"`
}

// DNSPublishPolicyStatus reports the observed state of a DNSPublishPolicy.
type DNSPublishPolicyStatus struct {
	// ObservedGeneration is the metadata.generation the status was last computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions hold Ready and ZonesResolved.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Published is the number of desired Records currently applied.
	// +optional
	Published int32 `json:"published,omitempty"`

	// Ready is, of those, the number reporting Ready=True.
	// +optional
	Ready int32 `json:"ready,omitempty"`

	// Skipped lists hostnames not published and why. Bounded and sorted by source then
	// hostname.
	// +kubebuilder:validation:MaxItems=50
	// +listType=atomic
	// +optional
	Skipped []SkippedSource `json:"skipped,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=uddipublish
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Published",type=integer,JSONPath=`.status.published`
// +kubebuilder:printcolumn:name="Ready-count",type=integer,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DNSPublishPolicy turns OpenShift Routes, Ingresses and LoadBalancer Services into
// Universal DDI DNS Records.
type DNSPublishPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DNSPublishPolicySpec   `json:"spec,omitempty"`
	Status DNSPublishPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DNSPublishPolicyList contains a list of DNSPublishPolicy.
type DNSPublishPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DNSPublishPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DNSPublishPolicy{}, &DNSPublishPolicyList{})
}
