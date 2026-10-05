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
	"net/netip"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

const (
	storageCIDR  = "10.20.0.0/16"
	storageIP    = "10.20.0.7"
	storageName  = "storage"
	nodeFQDN     = "worker-1.example.com"
	storageFQDN  = "worker-1-storage.example.com"
	storageSufx  = "-storage"
	storageMAC   = "52:55:00:d1:55:20"
	storageIPv6  = "fd00:20::7"
	storageCIDR6 = "fd00:20::/64"
)

// nodeWith builds a Node carrying the given status addresses.
func nodeWith(addrs ...corev1.NodeAddress) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: nodeWorker1},
		Status:     corev1.NodeStatus{Addresses: addrs},
	}
}

func internalIP(a string) corev1.NodeAddress {
	return corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: a}
}

// storageEntry is the additionalInterfaces entry most tests resolve.
func storageEntry(cidr string) uddiv1alpha1.AdditionalInterface {
	return uddiv1alpha1.AdditionalInterface{Name: storageName, CIDR: cidr, HostnameSuffix: storageSufx}
}

// nnsIfaces builds a currentState from interface maps.
func nnsIfaces(ifaces ...map[string]interface{}) map[string]interface{} {
	out := make([]interface{}, len(ifaces))
	for i, m := range ifaces {
		out[i] = m
	}
	return map[string]interface{}{"interfaces": out}
}

// nnsIface builds one NodeNetworkState interface with the given IPv4 and IPv6 addresses.
func nnsIface(name, mac string, v4, v6 []string) map[string]interface{} {
	i := map[string]interface{}{"name": name, "type": "ethernet", "state": "up"}
	if mac != "" {
		i["mac-address"] = mac
	}
	toEntries := func(ips []string) []interface{} {
		out := make([]interface{}, len(ips))
		for n, ip := range ips {
			out[n] = map[string]interface{}{"ip": ip, "prefix-length": int64(24)}
		}
		return out
	}
	if len(v4) > 0 {
		i["ipv4"] = map[string]interface{}{"address": toEntries(v4)}
	}
	if len(v6) > 0 {
		i["ipv6"] = map[string]interface{}{"address": toEntries(v6)}
	}
	return i
}

