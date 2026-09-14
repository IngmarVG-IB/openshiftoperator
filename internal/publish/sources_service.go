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

	corev1 "k8s.io/api/core/v1"
)

// EndpointsFromService implements DESIGN.md 4.7 step 1 for a Service. Only
// type: LoadBalancer is published (NotLoadBalancer otherwise), unless the target
// annotation is present, in which case the type check is bypassed. Unlike Route and
// Ingress, a Service has no hostname of its own: hostnames come only from the hostname
// annotation (NoHostname when absent). Every hostname shares the same target(s), derived
// from status.loadBalancer.ingress[] the same way as for Ingress, or replaced outright by
// the target annotation when present.
func EndpointsFromService(s *corev1.Service, o SourceOptions) ([]Endpoint, []Skip) {
	src := SourceRef{Kind: SourceService, Namespace: s.Namespace, Name: s.Name, UID: s.UID}

	ann, skip := parseAnnotations(o, s.Annotations, src)
	if skip != nil {
		return nil, []Skip{*skip}
	}

	if s.Spec.Type != corev1.ServiceTypeLoadBalancer && !ann.hasTargets {
		return nil, []Skip{{Source: src, Reason: SkipNotLoadBalancer, Message: "Service is not type LoadBalancer"}}
	}

	if len(ann.hostnames) == 0 {
		return nil, []Skip{{
			Source:  src,
			Reason:  SkipNoHostname,
			Message: fmt.Sprintf("no %s/%s annotation", o.Prefix(), AnnotationHostname),
		}}
	}
	hostnames := mergeHostnames(nil, ann.hostnames)

	var targets []Target
	if ann.hasTargets {
		targets = ann.targets
	} else {
		ips, hostNames := ipsAndHostnamesFromServiceLB(s.Status.LoadBalancer.Ingress)
		targets = targetsFromIPsAndHostnames(ips, hostNames)
	}
	targets = filterByMode(targets, o.Mode)

	return buildEndpoints(src, hostnames, targets, ann.ttl)
}

// ipsAndHostnamesFromServiceLB splits a Service's load balancer status into its IPs and
// hostnames, in the order they appear.
func ipsAndHostnamesFromServiceLB(ingress []corev1.LoadBalancerIngress) (ips, hostnames []string) {
	for _, lb := range ingress {
		if lb.IP != "" {
			ips = append(ips, lb.IP)
		}
		if lb.Hostname != "" {
			hostnames = append(hostnames, lb.Hostname)
		}
	}
	return ips, hostnames
}
