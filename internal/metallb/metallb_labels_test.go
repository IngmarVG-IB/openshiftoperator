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

// An external test package so that importing internal/ipalloc here can never form an import
// cycle if the planner ever imports this package.
package metallb_test

import (
	"testing"

	"github.com/IngmarVG-IB/openshiftoperator/internal/ipalloc"
	"github.com/IngmarVG-IB/openshiftoperator/internal/metallb"
)

// TestLabelPolicyMatchesIPAlloc keeps the policy label on mirrored pools identical to the
// one stamped on generated Addresses: the package declares its own copy so that it stays
// free of the IPAllocationPolicy API types.
func TestLabelPolicyMatchesIPAlloc(t *testing.T) {
	if metallb.LabelPolicy != ipalloc.LabelPolicy {
		t.Errorf("metallb.LabelPolicy = %q, ipalloc.LabelPolicy = %q", metallb.LabelPolicy, ipalloc.LabelPolicy)
	}
}
