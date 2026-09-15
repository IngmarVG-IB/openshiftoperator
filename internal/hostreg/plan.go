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
// spaceRef, addressType, register mode/dhcpOptions, tags and lifecycle), the Node views
// already selected and annotation/MAC-resolved, the IPSpace CR state the policy's
// spaceRef names, and the registration objects this policy currently owns.
type PlanInput struct {
	Policy   *uddiv1alpha1.HostRegistrationPolicy
	Nodes    []NodeView
	Space    SpaceState
	Existing []RegistrationState
}

// Compute turns Node views, IPSpace state and existing registration objects into the
// Addresses/FixedAddresses a HostRegistrationPolicy should own, the objects to release,
// and the Nodes skipped along with why. See DESIGN.md section 4.7, pipeline steps 2 to 7.
//
// NoAddress, NoMAC, InvalidAnnotation and SpaceNotReady are all treated as transient,
// retryable conditions: a Node hitting one of them keeps whatever registration object it
// already has rather than having it released out from under it, mirroring how
// internal/ipalloc.Compute freezes a Service on InvalidAnnotation or PoolNotReady. The
// three conditions that do release an existing registration are exactly the ones
// DESIGN.md names: the Node disappeared from the input, it opted out, or its chosen kind
// changed.
//
//nolint:gocyclo // the loop is a single linear pipeline; splitting it hides the order.
func Compute(in PlanInput) Plan {
	var plan Plan

	nodeRefs := make(map[string]NodeRef, len(in.Nodes))

	existingByNode := make(map[string]map[RegistrationKind]RegistrationState, len(in.Existing))
	for _, e := range in.Existing {
		byKind := existingByNode[e.Node]
		if byKind == nil {
			byKind = make(map[RegistrationKind]RegistrationState, 1)
			existingByNode[e.Node] = byKind
		}
		byKind[e.Kind] = e
	}

	// wanted tracks every Node name whose existing registration must survive this
	// round: it is in the input and did not opt out.
	wanted := make(map[string]bool, len(in.Nodes))

	// The CRD's RegisterMode (api/v1alpha1) and the planner's own RegisterMode share
	// their string values but are distinct types, converted here at the one place the
	// planner reads the policy's spec.
	mode := RegisterMode(in.Policy.Spec.Register.Mode)

	for _, v := range in.Nodes {
		nodeRefs[v.Ref.Name] = v.Ref

		if v.OptOut {
			plan.Skips = append(plan.Skips, Skip{Node: v.Ref, Reason: SkipExcluded, Message: `register annotation is "false"`})
			continue
		}
		wanted[v.Ref.Name] = true

		if v.Invalid != "" {
			plan.Skips = append(plan.Skips, Skip{Node: v.Ref, Reason: SkipInvalidAnnotation, Message: v.Invalid})
			continue
		}

		if v.Address == "" {
			plan.Skips = append(plan.Skips, Skip{
				Node:    v.Ref,
				Reason:  SkipNoAddress,
				Message: fmt.Sprintf("node has no %s address", in.Policy.Spec.AddressType),
			})
			continue
		}

		kind, ok := ChooseKind(mode, v.MAC)
		if !ok {
			plan.Skips = append(plan.Skips, Skip{
				Node:    v.Ref,
				Reason:  SkipNoMAC,
				Message: fmt.Sprintf("mode %s needs a MAC; no annotation and no NodeNetworkState", mode),
			})
			continue
		}

		if !in.Space.Ready {
			plan.Skips = append(plan.Skips, Skip{
				Node:    v.Ref,
				Reason:  SkipSpaceNotReady,
				Message: fmt.Sprintf("space %s has no status.id yet", in.Space.Name),
			})
			continue
		}

		plan.Ensure = append(plan.Ensure, buildDesired(in.Policy, v, kind))

		for exKind, ex := range existingByNode[v.Ref.Name] {
			if exKind == kind {
				continue
			}
			plan.Release = append(plan.Release, Release{Name: ex.Name, Kind: ex.Kind, Node: v.Ref})
		}

		if ex, ok := existingByNode[v.Ref.Name][kind]; ok {
			if ex.Ready {
				plan.Registered++
			} else {
				plan.Pending++
				plan.Skips = append(plan.Skips, Skip{
					Node:    v.Ref,
					Reason:  SkipRegistrationPending,
					Message: fmt.Sprintf("%s: %s", ex.Reason, ex.Message),
				})
			}
		}
	}

	for node, byKind := range existingByNode {
		if wanted[node] {
			continue
		}
		ref, ok := nodeRefs[node]
		if !ok {
			ref = NodeRef{Name: node}
		}
		for _, ex := range byKind {
			plan.Release = append(plan.Release, Release{Name: ex.Name, Kind: ex.Kind, Node: ref})
		}
	}

	sort.Slice(plan.Ensure, func(i, j int) bool { return plan.Ensure[i].Node.Name < plan.Ensure[j].Node.Name })
	sort.Slice(plan.Release, func(i, j int) bool {
		if plan.Release[i].Node.Name != plan.Release[j].Node.Name {
			return plan.Release[i].Node.Name < plan.Release[j].Node.Name
		}
		return plan.Release[i].Name < plan.Release[j].Name
	})
	sort.Slice(plan.Skips, func(i, j int) bool {
		if plan.Skips[i].Node.Name != plan.Skips[j].Node.Name {
			return plan.Skips[i].Node.Name < plan.Skips[j].Node.Name
		}
		return plan.Skips[i].Reason < plan.Skips[j].Reason
	})

	return plan
}

