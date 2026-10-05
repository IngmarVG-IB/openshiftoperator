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
	"net"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// EndpointsFromNode implements DESIGN.md 4.7 step 1 for a Node. A Node is cluster-scoped,
// so its SourceRef has no namespace. The hostnames are the Node name when it contains a
// dot (a bare short name such as "worker-0" is not a usable DNS name) plus the hostname
// annotation; a Node with neither is NoHostname. The targets are every address of
// SourceOptions.NodeAddressType (InternalIP when empty), as A for IPv4 and AAAA for IPv6,
// so a dual-stack Node yields both. The target annotation, when present, replaces them.
// Mode filtering and NoTarget work as for the other sources.
func EndpointsFromNode(n *corev1.Node, o SourceOptions) ([]Endpoint, []Skip) {
	src := SourceRef{Kind: SourceNode, Name: n.Name, UID: n.UID}

	ann, skip := parseAnnotations(o, n.Annotations, src)
	if skip != nil {
		return nil, []Skip{*skip}
	}

	var own []string
	if strings.Contains(n.Name, ".") {
		own = []string{normalizeHostname(n.Name)}
	}
	hostnames := mergeHostnames(own, ann.hostnames)
	if len(hostnames) == 0 {
		return nil, []Skip{{
			Source:  src,
			Reason:  SkipNoHostname,
			Message: fmt.Sprintf("the Node name has no dot and there is no %s/%s annotation", o.Prefix(), AnnotationHostname),
		}}
	}

	var targets []Target
	if ann.hasTargets {
		targets = ann.targets
	} else {
		targets = nodeAddressTargets(n.Status.Addresses, o.NodeAddressType)
	}
	targets = filterByMode(targets, o.Mode)

	return buildEndpoints(src, hostnames, targets, ann.ttl)
}

// nodeAddressTargets turns the Node addresses of one type (InternalIP when addrType is
// empty) into A and AAAA targets, in status order and without duplicates. A value that is
// not an IP address is ignored rather than turned into a CNAME.
func nodeAddressTargets(addresses []corev1.NodeAddress, addrType string) []Target {
	if addrType == "" {
		addrType = NodeAddressInternal
	}
	var targets []Target
	seen := make(map[Target]struct{}, len(addresses))
	for _, a := range addresses {
		if string(a.Type) != addrType {
			continue
		}
		ip := net.ParseIP(strings.TrimSpace(a.Address))
		if ip == nil {
			continue
		}
		t := Target{Type: TargetAAAA, Value: ip.String()}
		if ip4 := ip.To4(); ip4 != nil {
			t = Target{Type: TargetA, Value: ip4.String()}
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		targets = append(targets, t)
	}
	return targets
}