func TestResolveInterfaces_Address(t *testing.T) {
	cases := []struct {
		name     string
		cidr     string
		node     *corev1.Node
		nns      map[string]interface{}
		wantAddr string
	}{
		{
			name: "from the Node's InternalIP",
			cidr: storageCIDR, node: nodeWith(internalIP(nodeAddr), internalIP(storageIP)),
			wantAddr: storageIP,
		},
		{
			name: "from an address of any type, ExternalIP here",
			cidr: storageCIDR, node: nodeWith(corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: storageIP}),
			wantAddr: storageIP,
		},
		{
			name: "non-IP addresses are ignored",
			cidr: storageCIDR, node: nodeWith(
				corev1.NodeAddress{Type: corev1.NodeHostName, Address: nodeWorker1},
				corev1.NodeAddress{Type: corev1.NodeInternalDNS, Address: nodeFQDN},
				internalIP(storageIP)),
			wantAddr: storageIP,
		},
		{
			name: "first matching Node address in list order",
			cidr: storageCIDR, node: nodeWith(internalIP("10.20.0.9"), internalIP(storageIP)),
			wantAddr: "10.20.0.9",
		},
		{
			name: "from NodeNetworkState when the Node has none",
			cidr: storageCIDR, node: nodeWith(internalIP(nodeAddr)),
			nns:      nnsIfaces(nnsIface("eth1", storageMAC, []string{storageIP}, nil)),
			wantAddr: storageIP,
		},
		{
			name: "Node addresses are preferred over NodeNetworkState",
			cidr: storageCIDR, node: nodeWith(internalIP("10.20.0.9")),
			nns:      nnsIfaces(nnsIface("eth1", storageMAC, []string{storageIP}, nil)),
			wantAddr: "10.20.0.9",
		},
		{
			name: "NodeNetworkState: first interface in report order",
			cidr: storageCIDR, node: nodeWith(),
			nns: nnsIfaces(
				nnsIface("eth0", "", []string{nodeAddr}, nil),
				nnsIface("eth1", storageMAC, []string{"10.20.0.8"}, nil),
				nnsIface("eth2", "", []string{storageIP}, nil)),
			wantAddr: "10.20.0.8",
		},
		{
			name: "no address anywhere",
			cidr: storageCIDR, node: nodeWith(internalIP(nodeAddr)),
			nns: nnsIfaces(nnsIface("eth0", "", []string{nodeAddr}, nil)),
		},
		{
			name: "no NodeNetworkState and no matching Node address",
			cidr: storageCIDR, node: nodeWith(internalIP(nodeAddr)),
		},
		{
			name: "nil Node",
			cidr: storageCIDR, node: nil,
		},
		{
			name: "IPv6 CIDR, from the Node's address",
			cidr: storageCIDR6, node: nodeWith(internalIP(nodeAddr), internalIP("FD00:20:0:0:0:0:0:7")),
			wantAddr: storageIPv6,
		},
		{
			name: "IPv6 CIDR, from NodeNetworkState",
			cidr: storageCIDR6, node: nodeWith(internalIP(nodeAddr)),
			nns:      nnsIfaces(nnsIface("eth1", storageMAC, []string{nodeAddr}, []string{"fe80::1", storageIPv6})),
			wantAddr: storageIPv6,
		},
		{
			name: "dual-stack Node, IPv4 CIDR picks the IPv4 address",
			cidr: storageCIDR, node: nodeWith(internalIP(storageIPv6), internalIP(storageIP)),
			wantAddr: storageIP,
		},
		{
			name: "dual-stack Node, IPv6 CIDR picks the IPv6 address",
			cidr: storageCIDR6, node: nodeWith(internalIP(storageIP), internalIP(storageIPv6)),
			wantAddr: storageIPv6,
		},
		{
			name: "an IPv4 address never matches an IPv6 CIDR",
			cidr: "::/0", node: nodeWith(internalIP(storageIP)),
		},
		{
			name: "loopback interfaces are never searched",
			cidr: "127.0.0.0/8", node: nodeWith(),
			nns: nnsIfaces(map[string]interface{}{"name": "lo", "type": "loopback", "mac-address": "00:00:00:00:00:00",
				"ipv4": map[string]interface{}{"address": []interface{}{map[string]interface{}{"ip": "127.0.0.1"}}}}),
		},
		{
			name: "CIDR with host bits set still matches its network",
			cidr: "10.20.0.5/16", node: nodeWith(internalIP(storageIP)),
			wantAddr: storageIP,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveInterfaces([]uddiv1alpha1.AdditionalInterface{storageEntry(c.cidr)}, c.node, nodeFQDN, c.nns, nil)

			if len(got) != 1 {
				t.Fatalf("ResolveInterfaces returned %d views, want 1", len(got))
			}
			iv := got[0]
			if iv.Name != storageName || iv.Invalid != "" {
				t.Fatalf("view = %+v, want name %q and no Invalid", iv, storageName)
			}
			if iv.Address != c.wantAddr {
				t.Errorf("Address = %q, want %q", iv.Address, c.wantAddr)
			}
			if iv.Hostname != storageFQDN {
				t.Errorf("Hostname = %q, want %q", iv.Hostname, storageFQDN)
			}
		})
	}
}

