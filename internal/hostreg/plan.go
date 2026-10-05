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

package hostreg

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

// maxNameLength is the Kubernetes object name length limit RegistrationName must respect.
const maxNameLength = 253

// nameHashLength is how many hex characters of the sha256 digest RegistrationName keeps
// when the plain "<policy>-<node>" name would be too long.
const nameHashLength = 10

// RegistrationName derives the deterministic name of the object generated for one Node:
// "<policy>-<node>". When that would exceed the Kubernetes 253 character name limit, the
// node part is replaced with the first 10 hex characters of sha256(node) so the name
// stays short and deterministic.
func RegistrationName(policy, node string) string {
	name := policy + "-" + node
	if len(name) <= maxNameLength {
		return name
	}
	sum := sha256.Sum256([]byte(node))
	return policy + "-" + hex.EncodeToString(sum[:])[:nameHashLength]
}

// ChooseKind decides which kind of object represents a Node under mode: Address mode
// always registers an Address; FixedAddress mode needs a MAC (the bool result is false
// when mac is empty); Auto picks FixedAddress when a MAC is known and Address otherwise,
// so it always succeeds.
func ChooseKind(mode RegisterMode, mac string) (RegistrationKind, bool) {
	switch mode {
	case RegisterModeAddress:
		return KindAddress, true
	case RegisterModeFixedAddress:
		return KindFixedAddress, mac != ""
	default: // RegisterModeAuto (and the zero value, which CRD defaulting never leaves set)
		if mac != "" {
			return KindFixedAddress, true
		}
		return KindAddress, true
	}
}

// PlanInput is everything Compute needs: the policy (for its metadata, connectionRef,
// spaceRef, addressType, register mode/dhcpOptions, additionalInterfaces, tags and
// lifecycle), the Node views already selected and annotation/MAC-resolved (and
// interface-resolved, see ResolveInterfaces), the IPSpace CR state the policy's spaceRef
// names, and the registration objects this policy currently owns.
type PlanInput struct {
	Policy *uddiv1alpha1.HostRegistrationPolicy
	Nodes  []NodeView
	Space  SpaceState
	// Spaces holds the state of the other IPSpace CRs that additionalInterfaces entries
	// name in their own spaceRef, keyed by name (AdditionalSpaceNames lists them). A name
	// with no entry counts as not ready. It may be nil when no entry names a space of its
	// own; entries without a spaceRef use Space.
	Spaces   map[string]SpaceState
	Existing []RegistrationState
}

// slot identifies one registration a policy may own for a Node: its primary registration
// (iface empty) or the one generated for an additionalInterfaces entry.
type slot struct {
	node  string
	iface string
}

// planner holds the state Compute threads through its helpers.
type planner struct {
	in   PlanInput
	mode RegisterMode
	// existing indexes the policy's current objects by slot, then by kind, so a kind change
	// can release the object of the other kind.
	existing map[slot]map[RegistrationKind]RegistrationState
	plan     Plan
}

