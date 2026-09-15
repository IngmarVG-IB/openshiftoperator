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
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// -----------------------------------------------------------------------------------
// NormalizeMAC
// -----------------------------------------------------------------------------------

func TestNormalizeMAC(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "lower-case colon form", in: "aa:bb:cc:dd:ee:ff", want: "aa:bb:cc:dd:ee:ff"},
		{name: "upper-case colon form is lower-cased", in: "AA:BB:CC:DD:EE:FF", want: "aa:bb:cc:dd:ee:ff"},
		{name: "mixed case colon form", in: "Aa:bB:Cc:dD:eE:fF", want: "aa:bb:cc:dd:ee:ff"},
		{name: "hyphen form", in: "AA-BB-CC-DD-EE-FF", want: "aa:bb:cc:dd:ee:ff"},
		{name: "dot (cisco) form", in: "aabb.ccdd.eeff", want: "aa:bb:cc:dd:ee:ff"},
		{name: "bare 12 hex digits lower-case", in: "aabbccddeeff", want: "aa:bb:cc:dd:ee:ff"},
		{name: "bare 12 hex digits upper-case", in: "AABBCCDDEEFF", want: "aa:bb:cc:dd:ee:ff"},
		{name: "bare 12 hex digits mixed case", in: "AaBbCcDdEeFf", want: "aa:bb:cc:dd:ee:ff"},
		{name: "surrounding whitespace is trimmed", in: "  aa:bb:cc:dd:ee:ff  ", want: "aa:bb:cc:dd:ee:ff"},
		{name: "empty string is an error", in: "", wantErr: true},
		{name: "whitespace only is an error", in: "   ", wantErr: true},
		{name: "too short to be a MAC", in: "aa:bb:cc", wantErr: true},
		{name: "11 hex digits is not a bare form", in: "aabbccddeef", wantErr: true},
		{name: "13 hex digits is not a bare form", in: "aabbccddeeff0", wantErr: true},
		{name: "non-hex characters", in: "gg:hh:ii:jj:kk:ll", wantErr: true},
		{name: "bare 12 chars with a non-hex character", in: "aabbccddeegg", wantErr: true},
		{name: "EUI-64 (8 octets) is rejected", in: "02:00:5e:10:00:00:00:01", wantErr: true},
		{name: "garbage", in: "not-a-mac-address", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeMAC(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeMAC(%q): want error, got nil (result %q)", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeMAC(%q): unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeMAC(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------------
// NodeAddress
// -----------------------------------------------------------------------------------

func node(name string, addrs ...corev1.NodeAddress) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  types.UID(name + "-uid"),
		},
		Status: corev1.NodeStatus{Addresses: addrs},
	}
}

