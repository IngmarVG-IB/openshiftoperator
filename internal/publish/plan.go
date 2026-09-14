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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

// maxNameLength is the Kubernetes object name length limit every generated Record
// name must respect.
const maxNameLength = 253

// nameHashLength is how many hex characters of the sha256 digest RecordName keeps.
const nameHashLength = 10

// apexLabel is the name-in-zone MatchZone reports for the zone apex.
const apexLabel = "@"

// CanonicalHostname lower-cases h and ensures it ends with a trailing dot, the form
// every hostname is compared and matched in.
func CanonicalHostname(h string) string {
	h = strings.ToLower(h)
	if !strings.HasSuffix(h, ".") {
		h += "."
	}
	return h
}

// MatchZone finds the zone among zones whose FQDN equals hostname, or is a
// label-boundary suffix of it, preferring the longest match ("apps.example.com."
// beats "example.com." for "www.apps.example.com."). Both hostname and every zone's
// FQDN are expected to already be canonical (lower-case, trailing dot); use
// CanonicalHostname first if that is not guaranteed.
//
// It returns the winning zone, the name of hostname inside that zone ("@" for the
// apex), and whether a zone matched at all. MatchZone does not look at Zone.Ready;
// the caller decides what a match against a not-yet-ready zone means.
func MatchZone(hostname string, zones []Zone) (Zone, string, bool) {
	best := -1
	bestLen := -1
	for i, z := range zones {
		if z.FQDN == "" {
			continue
		}
		if hostname == z.FQDN {
			if len(z.FQDN) > bestLen {
				bestLen = len(z.FQDN)
				best = i
			}
			continue
		}
		if !strings.HasSuffix(hostname, z.FQDN) {
			continue
		}
		// A real suffix match must land on a label boundary: the character right
		// before the matched suffix must be a dot, otherwise "notexample.com." would
		// wrongly match zone "example.com.".
		boundary := len(hostname) - len(z.FQDN) - 1
		if boundary >= 0 && hostname[boundary] == '.' && len(z.FQDN) > bestLen {
			bestLen = len(z.FQDN)
			best = i
		}
	}
	if best < 0 {
		return Zone{}, "", false
	}
	z := zones[best]
	if hostname == z.FQDN {
		return z, apexLabel, true
	}
	return z, hostname[:len(hostname)-len(z.FQDN)-1], true
}

// RecordName derives the deterministic name of the Record generated for one
// (endpoint, target) pair: "<policy>-<10 hex of sha256(kind|source|hostname|type|target)>".
// When that would exceed the Kubernetes 253 character name limit, the policy part is
// truncated so the hash suffix always survives.
func RecordName(policy string, ep Endpoint, t Target) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%s|%s|%s|%s",
		ep.Source.Kind, ep.Source.Name, ep.Hostname, t.Type, t.Value))
	suffix := hex.EncodeToString(sum[:])[:nameHashLength]

	name := policy + "-" + suffix
	if len(name) <= maxNameLength {
		return name
	}
	keep := maxNameLength - 1 - nameHashLength // "-" plus the suffix must survive.
	if keep < 0 {
		keep = 0
	}
	return policy[:keep] + "-" + suffix
}

// PlanInput is everything Plan needs: the policy (for its metadata, connectionRef,
// tags, ttl and recordLifecycle), the endpoints already extracted from the selected
// sources, and the zones the policy's zoneRefs resolve to.
type PlanInput struct {
	Policy    *uddiv1alpha1.DNSPublishPolicy
	Endpoints []Endpoint
	Zones     []Zone
}

// DesiredRecord is one Record CR Plan wants the controller to apply for a
// DNSPublishPolicy.
type DesiredRecord struct {
	Name        string
	Labels      map[string]string
	Annotations map[string]string
	Spec        uddiv1alpha1.RecordSpec
}

// desiredKey identifies a Record by the fields that make it the same DNS record
// regardless of which source produced it, so two sources publishing the same
// hostname and target dedupe onto a single DesiredRecord.
type desiredKey struct {
	hostname string
	rtype    TargetType
	value    string
}

