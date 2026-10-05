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

// This file implements DESIGN.md section 4.7 IPAllocationPolicy pipeline steps 1, 2, 5 and
// 6 for Services: turning a Service into the Claim the planner can act on (one slot per IP
// family), and building the JSON merge-patch bodies (and match check) the controller uses
// to write and clear a Service's assignment field. Annotation parsing mirrors
// internal/publish/sources.go.
package ipalloc

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// ParsePoolRef parses an allocate-from annotation value: "subnet/<name>", "range/<name>" or
// "addressblock/<name>". The kind is case-insensitive; the name must be a DNS-1123
// subdomain (the same rule Kubernetes applies to object names, since <name> names a pool CR
// in the policy's namespace). Anything else is an error.
func ParsePoolRef(s string) (PoolRef, error) {
	kind, name, ok := strings.Cut(s, "/")
	if !ok || name == "" {
		return PoolRef{}, fmt.Errorf("%q is not <kind>/<name>", s)
	}
	if errs := validation.IsDNS1123Subdomain(name); len(errs) != 0 {
		return PoolRef{}, fmt.Errorf("%q is not a valid pool name: %s", name, errs[0])
	}
	switch strings.ToLower(kind) {
	case "subnet":
		return PoolRef{Kind: PoolSubnet, Name: name}, nil
	case "range":
		return PoolRef{Kind: PoolRange, Name: name}, nil
	case "addressblock":
		return PoolRef{Kind: PoolAddressBlock, Name: name}, nil
	default:
		return PoolRef{}, fmt.Errorf("%q is not subnet, range or addressblock", kind)
	}
}

// ViewService reads a Service into the Claim the planner needs, applying the annotations
// at prefix (DefaultAnnotationPrefix when prefix is empty).
//
// The wanted IP families are spec.ipFamilies in order, only the first for SingleStack (or
// an unset spec.ipFamilyPolicy); each becomes one SlotRequest. A Service without
// spec.ipFamilies (never the case once the API server has defaulted it) gets no Slots,
// which the planner reads as one slot of any family, the milestone 3 shape.
//
// Annotations are validated in the fixed order allocate, allocate-from, address,
// hostname; the first invalid one stops further annotation parsing and is reported in
// Invalid, naming that annotation. The address annotation takes one IP per wanted family,
// comma-separated, each placed on the slot of its family; an address of a family the
// Service does not want, two of one family, or (without spec.ipFamilies) more than one
// address is invalid. svc.Annotations may be nil; reading from a nil map is safe in Go
// and never panics.
func ViewService(svc *corev1.Service, via AssignVia, prefix string) Claim {
	if prefix == "" {
		prefix = DefaultAnnotationPrefix
	}
	ref := TargetRef{Kind: TargetService, Namespace: svc.Namespace, Name: svc.Name, UID: svc.UID}
	families, familyPolicy := serviceFamilies(svc)
	c := Claim{
		Target:            ref,
		Eligible:          svc.Spec.Type == corev1.ServiceTypeLoadBalancer,
		FamilyPolicy:      familyPolicy,
		Hostname:          DefaultHostname(ref),
		CurrentAssignment: CurrentAssignmentValues(svc, via),
		Labels:            svc.Labels,
	}

	annotations := svc.Annotations

	optIn, invalid := parseOptIn(annotations, prefix)
	if invalid != "" {
		c.Invalid = invalid
		return c
	}
	c.OptIn = optIn

	if raw, ok := annotations[prefix+"/"+AnnotationAllocateFrom]; ok {
		pool, err := ParsePoolRef(raw)
		if err != nil {
			c.Invalid = invalidAnnotationMsg(prefix, AnnotationAllocateFrom, raw, err.Error())
			return c
		}
		c.Pool = &pool
	}

	var explicit []string
	if raw, ok := annotations[prefix+"/"+AnnotationAddress]; ok {
		addrs, reason := parseServiceAddresses(raw, families)
		if reason != "" {
			c.Invalid = invalidAnnotationMsg(prefix, AnnotationAddress, raw, reason)
			return c
		}
		explicit = addrs
	}

	if raw, ok := annotations[prefix+"/"+AnnotationHostname]; ok {
		h := strings.ToLower(strings.TrimSpace(raw))
		if errs := validation.IsDNS1123Subdomain(h); len(errs) != 0 {
			c.Invalid = invalidAnnotationMsg(prefix, AnnotationHostname, raw, "not a valid DNS name")
			return c
		}
		c.Hostname = h
	}

	c.Slots = serviceSlots(families, explicit)
	return c
}

