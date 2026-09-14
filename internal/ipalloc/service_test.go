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

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func boolp(b bool) *bool { return &b }

func svc(namespace, name string, annotations map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        name,
			UID:         types.UID(name + "-uid"),
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
}

// -----------------------------------------------------------------------------------
// ParsePoolRef
// -----------------------------------------------------------------------------------

func TestParsePoolRef(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    PoolRef
		wantErr bool
	}{
		{name: "subnet", in: "subnet/lb-pool", want: PoolRef{Kind: PoolSubnet, Name: "lb-pool"}},
		{name: "range", in: "range/lb-pool", want: PoolRef{Kind: PoolRange, Name: "lb-pool"}},
		{name: "addressblock", in: "addressblock/lb-pool", want: PoolRef{Kind: PoolAddressBlock, Name: "lb-pool"}},
		{name: "kind is case-insensitive", in: "Subnet/lb-pool", want: PoolRef{Kind: PoolSubnet, Name: "lb-pool"}},
		{name: "kind upper-case", in: "RANGE/lb-pool", want: PoolRef{Kind: PoolRange, Name: "lb-pool"}},
		{name: "dns subdomain name with dots", in: "subnet/my.pool-1", want: PoolRef{Kind: PoolSubnet, Name: "my.pool-1"}},
		{name: "no slash is an error", in: "subnet-lb-pool", wantErr: true},
		{name: "empty name is an error", in: "subnet/", wantErr: true},
		{name: "empty kind is an error", in: "/lb-pool", wantErr: true},
		{name: "unknown kind is an error", in: "vlan/lb-pool", wantErr: true},
		{name: "name is not a DNS-1123 subdomain", in: "subnet/Not_Valid!", wantErr: true},
		{name: "extra slash makes an invalid name", in: "subnet/lb/pool", wantErr: true},
		{name: "empty string is an error", in: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePoolRef(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParsePoolRef(%q): want error, got nil (result %#v)", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePoolRef(%q): unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParsePoolRef(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------------
// ViewService
// -----------------------------------------------------------------------------------

func TestViewService(t *testing.T) {
	metalVia := AssignVia{Mode: AssignModeAnnotation}

	cases := []struct {
		name   string
		svc    *corev1.Service
		via    AssignVia
		prefix string
		want   ServiceView
	}{
		{
			name: "defaults with no annotations and nil map",
			svc:  svc("team-a", "web", nil),
			via:  metalVia,
			want: ServiceView{
				Ref:            ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer: true,
				Hostname:       "web.team-a",
			},
		},
		{
			name: "empty prefix defaults to DefaultAnnotationPrefix",
			svc:  svc("team-a", "web", map[string]string{"uddi.infoblox.com/allocate": "true"}),
			via:  metalVia,
			want: ServiceView{
				Ref:            ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer: true,
				OptIn:          boolp(true),
				Hostname:       "web.team-a",
			},
		},
		{
			name:   "explicit prefix used instead of the default",
			svc:    svc("team-a", "web", map[string]string{"custom.example/allocate": "false", "uddi.infoblox.com/allocate": "true"}),
			via:    metalVia,
			prefix: "custom.example",
			want: ServiceView{
				Ref:            ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer: true,
				OptIn:          boolp(false),
				Hostname:       "web.team-a",
			},
		},
		{
			name: "not a LoadBalancer",
			svc: func() *corev1.Service {
				s := svc("team-a", "web", nil)
				s.Spec.Type = corev1.ServiceTypeClusterIP
				return s
			}(),
			via: metalVia,
			want: ServiceView{
				Ref:      ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				Hostname: "web.team-a",
			},
		},
		{
			name: "allocate false opts out",
			svc:  svc("team-a", "web", map[string]string{"uddi.infoblox.com/allocate": "false"}),
			via:  metalVia,
			want: ServiceView{
				Ref:            ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer: true,
				OptIn:          boolp(false),
				Hostname:       "web.team-a",
			},
		},
		{
			name: "allocate is trimmed",
			svc:  svc("team-a", "web", map[string]string{"uddi.infoblox.com/allocate": " true "}),
			via:  metalVia,
			want: ServiceView{
				Ref:            ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer: true,
				OptIn:          boolp(true),
				Hostname:       "web.team-a",
			},
		},
		{
			name: "allocate invalid value",
			svc:  svc("team-a", "web", map[string]string{"uddi.infoblox.com/allocate": "yes"}),
			via:  metalVia,
			want: ServiceView{
				Ref:            ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer: true,
				Hostname:       "web.team-a",
				Invalid:        `uddi.infoblox.com/allocate: "yes" must be "true" or "false"`,
			},
		},
		{
			name: "allocate-from overrides the pool",
			svc:  svc("team-a", "web", map[string]string{"uddi.infoblox.com/allocate-from": "range/other-pool"}),
			via:  metalVia,
			want: ServiceView{
				Ref:            ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer: true,
				Pool:           &PoolRef{Kind: PoolRange, Name: "other-pool"},
				Hostname:       "web.team-a",
			},
		},
		{
			name: "allocate-from invalid value",
			svc:  svc("team-a", "web", map[string]string{"uddi.infoblox.com/allocate-from": "vlan/other-pool"}),
			via:  metalVia,
			want: ServiceView{
				Ref:            ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer: true,
				Hostname:       "web.team-a",
				Invalid:        `uddi.infoblox.com/allocate-from: "vlan/other-pool" "vlan" is not subnet, range or addressblock`,
			},
		},
		{
			name: "explicit IPv4 address",
			svc:  svc("team-a", "web", map[string]string{"uddi.infoblox.com/address": "192.0.2.10"}),
			via:  metalVia,
			want: ServiceView{
				Ref:             ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer:  true,
				ExplicitAddress: "192.0.2.10",
				Hostname:        "web.team-a",
			},
		},
		{
			name: "explicit IPv6 address is canonicalised",
			svc:  svc("team-a", "web", map[string]string{"uddi.infoblox.com/address": "2001:DB8:0:0::1"}),
			via:  metalVia,
			want: ServiceView{
				Ref:             ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer:  true,
				ExplicitAddress: "2001:db8::1",
				Hostname:        "web.team-a",
			},
		},
		{
			name: "explicit address invalid",
			svc:  svc("team-a", "web", map[string]string{"uddi.infoblox.com/address": "not-an-ip"}),
			via:  metalVia,
			want: ServiceView{
				Ref:            ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer: true,
				Hostname:       "web.team-a",
				Invalid:        `uddi.infoblox.com/address: "not-an-ip" not a valid IPv4 or IPv6 address`,
			},
		},
		{
			name: "custom hostname",
			svc:  svc("team-a", "web", map[string]string{"uddi.infoblox.com/hostname": "Web.Example.Com"}),
			via:  metalVia,
			want: ServiceView{
				Ref:            ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer: true,
				Hostname:       "web.example.com",
			},
		},
		{
			name: "hostname invalid",
			svc:  svc("team-a", "web", map[string]string{"uddi.infoblox.com/hostname": "not a hostname!"}),
			via:  metalVia,
			want: ServiceView{
				Ref:            ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer: true,
				Hostname:       "web.team-a",
				Invalid:        `uddi.infoblox.com/hostname: "not a hostname!" not a valid DNS name`,
			},
		},
		{
			name: "first bad annotation wins: allocate-from stops before address is checked",
			svc: svc("team-a", "web", map[string]string{
				"uddi.infoblox.com/allocate-from": "vlan/pool",
				"uddi.infoblox.com/address":       "also-not-an-ip",
			}),
			via: metalVia,
			want: ServiceView{
				Ref:            ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer: true,
				Hostname:       "web.team-a",
				Invalid:        `uddi.infoblox.com/allocate-from: "vlan/pool" "vlan" is not subnet, range or addressblock`,
			},
		},
		{
			name: "current assignment from the annotation",
			svc:  svc("team-a", "web", map[string]string{"metallb.io/loadBalancerIPs": " 192.0.2.20 "}),
			via:  metalVia,
			want: ServiceView{
				Ref:               ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer:    true,
				Hostname:          "web.team-a",
				CurrentAssignment: "192.0.2.20",
			},
		},
		{
			name: "dual-stack current assignment counts as foreign (returned whole)",
			svc:  svc("team-a", "web", map[string]string{"metallb.io/loadBalancerIPs": "192.0.2.20,2001:db8::20"}),
			via:  metalVia,
			want: ServiceView{
				Ref:               ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer:    true,
				Hostname:          "web.team-a",
				CurrentAssignment: "192.0.2.20,2001:db8::20",
			},
		},
		{
			name: "current assignment from spec.loadBalancerIP",
			svc: func() *corev1.Service {
				s := svc("team-a", "web", nil)
				s.Spec.LoadBalancerIP = "192.0.2.30"
				return s
			}(),
			via: AssignVia{Mode: AssignModeLoadBalancerIP},
			want: ServiceView{
				Ref:               ServiceRef{Namespace: "team-a", Name: "web", UID: "web-uid"},
				IsLoadBalancer:    true,
				Hostname:          "web.team-a",
				CurrentAssignment: "192.0.2.30",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ViewService(tc.svc, tc.via, tc.prefix)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ViewService() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------------
// CurrentAssignment / AssignmentMatches
// -----------------------------------------------------------------------------------

func TestCurrentAssignment(t *testing.T) {
	annotationVia := AssignVia{Mode: AssignModeAnnotation, Annotation: "kube-vip.io/loadbalancerIPs"}
	lbVia := AssignVia{Mode: AssignModeLoadBalancerIP}

	cases := []struct {
		name string
		svc  *corev1.Service
		via  AssignVia
		want string
	}{
		{
			name: "annotation absent",
			svc:  svc("ns", "s", nil),
			via:  annotationVia,
			want: "",
		},
		{
			name: "annotation present and trimmed",
			svc:  svc("ns", "s", map[string]string{"kube-vip.io/loadbalancerIPs": "  10.0.0.1  "}),
			via:  annotationVia,
			want: "10.0.0.1",
		},
		{
			name: "annotation mode uses the default key when Annotation is empty",
			svc:  svc("ns", "s", map[string]string{"metallb.io/loadBalancerIPs": "10.0.0.2"}),
			via:  AssignVia{Mode: AssignModeAnnotation},
			want: "10.0.0.2",
		},
		{
			name: "dual-stack list returned whole",
			svc:  svc("ns", "s", map[string]string{"kube-vip.io/loadbalancerIPs": "10.0.0.1,2001:db8::1"}),
			via:  annotationVia,
			want: "10.0.0.1,2001:db8::1",
		},
		{
			name: "loadBalancerIP mode ignores annotations",
			svc: func() *corev1.Service {
				s := svc("ns", "s", map[string]string{"kube-vip.io/loadbalancerIPs": "10.0.0.1"})
				s.Spec.LoadBalancerIP = "10.0.0.9"
				return s
			}(),
			via:  lbVia,
			want: "10.0.0.9",
		},
		{
			name: "loadBalancerIP mode empty",
			svc:  svc("ns", "s", nil),
			via:  lbVia,
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CurrentAssignment(tc.svc, tc.via); got != tc.want {
				t.Errorf("CurrentAssignment() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAssignmentMatches(t *testing.T) {
	via := AssignVia{Mode: AssignModeAnnotation}
	s := svc("ns", "s", map[string]string{"metallb.io/loadBalancerIPs": "10.0.0.5"})

	if !AssignmentMatches(s, via, "10.0.0.5") {
		t.Error("AssignmentMatches() = false, want true for an equal value")
	}
	if AssignmentMatches(s, via, "10.0.0.6") {
		t.Error("AssignmentMatches() = true, want false for a different value")
	}
	if AssignmentMatches(s, via, "") {
		t.Error("AssignmentMatches() = true, want false comparing against empty")
	}
}

// -----------------------------------------------------------------------------------
// AssignmentPatch / ClearAssignmentPatch
// -----------------------------------------------------------------------------------

func TestAssignmentPatch(t *testing.T) {
	cases := []struct {
		name string
		via  AssignVia
		want string
	}{
		{
			name: "annotation mode, default key",
			via:  AssignVia{Mode: AssignModeAnnotation},
			want: `{"metadata":{"annotations":{"metallb.io/loadBalancerIPs":"192.0.2.10"}}}`,
		},
		{
			name: "annotation mode, explicit key",
			via:  AssignVia{Mode: AssignModeAnnotation, Annotation: "kube-vip.io/loadbalancerIPs"},
			want: `{"metadata":{"annotations":{"kube-vip.io/loadbalancerIPs":"192.0.2.10"}}}`,
		},
		{
			name: "loadBalancerIP mode",
			via:  AssignVia{Mode: AssignModeLoadBalancerIP},
			want: `{"spec":{"loadBalancerIP":"192.0.2.10"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(AssignmentPatch(tc.via, "192.0.2.10"))
			if got != tc.want {
				t.Errorf("AssignmentPatch() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestClearAssignmentPatch(t *testing.T) {
	cases := []struct {
		name string
		via  AssignVia
		want string
	}{
		{
			name: "annotation mode nulls the key",
			via:  AssignVia{Mode: AssignModeAnnotation},
			want: `{"metadata":{"annotations":{"metallb.io/loadBalancerIPs":null}}}`,
		},
		{
			name: "annotation mode explicit key",
			via:  AssignVia{Mode: AssignModeAnnotation, Annotation: "io.cilium/lb-ipam-ips"},
			want: `{"metadata":{"annotations":{"io.cilium/lb-ipam-ips":null}}}`,
		},
		{
			name: "loadBalancerIP mode empties the field",
			via:  AssignVia{Mode: AssignModeLoadBalancerIP},
			want: `{"spec":{"loadBalancerIP":""}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(ClearAssignmentPatch(tc.via))
			if got != tc.want {
				t.Errorf("ClearAssignmentPatch() = %s, want %s", got, tc.want)
			}
		})
	}
}
