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
	"strings"
	"testing"
)

// MACs used by the chain tests, one per source so the winner is obvious.
const (
	macAnnotation = "aa:aa:aa:aa:aa:01"
	macNMState    = "bb:bb:bb:bb:bb:02"
	macMetal3NIC  = "cc:cc:cc:cc:cc:03"
	macMetal3Boot = "dd:dd:dd:dd:dd:04"
)

// fakeHardware is a Metal3Hardware over two maps: NIC MACs by IP and bootMACAddress by
// inspected hostname, with the same lookup rule metal3api.Index implements.
type fakeHardware struct {
	nics map[netip.Addr]string
	boot map[string]string
}

func (f fakeHardware) MACForNode(address, nodeName string) (string, string, bool) {
	if ip, err := netip.ParseAddr(address); err == nil {
		if mac, _, ok := f.MACForIP(ip); ok {
			return mac, "bmh", true
		}
	}
	if mac, ok := f.boot[nodeName]; ok {
		return mac, "bmh", true
	}
	return "", "", false
}

func (f fakeHardware) MACForIP(ip netip.Addr) (string, string, bool) {
	mac, ok := f.nics[ip]
	return mac, "bmh", ok
}

// nnsWith builds a NodeNetworkState currentState with one ethernet interface, eth0.
func nnsWith(mac, ip string) map[string]interface{} {
	return map[string]interface{}{"interfaces": []interface{}{
		map[string]interface{}{
			"name": "eth0", "type": "ethernet", "state": "up", "mac-address": mac,
			"ipv4": map[string]interface{}{"address": []interface{}{
				map[string]interface{}{"ip": ip, "prefix-length": int64(24)},
			}},
		},
	}}
}

// hwWith is a Metal3 inventory with one NIC (macMetal3NIC at nicIP) and one host whose
// inspected hostname is hostname (bootMACAddress macMetal3Boot).
func hwWith(nicIP, hostname string) fakeHardware {
	return fakeHardware{
		nics: map[netip.Addr]string{netip.MustParseAddr(nicIP): macMetal3NIC},
		boot: map[string]string{hostname: macMetal3Boot},
	}
}

func TestMACSourceHelpers(t *testing.T) {
	cases := []struct {
		name           string
		src            MACSource
		wantNMState    bool
		wantMetal3     bool
		wantMetal3Name string
	}{
		{"nothing enabled", MACSource{}, false, false, DefaultMetal3Namespace},
		{"enabled but API absent", MACSource{NMState: true, Metal3: true}, false, false, DefaultMetal3Namespace},
		{"API present but disabled", MACSource{NMStateAvailable: true, Metal3Available: true}, false, false, DefaultMetal3Namespace},
		{"nmstate on", MACSource{NMState: true, NMStateAvailable: true}, true, false, DefaultMetal3Namespace},
		{"metal3 on with namespace", MACSource{Metal3: true, Metal3Available: true, Metal3Namespace: "metal3"}, false, true, "metal3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.src.UseNMState(); got != c.wantNMState {
				t.Errorf("UseNMState() = %v, want %v", got, c.wantNMState)
			}
			if got := c.src.UseMetal3(); got != c.wantMetal3 {
				t.Errorf("UseMetal3() = %v, want %v", got, c.wantMetal3)
			}
			if got := c.src.Metal3NamespaceOrDefault(); got != c.wantMetal3Name {
				t.Errorf("Metal3NamespaceOrDefault() = %q, want %q", got, c.wantMetal3Name)
			}
		})
	}
	if DefaultMetal3Namespace != "openshift-machine-api" {
		t.Errorf("DefaultMetal3Namespace = %q, want openshift-machine-api", DefaultMetal3Namespace)
	}
}

