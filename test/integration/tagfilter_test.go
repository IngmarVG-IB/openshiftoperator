//go:build integration

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

package integration

import (
	"testing"

	"github.com/infobloxopen/universal-ddi-go-client/ipam"

	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// TestTagFilterGrammar answers DESIGN.md section 14's tag lookup question: confirms the
// single-quoted 'key'=='value' _tfilter form uddi.TagFilter builds actually works
// against a real tenant, and records whether the double-quoted form also works (the
// _filter grammar in docs/uddi-api-notes.md accepts double-quoted string literals, but
// the _tfilter grammar's confirmed source -- the Terraform provider's
// ExpandFrameworkMapTagFilterString -- only ever emits single quotes).
func TestTagFilterGrammar(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	const probeKey = "uddi_operator_it_probe"
	const probeValue = "probe-value"

	created, err := client.IPAM.CreateIPSpace(ctx, ipam.IPSpace{
		Name: objName("tagfilter-ipspace"),
		Tags: testTags(map[string]string{probeKey: probeValue}),
	})
	if err != nil {
		t.Fatalf("CreateIPSpace: %v", err)
	}
	id := strv(created.Id)
	registerDelete(t, "ip space", id, client.IPAM.DeleteIPSpace)

	singleQuoted := uddi.TagFilter(map[string]string{probeKey: probeValue})
	bySingle, err := client.IPAM.ListIPSpaces(ctx, uddi.ListOptions{TFilter: singleQuoted, Limit: 10})
	if err != nil {
		t.Fatalf("VERDICT: the single-quoted _tfilter form (%s) errored: %v", singleQuoted, err)
	}
	if !containsID(bySingle, id, func(s ipam.IPSpace) string { return strv(s.Id) }) {
		t.Fatalf("VERDICT: the single-quoted _tfilter form (%s) did not find the tagged object", singleQuoted)
	}
	t.Logf("VERDICT: the single-quoted _tfilter form ('key'=='value') works: %s", singleQuoted)

	doubleQuoted := `"` + probeKey + `"=="` + probeValue + `"`
	byDouble, err := client.IPAM.ListIPSpaces(ctx, uddi.ListOptions{TFilter: doubleQuoted, Limit: 10})
	switch {
	case err != nil:
		t.Logf("VERDICT: the double-quoted _tfilter form (%s) is rejected: %v", doubleQuoted, err)
	case containsID(byDouble, id, func(s ipam.IPSpace) string { return strv(s.Id) }):
		t.Logf("VERDICT: the double-quoted _tfilter form (%s) also works", doubleQuoted)
	default:
		t.Logf("VERDICT: the double-quoted _tfilter form (%s) returned no error but did not find the tagged object (%d results)", doubleQuoted, len(byDouble))
	}
}
