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

// Package hostreg holds the pure logic behind HostRegistrationPolicy: reading what a Node
// looks like (address, MAC, hostname), deciding whether it becomes a DHCP FixedAddress or a
// named IPAM Address, and planning which generated objects should exist. Nothing here
// talks to Kubernetes or Universal DDI; the controller in internal/controller feeds it
// objects and applies the plan. This file is the shared contract between the Node view,
// the NMState reader, the planner and the controller.
package hostreg

import (
	"fmt"
	"net/netip"

	"k8s.io/apimachinery/pkg/types"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

// NodeRef identifies the Node a decision is about.
type NodeRef struct {
	Name string
	UID  types.UID
}

// String renders the reference the way status.skipped and events show it: "Node/name".
func (n NodeRef) String() string { return fmt.Sprintf("Node/%s", n.Name) }

// AddressType selects which Node address is registered.
type AddressType string

const (
	AddressTypeInternal AddressType = "InternalIP"
	AddressTypeExternal AddressType = "ExternalIP"
)

// RegisterMode is HostRegistrationPolicy.spec.register.mode.
type RegisterMode string

const (
	// RegisterModeAuto registers a FixedAddress when a MAC is known and an Address otherwise.
	RegisterModeAuto RegisterMode = "Auto"
	// RegisterModeAddress always registers a named IPAM Address (hwaddr set when known).
	RegisterModeAddress RegisterMode = "Address"
	// RegisterModeFixedAddress always registers a DHCP FixedAddress and needs a MAC.
	RegisterModeFixedAddress RegisterMode = "FixedAddress"
)

// RegistrationKind is the object-layer kind a Node is registered as.
type RegistrationKind string

const (
	KindAddress      RegistrationKind = "Address"
	KindFixedAddress RegistrationKind = "FixedAddress"
)

// DefaultAnnotationPrefix is the domain of the Node annotations the policy reads.
const DefaultAnnotationPrefix = "uddi.infoblox.com"

// Node annotation keys relative to the prefix.
const (
	AnnotationRegister = "register" // "false" opts the Node out
	AnnotationHostname = "hostname" // the name registered in Universal DDI; default is the Node name
	AnnotationAddress  = "address"  // the address to register instead of the Node's status address
	AnnotationMAC      = "mac"      // the Node's MAC when no other source knows it
)

// DefaultMACAnnotation is the fully qualified default key of spec.macSource.nodeAnnotation.
const DefaultMACAnnotation = DefaultAnnotationPrefix + "/" + AnnotationMAC

// Labels, tags and the field manager stamped on generated objects.
const (
	LabelPolicy = "uddi.infoblox.com/policy"
	LabelNode   = "uddi.infoblox.com/node"
	// LabelInterface carries the additionalInterfaces entry name on the registration
	// generated for it. Primary registrations (one per Node) do not carry it.
	LabelInterface = "uddi.infoblox.com/interface"
	// TagSource is the Universal DDI tag key naming the Node an object was registered for.
	TagSource = "uddi_operator_source"
	// FieldManager is the server-side apply field manager for generated objects.
	FieldManager = "uddi-operator/hostregistrationpolicy"
)

// NodeView.MACFrom values, in the order the MAC chain consults the sources.
const (
	MACFromAnnotation = "annotation"
	MACFromNMState    = "nmstate"
	MACFromMetal3     = "metal3"
)

// DefaultMetal3Namespace is the namespace BareMetalHosts are read from when
// spec.macSource.metal3.namespace is empty.
const DefaultMetal3Namespace = uddiv1alpha1.DefaultMetal3Namespace

// MACSource is the resolved spec.macSource.
type MACSource struct {
	// NodeAnnotation is the fully qualified annotation key holding the MAC; empty means
	// DefaultMACAnnotation.
	NodeAnnotation string
	// NMState enables reading NodeNetworkState; NMStateAvailable says the API exists on
	// the cluster (the controller checks once with discovery).
	NMState          bool
	NMStateAvailable bool
	// NMStateInterface names the interface to take the MAC from; empty means the interface
	// carrying the registered address.
	NMStateInterface string
	// Metal3 enables reading Metal3 BareMetalHosts; Metal3Available says the
	// metal3.io/v1alpha1 API exists on the cluster (the controller checks once with
	// discovery). Metal3Namespace is where the controller lists BareMetalHosts from; empty
	// means DefaultMetal3Namespace.
	Metal3          bool
	Metal3Available bool
	Metal3Namespace string
}

// Key returns the effective MAC annotation key.
func (m MACSource) Key() string {
	if m.NodeAnnotation == "" {
		return DefaultMACAnnotation
	}
	return m.NodeAnnotation
}

// UseNMState reports whether NodeNetworkState is a MAC source: enabled in the spec and
// served by the cluster.
func (m MACSource) UseNMState() bool { return m.NMState && m.NMStateAvailable }

// UseMetal3 reports whether Metal3 BareMetalHosts are a MAC source: enabled in the spec and
// served by the cluster.
func (m MACSource) UseMetal3() bool { return m.Metal3 && m.Metal3Available }

// Metal3NamespaceOrDefault returns the namespace to list BareMetalHosts from.
func (m MACSource) Metal3NamespaceOrDefault() string {
	if m.Metal3Namespace == "" {
		return DefaultMetal3Namespace
	}
	return m.Metal3Namespace
}

// SkipReason explains why a Node was not (or not yet) registered. The values are the
// reason strings shown in HostRegistrationPolicy.status.skipped and Skipped events.
type SkipReason string

const (
	SkipExcluded            SkipReason = "Excluded"            // register annotation is "false"
	SkipNoAddress           SkipReason = "NoAddress"           // the Node has no address of the requested type
	SkipNoMAC               SkipReason = "NoMAC"               // mode FixedAddress and no MAC from any source
	SkipInvalidAnnotation   SkipReason = "InvalidAnnotation"   // a malformed hostname / address / mac value
	SkipSpaceNotReady       SkipReason = "SpaceNotReady"       // the IPSpace CR is missing or has no status.id
	SkipRegistrationPending SkipReason = "RegistrationPending" // the generated object exists but is not Ready yet
	// SkipInvalidInterface means an additionalInterfaces entry cannot be resolved for the
	// Node: its cidr does not parse, or inserting its hostnameSuffix does not give a valid
	// DNS name.
	SkipInvalidInterface SkipReason = "InvalidInterface"
)

// Skip records one Node (or one additional interface of a Node) that was not registered and
// why.
type Skip struct {
	Node NodeRef
	// Interface names the additionalInterfaces entry the skip is about; empty for the
	// Node's primary registration. Messages of an interface skip start with
	// "interface <name>: ".
	Interface string
	Reason    SkipReason
	Message   string
}

// NodeView is everything the planner needs to know about one Node. It is built by
// ViewNode from a corev1.Node, then completed with a MAC by ResolveMAC (annotation,
// NodeNetworkState, Metal3) and, when the policy has additionalInterfaces, with
// Interfaces by ResolveInterfaces.
type NodeView struct {
	Ref NodeRef
	// OptOut is true when the register annotation is "false".
	OptOut bool
	// Address is the address to register (from status.addresses or the annotation).
	Address string
	// MAC is the normalised lower-case colon-form MAC, empty when unknown.
	MAC string
	// MACFrom says where the MAC came from: MACFromAnnotation, MACFromNMState,
	// MACFromMetal3 or "".
	MACFrom string
	// Hostname is the annotation value or the Node name.
	Hostname string
	// Invalid carries the message of an InvalidAnnotation skip; empty when the view is valid.
	Invalid string
	// Interfaces holds one resolved entry per spec.additionalInterfaces item, built by
	// ResolveInterfaces. The planner looks entries up by name; a spec entry with no
	// InterfaceView here is treated as having no matching address.
	Interfaces []InterfaceView
}

// InterfaceView is one spec.additionalInterfaces entry resolved against one Node.
type InterfaceView struct {
	// Name is the entry's name.
	Name string
	// Address is the first IP inside the entry's cidr, in canonical text form; empty when
	// no Node address or NodeNetworkState interface address matched.
	Address string
	// MAC is the normalised MAC of the NodeNetworkState interface or Metal3 NIC carrying
	// Address, empty when neither knows it.
	MAC string
	// MACFrom is MACFromNMState, MACFromMetal3 or "".
	MACFrom string
	// Hostname is the Node's registered hostname with the entry's hostnameSuffix inserted
	// after the first label. Empty when Invalid is set.
	Hostname string
	// Invalid carries the message of an InvalidInterface skip (an unparsable cidr or a
	// suffix that makes the hostname invalid); empty when the entry resolved.
	Invalid string
}

// Metal3Hardware answers the two questions the MAC chain asks of Metal3 inventory.
// internal/metal3api implements it over the BareMetalHosts the controller lists; it is an
// interface here because metal3api imports this package (for NormalizeMAC).
type Metal3Hardware interface {
	// MACForNode returns the MAC for a Node: the NIC carrying address, else the
	// bootMACAddress of the host whose inspected hostname is nodeName. host is the
	// BareMetalHost name.
	MACForNode(address, nodeName string) (mac, host string, ok bool)
	// MACForIP returns the MAC of the NIC carrying ip. host is the BareMetalHost name.
	MACForIP(ip netip.Addr) (mac, host string, ok bool)
}

// SpaceState is the IPSpace CR as the planner sees it.
type SpaceState struct {
	Name  string
	Ready bool // the CR exists and has a status.id
}

// RegistrationState is an existing generated object as the planner sees it.
type RegistrationState struct {
	Name string
	Kind RegistrationKind
	Node string // from the LabelNode label
	// Interface is the LabelInterface label; empty for a Node's primary registration.
	Interface string
	Ready     bool
	// Reason and Message come from the object's Ready condition when it is not Ready.
	Reason  string
	Message string
}

// DesiredRegistration is one generated object the policy wants to exist. Exactly one of
// AddressSpec or FixedAddressSpec is set, matching Kind.
type DesiredRegistration struct {
	Name string
	Kind RegistrationKind
	Node NodeRef
	// Interface is the additionalInterfaces entry name; empty for the primary registration.
	Interface        string
	Labels           map[string]string
	Annotations      map[string]string
	AddressSpec      *uddiv1alpha1.AddressSpec
	FixedAddressSpec *uddiv1alpha1.FixedAddressSpec
}

// Release deletes a generated object that should no longer exist (Node gone, opted out,
// no longer selected, its kind changed, or its interface removed from the spec).
type Release struct {
	Name string
	Kind RegistrationKind
	Node NodeRef
	// Interface is the additionalInterfaces entry name; empty for a primary registration.
	Interface string
}

// Plan is the planner's output for one reconcile.
type Plan struct {
	Ensure     []DesiredRegistration
	Release    []Release
	Skips      []Skip
	Registered int32
	Pending    int32
}
