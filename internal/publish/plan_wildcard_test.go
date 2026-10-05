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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

func TestMatchZone_Wildcard(t *testing.T) {
	apps := Zone{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true}
	example := Zone{Name: "example-zone", FQDN: "example.com.", Ready: true}
	deep := Zone{Name: "b-zone", FQDN: "b.example.com.", Ready: true}

	tests := []struct {
		name     string
		hostname string
		zones    []Zone
		wantZone string
		wantName string
		wantOK   bool
	}{
		{
			name:     "wildcard directly under the zone apex is the name star",
			hostname: "*.apps.example.com.",
			zones:    []Zone{apps},
			wantZone: "apps-zone", wantName: "*", wantOK: true,
		},
		{
			name:     "the longest zone wins for a wildcard exactly like a normal hostname",
			hostname: "*.apps.example.com.",
			zones:    []Zone{example, apps},
			wantZone: "apps-zone", wantName: "*", wantOK: true,
		},
		{
			name:     "a wildcard below a nested label keeps the labels in the name",
			hostname: "*.apps.example.com.",
			zones:    []Zone{example},
			wantZone: "example-zone", wantName: "*.apps", wantOK: true,
		},
		{
			name:     "several labels after the wildcard",
			hostname: "*.a.b.example.com.",
			zones:    []Zone{example, deep},
			wantZone: "b-zone", wantName: "*.a", wantOK: true,
		},
		{
			name:     "a wildcard one level above the zone apex does not match the zone",
			hostname: "*.example.com.",
			zones:    []Zone{apps},
		},
		{
			name:     "no label-boundary false match for a wildcard",
			hostname: "*.notexample.com.",
			zones:    []Zone{example},
		},
		{
			name:     "a wildcard directly under the zone is star, never the apex",
			hostname: "*.example.com.",
			zones:    []Zone{example},
			wantZone: "example-zone", wantName: "*", wantOK: true,
		},
		{
			name:     "the plain apex stays the apex",
			hostname: "example.com.",
			zones:    []Zone{example},
			wantZone: "example-zone", wantName: "@", wantOK: true,
		},
		{
			name:     "a zone whose FQDN starts with a wildcard label never matches as an apex",
			hostname: "*.example.com.",
			zones:    []Zone{{Name: "bogus", FQDN: "*.example.com.", Ready: true}},
		},
		{
			name:     "a bare wildcard label matches nothing",
			hostname: "*.",
			zones:    []Zone{example},
		},
		{
			name:     "wildcard under a one-label zone",
			hostname: "*.com.",
			zones:    []Zone{{Name: "com-zone", FQDN: "com.", Ready: true}},
			wantZone: "com-zone", wantName: "*", wantOK: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			z, n, ok := MatchZone(tt.hostname, tt.zones)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v (zone %q name %q), want %v", ok, z.Name, n, tt.wantOK)
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

func wildcardEndpoint(hostname string, targets ...Target) Endpoint {
	return Endpoint{
		Source:   SourceRef{Kind: SourceDNSRecord, Namespace: "openshift-ingress-operator", Name: "default-wildcard"},
		Hostname: hostname,
		Targets:  targets,
	}
}

func TestPlan_WildcardsAgainstNestedZones(t *testing.T) {
	policy := testPolicy(int32Ptr(300), nil)
	zones := []Zone{
		{Name: "example-zone", FQDN: "example.com.", Ready: true},
		{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true},
	}
	a := Target{Type: TargetA, Value: "192.0.2.10"}

	endpoints := []Endpoint{
		wildcardEndpoint("*.apps.example.com.", a),      // apps-zone, name *
		wildcardEndpoint("*.team.apps.example.com.", a), // apps-zone, name *.team
		wildcardEndpoint("*.other.example.com.", a),     // example-zone, name *.other
		wildcardEndpoint("*.example.com.", a),           // example-zone, name *
	}

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: endpoints, Zones: zones})
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}

	got := map[string]string{} // hostname annotation -> "zone/name"
	for _, r := range records {
		got[r.Annotations[AnnotationRecordHost]] = r.Spec.ZoneRef.Name + "/" + r.Spec.Name
	}
	want := map[string]string{
		"*.apps.example.com.":      "apps-zone/*",
		"*.team.apps.example.com.": "apps-zone/*.team",
		"*.other.example.com.":     "example-zone/*.other",
		"*.example.com.":           "example-zone/*",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("placements = %v, want %v", got, want)
	}
}

func TestPlan_WildcardRecordNamesAreDeterministicAndValid(t *testing.T) {
	policy := testPolicy(nil, nil)
	zones := []Zone{{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true}}
	a := Target{Type: TargetA, Value: "192.0.2.10"}
	endpoints := []Endpoint{
		wildcardEndpoint("*.apps.example.com.", a),
		wildcardEndpoint("apps.example.com.", a),
		wildcardEndpoint("www.apps.example.com.", a),
	}

	first, _ := Plan(PlanInput{Policy: policy, Endpoints: endpoints, Zones: zones})
	second, _ := Plan(PlanInput{Policy: policy, Endpoints: endpoints, Zones: zones})
	if !reflect.DeepEqual(recordNames(first), recordNames(second)) {
		t.Fatalf("Plan is not deterministic: %v != %v", recordNames(first), recordNames(second))
	}
	if len(first) != 3 {
		t.Fatalf("expected 3 records, got %d", len(first))
	}

	seen := map[string]bool{}
	for _, r := range first {
		if seen[r.Name] {
			t.Errorf("duplicate Record CR name %q: the wildcard and its siblings must hash apart", r.Name)
		}
		seen[r.Name] = true
		if !strings.HasPrefix(r.Name, "public-") || len(r.Name) != len("public-")+nameHashLength {
			t.Errorf("name %q is not <policy>-<%d hex>", r.Name, nameHashLength)
		}
		if errs := validation.IsDNS1123Subdomain(r.Name); len(errs) != 0 {
			t.Errorf("name %q is not a valid object name: %v", r.Name, errs)
		}
	}

	// The name is a pure function of source kind, source name, hostname, type and target, so
	// the wildcard hostname name matches RecordName called directly.
	wantName := RecordName("public", endpoints[0], endpoints[0].Targets[0])
	if !seen[wantName] {
		t.Errorf("RecordName for the wildcard endpoint (%q) is not among the planned names %v", wantName, recordNames(first))
	}

	// Switching createPtr on must not rename or recreate anything.
	withPtr := testPolicy(nil, nil)
	withPtr.Spec.CreatePtr = true
	third, _ := Plan(PlanInput{Policy: withPtr, Endpoints: endpoints, Zones: zones})
	if !reflect.DeepEqual(recordNames(first), recordNames(third)) {
		t.Errorf("createPtr changed the Record CR names: %v != %v", recordNames(first), recordNames(third))
	}
}

