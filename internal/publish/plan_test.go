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
	"reflect"
	"sort"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

func int32Ptr(v int32) *int32 { return &v }

func TestCanonicalHostname(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Example.COM", "example.com."},
		{"example.com.", "example.com."},
		{"WWW.Example.com", "www.example.com."},
		{"already.lower.", "already.lower."},
	}
	for _, c := range cases {
		if got := CanonicalHostname(c.in); got != c.want {
			t.Errorf("CanonicalHostname(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMatchZone(t *testing.T) {
	zoneApps := Zone{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true}
	zoneExample := Zone{Name: "example-zone", FQDN: "example.com.", Ready: true}
	zoneNotReady := Zone{Name: "notready-zone", FQDN: "notready.example.com.", Ready: false}

	tests := []struct {
		name     string
		hostname string
		zones    []Zone
		wantZone string
		wantName string
		wantOK   bool
	}{
		{
			name:     "apex",
			hostname: "apps.example.com.",
			zones:    []Zone{zoneApps, zoneExample},
			wantZone: "apps-zone",
			wantName: "@",
			wantOK:   true,
		},
		{
			name:     "sub label",
			hostname: "www.apps.example.com.",
			zones:    []Zone{zoneApps, zoneExample},
			wantZone: "apps-zone",
			wantName: "www",
			wantOK:   true,
		},
		{
			name:     "longest suffix wins regardless of input order",
			hostname: "foo.apps.example.com.",
			zones:    []Zone{zoneExample, zoneApps},
			wantZone: "apps-zone",
			wantName: "foo",
			wantOK:   true,
		},
		{
			name:     "falls back to the shorter zone when the longer one does not match",
			hostname: "foo.example.com.",
			zones:    []Zone{zoneApps, zoneExample},
			wantZone: "example-zone",
			wantName: "foo",
			wantOK:   true,
		},
		{
			name:     "no label-boundary false match",
			hostname: "notexample.com.",
			zones:    []Zone{zoneExample},
			wantOK:   false,
		},
		{
			name:     "zone not ready still reports a match",
			hostname: "www.notready.example.com.",
			zones:    []Zone{zoneNotReady},
			wantZone: "notready-zone",
			wantName: "www",
			wantOK:   true,
		},
		{
			name:     "no zone at all",
			hostname: "other.org.",
			zones:    []Zone{zoneApps, zoneExample},
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			z, n, ok := MatchZone(tt.hostname, tt.zones)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if z.Name != tt.wantZone || n != tt.wantName {
				t.Errorf("got zone=%q name=%q, want zone=%q name=%q", z.Name, n, tt.wantZone, tt.wantName)
			}
		})
	}
}

func TestRecordName(t *testing.T) {
	ep := Endpoint{
		Source:   SourceRef{Kind: SourceRoute, Namespace: "team-a", Name: "web"},
		Hostname: "www.apps.example.com.",
	}
	target := Target{Type: TargetA, Value: "192.0.2.10"}

	name1 := RecordName("public", ep, target)
	name2 := RecordName("public", ep, target)
	if name1 != name2 {
		t.Fatalf("RecordName is not deterministic: %q != %q", name1, name2)
	}
	if !strings.HasPrefix(name1, "public-") {
		t.Fatalf("expected name to start with the policy name, got %q", name1)
	}
	suffix := strings.TrimPrefix(name1, "public-")
	if len(suffix) != 10 {
		t.Fatalf("expected a 10 hex character suffix, got %d chars (%q)", len(suffix), suffix)
	}

	other := RecordName("public", ep, Target{Type: TargetA, Value: "192.0.2.11"})
	if other == name1 {
		t.Fatalf("expected different targets to hash to different names")
	}

	// Name length cap: an overlong policy name is truncated so the hash suffix and
	// its separating dash both survive, keeping the total at the Kubernetes 253
	// character name limit. The suffix itself only depends on ep and target, so it
	// must be unchanged by the policy name being truncated.
	longPolicy := strings.Repeat("p", 300)
	long := RecordName(longPolicy, ep, target)
	if len(long) != maxNameLength {
		t.Fatalf("expected the truncated name to be exactly %d chars, got %d (%q)", maxNameLength, len(long), long)
	}
	if !strings.HasSuffix(long, "-"+suffix) {
		t.Fatalf("expected the hash suffix to survive truncation, got %q", long)
	}
}

// testPolicy builds a minimal DNSPublishPolicy carrying only the fields Plan reads.
func testPolicy(ttl *int32, tags map[string]string) *uddiv1alpha1.DNSPublishPolicy {
	return &uddiv1alpha1.DNSPublishPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "public"},
		Spec: uddiv1alpha1.DNSPublishPolicySpec{
			ConnectionRef:   uddiv1alpha1.ConnectionRef{Name: "default"},
			TTL:             ttl,
			Tags:            tags,
			RecordLifecycle: uddiv1alpha1.Lifecycle{DeletionPolicy: uddiv1alpha1.DeletionPolicyDelete},
		},
	}
}

