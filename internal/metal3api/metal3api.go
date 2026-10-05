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

// Package metal3api is the thin seam between HostRegistrationPolicy and the Metal3
// BareMetalHost API (metal3.io/v1alpha1): identifying the kind, discovering once whether
// the cluster serves it, and answering "which MAC belongs to this Node or IP" from
// BareMetalHosts read as unstructured data. Like internal/nmstateapi, and per DESIGN.md
// 4.7, it takes no dependency on the upstream Go module
// (github.com/metal3-io/baremetal-operator).
//
// The fields read are confirmed against the upstream API types
// (https://github.com/metal3-io/baremetal-operator/blob/main/apis/metal3.io/v1alpha1/baremetalhost_types.go
// and hardwaredata_types.go next to it) and the generated CRD
// (https://github.com/metal3-io/baremetal-operator/blob/main/config/base/crds/bases/metal3.io_baremetalhosts.yaml):
//
//   - spec.bootMACAddress: the MAC of the NIC used for provisioning (PXE) the host.
//   - status.hardware.hostname: the host's name at inspection time.
//   - status.hardware.nics[]: one entry per NIC and IP, each with name, mac and ip. A
//     dual-stack NIC is reported as two entries, one per IP, sharing a MAC.
//
// Note the status field is "hardware" (the Go field is named HardwareDetails, its JSON tag
// is "hardware"); there is no status.hardwareDetails in the API.
//
// The package imports internal/hostreg for NormalizeMAC, so hostreg reaches this data only
// through its Metal3Hardware interface, which Index implements.
package metal3api

import (
	"net/netip"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"

	"github.com/IngmarVG-IB/openshiftoperator/internal/hostreg"
)

// GVK identifies the BareMetalHost kind this operator reads.
var GVK = schema.GroupVersionKind{Group: "metal3.io", Version: "v1alpha1", Kind: "BareMetalHost"}

// ListGVK identifies the list kind, for listing BareMetalHosts as unstructured data.
var ListGVK = schema.GroupVersionKind{Group: GVK.Group, Version: GVK.Version, Kind: "BareMetalHostList"}

// BareMetalHostResourceName is the plural resource name Available looks for within the
// metal3.io/v1alpha1 group version.
const BareMetalHostResourceName = "baremetalhosts"

// DefaultNamespace is the namespace the OpenShift baremetal platform keeps its
// BareMetalHosts in.
const DefaultNamespace = hostreg.DefaultMetal3Namespace

// Available reports whether the cluster identified by cfg serves the metal3.io/v1alpha1 API
// group with a "baremetalhosts" resource. It performs a single discovery call and does not
// cache the result, mirroring internal/nmstateapi.Available: a cluster that does not run
// Metal3 is reported as (false, nil), not an error; any other discovery failure is returned
// so callers can tell "no Metal3 here" apart from "discovery is broken".
func Available(cfg *rest.Config) (bool, error) {
	client, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return false, err
	}

	resources, err := client.ServerResourcesForGroupVersion(GVK.GroupVersion().String())
	if err != nil {
		if apierrors.IsNotFound(err) || discovery.IsGroupDiscoveryFailedError(err) {
			return false, nil
		}
		return false, err
	}

	for _, r := range resources.APIResources {
		if r.Name == BareMetalHostResourceName {
			return true, nil
		}
	}
	return false, nil
}

// Index answers MAC questions over a set of BareMetalHosts. It parses each host once, so
// the controller builds one Index per reconcile and asks it about every Node. The zero
// value knows no hosts. Index implements hostreg.Metal3Hardware.
type Index struct {
	hosts []bareMetalHost
}

var _ hostreg.Metal3Hardware = Index{}

// bareMetalHost is the part of a BareMetalHost the lookups need, with MACs already normalised and
// IPs parsed. Entries that do not parse are dropped when the host is read.
type bareMetalHost struct {
	name     string
	bootMAC  string
	hostname string
	nics     []nic
}

type nic struct {
	mac string
	ip  netip.Addr
}

