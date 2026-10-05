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

package labelvalue

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

func TestForKeepsShortNames(t *testing.T) {
	for _, name := range []string{"web", "worker-1.example.com", strings.Repeat("a", MaxLength)} {
		if got := For(name); got != name {
			t.Errorf("For(%q) = %q, want it unchanged", name, got)
		}
	}
}

func TestForShortensLongNamesStablyAndValidly(t *testing.T) {
	long := strings.Repeat("node-", 20) + "worker.cluster.example.com"
	got := For(long)
	if len(got) > MaxLength {
		t.Fatalf("For returned %d characters, want at most %d", len(got), MaxLength)
	}
	if errs := validation.IsValidLabelValue(got); len(errs) > 0 {
		t.Fatalf("For(%q) = %q is not a valid label value: %v", long, got, errs)
	}
	if For(long) != got {
		t.Fatalf("For is not deterministic")
	}
	other := strings.Repeat("node-", 20) + "worker.cluster.example.org"
	if For(other) == got {
		t.Fatalf("two long names with a shared prefix map to the same value %q", got)
	}
}

func TestForTrimsTrailingSeparatorsBeforeTheHash(t *testing.T) {
	// The cut falls right after a dash; the value must still end the prefix with an
	// alphanumeric character before the joining dash.
	name := strings.Repeat("a", MaxLength-hashLength-2) + "-" + strings.Repeat("b", 40)
	got := For(name)
	if errs := validation.IsValidLabelValue(got); len(errs) > 0 {
		t.Fatalf("For(%q) = %q is not a valid label value: %v", name, got, errs)
	}
	if strings.Contains(got, "--") {
		t.Fatalf("For(%q) = %q has a doubled dash", name, got)
	}
}
