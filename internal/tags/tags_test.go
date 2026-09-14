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

func TestForPatchPreservesUnknownKeys(t *testing.T) {
	current := map[string]string{"set-by-hand": "keep", "env": "old"}
	desired := map[string]string{"env": "new", KeyUID: "uid-1"}

	got, changed := ForPatch(current, desired)
	if !changed {
		t.Fatal("ForPatch reported no change")
	}
	want := map[string]string{"set-by-hand": "keep", "env": "new", KeyUID: "uid-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ForPatch() = %#v, want %#v", got, want)
	}

	if _, changed := ForPatch(want, desired); changed {
		t.Fatal("ForPatch reported a change when the desired tags were already present")
	}
}

func TestUIDFilterUsesSingleQuotes(t *testing.T) {
	got := UIDFilter("uid-1")
	want := `'uddi_operator_uid'=='uid-1'`
	if got != want {
		t.Fatalf("UIDFilter() = %q, want %q", got, want)
	}
}