// Plan turns endpoints and zones into the Records a DNSPublishPolicy should own, plus
// the hostnames it could not place and why. See DESIGN.md section 4.7, pipeline steps
// 2 (zone matching) and 3 (desired Records).
//
// The returned records are sorted by Name and deduplicated: when two endpoints
// resolve to the same hostname and target, the first one (in in.Endpoints order)
// wins and the second is dropped silently, with no skip recorded. The returned skips
// are sorted by source then hostname.
func Plan(in PlanInput) ([]DesiredRecord, []Skip) {
	records := make([]DesiredRecord, 0, len(in.Endpoints))
	skips := make([]Skip, 0, len(in.Endpoints))
	seen := make(map[desiredKey]bool, len(in.Endpoints))

	for _, ep := range in.Endpoints {
		zone, nameInZone, ok := MatchZone(ep.Hostname, in.Zones)
		if !ok {
			skips = append(skips, Skip{
				Source:   ep.Source,
				Hostname: ep.Hostname,
				Reason:   SkipNoMatchingZone,
				Message:  "no zoneRef is a suffix of the hostname",
			})
			continue
		}
		if !zone.Ready {
			skips = append(skips, Skip{
				Source:   ep.Source,
				Hostname: ep.Hostname,
				Reason:   SkipZoneNotReady,
				Message:  fmt.Sprintf("zone %q has no status.id yet", zone.Name),
			})
			continue
		}

		targets, skip := apexTargets(ep, nameInZone)
		if skip != nil {
			skips = append(skips, *skip)
			continue
		}

		for _, t := range targets {
			key := desiredKey{hostname: ep.Hostname, rtype: t.Type, value: t.Value}
			if seen[key] {
				continue
			}
			seen[key] = true
			records = append(records, buildRecord(in.Policy, ep, t, zone, nameInZone))
		}
	}

	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	sort.Slice(skips, func(i, j int) bool {
		si, sj := skips[i].Source.String(), skips[j].Source.String()
		if si != sj {
			return si < sj
		}
		return skips[i].Hostname < skips[j].Hostname
	})
	return records, skips
}

// apexTargets applies the ApexCNAME rule: the zone apex can never carry a CNAME. An
// apex endpoint that also has address targets keeps only those, dropping the CNAMEs
// silently; an apex endpoint with only CNAME targets is skipped entirely. Every other
// endpoint keeps all its targets unchanged.
func apexTargets(ep Endpoint, nameInZone string) ([]Target, *Skip) {
	if nameInZone != apexLabel {
		return ep.Targets, nil
	}
	addresses := make([]Target, 0, len(ep.Targets))
	for _, t := range ep.Targets {
		if t.Type != TargetCNAME {
			addresses = append(addresses, t)
		}
	}
	if len(addresses) > 0 {
		return addresses, nil
	}
	return nil, &Skip{
		Source:   ep.Source,
		Hostname: ep.Hostname,
		Reason:   SkipApexCNAME,
		Message:  "the zone apex cannot carry a CNAME",
	}
}

// buildRecord assembles the DesiredRecord for one (endpoint, target) pair already
// known to belong to zone at nameInZone.
func buildRecord(policy *uddiv1alpha1.DNSPublishPolicy, ep Endpoint, t Target, zone Zone, nameInZone string) DesiredRecord {
	tags := make(map[string]string, len(policy.Spec.Tags)+1)
	for k, v := range policy.Spec.Tags {
		tags[k] = v
	}
	tags[TagSource] = fmt.Sprintf("%s/%s/%s", ep.Source.Kind, ep.Source.Namespace, ep.Source.Name)

	spec := uddiv1alpha1.RecordSpec{
		CommonSpec: uddiv1alpha1.CommonSpec{
			ConnectionRef: policy.Spec.ConnectionRef,
			Tags:          tags,
			Lifecycle:     policy.Spec.RecordLifecycle,
		},
		ZoneRef: uddiv1alpha1.ObjectRef{Name: zone.Name},
		Name:    nameInZone,
		Type:    uddiv1alpha1.RecordType(t.Type),
		RData:   rdataFor(t),
		TTL:     effectiveTTL(ep, policy),
		Comment: fmt.Sprintf("Published by DNSPublishPolicy %s/%s from %s %s",
			policy.Namespace, policy.Name, ep.Source.Kind, ep.Source.Name),
	}

	return DesiredRecord{
		Name: RecordName(policy.Name, ep, t),
		Labels: map[string]string{
			LabelPolicy:     policy.Name,
			LabelSourceKind: string(ep.Source.Kind),
			LabelSourceName: ep.Source.Name,
		},
		Annotations: map[string]string{
			AnnotationRecordHost: ep.Hostname,
			AnnotationRecordTgt:  t.Value,
		},
		Spec: spec,
	}
}

// effectiveTTL prefers the endpoint's own ttl (from a source's ttl annotation) and
// falls back to the policy's default. The returned pointer never aliases the
// endpoint's or the policy's, so the caller may hand it to a Record CR freely.
func effectiveTTL(ep Endpoint, policy *uddiv1alpha1.DNSPublishPolicy) *int32 {
	if ep.TTL != nil {
		v := *ep.TTL
		return &v
	}
	if policy.Spec.TTL != nil {
		v := *policy.Spec.TTL
		return &v
	}
	return nil
}

// rdataFor builds the typed rdata block matching t.Type.
func rdataFor(t Target) uddiv1alpha1.RecordRData {
	switch t.Type {
	case TargetA:
		return uddiv1alpha1.RecordRData{A: &uddiv1alpha1.ARData{Address: t.Value}}
	case TargetAAAA:
		return uddiv1alpha1.RecordRData{AAAA: &uddiv1alpha1.AAAARData{Address: t.Value}}
	case TargetCNAME:
		return uddiv1alpha1.RecordRData{CNAME: &uddiv1alpha1.CNAMERData{CNAME: t.Value}}
	default:
		return uddiv1alpha1.RecordRData{}
	}
}
