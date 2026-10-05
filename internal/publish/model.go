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

// Package publish holds the pure logic behind DNSPublishPolicy: turning Routes, Ingresses
// and LoadBalancer Services into endpoints, matching hostnames to zones, and planning the
// Record CRs a policy should own. Nothing in this package talks to Kubernetes or to
// Universal DDI; the controller in internal/controller feeds it objects and applies the
// result. This file is the shared contract between the source extractors and the planner.
package publish

import (
	"fmt"

	"k8s.io/apimachinery/pkg/types"
)

// SourceKind names a kind of Kubernetes object a policy can publish.
type SourceKind string

const (
	SourceRoute   SourceKind = "Route"
	SourceIngress SourceKind = "Ingress"
	SourceService SourceKind = "Service"
	// Milestone 7 sources; see DESIGN.md 4.7.
	SourceNode      SourceKind = "Node"
	SourceDNSRecord SourceKind = "DNSRecord"
	SourceHTTPRoute SourceKind = "HTTPRoute"
)

// SourceRef identifies the object an endpoint or skip came from.
type SourceRef struct {
	Kind      SourceKind
	Namespace string
	Name      string
	UID       types.UID
}

// String renders the reference the way status.skipped and events show it: "Kind/name".
func (s SourceRef) String() string { return fmt.Sprintf("%s/%s", s.Kind, s.Name) }

// TargetType is the DNS record type a target produces.
type TargetType string

const (
	TargetA     TargetType = "A"
	TargetAAAA  TargetType = "AAAA"
	TargetCNAME TargetType = "CNAME"
)

// Target is one address or name a hostname should point at.
type Target struct {
	Type  TargetType
	Value string // dotted IPv4, IPv6, or a canonical FQDN with a trailing dot
}

// Endpoint is one hostname a source wants published, with its resolved targets.
type Endpoint struct {
	Source   SourceRef
	Hostname string   // lower-case, with a trailing dot
	Targets  []Target // never empty for a returned endpoint; empty targets become a Skip
	TTL      *int32   // from the source's ttl annotation; nil means the policy default
}

// TargetMode is DNSPublishPolicy.spec.target.mode.
type TargetMode string

const (
	// TargetModeAuto picks CNAME for Routes and for load balancers that expose a hostname,
	// and A/AAAA for load balancers that expose IPs or when static addresses are given.
	TargetModeAuto TargetMode = "Auto"
	// TargetModeCNAME keeps only CNAME targets.
	TargetModeCNAME TargetMode = "CNAME"
	// TargetModeA keeps only A and AAAA targets.
	TargetModeA TargetMode = "A"
)

// SourceOptions is the part of a policy the source extractors need.
type SourceOptions struct {
	Mode              TargetMode
	StaticAddresses   []string // spec.target.addresses
	CanonicalHostname string   // spec.target.canonicalHostname, overrides routerCanonicalHostname
	// AnnotationPrefix is the annotation domain, "uddi.infoblox.com" by default. The
	// recognised keys are <prefix>/publish, /hostname, /ttl and /target.
	AnnotationPrefix string
	// ExternalDNS also reads the external-dns hostname, ttl and target annotations as
	// fallbacks; the uddi.infoblox.com annotation wins when both are set.
	ExternalDNS bool
	// NodeAddressType selects the Node addresses the Node source publishes:
	// NodeAddressInternal (default when empty) or NodeAddressExternal.
	NodeAddressType string
}

// Node address types for SourceOptions.NodeAddressType; they equal the corev1 values.
const (
	NodeAddressInternal = "InternalIP"
	NodeAddressExternal = "ExternalIP"
)

// external-dns annotation keys read when SourceOptions.ExternalDNS is set.
const (
	ExternalDNSHostname = "external-dns.alpha.kubernetes.io/hostname"
	ExternalDNSTTL      = "external-dns.alpha.kubernetes.io/ttl"
	ExternalDNSTarget   = "external-dns.alpha.kubernetes.io/target"
)

// DefaultAnnotationPrefix is the annotation domain when SourceOptions leaves it empty.
const DefaultAnnotationPrefix = "uddi.infoblox.com"

// Prefix returns the effective annotation prefix.
func (o SourceOptions) Prefix() string {
	if o.AnnotationPrefix == "" {
		return DefaultAnnotationPrefix
	}
	return o.AnnotationPrefix
}

// SkipReason explains why a source or hostname was not published. The values are the
// reason strings shown in DNSPublishPolicy.status.skipped and in Skipped events.
type SkipReason string

const (
	SkipExcluded          SkipReason = "Excluded"          // publish annotation is "false"
	SkipNoMatchingZone    SkipReason = "NoMatchingZone"    // no zoneRef fqdn is a suffix of the hostname
	SkipZoneNotReady      SkipReason = "ZoneNotReady"      // the matching AuthZone CR has no status.id yet
	SkipNoTarget          SkipReason = "NoTarget"          // no target could be derived for the mode
	SkipNoHostname        SkipReason = "NoHostname"        // a Service without a hostname annotation
	SkipNotAdmitted       SkipReason = "NotAdmitted"       // a Route no router has admitted
	SkipNotLoadBalancer   SkipReason = "NotLoadBalancer"   // a Service that is not type LoadBalancer
	SkipApexCNAME         SkipReason = "ApexCNAME"         // the zone apex cannot carry a CNAME
	SkipUnsupported       SkipReason = "Unsupported"       // e.g. a Route using spec.subdomain without a host
	SkipInvalidAnnotation SkipReason = "InvalidAnnotation" // a malformed ttl/target/hostname annotation
)

// Skip records one hostname (or a whole source when Hostname is empty) that was not
// published and why.
type Skip struct {
	Source   SourceRef
	Hostname string
	Reason   SkipReason
	Message  string
}

// Zone is a zone as the planner sees it: an AuthZone CR, or a Universal DDI zone given by
// id in spec.zoneRefs.
type Zone struct {
	// Name is the AuthZone CR name, used for the generated Record's zoneRef. It is empty
	// for a zone given by id.
	Name string
	// ID is the Universal DDI zone id of a zone given by id; empty for an AuthZone CR. A
	// zone with an ID is always Ready, takes its FQDN from the spec, and the generated
	// Record's zoneRef is {id} instead of {name}.
	ID    string
	FQDN  string // canonical zone fqdn, lower-case with a trailing dot
	Ready bool   // the CR has a status.id (or the zone is given by id), so Records can reference it
}

// Annotation keys relative to the prefix.
const (
	AnnotationPublish  = "publish"
	AnnotationHostname = "hostname"
	AnnotationTTL      = "ttl"
	AnnotationTarget   = "target"
)

// Labels and annotations the planner stamps on generated Records.
const (
	LabelPolicy          = "uddi.infoblox.com/policy"
	LabelSourceKind      = "uddi.infoblox.com/source-kind"
	LabelSourceName      = "uddi.infoblox.com/source-name"
	AnnotationRecordHost = "uddi.infoblox.com/hostname"
	AnnotationRecordTgt  = "uddi.infoblox.com/target"
	// TagSource is the Universal DDI tag key naming the source object of a published record.
	TagSource = "uddi_operator_source"
	// FieldManager is the server-side apply field manager for generated Records.
	FieldManager = "uddi-operator/dnspublishpolicy"
)
