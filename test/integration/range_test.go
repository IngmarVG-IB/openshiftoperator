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

// TestRange exercises the Range lifecycle: create inside a Subnet of a fresh IPSpace, read back, the
// natural-key filter rangeAdapter.Find builds (start, end and space), the run's tfilter,
// a comment patch and delete.
func TestRange(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	space, err := client.IPAM.CreateIPSpace(ctx, ipam.IPSpace{Name: objName("range-space"), Tags: testTags(nil)})
	if err != nil {
		t.Fatalf("CreateIPSpace (dependency): %v", err)
	}
	spaceID := strv(space.Id)
	registerDelete(t, "ip space", spaceID, client.IPAM.DeleteIPSpace)

	const start = "10.62.0.10"
	const end = "10.62.0.20"
	mustParentSubnet(ctx, t, spaceID, "10.62.0.0")
	created, err := client.IPAM.CreateRange(ctx, ipam.Range{
		Start: start,
		End:   end,
		Space: &spaceID,
		Tags:  testTags(nil),
	})
	if err != nil {
		t.Fatalf("CreateRange: %v", err)
	}
	id := strv(created.Id)
	registerDelete(t, "range", id, client.IPAM.DeleteRange)

	got, err := client.IPAM.GetRange(ctx, id)
	if err != nil {
		t.Fatalf("GetRange: %v", err)
	}
	if got.Start != start || got.End != end {
		t.Fatalf("GetRange: start/end = %q/%q, want %q/%q", got.Start, got.End, start, end)
	}

	natFilter := `start=="` + start + `" and end=="` + end + `" and space=="` + spaceID + `"`
	byNat, err := client.IPAM.ListRanges(ctx, uddi.ListOptions{Filter: natFilter, Limit: 1})
	if err != nil {
		t.Fatalf("ListRanges by natural key: %v", err)
	}
	if !containsID(byNat, id, func(r ipam.Range) string { return strv(r.Id) }) {
		t.Fatalf("ListRanges by natural key (%s): created range %s not among %d results", natFilter, id, len(byNat))
	}

	byTag, err := client.IPAM.ListRanges(ctx, uddi.ListOptions{TFilter: uddi.TagFilter(map[string]string{tagRunKey: runID}), Limit: 100})
	if err != nil {
		t.Fatalf("ListRanges by tfilter: %v", err)
	}
	if !containsID(byTag, id, func(r ipam.Range) string { return strv(r.Id) }) {
		t.Fatalf("ListRanges by tfilter: created range %s not among %d results", id, len(byTag))
	}

	// ipam.Range.Start and .End are required, non-pointer fields with no omitempty
	// guard in the generated MarshalJSON, so (like View.Name and IPSpace.Name) they must
	// ride along on every patch. rangeAdapter.Diff already does this; mirror it here.
	const comment = "patched by uddi-operator integration test"
	updated, err := client.IPAM.UpdateRange(ctx, id, ipam.Range{Start: start, End: end, Comment: ptr(comment)})
	if err != nil {
		t.Fatalf("UpdateRange: %v", err)
	}
	if strv(updated.Comment) != comment {
		t.Fatalf("UpdateRange: comment = %q, want %q", strv(updated.Comment), comment)
	}

	if err := client.IPAM.DeleteRange(ctx, id); err != nil {
		t.Fatalf("DeleteRange: %v", err)
	}
}