// Compute turns Node views, IPSpace state and existing registration objects into the
// Addresses/FixedAddresses a HostRegistrationPolicy should own, the objects to release,
// and the Nodes skipped along with why. See DESIGN.md section 4.7, pipeline steps 2 to 7
// and the milestone 7 extensions.
//
// Each Node gets a primary registration "<policy>-<node>" and, for every
// spec.additionalInterfaces entry, one more "<policy>-<node>-<interface>" labelled with
// LabelInterface. Interface registrations use the same register mode as the primary one
// but resolve their own address, MAC and hostname (NodeView.Interfaces), and are planned
// independently: a Node without an InternalIP still gets its interface registrations.
//
// NoAddress, NoMAC, InvalidAnnotation, InvalidInterface and SpaceNotReady are all treated
// as transient, retryable conditions: a slot hitting one of them keeps whatever
// registration object it already has rather than having it released out from under it,
// mirroring how internal/ipalloc.Compute freezes a Service on InvalidAnnotation or
// PoolNotReady. The conditions that do release an existing registration are the Node
// disappearing from the input, opting out, its chosen kind changing, and (for an interface
// registration) the interface being removed from the spec.
func Compute(in PlanInput) Plan {
	p := &planner{
		in:       in,
		mode:     RegisterMode(in.Policy.Spec.Register.Mode),
		existing: make(map[slot]map[RegistrationKind]RegistrationState, len(in.Existing)),
	}
	for _, e := range in.Existing {
		s := slot{node: e.Node, iface: e.Interface}
		byKind := p.existing[s]
		if byKind == nil {
			byKind = make(map[RegistrationKind]RegistrationState, 1)
			p.existing[s] = byKind
		}
		byKind[e.Kind] = e
	}

	nodeRefs := make(map[string]NodeRef, len(in.Nodes))
	// wanted tracks every Node name whose existing registrations must survive this
	// round: it is in the input and did not opt out.
	wanted := make(map[string]bool, len(in.Nodes))

	for _, v := range in.Nodes {
		nodeRefs[v.Ref.Name] = v.Ref

		if v.OptOut {
			p.plan.Skips = append(p.plan.Skips, Skip{Node: v.Ref, Reason: SkipExcluded, Message: `register annotation is "false"`})
			continue
		}
		wanted[v.Ref.Name] = true

		if v.Invalid != "" {
			p.plan.Skips = append(p.plan.Skips, Skip{Node: v.Ref, Reason: SkipInvalidAnnotation, Message: v.Invalid})
			continue
		}

		p.planPrimary(v)
		for _, entry := range in.Policy.Spec.AdditionalInterfaces {
			p.planInterface(v, entry)
		}
	}

	p.releaseStale(nodeRefs, wanted)
	p.sort()
	return p.plan
}

// planPrimary plans the Node's own registration, in the order the pipeline checks it:
// address, kind, space.
func (p *planner) planPrimary(v NodeView) {
	if v.Address == "" {
		p.skip(Skip{
			Node:    v.Ref,
			Reason:  SkipNoAddress,
			Message: fmt.Sprintf("node has no %s address", p.in.Policy.Spec.AddressType),
		})
		return
	}

	kind, ok := ChooseKind(p.mode, v.MAC)
	if !ok {
		p.skip(Skip{
			Node:    v.Ref,
			Reason:  SkipNoMAC,
			Message: fmt.Sprintf("mode %s needs a MAC; no annotation and no NodeNetworkState", p.mode),
		})
		return
	}

	if space := p.in.Space; !space.Ready {
		p.skip(Skip{
			Node:    v.Ref,
			Reason:  SkipSpaceNotReady,
			Message: fmt.Sprintf("space %s has no status.id yet", space.Name),
		})
		return
	}

	p.ensure(buildDesired(p.in.Policy, v.Ref, kind, registrationTarget{
		name:     RegistrationName(p.in.Policy.Name, v.Ref.Name),
		address:  v.Address,
		mac:      v.MAC,
		hostname: v.Hostname,
		space:    p.in.Policy.Spec.SpaceRef,
		comment:  fmt.Sprintf("Registered by HostRegistrationPolicy %s/%s for Node %s", p.in.Policy.Namespace, p.in.Policy.Name, v.Ref.Name),
	}), "")
}

