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

import "testing"

// realisticCurrentState mimics an unstructured status.currentState the way it would
// actually decode from a NodeNetworkState: a loopback, a physical NIC enslaved to a bond,
// the bond itself (with the address), and a VLAN sub-interface with no address at all.
// Field names and shapes follow the real report at
// https://raw.githubusercontent.com/nmstate/kubernetes-nmstate/main/docs/content/user-guide/101-reporting.md.
func realisticCurrentState() map[string]interface{} {
	return map[string]interface{}{
		"interfaces": []interface{}{
			map[string]interface{}{
				"name":  "lo",
				"type":  "loopback",
				"state": "up",
				// A real loopback carries an all-zero MAC and 127.0.0.1/::1, both of
				// which must never be treated as a match.
				"mac-address": "00:00:00:00:00:00",
				"ipv4": map[string]interface{}{
					"address": []interface{}{
						map[string]interface{}{"ip": "127.0.0.1", "prefix-length": int64(8)},
					},
				},
				"ipv6": map[string]interface{}{
					"address": []interface{}{
						map[string]interface{}{"ip": "::1", "prefix-length": int64(128)},
					},
				},
			},
			map[string]interface{}{
				"name":        "eth0",
				"type":        "ethernet",
				"state":       "up",
				"mac-address": "52:55:00:D1:55:01",
				// eth0 is enslaved to the bond and carries no address of its own.
			},
			map[string]interface{}{
				"name":        "bond0",
				"type":        "bond",
				"state":       "up",
				"mac-address": "AA:BB:CC:DD:EE:01",
				"ipv4": map[string]interface{}{
					"address": []interface{}{
						map[string]interface{}{"ip": "192.168.66.101", "prefix-length": int64(24)},
					},
				},
				"ipv6": map[string]interface{}{
					"address": []interface{}{
						map[string]interface{}{"ip": "2001:db8::66:101", "prefix-length": int64(64)},
					},
				},
			},
			map[string]interface{}{
				"name":        "bond0.100",
				"type":        "vlan",
				"state":       "up",
				"mac-address": "AA:BB:CC:DD:EE:01",
				// A VLAN sub-interface with no address configured yet.
			},
			map[string]interface{}{
				"name":  "eth1",
				"type":  "ethernet",
				"state": "up",
				// No mac-address reported at all (interface still coming up).
				"ipv4": map[string]interface{}{
					"address": []interface{}{
						map[string]interface{}{"ip": "192.168.67.5", "prefix-length": int64(24)},
					},
				},
			},
		},
	}
}

func TestMACFromNodeNetworkState(t *testing.T) {
	cases := []struct {
		name      string
		state     map[string]interface{}
		iface     string
		address   string
		wantMAC   string
		wantIface string
		wantOK    bool
	}{
		{
			name:      "match by explicit interface name",
			state:     realisticCurrentState(),
			iface:     "bond0",
			wantMAC:   "aa:bb:cc:dd:ee:01",
			wantIface: "bond0",
			wantOK:    true,
		},
		{
			name:      "match by explicit interface name, vlan",
			state:     realisticCurrentState(),
			iface:     "bond0.100",
			wantMAC:   "aa:bb:cc:dd:ee:01",
			wantIface: "bond0.100",
			wantOK:    true,
		},
		{
			name:      "match by IPv4 address on the bond",
			state:     realisticCurrentState(),
			address:   "192.168.66.101",
			wantMAC:   "aa:bb:cc:dd:ee:01",
			wantIface: "bond0",
			wantOK:    true,
		},
		{
			name:      "match by IPv6 address, different textual form",
			state:     realisticCurrentState(),
			address:   "2001:DB8:0:0:0:0:66:101",
			wantMAC:   "aa:bb:cc:dd:ee:01",
			wantIface: "bond0",
			wantOK:    true,
		},
		{
			name:    "loopback is never matched by address even though it carries one",
			state:   realisticCurrentState(),
			address: "127.0.0.1",
			wantOK:  false,
		},
		{
			name:   "loopback is never matched by explicit interface name",
			state:  realisticCurrentState(),
			iface:  "lo",
			wantOK: false,
		},
		{
			name:   "explicit interface name with no mac-address",
			state:  realisticCurrentState(),
			iface:  "eth1",
			wantOK: false,
		},
		{
			name:    "address matches an interface with no mac-address",
			state:   realisticCurrentState(),
			address: "192.168.67.5",
			wantOK:  false,
		},
		{
			name:   "explicit interface name not present",
			state:  realisticCurrentState(),
			iface:  "eth9",
			wantOK: false,
		},
		{
			name:    "address not present on any interface",
			state:   realisticCurrentState(),
			address: "10.10.10.10",
			wantOK:  false,
		},
		{
			name:    "no interface field at all",
			state:   map[string]interface{}{},
			address: "192.168.66.101",
			wantOK:  false,
		},
		{
			name:    "nil state",
			state:   nil,
			address: "192.168.66.101",
			wantOK:  false,
		},
		{
			name:   "empty iface and empty address both unset",
			state:  realisticCurrentState(),
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mac, matchedIface, ok := MACFromNodeNetworkState(tc.state, tc.iface, tc.address)
			if ok != tc.wantOK {
				t.Fatalf("MACFromNodeNetworkState() ok = %v, want %v (mac=%q iface=%q)", ok, tc.wantOK, mac, matchedIface)
			}
			if !tc.wantOK {
				return
			}
			if mac != tc.wantMAC {
				t.Errorf("MACFromNodeNetworkState() mac = %q, want %q", mac, tc.wantMAC)
			}
			if matchedIface != tc.wantIface {
				t.Errorf("MACFromNodeNetworkState() matchedInterface = %q, want %q", matchedIface, tc.wantIface)
			}
		})
	}
}