func TestPlan_RecordFieldsAndZoneMatching(t *testing.T) {
	policy := testPolicy(int32Ptr(300), map[string]string{"team": "a"})
	zones := []Zone{{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true}}
	ep := Endpoint{
		Source:   SourceRef{Kind: SourceRoute, Namespace: "team-a", Name: "web"},
		Hostname: "www.apps.example.com.",
		Targets:  []Target{{Type: TargetCNAME, Value: "router.apps.example.com."}},
	}

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: []Endpoint{ep}, Zones: zones})
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	if len(records) != 1 {
		t.Fatalf("expected exactly one record, got %d", len(records))
	}
	r := records[0]

	if r.Spec.ZoneRef.Name != "apps-zone" {
		t.Errorf("ZoneRef.Name = %q, want apps-zone", r.Spec.ZoneRef.Name)
	}
	if r.Spec.Name != "www" {
		t.Errorf("Spec.Name = %q, want www", r.Spec.Name)
	}
	if r.Spec.Type != uddiv1alpha1.RecordType("CNAME") {
		t.Errorf("Spec.Type = %q, want CNAME", r.Spec.Type)
	}
	if r.Spec.RData.CNAME == nil || r.Spec.RData.CNAME.CNAME != "router.apps.example.com." {
		t.Errorf("Spec.RData.CNAME = %+v, want router.apps.example.com.", r.Spec.RData.CNAME)
	}
	if r.Spec.TTL == nil || *r.Spec.TTL != 300 {
		t.Errorf("Spec.TTL = %v, want 300 (from the policy default)", r.Spec.TTL)
	}
	if r.Spec.ConnectionRef != policy.Spec.ConnectionRef {
		t.Errorf("Spec.ConnectionRef = %+v, want %+v", r.Spec.ConnectionRef, policy.Spec.ConnectionRef)
	}
	if r.Spec.Lifecycle != policy.Spec.RecordLifecycle {
		t.Errorf("Spec.Lifecycle = %+v, want %+v", r.Spec.Lifecycle, policy.Spec.RecordLifecycle)
	}
	wantComment := "Published by DNSPublishPolicy team-a/public from Route web"
	if r.Spec.Comment != wantComment {
		t.Errorf("Spec.Comment = %q, want %q", r.Spec.Comment, wantComment)
	}
	if r.Spec.Tags["team"] != "a" || r.Spec.Tags[TagSource] != "Route/team-a/web" {
		t.Errorf("Spec.Tags = %+v", r.Spec.Tags)
	}
	if r.Labels[LabelPolicy] != "public" || r.Labels[LabelSourceKind] != "Route" || r.Labels[LabelSourceName] != "web" {
		t.Errorf("Labels = %+v", r.Labels)
	}
	if r.Annotations[AnnotationRecordHost] != "www.apps.example.com." || r.Annotations[AnnotationRecordTgt] != "router.apps.example.com." {
		t.Errorf("Annotations = %+v", r.Annotations)
	}
	if r.Name != RecordName("public", ep, ep.Targets[0]) {
		t.Errorf("Name = %q, want the RecordName result", r.Name)
	}
}