// parseOptIn reads the allocate annotation: nil when absent, otherwise its value, or an
// InvalidAnnotation message when it is neither "true" nor "false".
func parseOptIn(annotations map[string]string, prefix string) (*bool, string) {
	raw, ok := annotations[prefix+"/"+AnnotationAllocate]
	if !ok {
		return nil, ""
	}
	switch strings.TrimSpace(raw) {
	case "true":
		b := true
		return &b, ""
	case "false":
		b := false
		return &b, ""
	default:
		return nil, invalidAnnotationMsg(prefix, AnnotationAllocate, raw, `must be "true" or "false"`)
	}
}

// serviceFamilies returns the IP families a Service wants, in order, and its family
// policy. SingleStack, and an unset policy, keep only the first family.
func serviceFamilies(svc *corev1.Service) ([]IPFamily, FamilyPolicy) {
	var policy FamilyPolicy
	if svc.Spec.IPFamilyPolicy != nil {
		policy = FamilyPolicy(*svc.Spec.IPFamilyPolicy)
	}
	var families []IPFamily
	for _, f := range svc.Spec.IPFamilies {
		switch IPFamily(f) {
		case IPv4, IPv6:
			families = append(families, IPFamily(f))
		}
	}
	if len(families) > 1 && (policy == SingleStack || policy == "") {
		families = families[:1]
	}
	return families, policy
}

// parseServiceAddresses validates the address annotation against the wanted families and
// returns the canonical addresses, or the reason it is invalid.
func parseServiceAddresses(raw string, families []IPFamily) ([]string, string) {
	const notAnIP = "not a valid IPv4 or IPv6 address"
	parts := SplitAssignment(raw)
	if len(parts) == 0 {
		return nil, notAnIP
	}
	if len(families) == 0 && len(parts) > 1 {
		return nil, "takes a single address on a Service without spec.ipFamilies"
	}
	seen := make(map[IPFamily]bool, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		addr, err := netip.ParseAddr(part)
		if err != nil {
			if len(parts) == 1 {
				return nil, notAnIP
			}
			return nil, fmt.Sprintf("%q is %s", part, notAnIP)
		}
		canonical := addr.String()
		family := FamilyOf(canonical)
		if seen[family] {
			return nil, fmt.Sprintf("has more than one %s address", family)
		}
		seen[family] = true
		if len(families) > 0 && !containsFamily(families, family) {
			return nil, fmt.Sprintf("%s is %s but the Service wants %s", canonical, family, joinFamilies(families))
		}
		out = append(out, canonical)
	}
	return out, ""
}

// serviceSlots builds one SlotRequest per wanted family, carrying the explicit address
// of that family. Without families it returns nil (one slot of any family), or a single
// family-agnostic slot carrying the one explicit address.
func serviceSlots(families []IPFamily, explicit []string) []SlotRequest {
	if len(families) == 0 {
		if len(explicit) == 0 {
			return nil
		}
		return []SlotRequest{{Address: explicit[0]}}
	}
	slots := make([]SlotRequest, 0, len(families))
	for _, f := range families {
		s := SlotRequest{Family: f}
		for _, a := range explicit {
			if FamilyOf(a) == f {
				s.Address = a
			}
		}
		slots = append(slots, s)
	}
	return slots
}