// NewIndex reads hosts into an Index. Hosts are ordered by namespace and name, so the
// answer does not depend on list order when several hosts claim the same IP or hostname.
func NewIndex(hosts []unstructured.Unstructured) Index {
	sorted := make([]*unstructured.Unstructured, len(hosts))
	for i := range hosts {
		sorted[i] = &hosts[i]
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].GetNamespace() != sorted[j].GetNamespace() {
			return sorted[i].GetNamespace() < sorted[j].GetNamespace()
		}
		return sorted[i].GetName() < sorted[j].GetName()
	})

	idx := Index{hosts: make([]bareMetalHost, 0, len(sorted))}
	for _, obj := range sorted {
		idx.hosts = append(idx.hosts, readHost(obj))
	}
	return idx
}

// readHost extracts the lookup fields of one BareMetalHost.
func readHost(obj *unstructured.Unstructured) bareMetalHost {
	h := bareMetalHost{name: obj.GetName()}

	if raw, found, _ := unstructured.NestedString(obj.Object, "spec", "bootMACAddress"); found {
		if mac, err := hostreg.NormalizeMAC(raw); err == nil {
			h.bootMAC = mac
		}
	}
	if raw, found, _ := unstructured.NestedString(obj.Object, "status", "hardware", "hostname"); found {
		h.hostname = normaliseHostname(raw)
	}

	nics, _, _ := unstructured.NestedSlice(obj.Object, "status", "hardware", "nics")
	for _, rawNIC := range nics {
		m, isMap := rawNIC.(map[string]interface{})
		if !isMap {
			continue
		}
		macStr, _ := m["mac"].(string)
		mac, err := hostreg.NormalizeMAC(macStr)
		if err != nil {
			continue
		}
		ipStr, _ := m["ip"].(string)
		ip, ok := parseIP(ipStr)
		if !ok {
			continue
		}
		h.nics = append(h.nics, nic{mac: mac, ip: ip})
	}
	return h
}

// MACForNode returns the MAC to use for a Node: the host with a NIC whose ip equals address
// gives that NIC's mac; failing that, the host whose status.hardware.hostname equals
// nodeName gives its spec.bootMACAddress. host is the BareMetalHost name. IPs are compared
// as net/netip addresses, so any textual form of an IPv6 address matches. An empty address
// skips the first rule and an empty nodeName the second.
func (x Index) MACForNode(address, nodeName string) (mac, hostName string, ok bool) {
	if ip, valid := parseIP(address); valid {
		if mac, hostName, ok = x.MACForIP(ip); ok {
			return mac, hostName, true
		}
	}

	want := normaliseHostname(nodeName)
	if want == "" {
		return "", "", false
	}
	for _, h := range x.hosts {
		if h.hostname == want && h.bootMAC != "" {
			return h.bootMAC, h.name, true
		}
	}
	return "", "", false
}

// MACForIP returns the MAC of the NIC carrying ip, taken from the first host (by namespace
// and name) that has one. host is the BareMetalHost name.
func (x Index) MACForIP(ip netip.Addr) (mac, hostName string, ok bool) {
	ip = canonical(ip)
	if !ip.IsValid() {
		return "", "", false
	}
	for _, h := range x.hosts {
		for _, n := range h.nics {
			if n.ip == ip {
				return n.mac, h.name, true
			}
		}
	}
	return "", "", false
}

// MACForNode is Index.MACForNode over a freshly built Index: the rule used for a Node's
// primary registration. Callers asking about many Nodes should build one Index with
// NewIndex instead.
func MACForNode(hosts []unstructured.Unstructured, address, nodeName string) (mac string, host string, ok bool) {
	return NewIndex(hosts).MACForNode(address, nodeName)
}

// MACForIP is Index.MACForIP over a freshly built Index: the MAC of the NIC carrying ip,
// used for additional interfaces.
func MACForIP(hosts []unstructured.Unstructured, ip netip.Addr) (mac string, host string, ok bool) {
	return NewIndex(hosts).MACForIP(ip)
}

// parseIP parses an IP literal into its canonical comparison form.
func parseIP(s string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return netip.Addr{}, false
	}
	return canonical(ip), true
}

// canonical drops an IPv6 zone and unwraps an IPv4-mapped IPv6 address, so equal
// addresses compare equal whichever form each side used.
func canonical(ip netip.Addr) netip.Addr {
	if !ip.IsValid() {
		return ip
	}
	return ip.WithZone("").Unmap()
}

// normaliseHostname lower-cases a hostname and drops a trailing dot, so a Node name and the
// hostname recorded at inspection compare equal regardless of case.
func normaliseHostname(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}