func TestResolveInterfaces_MAC(t *testing.T) {
	metal3 := fakeHardware{nics: map[netip.Addr]string{netip.MustParseAddr(storageIP): macMetal3NIC}}
	metal3v6 := fakeHardware{nics: map[netip.Addr]string{netip.MustParseAddr(storageIPv6): macMetal3NIC}}
	node := nodeWith(internalIP(nodeAddr), internalIP(storageIP))
	node6 := nodeWith(internalIP(nodeAddr), internalIP(storageIPv6))

	cases := []struct {
		name     string
		cidr     string
		node     *corev1.Node
		nns      map[string]interface{}
		hw       Metal3Hardware
		wantMAC  string
		wantFrom string
	}{
		{
			name: "from the NodeNetworkState interface carrying the IP", cidr: storageCIDR, node: node,
			nns:     nnsIfaces(nnsIface("eth0", "AA:AA:AA:AA:AA:00", []string{nodeAddr}, nil), nnsIface("eth1", strings.ToUpper(storageMAC), []string{storageIP}, nil)),
			wantMAC: storageMAC, wantFrom: MACFromNMState,
		},
		{
			name: "from the Metal3 NIC carrying the IP", cidr: storageCIDR, node: node, hw: metal3,
			wantMAC: macMetal3NIC, wantFrom: MACFromMetal3,
		},
		{
			name: "NodeNetworkState beats Metal3", cidr: storageCIDR, node: node, hw: metal3,
			nns:     nnsIfaces(nnsIface("eth1", storageMAC, []string{storageIP}, nil)),
			wantMAC: storageMAC, wantFrom: MACFromNMState,
		},
		{
			name: "Metal3 answers when the NodeNetworkState interface has no MAC", cidr: storageCIDR, node: node, hw: metal3,
			nns:     nnsIfaces(nnsIface("eth1", "", []string{storageIP}, nil)),
			wantMAC: macMetal3NIC, wantFrom: MACFromMetal3,
		},
		{
			name: "Metal3 answers when NodeNetworkState does not carry the IP", cidr: storageCIDR, node: node, hw: metal3,
			nns:     nnsIfaces(nnsIface("eth0", storageMAC, []string{nodeAddr}, nil)),
			wantMAC: macMetal3NIC, wantFrom: MACFromMetal3,
		},
		{
			name: "IP from NodeNetworkState, MAC from its interface", cidr: storageCIDR, node: nodeWith(),
			nns:     nnsIfaces(nnsIface("eth1", storageMAC, []string{storageIP}, nil)),
			wantMAC: storageMAC, wantFrom: MACFromNMState,
		},
		{
			name: "IPv6: from the NodeNetworkState interface", cidr: storageCIDR6, node: node6,
			nns:     nnsIfaces(nnsIface("eth1", storageMAC, nil, []string{"FD00:20:0:0:0:0:0:7"})),
			wantMAC: storageMAC, wantFrom: MACFromNMState,
		},
		{
			name: "IPv6: from the Metal3 NIC", cidr: storageCIDR6, node: node6, hw: metal3v6,
			wantMAC: macMetal3NIC, wantFrom: MACFromMetal3,
		},
		{
			name: "no source knows the MAC", cidr: storageCIDR, node: node,
			nns: nnsIfaces(nnsIface("eth0", storageMAC, []string{nodeAddr}, nil)),
			hw:  fakeHardware{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveInterfaces([]uddiv1alpha1.AdditionalInterface{storageEntry(c.cidr)}, c.node, nodeFQDN, c.nns, c.hw)[0]

			if got.Address == "" {
				t.Fatalf("no address resolved: %+v", got)
			}
			if got.MAC != c.wantMAC || got.MACFrom != c.wantFrom {
				t.Errorf("MAC/MACFrom = %q/%q, want %q/%q", got.MAC, got.MACFrom, c.wantMAC, c.wantFrom)
			}
		})
	}
}

func TestResolveInterfaces_Entries(t *testing.T) {
	if got := ResolveInterfaces(nil, nodeWith(internalIP(storageIP)), nodeFQDN, nil, nil); got != nil {
		t.Errorf("ResolveInterfaces(no entries) = %+v, want nil", got)
	}

	entries := []uddiv1alpha1.AdditionalInterface{
		{Name: "storage", CIDR: storageCIDR, HostnameSuffix: "-storage"},
		{Name: "mgmt", CIDR: "192.168.0.0/24"}, // no address
		{Name: "bad", CIDR: "not-a-cidr"},
		{Name: "v6", CIDR: storageCIDR6, HostnameSuffix: "-v6"},
	}
	node := nodeWith(internalIP(storageIP), internalIP(storageIPv6))

	got := ResolveInterfaces(entries, node, nodeFQDN, nil, nil)

	if len(got) != len(entries) {
		t.Fatalf("got %d views for %d entries", len(got), len(entries))
	}
	for i, e := range entries {
		if got[i].Name != e.Name {
			t.Errorf("view %d is %q, want %q (entry order)", i, got[i].Name, e.Name)
		}
	}
	if got[0].Address != storageIP || got[0].Hostname != storageFQDN {
		t.Errorf("storage view = %+v", got[0])
	}
	if got[1].Address != "" || got[1].Invalid != "" || got[1].Hostname != nodeFQDN {
		t.Errorf("mgmt view = %+v, want no address, valid, hostname unchanged", got[1])
	}
	if got[2].Invalid == "" || !strings.Contains(got[2].Invalid, "not-a-cidr") || got[2].Address != "" {
		t.Errorf("bad view = %+v, want Invalid naming the CIDR", got[2])
	}
	if got[3].Address != storageIPv6 || got[3].Hostname != "worker-1-v6.example.com" {
		t.Errorf("v6 view = %+v", got[3])
	}
}

