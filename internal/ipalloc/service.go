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
// 6: turning a Service into a ServiceView the planner can act on, and building the JSON
// merge-patch bodies (and match check) the controller uses to write and clear a Service's
// assignment field. Annotation parsing mirrors internal/publish/sources.go.
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

// ViewService reads a Service into the ServiceView the planner needs, applying the
// annotations at prefix (DefaultAnnotationPrefix when prefix is empty). Annotations are
// validated in the fixed order allocate, allocate-from, address, hostname; the first
// invalid one stops further annotation parsing and is reported in Invalid, naming that
// annotation. svc.Annotations may be nil; reading from a nil map is safe in Go and never
// panics.
func ViewService(svc *corev1.Service, via AssignVia, prefix string) ServiceView {
	if prefix == "" {
		prefix = DefaultAnnotationPrefix
	}
	ref := ServiceRef{Namespace: svc.Namespace, Name: svc.Name, UID: svc.UID}
	v := ServiceView{
		Ref:               ref,
		IsLoadBalancer:    svc.Spec.Type == corev1.ServiceTypeLoadBalancer,
		Hostname:          ref.Name + "." + ref.Namespace,
		CurrentAssignment: CurrentAssignment(svc, via),
	}

	annotations := svc.Annotations

	if raw, ok := annotations[prefix+"/"+AnnotationAllocate]; ok {
		switch strings.TrimSpace(raw) {
		case "true":
			b := true
			v.OptIn = &b
		case "false":
			b := false
			v.OptIn = &b
		default:
			v.Invalid = invalidAnnotationMsg(prefix, AnnotationAllocate, raw, `must be "true" or "false"`)
			return v
		}
	}

	if raw, ok := annotations[prefix+"/"+AnnotationAllocateFrom]; ok {
		pool, err := ParsePoolRef(raw)
		if err != nil {
			v.Invalid = invalidAnnotationMsg(prefix, AnnotationAllocateFrom, raw, err.Error())
			return v
		}
		v.Pool = &pool
	}

	if raw, ok := annotations[prefix+"/"+AnnotationAddress]; ok {
		addr, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil {
			v.Invalid = invalidAnnotationMsg(prefix, AnnotationAddress, raw, "not a valid IPv4 or IPv6 address")
			return v
		}
		v.ExplicitAddress = addr.String()
	}

	if raw, ok := annotations[prefix+"/"+AnnotationHostname]; ok {
		h := strings.ToLower(strings.TrimSpace(raw))
		if errs := validation.IsDNS1123Subdomain(h); len(errs) != 0 {
			v.Invalid = invalidAnnotationMsg(prefix, AnnotationHostname, raw, "not a valid DNS name")
			return v
		}
		v.Hostname = h
	}

	return v
}

func invalidAnnotationMsg(prefix, key, raw, reason string) string {
	return fmt.Sprintf("%s/%s: %q %s", prefix, key, raw, reason)
}

// CurrentAssignment returns what a Service's assignment field currently holds: in
// LoadBalancerIP mode, spec.loadBalancerIP; otherwise (Annotation mode, and any other value
// of via.Mode) the trimmed value of the via.Key() annotation, or "" when unset. A
// comma-separated dual-stack list is returned whole, so it never equals a single allocated
// address and is treated as a foreign (ManuallyAssigned) value by the caller.
func CurrentAssignment(svc *corev1.Service, via AssignVia) string {
	if via.Mode == AssignModeLoadBalancerIP {
		return svc.Spec.LoadBalancerIP
	}
	return strings.TrimSpace(svc.Annotations[via.Key()])
}

// AssignmentMatches reports whether a Service's current assignment (see CurrentAssignment)
// already equals value.
func AssignmentMatches(svc *corev1.Service, via AssignVia, value string) bool {
	return CurrentAssignment(svc, via) == value
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
// LoadBalancerIP mode. Apply it with client.RawPatch(types.MergePatchType, body).
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
