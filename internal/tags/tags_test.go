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

package tags

import (
	"reflect"
	"testing"
)

func TestMergeOrdering(t *testing.T) {
	owner := Ownership("cluster-1", "team-a", "View", "default", "uid-1")
	connectionDefaults := map[string]string{
		"env":      "prod",
		"team":     "platform",
		KeyManaged: "false",
		KeyUID:     "hijacked",
	}
	spec := map[string]string{
		"env":      "staging",
		"app":      "web",
		KeyCluster: "hijacked",
	}

	got := Merge(owner, connectionDefaults, spec)

	want := map[string]string{
		"team":     "platform",
		"app":      "web",
		"env":      "staging", // spec wins over the Connection's defaults
		KeyManaged: ValueManaged,
		KeyCluster: "cluster-1",
		KeyOwner:   "team-a/View/default",
		KeyUID:     "uid-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Merge() = %#v, want %#v", got, want)
	}
}

func TestMergeDoesNotAliasInputs(t *testing.T) {
	spec := map[string]string{"env": "prod"}
	got := Merge(nil, nil, spec)
	got["env"] = "mutated"
	if spec["env"] != "prod" {
		t.Fatalf("Merge aliased its input: spec[env] = %q", spec["env"])
	}
}

func TestMergeEmptyInputs(t *testing.T) {
	got := Merge(nil, nil, nil)
	if got == nil {
		t.Fatal("Merge(nil, nil, nil) returned nil, want an empty map")
	}
	if len(got) != 0 {
		t.Fatalf("Merge(nil, nil, nil) = %#v, want an empty map", got)
	}
}

func TestOwnershipOmitsAnEmptyClusterID(t *testing.T) {
	got := Ownership("", "team-a", "View", "default", "uid-1")
	if _, ok := got[KeyCluster]; ok {
		t.Fatalf("Ownership set %s with an empty cluster id", KeyCluster)
	}
}

func TestOwnedBy(t *testing.T) {
	cases := []struct {
		name string
		tags map[string]string
		uid  string
		want bool
	}{
		{"match", map[string]string{KeyUID: "uid-1"}, "uid-1", true},
		{"mismatch", map[string]string{KeyUID: "uid-2"}, "uid-1", false},
		{"absent", map[string]string{KeyManaged: ValueManaged}, "uid-1", false},
		{"nil tags", nil, "uid-1", false},
		{"empty uid never matches", map[string]string{KeyUID: ""}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OwnedBy(tc.tags, tc.uid); got != tc.want {
				t.Fatalf("OwnedBy(%v, %q) = %v, want %v", tc.tags, tc.uid, got, tc.want)
			}
		})
	}
}

func TestNewDesired(t *testing.T) {
	want := Merge(Ownership("cluster-1", "team-a", "View", "default", "uid-1"),
		map[string]string{"env": "prod"}, map[string]string{"app": "web"})

	cases := []struct {
		name              string
		want              map[string]string
		previouslyApplied []string
		wantRemove        []string
	}{
		{"nil previouslyApplied removes nothing", want, nil, nil},
		{"empty previouslyApplied removes nothing", want, []string{}, nil},
		{"every applied key still wanted", want, []string{"app", "env", KeyManaged, KeyUID}, nil},
		{"dropped keys are removed, sorted", want, []string{"zeta", "app", "alpha", "env"}, []string{"alpha", "zeta"}},
		{"duplicates collapse", want, []string{"old", "old"}, []string{"old"}},
		{
			// Ownership keys are never removed, even when they are absent from want, for
			// example a cluster tag that a later configuration no longer sets.
			"ownership keys are never removed",
			map[string]string{"app": "web"},
			[]string{KeyManaged, KeyCluster, KeyOwner, KeyUID, "old"},
			[]string{"old"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewDesired(tc.want, tc.previouslyApplied)
			if !reflect.DeepEqual(got.Remove, tc.wantRemove) {
				t.Fatalf("NewDesired(Remove) = %#v, want %#v", got.Remove, tc.wantRemove)
			}
			if !reflect.DeepEqual(got.Want, tc.want) {
				t.Fatalf("NewDesired(Want) = %#v, want %#v", got.Want, tc.want)
			}
		})
	}
}