func TestPlan_WildcardCNAMEIsNeverAnApexCNAME(t *testing.T) {
	policy := testPolicy(nil, nil)
	zones := []Zone{{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true}}
	ep := wildcardEndpoint("*.apps.example.com.", Target{Type: TargetCNAME, Value: "router.example.net."})

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: []Endpoint{ep}, Zones: zones})
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	if len(records) != 1 || records[0].Spec.Name != "*" || records[0].Spec.Type != uddiv1alpha1.RecordType("CNAME") {
		t.Fatalf("records = %+v, want one CNAME named *", records)
	}
}

func TestPlan_WildcardOutsideEveryZone(t *testing.T) {
	policy := testPolicy(nil, nil)
	zones := []Zone{{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true}}
	ep := wildcardEndpoint("*.example.com.", Target{Type: TargetA, Value: "192.0.2.10"})

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: []Endpoint{ep}, Zones: zones})
	if len(records) != 0 {
		t.Fatalf("expected no records, got %+v", records)
	}
	if len(skips) != 1 || skips[0].Reason != SkipNoMatchingZone || skips[0].Hostname != "*.example.com." {
		t.Fatalf("skips = %+v, want one NoMatchingZone for *.example.com.", skips)
	}
}

func TestPlan_WildcardInZoneNotReady(t *testing.T) {
	policy := testPolicy(nil, nil)
	zones := []Zone{{Name: "apps-zone", FQDN: "apps.example.com.", Ready: false}}
	ep := wildcardEndpoint("*.apps.example.com.", Target{Type: TargetA, Value: "192.0.2.10"})

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: []Endpoint{ep}, Zones: zones})
	if len(records) != 0 || len(skips) != 1 || skips[0].Reason != SkipZoneNotReady {
		t.Fatalf("records %+v skips %+v, want one ZoneNotReady skip", records, skips)
	}
}

func TestPlan_CreatePtr(t *testing.T) {
	zones := []Zone{{Name: "apps-zone", FQDN: "apps.example.com.", Ready: true}}
	src := SourceRef{Kind: SourceService, Namespace: "team-a", Name: "web"}
	endpoints := []Endpoint{
		{Source: src, Hostname: "v4.apps.example.com.", Targets: []Target{{Type: TargetA, Value: "192.0.2.10"}}},
		{Source: src, Hostname: "v6.apps.example.com.", Targets: []Target{{Type: TargetAAAA, Value: "2001:db8::10"}}},
		{Source: src, Hostname: "alias.apps.example.com.", Targets: []Target{{Type: TargetCNAME, Value: "v4.apps.example.com."}}},
		{Source: src, Hostname: "apps.example.com.", Targets: []Target{{Type: TargetA, Value: "192.0.2.11"}}}, // apex A
		wildcardEndpoint("*.apps.example.com.", Target{Type: TargetA, Value: "192.0.2.12"}),
	}

	byType := func(records []DesiredRecord) map[string][]*bool {
		out := map[string][]*bool{}
		for _, r := range records {
			out[string(r.Spec.Type)] = append(out[string(r.Spec.Type)], r.Spec.CreatePtr)
		}
		return out
	}

	t.Run("on: set on every A and AAAA, never on CNAME", func(t *testing.T) {
		policy := testPolicy(nil, nil)
		policy.Spec.CreatePtr = true
		records, skips := Plan(PlanInput{Policy: policy, Endpoints: endpoints, Zones: zones})
		if len(skips) != 0 {
			t.Fatalf("expected no skips, got %+v", skips)
		}
		got := byType(records)
		if len(got["A"]) != 3 || len(got["AAAA"]) != 1 || len(got["CNAME"]) != 1 {
			t.Fatalf("record types = %v, want 3 A, 1 AAAA, 1 CNAME", got)
		}
		for _, typ := range []string{"A", "AAAA"} {
			for _, p := range got[typ] {
				if p == nil || !*p {
					t.Errorf("%s record CreatePtr = %v, want true", typ, p)
				}
			}
		}
		if got["CNAME"][0] != nil {
			t.Errorf("CNAME record CreatePtr = %v, want nil", *got["CNAME"][0])
		}
	})

	t.Run("off: never set, not even to false", func(t *testing.T) {
		policy := testPolicy(nil, nil)
		records, _ := Plan(PlanInput{Policy: policy, Endpoints: endpoints, Zones: zones})
		for _, r := range records {
			if r.Spec.CreatePtr != nil {
				t.Errorf("%s record has CreatePtr %v with the policy flag off, want nil", r.Spec.Type, *r.Spec.CreatePtr)
			}
		}
	})
}
