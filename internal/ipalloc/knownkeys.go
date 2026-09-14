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

package ipalloc

// KnownKey documents one Service annotation a load balancer implementation reads to learn
// the IP(s) it should assign to a LoadBalancer Service. It is exposed so docs and future
// validation (e.g. warning when spec.assignVia.annotation matches none of these) can list
// the known options without duplicating the research below.
type KnownKey struct {
	// Key is the annotation key, e.g. "metallb.io/loadBalancerIPs".
	Key string
	// Implementation names the load balancer implementation that reads Key.
	Implementation string
	// ValueFormat describes what the implementation accepts as the annotation value.
	ValueFormat string
	// DocURL is the upstream documentation page this entry was checked against.
	DocURL string
}

// KnownAssignAnnotations lists the annotation each supported load balancer implementation
// currently reads to request a specific LoadBalancer IP, researched against each project's
// own documentation on 2026-09-14:
//
//   - MetalLB: "metallb.io/loadBalancerIPs" accepts a single IP or a comma-separated list
//     for dual-stack services. spec.loadBalancerIP is still honoured for a single IP, but
//     "is planned to be deprecated in k8s apis" upstream and cannot express a dual-stack
//     pair. The older "metallb.universe.tf/loadBalancerIPs" annotation (pre-rename) still
//     works but is not documented on this page any more; kept as AnnotationMetalLBLegacy
//     for recognising a foreign assignment, not offered here as a current key.
//     https://metallb.io/usage/#requesting-specific-ips
//
//   - Cilium LB-IPAM: "lbipam.cilium.io/ips" accepts a comma-separated list of IPs (a
//     single IP is a one-element list). spec.loadBalancerIP is still honoured for a single
//     IP ("deprecated in k8s v1.24 but is supported until its future removal", per the
//     doc). NOTE: model.go's AnnotationCilium previously held "io.cilium/lb-ipam-ips",
//     which is the key Cilium's own 1.14 docs (docs.cilium.io/en/v1.14/network/lb-ipam/)
//     show for the same feature; the stable docs no longer mention that spelling at all,
//     so it has been corrected to "lbipam.cilium.io/ips" in this change — see the comment
//     on AnnotationCilium in model.go. Deployments still running Cilium <= 1.14 would need
//     the old key instead.
//     https://docs.cilium.io/en/stable/network/lb-ipam/#requesting-ips
//
//   - kube-vip: "kube-vip.io/loadbalancerIPs" (available since kube-vip 0.5.12) accepts a
//     single IP, a DNS name resolved via lookup, or a comma-separated pair for dual-stack;
//     "0.0.0.0" and/or "::" request DHCP-assigned addresses. spec.loadBalancerIP is still
//     honoured as an alternative to the annotation.
//     https://kube-vip.io/docs/usage/kubernetes-services/
func KnownAssignAnnotations() []KnownKey {
	return []KnownKey{
		{
			Key:            AnnotationMetalLB,
			Implementation: "MetalLB",
			ValueFormat:    "a single IP, or a comma-separated list of IPs for dual-stack",
			DocURL:         "https://metallb.io/usage/#requesting-specific-ips",
		},
		{
			Key:            AnnotationCilium,
			Implementation: "Cilium LB-IPAM",
			ValueFormat:    "a comma-separated list of IPs (a single IP is a one-element list)",
			DocURL:         "https://docs.cilium.io/en/stable/network/lb-ipam/#requesting-ips",
		},
		{
			Key:            AnnotationKubeVIP,
			Implementation: "kube-vip",
			ValueFormat:    "a single IP, a DNS name, or a comma-separated pair for dual-stack (\"0.0.0.0\"/\"::\" requests DHCP)",
			DocURL:         "https://kube-vip.io/docs/usage/kubernetes-services/",
		},
	}
}
