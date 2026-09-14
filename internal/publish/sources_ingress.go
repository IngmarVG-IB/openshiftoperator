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

import networkingv1 "k8s.io/api/networking/v1"

// EndpointsFromIngress implements DESIGN.md 4.7 step 1 for an Ingress. Hostnames come
// from spec.rules[].host (empty hosts are skipped); the hostname annotation adds more.
// Every hostname shares the same target(s): status.loadBalancer.ingress[] IPs give one
// A/AAAA target per IP, no IPs but a hostname gives one CNAME, and nothing gives
// NoTarget. The target annotation, when present, replaces the computed target(s).
func EndpointsFromIngress(i *networkingv1.Ingress, o SourceOptions) ([]Endpoint, []Skip) {
	src := SourceRef{Kind: SourceIngress, Namespace: i.Namespace, Name: i.Name, UID: i.UID}

	ann, skip := parseAnnotations(o, i.Annotations, src)
	if skip != nil {
		return nil, []Skip{*skip}
	}

	base := make([]string, 0, len(i.Spec.Rules))
	for _, rule := range i.Spec.Rules {
		if rule.Host == "" {
			continue
		}
		base = append(base, normalizeHostname(rule.Host))
	}
	hostnames := mergeHostnames(base, ann.hostnames)
	if len(hostnames) == 0 {
		return nil, nil
	}

	var targets []Target
	if ann.hasTargets {
		targets = ann.targets
	} else {
		ips, hostNames := ipsAndHostnamesFromIngressLB(i.Status.LoadBalancer.Ingress)
		targets = targetsFromIPsAndHostnames(ips, hostNames)
	}
	targets = filterByMode(targets, o.Mode)

	return buildEndpoints(src, hostnames, targets, ann.ttl)
}

// ipsAndHostnamesFromIngressLB splits an Ingress's load balancer status into its IPs and
// hostnames, in the order they appear.
func ipsAndHostnamesFromIngressLB(ingress []networkingv1.IngressLoadBalancerIngress) (ips, hostnames []string) {
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
