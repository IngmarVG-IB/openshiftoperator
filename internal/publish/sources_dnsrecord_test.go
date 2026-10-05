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
	"math"
	"testing"

	operatoringressv1 "github.com/openshift/api/operatoringress/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEndpointsFromDNSRecord(t *testing.T) {
	src := SourceRef{Kind: SourceDNSRecord, Namespace: "openshift-ingress-operator", Name: "default-wildcard", UID: "dnsrecord-uid"}

	const wildcard = "*.apps.cluster.example.com."
	const noTargetMsg = "no target could be derived for the configured mode"

	cases := []struct {
		name        string
		spec        operatoringressv1.DNSRecordSpec
		annotations map[string]string
		opts        SourceOptions
		wantEps     []Endpoint
		wantSkips   []Skip
	}{
		{
			name: "the default router wildcard on bare metal: A record, wildcard hostname, record TTL",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:             wildcard,
				Targets:             []string{"192.0.2.10"},
				RecordType:          operatoringressv1.ARecordType,
				RecordTTL:           30,
				DNSManagementPolicy: operatoringressv1.ManagedDNS,
			},
			wantEps: []Endpoint{{
				Source: src, Hostname: wildcard,
				Targets: []Target{{Type: TargetA, Value: "192.0.2.10"}},
				TTL:     int32p(30),
			}},
		},
		{
			name: "the default router wildcard on a cloud: CNAME to the load balancer, trailing dot added",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"a1b2c3.elb.us-east-1.amazonaws.com"},
				RecordType: operatoringressv1.CNAMERecordType,
				RecordTTL:  30,
			},
			wantEps: []Endpoint{{
				Source: src, Hostname: wildcard,
				Targets: []Target{{Type: TargetCNAME, Value: "a1b2c3.elb.us-east-1.amazonaws.com."}},
				TTL:     int32p(30),
			}},
		},
		{
			name: "a CNAME target that already ends in a dot stays canonical",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"Router.Example.COM."},
				RecordType: operatoringressv1.CNAMERecordType,
			},
			wantEps: []Endpoint{{
				Source: src, Hostname: wildcard,
				Targets: []Target{{Type: TargetCNAME, Value: "router.example.com."}},
			}},
		},
		{
			name: "AAAA record with an IPv6 target",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"2001:db8::10"},
				RecordType: operatoringressv1.AAAARecordType,
				RecordTTL:  60,
			},
			wantEps: []Endpoint{{
				Source: src, Hostname: wildcard,
				Targets: []Target{{Type: TargetAAAA, Value: "2001:db8::10"}},
				TTL:     int32p(60),
			}},
		},
		{
			name: "several A targets stay on one endpoint",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    "apps.example.com",
				Targets:    []string{"192.0.2.10", "192.0.2.11"},
				RecordType: operatoringressv1.ARecordType,
			},
			wantEps: []Endpoint{{
				Source: src, Hostname: "apps.example.com.",
				Targets: []Target{
					{Type: TargetA, Value: "192.0.2.10"},
					{Type: TargetA, Value: "192.0.2.11"},
				},
			}},
		},
		{
			name: "a zero record TTL leaves the TTL to the policy",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"192.0.2.10"},
				RecordType: operatoringressv1.ARecordType,
				RecordTTL:  0,
			},
			wantEps: []Endpoint{{
				Source: src, Hostname: wildcard,
				Targets: []Target{{Type: TargetA, Value: "192.0.2.10"}},
			}},
		},
		{
			name: "a record TTL beyond int32 is clamped",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"192.0.2.10"},
				RecordType: operatoringressv1.ARecordType,
				RecordTTL:  math.MaxInt32 + 1000,
			},
			wantEps: []Endpoint{{
				Source: src, Hostname: wildcard,
				Targets: []Target{{Type: TargetA, Value: "192.0.2.10"}},
				TTL:     int32p(math.MaxInt32),
			}},
		},
		{
			name: "the ttl annotation wins over spec.recordTTL",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"192.0.2.10"},
				RecordType: operatoringressv1.ARecordType,
				RecordTTL:  30,
			},
			annotations: map[string]string{"uddi.infoblox.com/ttl": "600"},
			wantEps: []Endpoint{{
				Source: src, Hostname: wildcard,
				Targets: []Target{{Type: TargetA, Value: "192.0.2.10"}},
				TTL:     int32p(600),
			}},
		},
		{
			name: "the external-dns ttl annotation also wins over spec.recordTTL with compatibility on",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"192.0.2.10"},
				RecordType: operatoringressv1.ARecordType,
				RecordTTL:  30,
			},
			annotations: map[string]string{ExternalDNSTTL: "900"},
			opts:        SourceOptions{ExternalDNS: true},
			wantEps: []Endpoint{{
				Source: src, Hostname: wildcard,
				Targets: []Target{{Type: TargetA, Value: "192.0.2.10"}},
				TTL:     int32p(900),
			}},
		},
		{
			name: "the external-dns ttl annotation is ignored with compatibility off",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"192.0.2.10"},
				RecordType: operatoringressv1.ARecordType,
				RecordTTL:  30,
			},
			annotations: map[string]string{ExternalDNSTTL: "900"},
			wantEps: []Endpoint{{
				Source: src, Hostname: wildcard,
				Targets: []Target{{Type: TargetA, Value: "192.0.2.10"}},
				TTL:     int32p(30),
			}},
		},
		{
			name: "the hostname annotation adds hostnames sharing the targets",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"192.0.2.10"},
				RecordType: operatoringressv1.ARecordType,
			},
			annotations: map[string]string{"uddi.infoblox.com/hostname": "console.apps.cluster.example.com"},
			wantEps: []Endpoint{
				{Source: src, Hostname: wildcard, Targets: []Target{{Type: TargetA, Value: "192.0.2.10"}}},
				{Source: src, Hostname: "console.apps.cluster.example.com.", Targets: []Target{{Type: TargetA, Value: "192.0.2.10"}}},
			},
		},
		{
			name: "the target annotation replaces spec.targets",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"192.0.2.10"},
				RecordType: operatoringressv1.ARecordType,
			},
			annotations: map[string]string{"uddi.infoblox.com/target": "198.51.100.1,2001:db8::1"},
			wantEps: []Endpoint{{
				Source: src, Hostname: wildcard,
				Targets: []Target{
					{Type: TargetA, Value: "198.51.100.1"},
					{Type: TargetAAAA, Value: "2001:db8::1"},
				},
			}},
		},
		{
			name: "the target annotation also rescues an unsupported record type",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"x"},
				RecordType: "TXT",
			},
			annotations: map[string]string{"uddi.infoblox.com/target": "198.51.100.1"},
			wantEps: []Endpoint{{
				Source: src, Hostname: wildcard,
				Targets: []Target{{Type: TargetA, Value: "198.51.100.1"}},
			}},
		},
		{
			name: "an unmanaged record is still published",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:             wildcard,
				Targets:             []string{"192.0.2.10"},
				RecordType:          operatoringressv1.ARecordType,
				DNSManagementPolicy: operatoringressv1.UnmanagedDNS,
			},
			wantEps: []Endpoint{{
				Source: src, Hostname: wildcard,
				Targets: []Target{{Type: TargetA, Value: "192.0.2.10"}},
			}},
		},
		{
			name: "an unknown record type is unsupported",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"v=spf1 -all"},
				RecordType: "TXT",
			},
			wantSkips: []Skip{{
				Source: src, Hostname: wildcard, Reason: SkipUnsupported,
				Message: `spec.recordType "TXT" is not supported, only A, AAAA and CNAME are`,
			}},
		},
		{
			name: "an empty record type is unsupported even with no targets",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName: wildcard,
			},
			wantSkips: []Skip{{
				Source: src, Hostname: wildcard, Reason: SkipUnsupported,
				Message: `spec.recordType "" is not supported, only A, AAAA and CNAME are`,
			}},
		},
		{
			name: "an IPv6 target in an A record is unsupported",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"2001:db8::1"},
				RecordType: operatoringressv1.ARecordType,
			},
			wantSkips: []Skip{{
				Source: src, Hostname: wildcard, Reason: SkipUnsupported,
				Message: `spec.targets[0] "2001:db8::1" is not an IPv4 address, which recordType A needs`,
			}},
		},
		{
			name: "an IPv4 target in an AAAA record is unsupported",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"192.0.2.1"},
				RecordType: operatoringressv1.AAAARecordType,
			},
			wantSkips: []Skip{{
				Source: src, Hostname: wildcard, Reason: SkipUnsupported,
				Message: `spec.targets[0] "192.0.2.1" is not an IPv6 address, which recordType AAAA needs`,
			}},
		},
		{
			name: "an invalid CNAME target is unsupported, and no partial set is published",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"ok.example.com", "not_a_name!"},
				RecordType: operatoringressv1.CNAMERecordType,
			},
			wantSkips: []Skip{{
				Source: src, Hostname: wildcard, Reason: SkipUnsupported,
				Message: `spec.targets[1] "not_a_name!" is not a valid DNS name, which recordType CNAME needs`,
			}},
		},
		{
			name: "a wildcard CNAME target is unsupported",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"*.example.com"},
				RecordType: operatoringressv1.CNAMERecordType,
			},
			wantSkips: []Skip{{
				Source: src, Hostname: wildcard, Reason: SkipUnsupported,
				Message: `spec.targets[0] "*.example.com" is not a valid DNS name, which recordType CNAME needs`,
			}},
		},
		{
			name: "no targets is NoTarget",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				RecordType: operatoringressv1.ARecordType,
			},
			wantSkips: []Skip{{Source: src, Hostname: wildcard, Reason: SkipNoTarget, Message: noTargetMsg}},
		},
		{
			name: "mode A filters out a CNAME leaving no target",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"router.example.com"},
				RecordType: operatoringressv1.CNAMERecordType,
			},
			opts:      SourceOptions{Mode: TargetModeA},
			wantSkips: []Skip{{Source: src, Hostname: wildcard, Reason: SkipNoTarget, Message: noTargetMsg}},
		},
		{
			name: "mode CNAME filters out an A leaving no target",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"192.0.2.10"},
				RecordType: operatoringressv1.ARecordType,
			},
			opts:      SourceOptions{Mode: TargetModeCNAME},
			wantSkips: []Skip{{Source: src, Hostname: wildcard, Reason: SkipNoTarget, Message: noTargetMsg}},
		},
		{
			name: "mode A keeps an A record",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"192.0.2.10"},
				RecordType: operatoringressv1.ARecordType,
			},
			opts: SourceOptions{Mode: TargetModeA},
			wantEps: []Endpoint{{
				Source: src, Hostname: wildcard,
				Targets: []Target{{Type: TargetA, Value: "192.0.2.10"}},
			}},
		},
		{
			name: "an empty dnsName with no hostname annotation has no hostname",
			spec: operatoringressv1.DNSRecordSpec{
				Targets:    []string{"192.0.2.10"},
				RecordType: operatoringressv1.ARecordType,
			},
			wantSkips: []Skip{{Source: src, Reason: SkipNoHostname, Message: "DNSRecord has no spec.dnsName"}},
		},
		{
			name: "an invalid dnsName is unsupported",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    "bad_name!.example.com.",
				Targets:    []string{"192.0.2.10"},
				RecordType: operatoringressv1.ARecordType,
			},
			wantSkips: []Skip{{
				Source: src, Hostname: "bad_name!.example.com.", Reason: SkipUnsupported,
				Message: `spec.dnsName "bad_name!.example.com." is not a valid DNS name`,
			}},
		},
		{
			name: "a wildcard that is not the leftmost label is unsupported",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    "apps.*.example.com.",
				Targets:    []string{"192.0.2.10"},
				RecordType: operatoringressv1.ARecordType,
			},
			wantSkips: []Skip{{
				Source: src, Hostname: "apps.*.example.com.", Reason: SkipUnsupported,
				Message: `spec.dnsName "apps.*.example.com." is not a valid DNS name`,
			}},
		},
		{
			name: "publish false excludes the DNSRecord",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"192.0.2.10"},
				RecordType: operatoringressv1.ARecordType,
			},
			annotations: map[string]string{"uddi.infoblox.com/publish": "false"},
			wantSkips:   []Skip{{Source: src, Reason: SkipExcluded, Message: "uddi.infoblox.com/publish is false"}},
		},
		{
			name: "malformed ttl annotation is invalid",
			spec: operatoringressv1.DNSRecordSpec{
				DNSName:    wildcard,
				Targets:    []string{"192.0.2.10"},
				RecordType: operatoringressv1.ARecordType,
			},
			annotations: map[string]string{"uddi.infoblox.com/ttl": "-5"},
			wantSkips: []Skip{{
				Source: src, Reason: SkipInvalidAnnotation,
				Message: `uddi.infoblox.com/ttl: "-5" is not a positive integer`,
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &operatoringressv1.DNSRecord{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "openshift-ingress-operator", Name: "default-wildcard", UID: "dnsrecord-uid",
					Annotations: tc.annotations,
				},
				Spec: tc.spec,
			}
			gotEps, gotSkips := EndpointsFromDNSRecord(r, tc.opts)
			assertResult(t, gotEps, gotSkips, tc.wantEps, tc.wantSkips)
		})
	}
}