// planInterface plans the registration of one additionalInterfaces entry of a Node. A spec
// entry the view has no resolution for counts as having no matching address.
func (p *planner) planInterface(v NodeView, entry uddiv1alpha1.AdditionalInterface) {
	iv := InterfaceView{Name: entry.Name}
	for _, candidate := range v.Interfaces {
		if candidate.Name == entry.Name {
			iv = candidate
			break
		}
	}

	if iv.Invalid != "" {
		p.skip(Skip{Node: v.Ref, Interface: entry.Name, Reason: SkipInvalidInterface, Message: interfaceMessage(entry.Name, iv.Invalid)})
		return
	}
	if iv.Address == "" {
		p.skip(Skip{
			Node:      v.Ref,
			Interface: entry.Name,
			Reason:    SkipNoAddress,
			Message:   interfaceMessage(entry.Name, fmt.Sprintf("no Node or NodeNetworkState address inside %s", entry.CIDR)),
		})
		return
	}

	kind, ok := ChooseKind(p.mode, iv.MAC)
	if !ok {
		p.skip(Skip{
			Node:      v.Ref,
			Interface: entry.Name,
			Reason:    SkipNoMAC,
			Message:   interfaceMessage(entry.Name, fmt.Sprintf("mode %s needs a MAC; no NodeNetworkState interface or Metal3 NIC carries %s", p.mode, iv.Address)),
		})
		return
	}

	spaceRef := interfaceSpaceRef(p.in.Policy, entry)
	if space := p.spaceState(spaceRef); !space.Ready {
		p.skip(Skip{
			Node:      v.Ref,
			Interface: entry.Name,
			Reason:    SkipSpaceNotReady,
			Message:   interfaceMessage(entry.Name, fmt.Sprintf("space %s has no status.id yet", space.Name)),
		})
		return
	}

	p.ensure(buildDesired(p.in.Policy, v.Ref, kind, registrationTarget{
		name:     InterfaceRegistrationName(p.in.Policy.Name, v.Ref.Name, entry.Name),
		iface:    entry.Name,
		address:  iv.Address,
		mac:      iv.MAC,
		hostname: iv.Hostname,
		space:    spaceRef,
		comment: fmt.Sprintf("Registered by HostRegistrationPolicy %s/%s for Node %s interface %s",
			p.in.Policy.Namespace, p.in.Policy.Name, v.Ref.Name, entry.Name),
	}), entry.Name)
}

// spaceState returns the planner's view of the IPSpace CR ref names: Space for the policy's
// own spaceRef, otherwise the entry in Spaces (not ready when absent).
func (p *planner) spaceState(ref uddiv1alpha1.ObjectRef) SpaceState {
	if ref.Name == p.in.Policy.Spec.SpaceRef.Name {
		return p.in.Space
	}
	if s, ok := p.in.Spaces[ref.Name]; ok {
		return s
	}
	return SpaceState{Name: ref.Name}
}

// skip records a skip.
func (p *planner) skip(s Skip) { p.plan.Skips = append(p.plan.Skips, s) }

// ensure adds d to the plan, releases the object of the other kind that the same slot
// already has (a kind change), and counts the slot as registered or pending when its
// object of the chosen kind already exists. iface is d's interface name, "" for a primary
// registration.
func (p *planner) ensure(d DesiredRegistration, iface string) {
	p.plan.Ensure = append(p.plan.Ensure, d)

	s := slot{node: d.Node.Name, iface: iface}
	for exKind, ex := range p.existing[s] {
		if exKind == d.Kind {
			continue
		}
		p.plan.Release = append(p.plan.Release, Release{Name: ex.Name, Kind: ex.Kind, Node: d.Node, Interface: iface})
	}

	ex, ok := p.existing[s][d.Kind]
	if !ok {
		return
	}
	if ex.Ready {
		p.plan.Registered++
		return
	}
	p.plan.Pending++
	msg := fmt.Sprintf("%s: %s", ex.Reason, ex.Message)
	if iface != "" {
		msg = interfaceMessage(iface, msg)
	}
	p.skip(Skip{Node: d.Node, Interface: iface, Reason: SkipRegistrationPending, Message: msg})
}

// releaseStale releases the existing objects no slot of this round keeps: those of a Node
// that is gone from the input or opted out, and interface registrations whose interface is
// no longer in the spec (for any Node still in the input, even one frozen on an invalid
// annotation: removing an interface from the spec is deliberate).
func (p *planner) releaseStale(nodeRefs map[string]NodeRef, wanted map[string]bool) {
	specIfaces := make(map[string]bool, len(p.in.Policy.Spec.AdditionalInterfaces))
	for _, e := range p.in.Policy.Spec.AdditionalInterfaces {
		specIfaces[e.Name] = true
	}

	for s, byKind := range p.existing {
		if wanted[s.node] && (s.iface == "" || specIfaces[s.iface]) {
			continue
		}
		ref, ok := nodeRefs[s.node]
		if !ok {
			ref = NodeRef{Name: s.node}
		}
		for _, ex := range byKind {
			p.plan.Release = append(p.plan.Release, Release{Name: ex.Name, Kind: ex.Kind, Node: ref, Interface: s.iface})
		}
	}
}

