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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEndpointsFromNode(t *testing.T) {
	nodeSrc := func(name string) SourceRef { return SourceRef{Kind: SourceNode, Name: name, UID: "node-uid"} }

	internalV4 := corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.0.5"}
	internalV6 := corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "fd00::5"}
	externalV4 := corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "203.0.113.5"}
	externalV6 := corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "2001:db8::5"}
	hostnameAddr := corev1.NodeAddress{Type: corev1.NodeHostName, Address: "worker-0.example.com"}

	const fqdn = "worker-0.cluster.example.com"
	const noHostMsg = "the Node name has no dot and there is no uddi.infoblox.com/hostname annotation"
	const noTargetMsg = "no target could be derived for the configured mode"

	cases := []struct {
		name        string
		nodeName    string
		addresses   []corev1.NodeAddress
		annotations map[string]string
		opts        SourceOptions
		wantEps     []Endpoint
		wantSkips   []Skip
	}{
		{
			name:      "a short name with no hostname annotation has no hostname",
			nodeName:  "worker-0",
			addresses: []corev1.NodeAddress{internalV4},
			wantSkips: []Skip{{Source: nodeSrc("worker-0"), Reason: SkipNoHostname, Message: noHostMsg}},
		},
		{
			name:      "a dotted Node name is the hostname and InternalIP is the default target",
			nodeName:  fqdn,
			addresses: []corev1.NodeAddress{internalV4, externalV4, hostnameAddr},
			wantEps: []Endpoint{{
				Source: nodeSrc(fqdn), Hostname: fqdn + ".",
				Targets: []Target{{Type: TargetA, Value: "10.0.0.5"}},
			}},
		},
		{
			name:      "a dual-stack Node yields an A and an AAAA target",
			nodeName:  fqdn,
			addresses: []corev1.NodeAddress{internalV4, internalV6},
			wantEps: []Endpoint{{
				Source: nodeSrc(fqdn), Hostname: fqdn + ".",
				Targets: []Target{
					{Type: TargetA, Value: "10.0.0.5"},
					{Type: TargetAAAA, Value: "fd00::5"},
				},
			}},
		},
		{
			name:      "nodeAddressType ExternalIP selects the external addresses, dual-stack",
			nodeName:  fqdn,
			addresses: []corev1.NodeAddress{internalV4, internalV6, externalV4, externalV6},
			opts:      SourceOptions{NodeAddressType: NodeAddressExternal},
			wantEps: []Endpoint{{
				Source: nodeSrc(fqdn), Hostname: fqdn + ".",
				Targets: []Target{
					{Type: TargetA, Value: "203.0.113.5"},
					{Type: TargetAAAA, Value: "2001:db8::5"},
				},
			}},
		},
		{
			name:      "nodeAddressType InternalIP written out matches the default",
			nodeName:  fqdn,
			addresses: []corev1.NodeAddress{internalV4, externalV4},
			opts:      SourceOptions{NodeAddressType: NodeAddressInternal},
			wantEps: []Endpoint{{
				Source: nodeSrc(fqdn), Hostname: fqdn + ".",
				Targets: []Target{{Type: TargetA, Value: "10.0.0.5"}},
			}},
		},
		{
			name:      "several addresses of the type each become a target, duplicates dropped",
			nodeName:  fqdn,
			addresses: []corev1.NodeAddress{internalV4, {Type: corev1.NodeInternalIP, Address: "10.0.0.6"}, internalV4},
			wantEps: []Endpoint{{
				Source: nodeSrc(fqdn), Hostname: fqdn + ".",
				Targets: []Target{
					{Type: TargetA, Value: "10.0.0.5"},
					{Type: TargetA, Value: "10.0.0.6"},
				},
			}},
		},
		{
			name:      "a value that is not an IP is ignored, not turned into a CNAME",
			nodeName:  fqdn,
			addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "not-an-ip.example.com"}},
			wantSkips: []Skip{{Source: nodeSrc(fqdn), Hostname: fqdn + ".", Reason: SkipNoTarget, Message: noTargetMsg}},
		},
		{
			name:      "no address of the selected type is NoTarget",
			nodeName:  fqdn,
			addresses: []corev1.NodeAddress{internalV4},
			opts:      SourceOptions{NodeAddressType: NodeAddressExternal},
			wantSkips: []Skip{{Source: nodeSrc(fqdn), Hostname: fqdn + ".", Reason: SkipNoTarget, Message: noTargetMsg}},
		},
		{
			name:     "a short name with the hostname annotation publishes the annotated names",
			nodeName: "worker-0",
			annotations: map[string]string{
				"uddi.infoblox.com/hostname": "node0.example.com, node0.alt.example.com",
			},
			addresses: []corev1.NodeAddress{internalV4},
			wantEps: []Endpoint{
				{Source: nodeSrc("worker-0"), Hostname: "node0.alt.example.com.", Targets: []Target{{Type: TargetA, Value: "10.0.0.5"}}},
				{Source: nodeSrc("worker-0"), Hostname: "node0.example.com.", Targets: []Target{{Type: TargetA, Value: "10.0.0.5"}}},
			},
		},
		{
			name:     "the hostname annotation is added to a dotted Node name",
			nodeName: fqdn,
			annotations: map[string]string{
				"uddi.infoblox.com/hostname": "alias.example.com",
			},
			addresses: []corev1.NodeAddress{internalV4},
			wantEps: []Endpoint{
				{Source: nodeSrc(fqdn), Hostname: "alias.example.com.", Targets: []Target{{Type: TargetA, Value: "10.0.0.5"}}},
				{Source: nodeSrc(fqdn), Hostname: fqdn + ".", Targets: []Target{{Type: TargetA, Value: "10.0.0.5"}}},
			},
		},
		{
			name:        "the target annotation replaces the Node addresses",
			nodeName:    fqdn,
			addresses:   []corev1.NodeAddress{internalV4},
			annotations: map[string]string{"uddi.infoblox.com/target": "198.51.100.7,lb.example.com"},
			wantEps: []Endpoint{{
				Source: nodeSrc(fqdn), Hostname: fqdn + ".",
				Targets: []Target{
					{Type: TargetA, Value: "198.51.100.7"},
					{Type: TargetCNAME, Value: "lb.example.com."},
				},
			}},
		},
		{
			name:      "mode A keeps the address targets",
			nodeName:  fqdn,
			addresses: []corev1.NodeAddress{internalV4, internalV6},
			opts:      SourceOptions{Mode: TargetModeA},
			wantEps: []Endpoint{{
				Source: nodeSrc(fqdn), Hostname: fqdn + ".",
				Targets: []Target{
					{Type: TargetA, Value: "10.0.0.5"},
					{Type: TargetAAAA, Value: "fd00::5"},
				},
			}},
		},
		{
			name:      "mode CNAME filters out the addresses leaving no target",
			nodeName:  fqdn,
			addresses: []corev1.NodeAddress{internalV4},
			opts:      SourceOptions{Mode: TargetModeCNAME},
			wantSkips: []Skip{{Source: nodeSrc(fqdn), Hostname: fqdn + ".", Reason: SkipNoTarget, Message: noTargetMsg}},
		},
		{
			name:        "ttl annotation sets the endpoint TTL",
			nodeName:    fqdn,
			addresses:   []corev1.NodeAddress{internalV4},
			annotations: map[string]string{"uddi.infoblox.com/ttl": "120"},
			wantEps: []Endpoint{{
				Source: nodeSrc(fqdn), Hostname: fqdn + ".",
				Targets: []Target{{Type: TargetA, Value: "10.0.0.5"}},
				TTL:     int32p(120),
			}},
		},
		{
			name:        "publish false excludes the Node",
			nodeName:    fqdn,
			addresses:   []corev1.NodeAddress{internalV4},
			annotations: map[string]string{"uddi.infoblox.com/publish": "false"},
			wantSkips:   []Skip{{Source: nodeSrc(fqdn), Reason: SkipExcluded, Message: "uddi.infoblox.com/publish is false"}},
		},
		{
			name:        "publish false wins over a hostname annotation",
			nodeName:    "worker-0",
			addresses:   []corev1.NodeAddress{internalV4},
			annotations: map[string]string{"uddi.infoblox.com/publish": "false", "uddi.infoblox.com/hostname": "node0.example.com"},
			wantSkips:   []Skip{{Source: nodeSrc("worker-0"), Reason: SkipExcluded, Message: "uddi.infoblox.com/publish is false"}},
		},
		{
			name:        "malformed hostname annotation is invalid, not NoHostname",
			nodeName:    "worker-0",
			addresses:   []corev1.NodeAddress{internalV4},
			annotations: map[string]string{"uddi.infoblox.com/hostname": "bad_host!"},
			wantSkips: []Skip{{
				Source: nodeSrc("worker-0"), Reason: SkipInvalidAnnotation,
				Message: `uddi.infoblox.com/hostname: "bad_host!" is not a valid DNS name`,
			}},
		},
		{
			name:      "external-dns hostname is read only with the compatibility option",
			nodeName:  "worker-0",
			addresses: []corev1.NodeAddress{internalV4},
			annotations: map[string]string{
				ExternalDNSHostname: "node0.example.com",
			},
			opts: SourceOptions{ExternalDNS: true},
			wantEps: []Endpoint{{
				Source: nodeSrc("worker-0"), Hostname: "node0.example.com.",
				Targets: []Target{{Type: TargetA, Value: "10.0.0.5"}},
			}},
		},
		{
			name:      "external-dns hostname is ignored without the compatibility option",
			nodeName:  "worker-0",
			addresses: []corev1.NodeAddress{internalV4},
			annotations: map[string]string{
				ExternalDNSHostname: "node0.example.com",
			},
			wantSkips: []Skip{{Source: nodeSrc("worker-0"), Reason: SkipNoHostname, Message: noHostMsg}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: tc.nodeName, UID: "node-uid", Annotations: tc.annotations},
				Status:     corev1.NodeStatus{Addresses: tc.addresses},
			}
			gotEps, gotSkips := EndpointsFromNode(n, tc.opts)
			assertResult(t, gotEps, gotSkips, tc.wantEps, tc.wantSkips)
		})
	}
}

// TestEndpointsFromNode_UppercaseNameIsCanonical keeps the hostname lower-case with one
// trailing dot like every other source.
func TestEndpointsFromNode_UppercaseNameIsCanonical(t *testing.T) {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "Worker-0.Example.COM"},
		Status:     corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.5"}}},
	}
	eps, skips := EndpointsFromNode(n, SourceOptions{})
	if len(skips) != 0 || len(eps) != 1 || eps[0].Hostname != "worker-0.example.com." {
		t.Fatalf("got endpoints %#v skips %#v, want one endpoint for worker-0.example.com.", eps, skips)
	}
}