func TestPlan_NoMatchingZone(t *testing.T) {
	policy := testPolicy(int32Ptr(300), nil)
	zones := []Zone{{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true}}
	ep := Endpoint{
		Source:   SourceRef{Kind: SourceRoute, Namespace: "team-a", Name: "legacy"},
		Hostname: "legacy.old.example.com.",
		Targets:  []Target{{Type: TargetCNAME, Value: "router.apps.example.com."}},
	}

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: []Endpoint{ep}, Zones: zones})
	if len(records) != 0 {
		t.Fatalf("expected no records, got %d", len(records))
	}
	if len(skips) != 1 || skips[0].Reason != SkipNoMatchingZone {
		t.Fatalf("expected one NoMatchingZone skip, got %+v", skips)
	}
	if skips[0].Source != ep.Source || skips[0].Hostname != ep.Hostname {
		t.Errorf("skip Source/Hostname = %+v/%q, want %+v/%q", skips[0].Source, skips[0].Hostname, ep.Source, ep.Hostname)
	}
}

func TestPlan_ZoneNotReady(t *testing.T) {
	policy := testPolicy(int32Ptr(300), nil)
	zones := []Zone{{Name: "apps-zone", FQDN: "apps.example.com.", Ready: false}}
	ep := Endpoint{
		Source:   SourceRef{Kind: SourceIngress, Namespace: "team-a", Name: "web"},
		Hostname: "www.apps.example.com.",
		Targets:  []Target{{Type: TargetA, Value: "192.0.2.10"}},
	}

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: []Endpoint{ep}, Zones: zones})
	if len(records) != 0 {
		t.Fatalf("expected no records, got %d", len(records))
	}
	if len(skips) != 1 || skips[0].Reason != SkipZoneNotReady {
		t.Fatalf("expected one ZoneNotReady skip, got %+v", skips)
	}
}

func TestPlan_ApexCNAMESkipped(t *testing.T) {
	policy := testPolicy(int32Ptr(300), nil)
	zones := []Zone{{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true}}
	ep := Endpoint{
		Source:   SourceRef{Kind: SourceRoute, Namespace: "team-a", Name: "root"},
		Hostname: "apps.example.com.",
		Targets:  []Target{{Type: TargetCNAME, Value: "router.apps.example.com."}},
	}

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: []Endpoint{ep}, Zones: zones})
	if len(records) != 0 {
		t.Fatalf("expected no records for an apex CNAME, got %d", len(records))
	}
	if len(skips) != 1 || skips[0].Reason != SkipApexCNAME {
		t.Fatalf("expected one ApexCNAME skip, got %+v", skips)
	}
}

func TestPlan_ApexWithAddressesKeepsAddressesDropsCNAME(t *testing.T) {
	policy := testPolicy(int32Ptr(300), nil)
	zones := []Zone{{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true}}
	ep := Endpoint{
		Source:   SourceRef{Kind: SourceIngress, Namespace: "team-a", Name: "root"},
		Hostname: "apps.example.com.",
		Targets: []Target{
			{Type: TargetCNAME, Value: "should-be-dropped.example.com."},
			{Type: TargetA, Value: "192.0.2.10"},
		},
	}

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: []Endpoint{ep}, Zones: zones})
	if len(skips) != 0 {
		t.Fatalf("expected no skip when address targets survive, got %+v", skips)
	}
	if len(records) != 1 {
		t.Fatalf("expected exactly one record (the address target), got %d", len(records))
	}
	if records[0].Spec.Type != uddiv1alpha1.RecordType("A") || records[0].Spec.RData.A == nil {
		t.Fatalf("expected the surviving record to be the A target, got %+v", records[0].Spec)
	}
}