func TestForPatch(t *testing.T) {
	owner := Ownership("cluster-1", "team-a", "View", "default", "uid-1")
	applied := func(extra map[string]string) map[string]string { return Merge(owner, nil, extra) }

	cases := []struct {
		name        string
		current     map[string]string
		want        map[string]string
		applied     []string
		expect      map[string]string
		wantChanged bool
	}{
		{
			name:        "a key dropped from the desired set is removed",
			current:     applied(map[string]string{"env": "prod", "app": "web"}),
			want:        applied(map[string]string{"env": "prod"}),
			applied:     Keys(applied(map[string]string{"env": "prod", "app": "web"})),
			expect:      applied(map[string]string{"env": "prod"}),
			wantChanged: true,
		},
		{
			name:        "a foreign key the operator never wrote is kept",
			current:     applied(map[string]string{"env": "prod", "set-by-hand": "keep"}),
			want:        applied(map[string]string{"env": "prod"}),
			applied:     Keys(applied(map[string]string{"env": "prod"})),
			expect:      applied(map[string]string{"env": "prod", "set-by-hand": "keep"}),
			wantChanged: false,
		},
		{
			// The desired set no longer carries a cluster tag (an empty cluster id), and
			// the applied keys list every ownership key: "old" goes, the cluster tag
			// already on the object stays.
			name:        "ownership keys are never removed",
			current:     applied(map[string]string{"old": "x"}),
			want:        Ownership("", "team-a", "View", "default", "uid-1"),
			applied:     []string{KeyManaged, KeyCluster, KeyOwner, KeyUID, "old"},
			expect:      applied(nil),
			wantChanged: true,
		},
		{
			name:        "an empty previouslyApplied removes nothing",
			current:     applied(map[string]string{"env": "prod", "app": "web"}),
			want:        applied(map[string]string{"env": "prod"}),
			applied:     nil,
			expect:      applied(map[string]string{"env": "prod", "app": "web"}),
			wantChanged: false,
		},
		{
			name:        "a changed value is a change",
			current:     applied(map[string]string{"env": "old", "set-by-hand": "keep"}),
			want:        applied(map[string]string{"env": "new"}),
			applied:     Keys(applied(map[string]string{"env": "old"})),
			expect:      applied(map[string]string{"env": "new", "set-by-hand": "keep"}),
			wantChanged: true,
		},
		{
			name:        "a new key is a change",
			current:     applied(nil),
			want:        applied(map[string]string{"env": "prod"}),
			applied:     Keys(applied(nil)),
			expect:      applied(map[string]string{"env": "prod"}),
			wantChanged: true,
		},
		{
			name:        "no-op when everything already matches",
			current:     applied(map[string]string{"env": "prod"}),
			want:        applied(map[string]string{"env": "prod"}),
			applied:     Keys(applied(map[string]string{"env": "prod"})),
			expect:      applied(map[string]string{"env": "prod"}),
			wantChanged: false,
		},
		{
			name:        "a Remove key already absent from current is not a change",
			current:     applied(map[string]string{"env": "prod"}),
			want:        applied(map[string]string{"env": "prod"}),
			applied:     Keys(applied(map[string]string{"env": "prod", "gone-already": "x"})),
			expect:      applied(map[string]string{"env": "prod"}),
			wantChanged: false,
		},
		{
			name:        "an untagged object gets the desired tags",
			current:     nil,
			want:        applied(nil),
			applied:     nil,
			expect:      applied(nil),
			wantChanged: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := make(map[string]string, len(tc.current))
			for k, v := range tc.current {
				before[k] = v
			}
			got, changed := ForPatch(tc.current, NewDesired(tc.want, tc.applied))
			if changed != tc.wantChanged {
				t.Fatalf("ForPatch changed = %v, want %v", changed, tc.wantChanged)
			}
			if !reflect.DeepEqual(got, tc.expect) {
				t.Fatalf("ForPatch() = %#v, want %#v", got, tc.expect)
			}
			if len(tc.current) > 0 && !reflect.DeepEqual(tc.current, before) {
				t.Fatalf("ForPatch mutated current: %#v, was %#v", tc.current, before)
			}
		})
	}
}

func TestKeysIsSorted(t *testing.T) {
	got := Keys(map[string]string{"b": "1", "a": "2", "c": "3"})
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Keys() = %#v, want %#v", got, want)
	}
	if got := Keys(nil); got != nil {
		t.Fatalf("Keys(nil) = %#v, want nil", got)
	}
}

func TestUIDFilterUsesSingleQuotes(t *testing.T) {
	got := UIDFilter("uid-1")
	want := `'uddi_operator_uid'=='uid-1'`
	if got != want {
		t.Fatalf("UIDFilter() = %q, want %q", got, want)
	}
}
