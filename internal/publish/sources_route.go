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
	"strings"

	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
)

// EndpointsFromRoute implements DESIGN.md 4.7 step 1 for a Route. The hostname is
// spec.host (a Route using spec.subdomain without a host is Unsupported); the Route must
// be admitted by at least one router, else NotAdmitted. The Auto target is a CNAME to the
// admitted ingress's routerCanonicalHostname, or SourceOptions.CanonicalHostname when
// set; when SourceOptions.StaticAddresses is non-empty, A/AAAA targets to those addresses
// are used instead. The hostname annotation adds extra hostnames that share the same
// target(s); the target annotation, when present, replaces the computed target(s)
// entirely, for every hostname.
func EndpointsFromRoute(r *routev1.Route, o SourceOptions) ([]Endpoint, []Skip) {
	src := SourceRef{Kind: SourceRoute, Namespace: r.Namespace, Name: r.Name, UID: r.UID}

	ann, skip := parseAnnotations(o, r.Annotations, src)
	if skip != nil {
		return nil, []Skip{*skip}
	}

	if r.Spec.Host == "" {
		msg := "Route has neither spec.host nor spec.subdomain set"
		if r.Spec.Subdomain != "" {
			msg = "Route uses spec.subdomain without spec.host, which is not supported"
		}
		return nil, []Skip{{Source: src, Reason: SkipUnsupported, Message: msg}}
	}
	primary := normalizeHostname(r.Spec.Host)

	admittedHostname, admitted := routeAdmittedCanonicalHostname(r)
	if !admitted {
		return nil, []Skip{{
			Source:   src,
			Hostname: primary,
			Reason:   SkipNotAdmitted,
			Message:  "no router has admitted this route",
		}}
	}

	hostnames := mergeHostnames([]string{primary}, ann.hostnames)

	targets := routeTargets(o, ann, admittedHostname)
	targets = filterByMode(targets, o.Mode)

	return buildEndpoints(src, hostnames, targets, ann.ttl)
}

// routeAdmittedCanonicalHostname reports the RouterCanonicalHostname of the first
// ingress entry with an Admitted=True condition, and whether any such entry exists.
func routeAdmittedCanonicalHostname(r *routev1.Route) (string, bool) {
	for _, ing := range r.Status.Ingress {
		for _, cond := range ing.Conditions {
			if cond.Type == routev1.RouteAdmitted && cond.Status == corev1.ConditionTrue {
				return ing.RouterCanonicalHostname, true
			}
		}
	}
	return "", false
}

// routeTargets computes the Route's target list before mode filtering: the target
// annotation wins outright, then static addresses, then the canonical-hostname CNAME.
func routeTargets(o SourceOptions, ann parsedAnnotations, admittedHostname string) []Target {
	if ann.hasTargets {
		return ann.targets
	}
	if len(o.StaticAddresses) > 0 {
		targets, err := ParseTargets(strings.Join(o.StaticAddresses, ","))
		if err != nil {
			// spec.target.addresses is validated when the policy is admitted; treat an
			// unexpected failure here as no target rather than panicking.
			return nil
		}
		return targets
	}
	canonical := o.CanonicalHostname
	if canonical == "" {
		canonical = admittedHostname
	}
	if canonical == "" {
		return nil
	}
	return []Target{{Type: TargetCNAME, Value: normalizeHostname(canonical)}}
}