func containsFamily(families []IPFamily, f IPFamily) bool {
	for _, x := range families {
		if x == f {
			return true
		}
	}
	return false
}

func joinFamilies(families []IPFamily) string {
	parts := make([]string, 0, len(families))
	for _, f := range families {
		parts = append(parts, string(f))
	}
	return strings.Join(parts, ",")
}

func invalidAnnotationMsg(prefix, key, raw, reason string) string {
	return fmt.Sprintf("%s/%s: %q %s", prefix, key, raw, reason)
}

// SplitAssignment splits a comma-separated assignment value into its entries, trimmed of
// surrounding space, with empty entries dropped and the order kept; nil when s holds
// none.
func SplitAssignment(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// CurrentAssignment returns what a Service's assignment field currently holds: in
// LoadBalancerIP mode, spec.loadBalancerIP; otherwise (Annotation mode, and any other value
// of via.Mode) the trimmed value of the via.Key() annotation, or "" when unset. A
// comma-separated dual-stack list is returned whole; CurrentAssignmentValues splits it.
func CurrentAssignment(svc *corev1.Service, via AssignVia) string {
	if via.Mode == AssignModeLoadBalancerIP {
		return svc.Spec.LoadBalancerIP
	}
	return strings.TrimSpace(svc.Annotations[via.Key()])
}

// CurrentAssignmentValues returns CurrentAssignment split into its addresses (see
// SplitAssignment), the form Claim.CurrentAssignment and Unassign.Applies take.
func CurrentAssignmentValues(svc *corev1.Service, via AssignVia) []string {
	return SplitAssignment(CurrentAssignment(svc, via))
}

// AssignmentMatches reports whether a Service's current assignment (see CurrentAssignment)
// already holds exactly the addresses of value, in order, ignoring space around commas.
func AssignmentMatches(svc *corev1.Service, via AssignVia, value string) bool {
	return equalStrings(CurrentAssignmentValues(svc, via), SplitAssignment(value))
}

// annotationPatch and loadBalancerIPPatch are the two JSON merge-patch document shapes
// AssignmentPatch and ClearAssignmentPatch produce. A nil Annotations value marshals to
// the JSON null the merge patch needs to delete an annotation.
type annotationPatch struct {
	Metadata struct {
		Annotations map[string]*string `json:"annotations"`
	} `json:"metadata"`
}

type loadBalancerIPPatch struct {
	Spec struct {
		LoadBalancerIP string `json:"loadBalancerIP"`
	} `json:"spec"`
}

// AssignmentPatch builds the JSON merge-patch body that writes value into a Service's
// assignment field: the via.Key() annotation in Annotation mode, spec.loadBalancerIP in
// LoadBalancerIP mode. For a dual-stack claim pass Assignment.Value(), the slot addresses
// comma-joined. Apply it with client.RawPatch(types.MergePatchType, body).
func AssignmentPatch(via AssignVia, value string) []byte {
	return assignmentPatch(via, &value)
}

// ClearAssignmentPatch builds the JSON merge-patch body that clears a Service's assignment
// field: it removes the via.Key() annotation entirely in Annotation mode (JSON null), or
// sets spec.loadBalancerIP to "" in LoadBalancerIP mode.
func ClearAssignmentPatch(via AssignVia) []byte {
	return assignmentPatch(via, nil)
}

// assignmentPatch marshals the patch body for via. value == nil means "clear"; a non-nil
// value is the address to write. Marshaling either patch struct cannot fail (both hold only
// strings, a string pointer and a fixed key), so the error is safe to discard.
func assignmentPatch(via AssignVia, value *string) []byte {
	if via.Mode == AssignModeLoadBalancerIP {
		var doc loadBalancerIPPatch
		if value != nil {
			doc.Spec.LoadBalancerIP = *value
		}
		b, _ := json.Marshal(doc)
		return b
	}
	var doc annotationPatch
	doc.Metadata.Annotations = map[string]*string{via.Key(): value}
	b, _ := json.Marshal(doc)
	return b
}