func TestInterfaceHostname(t *testing.T) {
	cases := []struct {
		name     string
		hostname string
		suffix   string
		want     string
		wantErr  bool
	}{
		{name: "inserted after the first label", hostname: "node1.example.com", suffix: "-storage", want: "node1-storage.example.com"},
		{name: "single label gets it appended", hostname: "node1", suffix: "-storage", want: "node1-storage"},
		{name: "empty suffix leaves it unchanged", hostname: "node1.example.com", suffix: "", want: "node1.example.com"},
		{name: "only the first label changes", hostname: "a.b.c.example.com", suffix: "-x", want: "a-x.b.c.example.com"},
		{name: "suffix is lower-cased", hostname: "node1.example.com", suffix: "-Storage", want: "node1-storage.example.com"},
		{name: "suffix without a hyphen", hostname: "node1.example.com", suffix: "s", want: "node1s.example.com"},
		{name: "first label exactly 63 characters", hostname: strings.Repeat("a", 60) + ".example.com", suffix: "-st", want: strings.Repeat("a", 60) + "-st.example.com"},
		{name: "first label over 63 characters", hostname: strings.Repeat("a", 60) + ".example.com", suffix: "-stor", wantErr: true},
		{name: "a trailing hyphen is not a DNS name", hostname: "node1.example.com", suffix: "-", wantErr: true},
		{name: "a leading hyphen is not a DNS name", hostname: "-x", suffix: "", wantErr: true},
		{name: "total over 253 characters", hostname: "n." + strings.Repeat("a.", 125) + "com", suffix: "-s", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := InterfaceHostname(c.hostname, c.suffix)
			if (err != nil) != c.wantErr {
				t.Fatalf("InterfaceHostname(%q, %q) error = %v, wantErr %v", c.hostname, c.suffix, err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Errorf("InterfaceHostname(%q, %q) = %q, want %q", c.hostname, c.suffix, got, c.want)
			}
		})
	}
}

func TestResolveInterfaces_InvalidHostname(t *testing.T) {
	entry := uddiv1alpha1.AdditionalInterface{Name: storageName, CIDR: storageCIDR, HostnameSuffix: "-"}

	got := ResolveInterfaces([]uddiv1alpha1.AdditionalInterface{entry}, nodeWith(internalIP(storageIP)), nodeFQDN, nil, nil)[0]

	if got.Invalid == "" || got.Address != "" || got.Hostname != "" {
		t.Errorf("view = %+v, want Invalid set and nothing else resolved", got)
	}
}

func TestInterfaceRegistrationName(t *testing.T) {
	if got, want := InterfaceRegistrationName("nodes", nodeWorker1, storageName), "nodes-worker-1-storage"; got != want {
		t.Errorf("InterfaceRegistrationName = %q, want %q", got, want)
	}

	longNode := strings.Repeat("n", 250)
	long := InterfaceRegistrationName("nodes", longNode, storageName)
	if len(long) > maxNameLength {
		t.Fatalf("hashed name is %d characters, want at most %d", len(long), maxNameLength)
	}
	if !strings.HasPrefix(long, "nodes-") || !strings.HasSuffix(long, "-"+storageName) {
		t.Errorf("hashed name %q lost the policy prefix or the interface suffix", long)
	}
	if mid := strings.TrimSuffix(strings.TrimPrefix(long, "nodes-"), "-"+storageName); len(mid) != nameHashLength {
		t.Errorf("hashed node part %q is %d characters, want %d", mid, len(mid), nameHashLength)
	}
	if again := InterfaceRegistrationName("nodes", longNode, storageName); again != long {
		t.Errorf("not deterministic: %q != %q", again, long)
	}
	if other := InterfaceRegistrationName("nodes", longNode, "mgmt"); other == long {
		t.Errorf("different interfaces produced the same hashed name %q", long)
	}
	if primary := RegistrationName("nodes", longNode); primary == long {
		t.Errorf("primary and interface names collide: %q", long)
	}
}

