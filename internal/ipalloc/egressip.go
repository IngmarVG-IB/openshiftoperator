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

// This file implements the EgressIP target of DESIGN.md section 4.7 (milestone 7): turning
// an OVN-Kubernetes EgressIP, read as unstructured data, into the Claim the planner acts
// on, and building the JSON merge patches that write and clear its spec.egressIPs.
package ipalloc

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/IngmarVG-IB/openshiftoperator/internal/egressip"
)

// ViewEgressIP reads an EgressIP (k8s.ovn.org/v1, cluster-scoped) into the Claim the
// planner needs, applying the annotations at prefix (DefaultAnnotationPrefix when prefix
// is empty): allocate (the planner requires "true" whatever requireAnnotation says),
// count (the number of slots, an integer from 1 to 16, default 1) and allocate-from (the
// pool every slot draws from instead of allocateFrom). The slots are family-agnostic.
// The current assignment is spec.egressIPs and the labels are kept for spec.selector.
// Annotations are validated in the order allocate, allocate-from, count, then
// spec.egressIPs; the first invalid one is reported in Invalid. It does not check the
// policy annotation: the caller keeps only the EgressIPs EgressIPBoundTo the policy.
func ViewEgressIP(obj *unstructured.Unstructured, prefix string) Claim {
	if prefix == "" {
		prefix = DefaultAnnotationPrefix
	}
	c := Claim{
		Target:   TargetRef{Kind: TargetEgressIP, Name: obj.GetName(), UID: obj.GetUID()},
		Eligible: true,
		Labels:   obj.GetLabels(),
	}
	annotations := obj.GetAnnotations()

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

	count := DefaultEgressIPCount
	if raw, ok := annotations[prefix+"/"+AnnotationCount]; ok {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < MinEgressIPCount || n > MaxEgressIPCount {
			c.Invalid = invalidAnnotationMsg(prefix, AnnotationCount, raw,
				fmt.Sprintf("must be an integer from %d to %d", MinEgressIPCount, MaxEgressIPCount))
			return c
		}
		count = n
	}

	current, _, err := egressip.EgressIPs(obj)
	if err != nil {
		c.Invalid = err.Error()
		return c
	}
	c.CurrentAssignment = current
	c.Slots = make([]SlotRequest, count)
	return c
}

// EgressIPBoundTo reports whether the EgressIP names the policy policyNamespace/policy in
// its policy annotation at prefix (DefaultAnnotationPrefix when prefix is empty), as
// "<namespace>/<name>". EgressIPs are cluster-scoped, so this annotation, set by whoever
// may edit the EgressIP, is what ties one to the single policy allowed to read it as a
// claim and patch its spec.egressIPs; every other policy ignores it.
func EgressIPBoundTo(obj *unstructured.Unstructured, prefix, policyNamespace, policy string) bool {
	if obj == nil || policyNamespace == "" || policy == "" {
		return false
	}
	if prefix == "" {
		prefix = DefaultAnnotationPrefix
	}
	return strings.TrimSpace(obj.GetAnnotations()[prefix+"/"+AnnotationPolicy]) == policyNamespace+"/"+policy
}

// egressIPsPatch is the JSON merge-patch document EgressIPsPatch produces. EgressIPs is
// never nil when marshaled, so a clear writes [] rather than null: the EgressIP CRD
// requires spec.egressIPs.
type egressIPsPatch struct {
	Spec struct {
		EgressIPs []string `json:"egressIPs"`
	} `json:"spec"`
}

// EgressIPsPatch builds the JSON merge-patch body that sets an EgressIP's spec.egressIPs
// to values, in order (Assignment.Values). Apply it with client.RawPatch(
// types.MergePatchType, body) on an egressip.New() object.
func EgressIPsPatch(values []string) []byte {
	var doc egressIPsPatch
	doc.Spec.EgressIPs = append([]string{}, values...)
	// Marshaling a struct of strings cannot fail.
	b, _ := json.Marshal(doc)
	return b
}

// ClearEgressIPsPatch builds the JSON merge-patch body that empties an EgressIP's
// spec.egressIPs: an empty list, since the field is required.
func ClearEgressIPsPatch() []byte {
	return EgressIPsPatch(nil)
}