// sort puts the plan in a deterministic order.
func (p *planner) sort() {
	plan := &p.plan
	sort.Slice(plan.Ensure, func(i, j int) bool {
		if plan.Ensure[i].Node.Name != plan.Ensure[j].Node.Name {
			return plan.Ensure[i].Node.Name < plan.Ensure[j].Node.Name
		}
		return plan.Ensure[i].Name < plan.Ensure[j].Name
	})
	sort.Slice(plan.Release, func(i, j int) bool {
		if plan.Release[i].Node.Name != plan.Release[j].Node.Name {
			return plan.Release[i].Node.Name < plan.Release[j].Node.Name
		}
		return plan.Release[i].Name < plan.Release[j].Name
	})
	sort.Slice(plan.Skips, func(i, j int) bool {
		a, b := plan.Skips[i], plan.Skips[j]
		if a.Node.Name != b.Node.Name {
			return a.Node.Name < b.Node.Name
		}
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		if a.Interface != b.Interface {
			return a.Interface < b.Interface
		}
		return a.Message < b.Message
	})
}

// interfaceMessage prefixes msg with the interface it is about.
func interfaceMessage(iface, msg string) string {
	return fmt.Sprintf("interface %s: %s", iface, msg)
}

// registrationTarget is what one generated object registers: the primary registration of a
// Node or the one of an additional interface.
type registrationTarget struct {
	name     string
	iface    string // additionalInterfaces entry name; empty for the primary registration
	address  string
	mac      string
	hostname string
	space    uddiv1alpha1.ObjectRef
	comment  string
}

// buildDesired assembles the DesiredRegistration for one target of a Node already known to
// want a registration of kind.
func buildDesired(p *uddiv1alpha1.HostRegistrationPolicy, node NodeRef, kind RegistrationKind, t registrationTarget) DesiredRegistration {
	tags := make(map[string]string, len(p.Spec.Tags)+1)
	for k, val := range p.Spec.Tags {
		tags[k] = val
	}
	tags[TagSource] = node.String()

	d := DesiredRegistration{
		Name:      t.name,
		Kind:      kind,
		Node:      node,
		Interface: t.iface,
		Labels: map[string]string{
			LabelPolicy: p.Name,
			LabelNode:   node.Name,
		},
	}
	if t.iface != "" {
		d.Labels[LabelInterface] = t.iface
	}

	switch kind {
	case KindFixedAddress:
		d.FixedAddressSpec = &uddiv1alpha1.FixedAddressSpec{
			CommonSpec: uddiv1alpha1.CommonSpec{
				ConnectionRef: p.Spec.ConnectionRef,
				Tags:          tags,
				Lifecycle:     p.Spec.Lifecycle,
			},
			SpaceRef:    t.space,
			Address:     t.address,
			MatchType:   "mac",
			MatchValue:  t.mac,
			Name:        t.hostname,
			Hostname:    t.hostname,
			Comment:     t.comment,
			DHCPOptions: p.Spec.Register.DHCPOptions,
		}
	case KindAddress:
		spec := &uddiv1alpha1.AddressSpec{
			CommonSpec: uddiv1alpha1.CommonSpec{
				ConnectionRef: p.Spec.ConnectionRef,
				Tags:          tags,
				Lifecycle:     p.Spec.Lifecycle,
			},
			SpaceRef: t.space,
			Address:  t.address,
			Names:    []uddiv1alpha1.AddressName{{Name: t.hostname, Type: "user"}},
			Comment:  t.comment,
		}
		if t.mac != "" {
			spec.HWAddr = t.mac
		}
		d.AddressSpec = spec
	}

	return d
}