func TestPlan_TTLPrecedence(t *testing.T) {
	policy := testPolicy(int32Ptr(300), nil)
	zones := []Zone{{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true}}
	epOverride := Endpoint{
		Source:   SourceRef{Kind: SourceRoute, Namespace: "team-a", Name: "override"},
		Hostname: "override.apps.example.com.",
		Targets:  []Target{{Type: TargetA, Value: "192.0.2.10"}},
		TTL:      int32Ptr(60),
	}
	epDefault := Endpoint{
		Source:   SourceRef{Kind: SourceRoute, Namespace: "team-a", Name: "default"},
		Hostname: "default.apps.example.com.",
		Targets:  []Target{{Type: TargetA, Value: "192.0.2.11"}},
	}

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: []Endpoint{epOverride, epDefault}, Zones: zones})
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	byHost := map[string]*int32{}
	for _, r := range records {
		byHost[r.Annotations[AnnotationRecordHost]] = r.Spec.TTL
	}
	if ttl := byHost["override.apps.example.com."]; ttl == nil || *ttl != 60 {
		t.Errorf("expected the endpoint ttl (60) to win, got %v", ttl)
	}
	if ttl := byHost["default.apps.example.com."]; ttl == nil || *ttl != 300 {
		t.Errorf("expected the policy default ttl (300) to apply, got %v", ttl)
	}

	// The returned pointers must not alias the policy's, so mutating one record's TTL
	// cannot corrupt the policy default or any other record that fell back to it.
	*byHost["default.apps.example.com."] = 999
	if *policy.Spec.TTL != 300 {
		t.Errorf("policy TTL was mutated through an aliased pointer")
	}
	if ttl := byHost["override.apps.example.com."]; ttl == nil || *ttl != 60 {
		t.Errorf("expected the override record's TTL to stay 60, got %v", ttl)
	}
}

func TestPlan_DedupFirstSourceWinsSecondGetsNoSkip(t *testing.T) {
	policy := testPolicy(int32Ptr(300), nil)
	zones := []Zone{{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true}}
	first := Endpoint{
		Source:   SourceRef{Kind: SourceRoute, Namespace: "team-a", Name: "first"},
		Hostname: "shared.apps.example.com.",
		Targets:  []Target{{Type: TargetA, Value: "192.0.2.10"}},
	}
	second := Endpoint{
		Source:   SourceRef{Kind: SourceIngress, Namespace: "team-a", Name: "second"},
		Hostname: "shared.apps.example.com.",
		Targets:  []Target{{Type: TargetA, Value: "192.0.2.10"}},
	}

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: []Endpoint{first, second}, Zones: zones})
	if len(skips) != 0 {
		t.Fatalf("expected the second source to be dropped silently with no skip, got %+v", skips)
	}
	if len(records) != 1 {
		t.Fatalf("expected exactly one deduplicated record, got %d", len(records))
	}
	if records[0].Labels[LabelSourceName] != "first" {
		t.Errorf("expected the first source to win, got source %q", records[0].Labels[LabelSourceName])
	}
}

func TestPlan_IPv6GivesAAAA(t *testing.T) {
	policy := testPolicy(int32Ptr(300), nil)
	zones := []Zone{{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true}}
	ep := Endpoint{
		Source:   SourceRef{Kind: SourceService, Namespace: "team-a", Name: "web"},
		Hostname: "www.apps.example.com.",
		Targets:  []Target{{Type: TargetAAAA, Value: "2001:db8::10"}},
	}

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: []Endpoint{ep}, Zones: zones})
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	if len(records) != 1 {
		t.Fatalf("expected exactly one record, got %d", len(records))
	}
	r := records[0]
	if r.Spec.Type != uddiv1alpha1.RecordType("AAAA") {
		t.Errorf("Spec.Type = %q, want AAAA", r.Spec.Type)
	}
	if r.Spec.RData.AAAA == nil || r.Spec.RData.AAAA.Address != "2001:db8::10" {
		t.Fatalf("Spec.RData.AAAA = %+v, want 2001:db8::10", r.Spec.RData.AAAA)
	}
	if r.Spec.RData.A != nil || r.Spec.RData.CNAME != nil {
		t.Errorf("expected only the AAAA rdata block to be set, got %+v", r.Spec.RData)
	}
}

