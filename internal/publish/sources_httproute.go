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

package publish

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/IngmarVG-IB/openshiftoperator/internal/gatewayapi"
)

// EndpointsFromHTTPRoute implements DESIGN.md 4.7 (milestone 7) step 1 for a Gateway API
// HTTPRoute, read as unstructured data. Hostnames are spec.hostnames (wildcards such as
// *.example.com are kept) plus the hostname annotation; a route with neither is NoHostname.
// The route must be accepted by at least one parent Gateway, meaning a status.parents entry
// with Accepted=True whose parentRef is a Gateway, else NotAdmitted. Every hostname shares
// the same targets: the union of the status.addresses of the accepted Gateways found in
// gateways (an IPAddress becomes an A or AAAA target, a Hostname a CNAME), deduplicated and
// sorted. A Gateway the map does not hold contributes nothing, and no addresses at all is
// NoTarget. The target annotation, when present, replaces the computed targets, and mode
// filtering applies as for the other sources. SourceOptions.StaticAddresses is not used,
// as for Ingress and Service.
func EndpointsFromHTTPRoute(route *unstructured.Unstructured, gateways map[types.NamespacedName]*unstructured.Unstructured, o SourceOptions) ([]Endpoint, []Skip) {
	src := SourceRef{Kind: SourceHTTPRoute, Namespace: route.GetNamespace(), Name: route.GetName(), UID: route.GetUID()}

	ann, skip := parseAnnotations(o, route.GetAnnotations(), src)
	if skip != nil {
		return nil, []Skip{*skip}
	}

	base, invalid := httpRouteHostnames(src, route)
	hostnames := mergeHostnames(base, ann.hostnames)
	if len(hostnames) == 0 {
		if len(invalid) > 0 {
			return nil, invalid
		}
		return nil, []Skip{{
			Source:  src,
			Reason:  SkipNoHostname,
			Message: fmt.Sprintf("no spec.hostnames and no %s/%s annotation", o.Prefix(), AnnotationHostname),
		}}
	}

	accepted := gatewayapi.AcceptedGateways(route)
	if len(accepted) == 0 {
		skips := make([]Skip, 0, len(hostnames)+len(invalid))
		for _, h := range hostnames {
			skips = append(skips, Skip{
				Source:   src,
				Hostname: h,
				Reason:   SkipNotAdmitted,
				Message:  "no parent Gateway has accepted this route",
			})
		}
		return nil, append(skips, invalid...)
	}

	var targets []Target
	if ann.hasTargets {
		targets = ann.targets
	} else {
		targets = httpRouteGatewayTargets(accepted, gateways)
	}
	targets = filterByMode(targets, o.Mode)

	eps, skips := buildEndpoints(src, hostnames, targets, ann.ttl)
	return eps, append(skips, invalid...)
}

// httpRouteHostnames normalizes spec.hostnames. The API server validates them against the
// Gateway API hostname pattern, so an entry that is not a DNS name or a "*." wildcard of
// one only appears when that validation was bypassed; it becomes an Unsupported Skip
// instead of a Record the planner could not name.
func httpRouteHostnames(src SourceRef, route *unstructured.Unstructured) (valid []string, invalid []Skip) {
	for _, raw := range gatewayapi.Hostnames(route) {
		h := normalizeHostname(raw)
		// A leading "*." label is allowed; the rest must be a plain DNS name.
		if len(validation.IsDNS1123Subdomain(strings.TrimPrefix(strings.TrimSuffix(h, "."), "*."))) != 0 {
			invalid = append(invalid, Skip{
				Source:   src,
				Hostname: h,
				Reason:   SkipUnsupported,
				Message:  fmt.Sprintf("spec.hostnames entry %q is not a DNS name or a *. wildcard", raw),
			})
			continue
		}
		valid = append(valid, h)
	}
	return valid, invalid
}

// httpRouteGatewayTargets unions the addresses of the accepted Gateways present in gateways
// into targets, deduplicated and sorted by type then value. Addresses of any other type
// than IPAddress and Hostname, and values that do not parse as the type they claim, are
// ignored.
func httpRouteGatewayTargets(accepted []types.NamespacedName, gateways map[types.NamespacedName]*unstructured.Unstructured) []Target {
	seen := map[Target]struct{}{}
	var targets []Target
	for _, key := range accepted {
		gw, ok := gateways[key]
		if !ok || gw == nil {
			continue
		}
		for _, addr := range gatewayapi.GatewayAddresses(gw) {
			t, ok := gatewayAddressTarget(addr)
			if !ok {
				continue
			}
			if _, dup := seen[t]; dup {
				continue
			}
			seen[t] = struct{}{}
			targets = append(targets, t)
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Type != targets[j].Type {
			return targets[i].Type < targets[j].Type
		}
		return targets[i].Value < targets[j].Value
	})
	return targets
}

// gatewayAddressTarget converts one Gateway status address, using the shared target
// parsing: an IPAddress must be an IP and gives A or AAAA by family, a Hostname must be a
// DNS name and gives a CNAME.
func gatewayAddressTarget(addr gatewayapi.Address) (Target, bool) {
	t, err := parseTarget(addr.Value)
	if err != nil {
		return Target{}, false
	}
	switch addr.Type {
	case gatewayapi.AddressTypeIPAddress:
		return t, t.Type == TargetA || t.Type == TargetAAAA
	case gatewayapi.AddressTypeHostname:
		return t, t.Type == TargetCNAME
	default:
		return Target{}, false
	}
}
