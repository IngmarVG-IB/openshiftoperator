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
	"fmt"
	"math"
	"net"
	"strings"

	operatoringressv1 "github.com/openshift/api/operatoringress/v1"
)

// EndpointsFromDNSRecord implements DESIGN.md 4.7 step 1 for an OpenShift ingress operator
// DNSRecord (ingress.operator.openshift.io/v1). The hostname is spec.dnsName, kept
// canonical with any leading "*." wildcard label (the default router wildcard is
// "*.apps.<cluster>."); the hostname annotation adds more. The targets are spec.targets
// typed by spec.recordType: A and AAAA take IP addresses of that family and CNAME takes
// DNS names, which get a trailing dot. Any other record type, or a target that does not fit
// the type, is Unsupported. The target annotation, when present, replaces the computed
// targets and bypasses that check, as it does for a Service that is not a load balancer.
//
// The TTL is the ttl annotation, else spec.recordTTL when positive, else nil so the policy
// TTL applies. spec.dnsManagementPolicy is not consulted: it tells the ingress operator
// whether it manages the cloud provider's DNS, not whether this policy may publish.
func EndpointsFromDNSRecord(r *operatoringressv1.DNSRecord, o SourceOptions) ([]Endpoint, []Skip) {
	src := SourceRef{Kind: SourceDNSRecord, Namespace: r.Namespace, Name: r.Name, UID: r.UID}

	ann, skip := parseAnnotations(o, r.Annotations, src)
	if skip != nil {
		return nil, []Skip{*skip}
	}

	var own []string
	if name := strings.TrimSpace(r.Spec.DNSName); name != "" {
		if !isValidPublishedHostname(name) {
			return nil, []Skip{{
				Source:   src,
				Hostname: name,
				Reason:   SkipUnsupported,
				Message:  fmt.Sprintf("spec.dnsName %q is not a valid DNS name", r.Spec.DNSName),
			}}
		}
		own = []string{normalizeHostname(name)}
	}
	hostnames := mergeHostnames(own, ann.hostnames)
	if len(hostnames) == 0 {
		return nil, []Skip{{Source: src, Reason: SkipNoHostname, Message: "DNSRecord has no spec.dnsName"}}
	}

	var targets []Target
	if ann.hasTargets {
		targets = ann.targets
	} else {
		var err error
		targets, err = dnsRecordTargets(r.Spec)
		if err != nil {
			skips := make([]Skip, 0, len(hostnames))
			for _, h := range hostnames {
				skips = append(skips, Skip{Source: src, Hostname: h, Reason: SkipUnsupported, Message: err.Error()})
			}
			return nil, skips
		}
	}
	targets = filterByMode(targets, o.Mode)

	ttl := ann.ttl
	if ttl == nil && r.Spec.RecordTTL > 0 {
		v := int32(min(r.Spec.RecordTTL, math.MaxInt32))
		ttl = &v
	}

	return buildEndpoints(src, hostnames, targets, ttl)
}

// dnsRecordTargets types spec.targets by spec.recordType. An error names the field that
// does not fit.
func dnsRecordTargets(spec operatoringressv1.DNSRecordSpec) ([]Target, error) {
	switch spec.RecordType {
	case operatoringressv1.ARecordType, operatoringressv1.AAAARecordType, operatoringressv1.CNAMERecordType:
	default:
		return nil, fmt.Errorf("spec.recordType %q is not supported, only A, AAAA and CNAME are", spec.RecordType)
	}
	out := make([]Target, 0, len(spec.Targets))
	for i, raw := range spec.Targets {
		v := strings.TrimSpace(raw)
		switch spec.RecordType {
		case operatoringressv1.ARecordType:
			ip := net.ParseIP(v)
			if ip == nil || ip.To4() == nil {
				return nil, fmt.Errorf("spec.targets[%d] %q is not an IPv4 address, which recordType A needs", i, raw)
			}
			out = append(out, Target{Type: TargetA, Value: ip.To4().String()})
		case operatoringressv1.AAAARecordType:
			ip := net.ParseIP(v)
			if ip == nil || ip.To4() != nil {
				return nil, fmt.Errorf("spec.targets[%d] %q is not an IPv6 address, which recordType AAAA needs", i, raw)
			}
			out = append(out, Target{Type: TargetAAAA, Value: ip.String()})
		case operatoringressv1.CNAMERecordType:
			if !isValidHostname(v) {
				return nil, fmt.Errorf("spec.targets[%d] %q is not a valid DNS name, which recordType CNAME needs", i, raw)
			}
			out = append(out, Target{Type: TargetCNAME, Value: normalizeHostname(v)})
		}
	}
	return out, nil
}
