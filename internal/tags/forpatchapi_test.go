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

func TestForPatchAPIKeepsForeignValuesOfAnyType(t *testing.T) {
	current := map[string]interface{}{
		KeyUID:       "uid-1",
		"env":        "prod",
		"stale":      "old",
		"cost-units": float64(42),
		"pinned":     true,
		"nested":     map[string]interface{}{"a": "b"},
	}
	d := NewDesired(map[string]string{KeyUID: "uid-1", "env": "staging"}, []string{KeyUID, "env", "stale"})

	got, changed := ForPatchAPI(current, d)
	if !changed {
		t.Fatalf("expected a change: env differs and stale is removed")
	}
	want := map[string]interface{}{
		KeyUID:       "uid-1",
		"env":        "staging",
		"cost-units": float64(42),
		"pinned":     true,
		"nested":     map[string]interface{}{"a": "b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ForPatchAPI = %#v, want %#v", got, want)
	}
	if _, ok := current["stale"]; !ok {
		t.Fatalf("ForPatchAPI must not modify the current map")
	}
}

func TestForPatchAPINoChange(t *testing.T) {
	current := map[string]interface{}{KeyUID: "uid-1", "env": "prod", "count": float64(3)}
	d := NewDesired(map[string]string{KeyUID: "uid-1", "env": "prod"}, []string{KeyUID, "env", "gone"})

	if _, changed := ForPatchAPI(current, d); changed {
		t.Fatalf("no change expected: wanted values match and the removed key is already absent")
	}
}

func TestForPatchAPIStringReplacesForeignTypedValue(t *testing.T) {
	// A key the operator wants, currently holding a non-string value, is a change and is
	// overwritten with the wanted string.
	current := map[string]interface{}{"env": float64(1)}
	got, changed := ForPatchAPI(current, NewDesired(map[string]string{"env": "prod"}, nil))
	if !changed || got["env"] != "prod" {
		t.Fatalf("ForPatchAPI = %#v, %v; want env=prod and changed", got, changed)
	}
}

func TestForPatchAPIWantWinsOverRemove(t *testing.T) {
	current := map[string]interface{}{"env": "prod"}
	d := Desired{Want: map[string]string{"env": "prod"}, Remove: []string{"env"}}
	got, changed := ForPatchAPI(current, d)
	if changed || got["env"] != "prod" {
		t.Fatalf("ForPatchAPI = %#v, %v; want env kept and no change", got, changed)
	}
}