// buildDesired assembles the DesiredRegistration for one Node view already known to
// want a registration of kind.
func buildDesired(p *uddiv1alpha1.HostRegistrationPolicy, v NodeView, kind RegistrationKind) DesiredRegistration {
	tags := make(map[string]string, len(p.Spec.Tags)+1)
	for k, val := range p.Spec.Tags {
		tags[k] = val
	}
	tags[TagSource] = v.Ref.String()

	comment := fmt.Sprintf("Registered by HostRegistrationPolicy %s/%s for Node %s", p.Namespace, p.Name, v.Ref.Name)

	d := DesiredRegistration{
		Name: RegistrationName(p.Name, v.Ref.Name),
		Kind: kind,
		Node: v.Ref,
		Labels: map[string]string{
			LabelPolicy: p.Name,
			LabelNode:   v.Ref.Name,
		},
	}

	switch kind {
	case KindFixedAddress:
		d.FixedAddressSpec = &uddiv1alpha1.FixedAddressSpec{
			CommonSpec: uddiv1alpha1.CommonSpec{
				ConnectionRef: p.Spec.ConnectionRef,
				Tags:          tags,
				Lifecycle:     p.Spec.Lifecycle,
			},
			SpaceRef:    p.Spec.SpaceRef,
			Address:     v.Address,
			MatchType:   "mac",
			MatchValue:  v.MAC,
			Name:        v.Hostname,
			Hostname:    v.Hostname,
			Comment:     comment,
			DHCPOptions: p.Spec.Register.DHCPOptions,
		}
	case KindAddress:
		spec := &uddiv1alpha1.AddressSpec{
			CommonSpec: uddiv1alpha1.CommonSpec{
				ConnectionRef: p.Spec.ConnectionRef,
				Tags:          tags,
				Lifecycle:     p.Spec.Lifecycle,
			},
			SpaceRef: p.Spec.SpaceRef,
			Address:  v.Address,
			Names:    []uddiv1alpha1.AddressName{{Name: v.Hostname, Type: "user"}},
			Comment:  comment,
		}
		if v.MAC != "" {
			spec.HWAddr = v.MAC
		}
		d.AddressSpec = spec
	}

	return d
}
