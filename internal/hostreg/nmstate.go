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

// This file implements DESIGN.md section 4.7 HostRegistrationPolicy pipeline step 3: the
// NodeNetworkState half of MAC resolution. currentState is read as unstructured data (a
// map[string]interface{} produced by decoding JSON/YAML, e.g. via
// internal/nmstateapi.CurrentState), so this package takes no dependency on the NMState Go
// module (github.com/nmstate/kubernetes-nmstate).
package hostreg

import (
	"net/netip"
	"strings"
)

// MACFromNodeNetworkState reads the mac-address of one interface out of the unstructured
// status.currentState of an nmstate.io/v1beta1 NodeNetworkState. The field names below —
// interfaces (a list), each with name, type, state and mac-address, plus ipv4.address[]
// and ipv6.address[] entries shaped {ip, prefix-length} — are verified against a real
// report in the NMState user guide and the interface schema catalogue:
//
//   - https://raw.githubusercontent.com/nmstate/kubernetes-nmstate/main/docs/content/user-guide/101-reporting.md
//     (a full `kubectl get nns <node> -o yaml`; e.g. an interface reads
//     `name: eth0`, `type: ethernet`, `state: up`, `mac-address: 52:55:00:D1:55:01`, with
//     `ipv4: {address: [{ip: 192.168.66.101, prefix-length: 24}], ...}`)
//   - https://nmstate.io/examples.html (the general interface state schema, confirming the
//     same ipv4/ipv6 address shape for every interface type)
//   - the `loopback` type itself is confirmed by github.com/nmstate/nmstate's
//     rust/src/lib/ifaces/loopback.rs doc comment ("Example yaml outpuf ... type: loopback")
//
// When iface is non-empty, that interface's mac-address is returned (matchedInterface ==
// iface). Otherwise the interfaces are searched, in report order, for the one whose ipv4
// or ipv6 address list contains address; addresses are compared with net/netip so
// different textual forms of the same address (e.g. "2001:db8::1" and "2001:DB8:0:0::1")
// still match. Interfaces of type "loopback" are always skipped, whichever way the search
// is performed, since a loopback MAC (or its 127.0.0.1/::1 address) can never be the
// Node's registered identity. ok is false when nothing matched, or the matched interface
// has no usable mac-address.
func MACFromNodeNetworkState(currentState map[string]interface{}, iface string, address string) (mac string, matchedInterface string, ok bool) {
	interfaces, _ := currentState["interfaces"].([]interface{})

	wantAddr, haveWantAddr := netip.Addr{}, false
	if iface == "" {
		if a, err := netip.ParseAddr(strings.TrimSpace(address)); err == nil {
			wantAddr, haveWantAddr = a, true
		}
	}

	for _, rawIface := range interfaces {
		i, isMap := rawIface.(map[string]interface{})
		if !isMap {
			continue
		}
		if typ, _ := i["type"].(string); typ == "loopback" {
			continue
		}
		name, _ := i["name"].(string)

		switch {
		case iface != "":
			if name != iface {
				continue
			}
			return macAddressOf(i, name)
		case haveWantAddr:
			if interfaceHasAddress(i, wantAddr) {
				return macAddressOf(i, name)
			}
		}
	}

	return "", "", false
}

// AddressInPrefixFromNodeNetworkState returns the first address, in report order, of any
// non-loopback interface in the unstructured status.currentState that lies inside prefix,
// together with the name of the interface carrying it. Within an interface the ipv4 list
// is searched before the ipv6 list; addresses are parsed with net/netip, so IPv4 and IPv6
// prefixes both work and an address of the other family never matches. ok is false when no
// interface has an address inside prefix.
func AddressInPrefixFromNodeNetworkState(currentState map[string]interface{}, prefix netip.Prefix) (addr netip.Addr, iface string, ok bool) {
	interfaces, _ := currentState["interfaces"].([]interface{})
	for _, rawIface := range interfaces {
		i, isMap := rawIface.(map[string]interface{})
		if !isMap {
			continue
		}
		if typ, _ := i["type"].(string); typ == "loopback" {
			continue
		}
		name, _ := i["name"].(string)
		for _, family := range []string{"ipv4", "ipv6"} {
			if a, found := familyAddressInPrefix(i, family, prefix); found {
				return a, name, true
			}
		}
	}
	return netip.Addr{}, "", false
}

// familyAddressInPrefix returns the first address in interface i's ipv4 or ipv6 block
// (family) that lies inside prefix.
func familyAddressInPrefix(i map[string]interface{}, family string, prefix netip.Prefix) (netip.Addr, bool) {
	block, isMap := i[family].(map[string]interface{})
	if !isMap {
		return netip.Addr{}, false
	}
	entries, isSlice := block["address"].([]interface{})
	if !isSlice {
		return netip.Addr{}, false
	}
	for _, rawEntry := range entries {
		entry, isMap := rawEntry.(map[string]interface{})
		if !isMap {
			continue
		}
		ipStr, _ := entry["ip"].(string)
		got, err := netip.ParseAddr(strings.TrimSpace(ipStr))
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

// macAddressOf reads and normalises an interface's mac-address field.
func macAddressOf(i map[string]interface{}, name string) (mac string, matchedInterface string, ok bool) {
	raw, _ := i["mac-address"].(string)
	normalised, err := NormalizeMAC(raw)
	if err != nil {
		return "", "", false
	}
	return normalised, name, true
}

// interfaceHasAddress reports whether an interface's ipv4 or ipv6 address list contains
// want.
func interfaceHasAddress(i map[string]interface{}, want netip.Addr) bool {
	return familyHasAddress(i, "ipv4", want) || familyHasAddress(i, "ipv6", want)
}

// familyHasAddress reports whether interface i's ipv4 or ipv6 block (family) lists want
// among its address[].ip entries.
func familyHasAddress(i map[string]interface{}, family string, want netip.Addr) bool {
	block, isMap := i[family].(map[string]interface{})
	if !isMap {
		return false
	}
	entries, isSlice := block["address"].([]interface{})
	if !isSlice {
		return false
	}
	for _, rawEntry := range entries {
		entry, isMap := rawEntry.(map[string]interface{})
		if !isMap {
			continue
		}
		ipStr, _ := entry["ip"].(string)
		got, err := netip.ParseAddr(strings.TrimSpace(ipStr))
		if err != nil {
			continue
		}
		if got == want {
			return true
		}
	}
	return false
}