func TestAdditionalSpaceNames(t *testing.T) {
	p := hrPolicy(uddiv1alpha1.RegisterModeAuto) // spaceRef corp
	p.Spec.AdditionalInterfaces = []uddiv1alpha1.AdditionalInterface{
		{Name: "a", CIDR: storageCIDR}, // defaults to corp
		{Name: "b", CIDR: storageCIDR, SpaceRef: &uddiv1alpha1.ObjectRef{Name: "storage"}}, // own
		{Name: "c", CIDR: storageCIDR, SpaceRef: &uddiv1alpha1.ObjectRef{Name: "corp"}},    // same as the policy
		{Name: "d", CIDR: storageCIDR, SpaceRef: &uddiv1alpha1.ObjectRef{Name: "backup"}},  // own
		{Name: "e", CIDR: storageCIDR, SpaceRef: &uddiv1alpha1.ObjectRef{Name: "storage"}}, // duplicate
	}

	got := AdditionalSpaceNames(p)

	if want := []string{"backup", "storage"}; !reflect.DeepEqual(got, want) {
		t.Errorf("AdditionalSpaceNames = %v, want %v", got, want)
	}
	if got := AdditionalSpaceNames(hrPolicy(uddiv1alpha1.RegisterModeAuto)); got != nil {
		t.Errorf("AdditionalSpaceNames(no interfaces) = %v, want nil", got)
	}
}

func TestAddressInPrefixFromNodeNetworkState(t *testing.T) {
	nns := nnsIfaces(
		map[string]interface{}{"name": "lo", "type": "loopback",
			"ipv4": map[string]interface{}{"address": []interface{}{map[string]interface{}{"ip": "10.20.0.1"}}}},
		nnsIface("eth0", "", []string{nodeAddr, "bad-ip"}, []string{"fe80::1"}),
		nnsIface("eth1", storageMAC, []string{"10.20.0.3"}, []string{storageIPv6}),
	)
	// A malformed list entry is skipped, not fatal.
	nns["interfaces"] = append(nns["interfaces"].([]interface{}), "not-a-map")

	cases := []struct {
		prefix    string
		wantAddr  string
		wantIface string
		wantOK    bool
	}{
		{prefix: storageCIDR, wantAddr: "10.20.0.3", wantIface: "eth1", wantOK: true}, // the loopback copy is skipped
		{prefix: storageCIDR6, wantAddr: storageIPv6, wantIface: "eth1", wantOK: true},
		{prefix: "fe80::/10", wantAddr: "fe80::1", wantIface: "eth0", wantOK: true},
		{prefix: "10.0.0.0/8", wantAddr: nodeAddr, wantIface: "eth0", wantOK: true}, // first in report order
		{prefix: "192.168.0.0/16", wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.prefix, func(t *testing.T) {
			addr, iface, ok := AddressInPrefixFromNodeNetworkState(nns, netip.MustParsePrefix(c.prefix))
			if ok != c.wantOK || (ok && (addr.String() != c.wantAddr || iface != c.wantIface)) {
				t.Errorf("AddressInPrefixFromNodeNetworkState(%s) = (%v, %q, %v), want (%s, %q, %v)",
					c.prefix, addr, iface, ok, c.wantAddr, c.wantIface, c.wantOK)
			}
		})
	}

	if _, _, ok := AddressInPrefixFromNodeNetworkState(nil, netip.MustParsePrefix(storageCIDR)); ok {
		t.Errorf("nil currentState matched")
	}
}
