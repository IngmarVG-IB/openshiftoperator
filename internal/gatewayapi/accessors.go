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

package gatewayapi

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// Gateway address types (status.addresses[].type). A Gateway may also report
// implementation-specific, domain-prefixed types and the deprecated NamedAddress; the
// HTTPRoute source ignores those because they carry no DNS target.
const (
	AddressTypeIPAddress = "IPAddress"
	AddressTypeHostname  = "Hostname"
)

// ConditionAccepted is the route condition type a parent sets to report that it accepted
// the route (status.parents[].conditions[].type).
const ConditionAccepted = "Accepted"

// ParentRef is one entry of an HTTPRoute's spec.parentRefs or status.parents[].parentRef.
type ParentRef struct {
	// Group and Kind are as written in the object. An API server serving the real CRD fills
	// in the defaults (gateway.networking.k8s.io and Gateway); an empty value here means the
	// field was absent and is treated as the default by IsGateway.
	Group string
	Kind  string
	// Namespace defaults to the route's namespace when the reference leaves it out.
	Namespace   string
	Name        string
	SectionName string
}

// IsGateway reports whether the reference points at a Gateway: group
// gateway.networking.k8s.io or empty, and kind Gateway or empty. Other parents (a Service
// in a service-mesh route, for instance) have no addresses to publish.
func (p ParentRef) IsGateway() bool {
	return (p.Group == "" || p.Group == Group) && (p.Kind == "" || p.Kind == GatewayGVK.Kind)
}

// NamespacedName is the key of the referenced object.
func (p ParentRef) NamespacedName() types.NamespacedName {
	return types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
}

// ParentStatus is one entry of an HTTPRoute's status.parents.
type ParentStatus struct {
	ParentRef      ParentRef
	ControllerName string
	// Accepted is true when the entry carries an Accepted condition with status True.
	Accepted bool
}

// Address is one entry of a Gateway's status.addresses.
type Address struct {
	// Type is AddressTypeIPAddress when the object leaves it out, which is the API default.
	Type  string
	Value string
}

// Hostnames returns the route's spec.hostnames as written (trimmed; empty entries
// dropped). The list is empty when the route matches every hostname.
func Hostnames(route *unstructured.Unstructured) []string {
	items := nestedSlice(route, "spec", "hostnames")
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, _ := item.(string)
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ParentRefs returns the route's spec.parentRefs in order, each with its namespace
// defaulted to the route's. Entries without a name are dropped.
func ParentRefs(route *unstructured.Unstructured) []ParentRef {
	items := nestedSlice(route, "spec", "parentRefs")
	out := make([]ParentRef, 0, len(items))
	for _, item := range items {
		if ref, ok := parentRefFrom(item, route.GetNamespace()); ok {
			out = append(out, ref)
		}
	}
	return out
}

// ParentStatuses returns the route's status.parents in order, each parentRef with its
// namespace defaulted to the route's. Entries whose parentRef has no name are dropped.
func ParentStatuses(route *unstructured.Unstructured) []ParentStatus {
	items := nestedSlice(route, "status", "parents")
	out := make([]ParentStatus, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		ref, ok := parentRefFrom(m["parentRef"], route.GetNamespace())
		if !ok {
			continue
		}
		out = append(out, ParentStatus{
			ParentRef:      ref,
			ControllerName: stringField(m, "controllerName"),
			Accepted:       hasAcceptedCondition(m["conditions"]),
		})
	}
	return out
}

// AcceptedGateways returns the keys of the Gateways that have accepted the route: the
// status.parents entries with Accepted=True whose parentRef is a Gateway. The result is
// deduplicated (a route attached to several listeners of one Gateway has one entry per
// listener) and sorted by namespace, then name.
func AcceptedGateways(route *unstructured.Unstructured) []types.NamespacedName {
	seen := map[types.NamespacedName]struct{}{}
	var out []types.NamespacedName
	for _, p := range ParentStatuses(route) {
		if !p.Accepted || !p.ParentRef.IsGateway() {
			continue
		}
		key := p.ParentRef.NamespacedName()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// GatewayAddresses returns the Gateway's status.addresses in order. Entries without a
// value are dropped; a missing type means IPAddress.
func GatewayAddresses(gateway *unstructured.Unstructured) []Address {
	items := nestedSlice(gateway, "status", "addresses")
	out := make([]Address, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		value := strings.TrimSpace(stringField(m, "value"))
		if value == "" {
			continue
		}
		addrType := stringField(m, "type")
		if addrType == "" {
			addrType = AddressTypeIPAddress
		}
		out = append(out, Address{Type: addrType, Value: value})
	}
	return out
}

// parentRefFrom reads one parentRef map; ok is false when v is not a map or has no name.
func parentRefFrom(v interface{}, routeNamespace string) (ParentRef, bool) {
	m, ok := v.(map[string]interface{})
	if !ok {
		return ParentRef{}, false
	}
	name := stringField(m, "name")
	if name == "" {
		return ParentRef{}, false
	}
	ref := ParentRef{
		Group:       stringField(m, "group"),
		Kind:        stringField(m, "kind"),
		Namespace:   stringField(m, "namespace"),
		Name:        name,
		SectionName: stringField(m, "sectionName"),
	}
	if ref.Namespace == "" {
		ref.Namespace = routeNamespace
	}
	return ref, true
}

// hasAcceptedCondition reports whether a conditions list holds an Accepted condition whose
// status is "True".
func hasAcceptedCondition(v interface{}) bool {
	conditions, _ := v.([]interface{})
	for _, c := range conditions {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if stringField(m, "type") == ConditionAccepted && stringField(m, "status") == "True" {
			return true
		}
	}
	return false
}

// nestedSlice returns the list at the given path, or nil when obj is nil or the path is
// absent or not a list. It reads without copying, so unlike unstructured.NestedSlice it
// cannot panic on a hand-built object holding non-JSON values.
func nestedSlice(obj *unstructured.Unstructured, fields ...string) []interface{} {
	if obj == nil {
		return nil
	}
	v, found, err := unstructured.NestedFieldNoCopy(obj.Object, fields...)
	if err != nil || !found {
		return nil
	}
	s, _ := v.([]interface{})
	return s
}

// stringField returns m[key] when it is a string, else "".
func stringField(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}