func TestPlan_DeterministicAndSorted(t *testing.T) {
	policy := testPolicy(int32Ptr(300), nil)
	zones := []Zone{{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true}}
	endpoints := []Endpoint{
		{
			Source:   SourceRef{Kind: SourceRoute, Namespace: "team-a", Name: "c"},
			Hostname: "c.apps.example.com.",
			Targets:  []Target{{Type: TargetA, Value: "192.0.2.12"}},
		},
		{
			Source:   SourceRef{Kind: SourceRoute, Namespace: "team-a", Name: "a"},
			Hostname: "a.apps.example.com.",
			Targets:  []Target{{Type: TargetA, Value: "192.0.2.10"}},
		},
		{
			Source:   SourceRef{Kind: SourceRoute, Namespace: "team-a", Name: "b"},
			Hostname: "b.apps.example.com.",
			Targets:  []Target{{Type: TargetA, Value: "192.0.2.11"}},
		},
	}

	records1, _ := Plan(PlanInput{Policy: policy, Endpoints: endpoints, Zones: zones})
	records2, _ := Plan(PlanInput{Policy: policy, Endpoints: endpoints, Zones: zones})

	names1 := recordNames(records1)
	names2 := recordNames(records2)
	if !reflect.DeepEqual(names1, names2) {
		t.Fatalf("Plan is not deterministic: %v != %v", names1, names2)
	}
	if !sort.StringsAreSorted(names1) {
		t.Fatalf("expected records sorted by Name, got %v", names1)
	}
	if len(names1) != 3 {
		t.Fatalf("expected 3 records, got %d", len(names1))
	}
}

func recordNames(records []DesiredRecord) []string {
	names := make([]string, len(records))
	for i, r := range records {
		names[i] = r.Name
	}
	return names
}

func TestPlan_SkipsSortedBySourceThenHostname(t *testing.T) {
	policy := testPolicy(int32Ptr(300), nil)
	var zones []Zone // no zones at all: every endpoint is skipped as NoMatchingZone.
	endpoints := []Endpoint{
		{
			Source:   SourceRef{Kind: SourceRoute, Namespace: "team-a", Name: "z"},
			Hostname: "b.example.com.",
			Targets:  []Target{{Type: TargetA, Value: "192.0.2.10"}},
		},
		{
			Source:   SourceRef{Kind: SourceRoute, Namespace: "team-a", Name: "z"},
			Hostname: "a.example.com.",
			Targets:  []Target{{Type: TargetA, Value: "192.0.2.11"}},
		},
		{
			Source:   SourceRef{Kind: SourceIngress, Namespace: "team-a", Name: "a"},
			Hostname: "c.example.com.",
			Targets:  []Target{{Type: TargetA, Value: "192.0.2.12"}},
		},
	}

	_, skips := Plan(PlanInput{Policy: policy, Endpoints: endpoints, Zones: zones})
	if len(skips) != 3 {
		t.Fatalf("expected 3 skips, got %d", len(skips))
	}
	// Sorted by Source.String() ("Kind/name") then Hostname: Ingress/a < Route/z, and
	// within Route/z, "a.example.com." < "b.example.com.".
	want := []string{"c.example.com.", "a.example.com.", "b.example.com."}
	got := make([]string, 0, len(skips))
	for _, s := range skips {
		got = append(got, s.Hostname)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skips not sorted as expected: got %v, want %v", got, want)
	}
}
