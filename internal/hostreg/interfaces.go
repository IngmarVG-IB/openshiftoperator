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

package hostreg

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

// maxLabelLength is the DNS label length limit applied to the first label of an interface
// hostname.
const maxLabelLength = 63

// InterfaceRegistrationName derives the deterministic name of the object generated for one
// additional interface of one Node: "<policy>-<node>-<interface>". When that would exceed
// the Kubernetes 253 character name limit, the node part is replaced with the first 10 hex
// characters of sha256(node), as RegistrationName does, so the interface name (at most 63
// characters) always survives.
func InterfaceRegistrationName(policy, node, iface string) string {
	name := policy + "-" + node + "-" + iface
	if len(name) <= maxNameLength {
		return name
	}
	sum := sha256.Sum256([]byte(node))
	return policy + "-" + hex.EncodeToString(sum[:])[:nameHashLength] + "-" + iface
}

// InterfaceHostname inserts suffix after the first label of hostname: "node1.example.com"
// with "-storage" gives "node1-storage.example.com". A hostname of a single label gets the
// suffix appended, and an empty suffix returns hostname unchanged. The result must be a
// valid DNS subdomain with a first label of at most 63 characters, otherwise an error is
// returned.
func InterfaceHostname(hostname, suffix string) (string, error) {
	first, rest, dotted := strings.Cut(hostname, ".")
	label := first + strings.ToLower(suffix)
	out := label
	if dotted {
		out += "." + rest
	}
	if len(label) > maxLabelLength {
		return "", fmt.Errorf("hostname %q with suffix %q has a first label longer than %d characters", hostname, suffix, maxLabelLength)
	}
	if errs := validation.IsDNS1123Subdomain(out); len(errs) != 0 {
		return "", fmt.Errorf("hostname %q with suffix %q is not a valid DNS name", hostname, suffix)
	}
	return out, nil
}

// AdditionalSpaceNames returns, sorted and without duplicates, the names of the IPSpace
// CRs that additionalInterfaces entries name in their own spaceRef and that differ from the
// policy's spaceRef. The controller resolves these (as SpaceState) into PlanInput.Spaces.
func AdditionalSpaceNames(p *uddiv1alpha1.HostRegistrationPolicy) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range p.Spec.AdditionalInterfaces {
		name := interfaceSpaceRef(p, e).Name
		if name == "" || name == p.Spec.SpaceRef.Name || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// interfaceSpaceRef is the entry's spaceRef, or the policy's when the entry sets none.
func interfaceSpaceRef(p *uddiv1alpha1.HostRegistrationPolicy, e uddiv1alpha1.AdditionalInterface) uddiv1alpha1.ObjectRef {
	if e.SpaceRef != nil {
		return *e.SpaceRef
	}
	return p.Spec.SpaceRef
}

// ResolveInterfaces resolves every additionalInterfaces entry against one Node and returns
// one InterfaceView per entry, in entry order. hostname is the Node's registered hostname
// (NodeView.Hostname). For each entry:
//
//   - the address is the first IP inside the entry's cidr among the Node's status.addresses
//     of any type, else among the interface addresses of nns (the Node's NodeNetworkState
//     status.currentState, nil when unknown); IPv4 and IPv6 both work, and an address of
//     the other family never matches;
//   - the MAC is that of the NodeNetworkState interface carrying the address, else of the
//     Metal3 NIC carrying it (hw may be nil, or hold no hosts);
//   - the hostname is hostname with the entry's hostnameSuffix inserted after its first
//     label (see InterfaceHostname).
//
// An entry whose cidr does not parse, or whose suffix makes the hostname invalid, comes
// back with Invalid set. An entry with no matching address comes back with Address empty.
// The caller decides which sources to consult by what it passes: nns and hw are used as
// given, so a nil nns when NodeNetworkState is disabled or absent, and a nil hw when Metal3
// is, keeps them out of the answer.
func ResolveInterfaces(entries []uddiv1alpha1.AdditionalInterface, node *corev1.Node, hostname string, nns map[string]interface{}, hw Metal3Hardware) []InterfaceView {
	if len(entries) == 0 {
		return nil
	}
	out := make([]InterfaceView, 0, len(entries))
	for _, e := range entries {
		out = append(out, resolveInterface(e, node, hostname, nns, hw))
	}
	return out
}

// resolveInterface resolves one additionalInterfaces entry; see ResolveInterfaces.
func resolveInterface(e uddiv1alpha1.AdditionalInterface, node *corev1.Node, hostname string, nns map[string]interface{}, hw Metal3Hardware) InterfaceView {
	iv := InterfaceView{Name: e.Name}

	prefix, err := netip.ParsePrefix(strings.TrimSpace(e.CIDR))
	if err != nil {
		iv.Invalid = fmt.Sprintf("cidr %q is not a valid IPv4 or IPv6 CIDR", e.CIDR)
		return iv
	}
	host, err := InterfaceHostname(hostname, e.HostnameSuffix)
	if err != nil {
		iv.Invalid = err.Error()
		return iv
	}
	iv.Hostname = host

	addr, ok := nodeAddressInPrefix(node, prefix)
	if !ok && nns != nil {
		addr, _, ok = AddressInPrefixFromNodeNetworkState(nns, prefix)
	}
	if !ok {
		return iv
	}
	iv.Address = addr.String()

	if nns != nil {
		if mac, _, found := MACFromNodeNetworkState(nns, "", iv.Address); found {
			iv.MAC, iv.MACFrom = mac, MACFromNMState
			return iv
		}
	}
	if hw != nil {
		if mac, _, found := hw.MACForIP(addr); found {
			iv.MAC, iv.MACFrom = mac, MACFromMetal3
		}
	}
	return iv
}

// nodeAddressInPrefix returns the first status.addresses entry, of any type, that parses
// as an IP inside prefix. Entries that are not IP literals (Hostname, InternalDNS) never
// match.
func nodeAddressInPrefix(node *corev1.Node, prefix netip.Prefix) (netip.Addr, bool) {
	if node == nil {
		return netip.Addr{}, false
	}
	for _, a := range node.Status.Addresses {
		got, err := netip.ParseAddr(strings.TrimSpace(a.Address))
		if err != nil {
			continue
		}
		got = got.WithZone("")
		if prefix.Contains(got) {
			return got, true
		}
	}
	return netip.Addr{}, false
}