func TestResolveMAC_Chain(t *testing.T) {
	allOn := MACSource{NMState: true, NMStateAvailable: true, Metal3: true, Metal3Available: true}
	nmstateOnly := MACSource{NMState: true, NMStateAvailable: true}
	metal3Only := MACSource{Metal3: true, Metal3Available: true}

	// Every source knows the Node under its own MAC.
	nns := nnsWith(strings.ToUpper(macNMState), nodeAddr)
	hw := hwWith(nodeAddr, nodeWorker1)

	cases := []struct {
		name        string
		view        NodeView
		src         MACSource
		nns         map[string]interface{}
		hw          Metal3Hardware
		wantMAC     string
		wantMACFrom string
	}{
		{
			name: "annotation alone", src: MACSource{},
			view:    NodeView{MAC: macAnnotation, MACFrom: MACFromAnnotation},
			wantMAC: macAnnotation, wantMACFrom: MACFromAnnotation,
		},
		{
			name: "annotation beats every other source", src: allOn, nns: nns, hw: hw,
			view:    NodeView{MAC: macAnnotation, MACFrom: MACFromAnnotation},
			wantMAC: macAnnotation, wantMACFrom: MACFromAnnotation,
		},
		{
			name: "nmstate alone", src: nmstateOnly, nns: nns, hw: hw,
			wantMAC: macNMState, wantMACFrom: MACFromNMState,
		},
		{
			name: "metal3 alone, by NIC IP", src: metal3Only, nns: nns, hw: hw,
			wantMAC: macMetal3NIC, wantMACFrom: MACFromMetal3,
		},
		{
			name: "metal3 alone, by hostname bootMAC", src: metal3Only, hw: hwWith("10.9.9.9", nodeWorker1),
			wantMAC: macMetal3Boot, wantMACFrom: MACFromMetal3,
		},
		{
			name: "nmstate beats metal3 when both know the Node", src: allOn, nns: nns, hw: hw,
			wantMAC: macNMState, wantMACFrom: MACFromNMState,
		},
		{
			name: "metal3 answers when nmstate has no matching interface", src: allOn,
			nns:     nnsWith(macNMState, "192.168.1.1"),
			hw:      hw,
			wantMAC: macMetal3NIC, wantMACFrom: MACFromMetal3,
		},
		{
			name: "metal3 answers when the Node has no NodeNetworkState", src: allOn, nns: nil, hw: hw,
			wantMAC: macMetal3NIC, wantMACFrom: MACFromMetal3,
		},
		{
			name: "nmstate disabled in the spec falls through to metal3",
			src:  MACSource{NMStateAvailable: true, Metal3: true, Metal3Available: true}, nns: nns, hw: hw,
			wantMAC: macMetal3NIC, wantMACFrom: MACFromMetal3,
		},
		{
			name: "nmstate API absent falls through to metal3",
			src:  MACSource{NMState: true, Metal3: true, Metal3Available: true}, nns: nns, hw: hw,
			wantMAC: macMetal3NIC, wantMACFrom: MACFromMetal3,
		},
		{
			name: "metal3 disabled in the spec leaves nmstate", src: MACSource{NMState: true, NMStateAvailable: true, Metal3Available: true},
			nns: nnsWith(macNMState, "192.168.1.1"), hw: hw,
			wantMAC: "", wantMACFrom: "",
		},
		{
			name: "metal3 API absent leaves nmstate", src: MACSource{NMState: true, NMStateAvailable: true, Metal3: true},
			nns: nnsWith(macNMState, "192.168.1.1"), hw: hw,
			wantMAC: "", wantMACFrom: "",
		},
		{
			name: "nmstate interface named in the spec", src: MACSource{NMState: true, NMStateAvailable: true, NMStateInterface: "bond0"},
			nns: map[string]interface{}{"interfaces": []interface{}{
				map[string]interface{}{"name": "eth0", "type": "ethernet", "mac-address": macNMState, "ipv4": map[string]interface{}{"address": []interface{}{
					map[string]interface{}{"ip": nodeAddr},
				}}},
				map[string]interface{}{"name": "bond0", "type": "bond", "mac-address": "ee:ee:ee:ee:ee:05"},
			}},
			wantMAC: "ee:ee:ee:ee:ee:05", wantMACFrom: MACFromNMState,
		},
		{
			name: "no source knows the Node", src: allOn,
			nns: nnsWith(macNMState, "192.168.1.1"), hw: hwWith("10.9.9.9", "other"),
			wantMAC: "", wantMACFrom: "",
		},
		{
			name: "no inputs at all", src: allOn,
			wantMAC: "", wantMACFrom: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			view := c.view
			view.Ref = NodeRef{Name: nodeWorker1}
			view.Address = nodeAddr

			got := ResolveMAC(view, c.src, c.nns, c.hw)

			if got.MAC != c.wantMAC || got.MACFrom != c.wantMACFrom {
				t.Errorf("ResolveMAC() MAC/MACFrom = %q/%q, want %q/%q", got.MAC, got.MACFrom, c.wantMAC, c.wantMACFrom)
			}
			if got.Address != nodeAddr || got.Ref.Name != nodeWorker1 {
				t.Errorf("ResolveMAC() changed fields other than MAC/MACFrom: %+v", got)
			}
		})
	}
}

func TestResolveMAC_MetalByHostnameWithoutAddress(t *testing.T) {
	// A Node with no address can still be matched by hostname through Metal3.
	src := MACSource{Metal3: true, Metal3Available: true}
	hw := hwWith("10.9.9.9", nodeWorker1)

	got := ResolveMAC(NodeView{Ref: NodeRef{Name: nodeWorker1}}, src, nil, hw)

	if got.MAC != macMetal3Boot || got.MACFrom != MACFromMetal3 {
		t.Errorf("ResolveMAC() = %q/%q, want %q/%q", got.MAC, got.MACFrom, macMetal3Boot, MACFromMetal3)
	}
}

func TestMACFromValues(t *testing.T) {
	// These strings are shown to users (and asserted by the controller's tests), so they
	// are part of the contract.
	if MACFromAnnotation != "annotation" || MACFromNMState != "nmstate" || MACFromMetal3 != "metal3" {
		t.Errorf("MACFrom constants = %q/%q/%q, want annotation/nmstate/metal3", MACFromAnnotation, MACFromNMState, MACFromMetal3)
	}
}
