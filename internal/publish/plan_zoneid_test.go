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

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

const testZoneID = "dns/auth_zone/2c1a0f5e-0000-4000-8000-000000000001"

// byIDZone is the zone apps.example.com. given by id in spec.zoneRefs: Ready, with no CR name.
func byIDZone() Zone {
	return Zone{ID: testZoneID, FQDN: "apps.example.com.", Ready: true}
}

func TestPlan_ZoneGivenByIDReferencesTheID(t *testing.T) {
	policy := testPolicy(int32Ptr(300), nil)
	ep := Endpoint{
		Source:   SourceRef{Kind: SourceIngress, Namespace: "team-a", Name: "web"},
		Hostname: "www.apps.example.com.",
		Targets:  []Target{{Type: TargetA, Value: "192.0.2.10"}},
	}

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: []Endpoint{ep}, Zones: []Zone{byIDZone()}})
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	if len(records) != 1 {
		t.Fatalf("expected one record, got %d", len(records))
	}
	got := records[0].Spec
	if got.ZoneRef.ID != testZoneID || got.ZoneRef.Name != "" {
		t.Errorf("ZoneRef = %+v, want only the id %q", got.ZoneRef, testZoneID)
	}
	if got.Name != "www" {
		t.Errorf("Spec.Name = %q, want www", got.Name)
	}
}

func TestPlan_ZoneGivenByNameKeepsTheNameRef(t *testing.T) {
	policy := testPolicy(nil, nil)
	ep := Endpoint{
		Source:   SourceRef{Kind: SourceIngress, Namespace: "team-a", Name: "web"},
		Hostname: "www.apps.example.com.",
		Targets:  []Target{{Type: TargetA, Value: "192.0.2.10"}},
	}

	records, _ := Plan(PlanInput{Policy: policy, Endpoints: []Endpoint{ep},
		Zones: []Zone{{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true}}})
	if len(records) != 1 {
		t.Fatalf("expected one record, got %d", len(records))
	}
	if ref := records[0].Spec.ZoneRef; ref.Name != "apps-zone" || ref.ID != "" {
		t.Errorf("ZoneRef = %+v, want only the name apps-zone", ref)
	}
}

func TestPlan_NameAndIDZonesMixed(t *testing.T) {
	policy := testPolicy(nil, nil)
	// The id zone is the longer suffix for the first hostname, the named zone for the second.
	zones := []Zone{
		{Name: "example-zone", FQDN: "example.com.", Ready: true},
		byIDZone(),
	}
	src := SourceRef{Kind: SourceIngress, Namespace: "team-a", Name: "web"}
	targets := []Target{{Type: TargetA, Value: "192.0.2.10"}}
	eps := []Endpoint{
		{Source: src, Hostname: "www.apps.example.com.", Targets: targets},
		{Source: src, Hostname: "www.example.com.", Targets: targets},
	}

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: eps, Zones: zones})
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	byName := map[string]uddiv1alpha1.ObjectRef{}
	for _, r := range records {
		byName[r.Annotations[AnnotationRecordHost]] = r.Spec.ZoneRef
	}
	if got := byName["www.apps.example.com."]; got != (uddiv1alpha1.ObjectRef{ID: testZoneID}) {
		t.Errorf("www.apps.example.com. zoneRef = %+v, want the id", got)
	}
	if got := byName["www.example.com."]; got != (uddiv1alpha1.ObjectRef{Name: "example-zone"}) {
		t.Errorf("www.example.com. zoneRef = %+v, want the name", got)
	}
}

func TestPlan_ZoneGivenByIDTakesWildcardAndApex(t *testing.T) {
	policy := testPolicy(nil, nil)
	src := SourceRef{Kind: SourceDNSRecord, Namespace: "openshift-ingress-operator", Name: "default-wildcard"}
	eps := []Endpoint{
		{Source: src, Hostname: "*.apps.example.com.", Targets: []Target{{Type: TargetA, Value: "192.0.2.10"}}},
		{Source: src, Hostname: "apps.example.com.", Targets: []Target{{Type: TargetAAAA, Value: "2001:db8::10"}}},
	}

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: eps, Zones: []Zone{byIDZone()}})
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	names := map[string]bool{}
	for _, r := range records {
		names[r.Spec.Name] = true
		if r.Spec.ZoneRef.ID != testZoneID {
			t.Errorf("record %q zoneRef = %+v, want the id", r.Spec.Name, r.Spec.ZoneRef)
		}
	}
	if !names["*"] || !names["@"] || len(records) != 2 {
		t.Errorf("record names = %v, want * and @", names)
	}
}

func TestPlan_ZoneGivenByIDStillNeedsAMatchingFQDN(t *testing.T) {
	policy := testPolicy(nil, nil)
	ep := Endpoint{
		Source:   SourceRef{Kind: SourceIngress, Namespace: "team-a", Name: "web"},
		Hostname: "www.other.example.org.",
		Targets:  []Target{{Type: TargetA, Value: "192.0.2.10"}},
	}

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: []Endpoint{ep}, Zones: []Zone{byIDZone()}})
	if len(records) != 0 {
		t.Fatalf("expected no records, got %d", len(records))
	}
	if len(skips) != 1 || skips[0].Reason != SkipNoMatchingZone {
		t.Fatalf("expected one NoMatchingZone skip, got %+v", skips)
	}
}