func TestNodeAddress(t *testing.T) {
	n := node("worker-1",
		corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.0.5"},
		corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "203.0.113.5"},
		corev1.NodeAddress{Type: corev1.NodeHostName, Address: "worker-1.example.com"},
	)

	cases := []struct {
		name string
		node *corev1.Node
		t    AddressType
		want string
	}{
		{name: "internal IP present", node: n, t: AddressTypeInternal, want: "10.0.0.5"},
		{name: "external IP present", node: n, t: AddressTypeExternal, want: "203.0.113.5"},
		{name: "type with no matching address", node: node("worker-2"), t: AddressTypeInternal, want: ""},
		{name: "nil node", node: nil, t: AddressTypeInternal, want: ""},
		{
			name: "first matching entry wins",
			node: node("worker-3",
				corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
				corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.0.2"},
			),
			t:    AddressTypeInternal,
			want: "10.0.0.1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NodeAddress(tc.node, tc.t); got != tc.want {
				t.Errorf("NodeAddress() = %q, want %q", got, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------------
// ViewNode
// -----------------------------------------------------------------------------------

func TestViewNode(t *testing.T) {
	defaultSrc := MACSource{}
	internalOnly := func(name string, annotations map[string]string) *corev1.Node {
		n := node(name, corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.0.9"})
		n.Annotations = annotations
		return n
	}

	cases := []struct {
		name   string
		node   *corev1.Node
		t      AddressType
		src    MACSource
		prefix string
		want   NodeView
	}{
		{
			name: "defaults with nil annotations map",
			node: internalOnly("worker-1", nil),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "10.0.0.9",
				Hostname: "worker-1",
			},
		},
		{
			name: "empty prefix defaults to DefaultAnnotationPrefix",
			node: internalOnly("worker-1", map[string]string{"uddi.infoblox.com/register": "false"}),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				OptOut:   true,
				Address:  "10.0.0.9",
				Hostname: "worker-1",
			},
		},
		{
			name:   "explicit prefix used instead of the default",
			node:   internalOnly("worker-1", map[string]string{"custom.example/register": "false", "uddi.infoblox.com/register": "true"}),
			t:      AddressTypeInternal,
			src:    defaultSrc,
			prefix: "custom.example",
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				OptOut:   true,
				Address:  "10.0.0.9",
				Hostname: "worker-1",
			},
		},
		{
			name: "register true is an explicit opt-in, same as absent",
			node: internalOnly("worker-1", map[string]string{"uddi.infoblox.com/register": "true"}),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "10.0.0.9",
				Hostname: "worker-1",
			},
		},
		{
			name: "register is trimmed",
			node: internalOnly("worker-1", map[string]string{"uddi.infoblox.com/register": " false "}),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				OptOut:   true,
				Address:  "10.0.0.9",
				Hostname: "worker-1",
			},
		},
		{
			name: "register invalid value",
			node: internalOnly("worker-1", map[string]string{"uddi.infoblox.com/register": "nope"}),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "10.0.0.9",
				Hostname: "worker-1",
				Invalid:  `uddi.infoblox.com/register: "nope" must be "true" or "false"`,
			},
		},
		{
			name: "explicit IPv4 address overrides the status address",
			node: internalOnly("worker-1", map[string]string{"uddi.infoblox.com/address": "192.0.2.10"}),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "192.0.2.10",
				Hostname: "worker-1",
			},
		},
		{
			name: "explicit IPv6 address is canonicalised",
			node: internalOnly("worker-1", map[string]string{"uddi.infoblox.com/address": "2001:DB8:0:0::1"}),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "2001:db8::1",
				Hostname: "worker-1",
			},
		},
		{
			name: "address annotation invalid",
			node: internalOnly("worker-1", map[string]string{"uddi.infoblox.com/address": "not-an-ip"}),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "10.0.0.9",
				Hostname: "worker-1",
				Invalid:  `uddi.infoblox.com/address: "not-an-ip" not a valid IPv4 or IPv6 address`,
			},
		},
		{
			name: "no status address and no annotation leaves Address empty",
			node: node("worker-4"),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-4", UID: "worker-4-uid"},
				Hostname: "worker-4",
			},
		},
		{
			name: "mac annotation at the default key, colon form",
			node: internalOnly("worker-1", map[string]string{"uddi.infoblox.com/mac": "AA:BB:CC:DD:EE:FF"}),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "10.0.0.9",
				MAC:      "aa:bb:cc:dd:ee:ff",
				MACFrom:  "annotation",
				Hostname: "worker-1",
			},
		},
		{
			name: "mac annotation, bare hex form",
			node: internalOnly("worker-1", map[string]string{"uddi.infoblox.com/mac": "aabbccddeeff"}),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "10.0.0.9",
				MAC:      "aa:bb:cc:dd:ee:ff",
				MACFrom:  "annotation",
				Hostname: "worker-1",
			},
		},
		{
			name: "mac annotation at a custom key from MACSource.NodeAnnotation",
			node: internalOnly("worker-1", map[string]string{"custom.example/mac": "aa-bb-cc-dd-ee-ff", "uddi.infoblox.com/mac": "11:22:33:44:55:66"}),
			t:    AddressTypeInternal,
			src:  MACSource{NodeAnnotation: "custom.example/mac"},
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "10.0.0.9",
				MAC:      "aa:bb:cc:dd:ee:ff",
				MACFrom:  "annotation",
				Hostname: "worker-1",
			},
		},
		{
			name: "mac annotation invalid",
			node: internalOnly("worker-1", map[string]string{"uddi.infoblox.com/mac": "not-a-mac"}),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "10.0.0.9",
				Hostname: "worker-1",
				Invalid:  `uddi.infoblox.com/mac: "not-a-mac" not a valid MAC address`,
			},
		},
		{
			name: "no mac annotation leaves MAC and MACFrom empty",
			node: internalOnly("worker-1", nil),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "10.0.0.9",
				Hostname: "worker-1",
			},
		},
		{
			name: "custom hostname is lower-cased",
			node: internalOnly("worker-1", map[string]string{"uddi.infoblox.com/hostname": "Worker-1.Example.Com"}),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "10.0.0.9",
				Hostname: "worker-1.example.com",
			},
		},
		{
			name: "hostname invalid",
			node: internalOnly("worker-1", map[string]string{"uddi.infoblox.com/hostname": "not a hostname!"}),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "10.0.0.9",
				Hostname: "worker-1",
				Invalid:  `uddi.infoblox.com/hostname: "not a hostname!" not a valid DNS name`,
			},
		},
		{
			name: "no hostname annotation defaults to the Node name",
			node: internalOnly("worker-1", nil),
			t:    AddressTypeInternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "10.0.0.9",
				Hostname: "worker-1",
			},
		},
		{
			name: "first bad annotation wins: register stops before address is checked",
			node: internalOnly("worker-1", map[string]string{
				"uddi.infoblox.com/register": "nope",
				"uddi.infoblox.com/address":  "also-not-an-ip",
			}),
			t:   AddressTypeInternal,
			src: defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "10.0.0.9",
				Hostname: "worker-1",
				Invalid:  `uddi.infoblox.com/register: "nope" must be "true" or "false"`,
			},
		},
		{
			name: "first bad annotation wins: address stops before mac is checked",
			node: internalOnly("worker-1", map[string]string{
				"uddi.infoblox.com/address": "also-not-an-ip",
				"uddi.infoblox.com/mac":     "not-a-mac-either",
			}),
			t:   AddressTypeInternal,
			src: defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "10.0.0.9",
				Hostname: "worker-1",
				Invalid:  `uddi.infoblox.com/address: "also-not-an-ip" not a valid IPv4 or IPv6 address`,
			},
		},
		{
			name: "first bad annotation wins: mac stops before hostname is checked",
			node: internalOnly("worker-1", map[string]string{
				"uddi.infoblox.com/mac":      "not-a-mac",
				"uddi.infoblox.com/hostname": "not a hostname!",
			}),
			t:   AddressTypeInternal,
			src: defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-1", UID: "worker-1-uid"},
				Address:  "10.0.0.9",
				Hostname: "worker-1",
				Invalid:  `uddi.infoblox.com/mac: "not-a-mac" not a valid MAC address`,
			},
		},
		{
			name: "external address type",
			node: node("worker-5", corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "203.0.113.9"}),
			t:    AddressTypeExternal,
			src:  defaultSrc,
			want: NodeView{
				Ref:      NodeRef{Name: "worker-5", UID: "worker-5-uid"},
				Address:  "203.0.113.9",
				Hostname: "worker-5",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ViewNode(tc.node, tc.t, tc.src, tc.prefix)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ViewNode() = %#v, want %#v", got, tc.want)
			}
		})
	}
}
