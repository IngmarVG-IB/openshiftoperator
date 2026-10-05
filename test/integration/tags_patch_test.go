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
)

// TestTagsPatchSemantics answers the question DESIGN.md section 6 ("Tag removal") marks
// **verify**: does a PATCH that carries tags replace the object's whole tag map, or merge
// into it? The operator removes a tag it applied earlier by sending the full desired map
// without that key (internal/tags.ForPatch), which only works if PATCH replaces the map.
//
// The test creates an IP space with two extra tags next to the run's own tags, patches it
// with the same map minus one of the extras, reads it back and logs a VERDICT line. The
// run tags ride along on the patch either way, so cleanup still finds the object.
func TestTagsPatchSemantics(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	const (
		keepKey   = "uddi_operator_it_keep"
		keepValue = "keep-value"
		dropKey   = "uddi_operator_it_drop"
		dropValue = "drop-value"
	)

	spaceName := objName("tagspatch-ipspace")
	created, err := client.IPAM.CreateIPSpace(ctx, ipam.IPSpace{
		Name: spaceName,
		Tags: testTags(map[string]string{keepKey: keepValue, dropKey: dropValue}),
	})
	if err != nil {
		t.Fatalf("CreateIPSpace: %v", err)
	}
	id := strv(created.Id)
	registerDelete(t, "ip space", id, client.IPAM.DeleteIPSpace)

	if !hasTag(created.Tags, keepKey) || !hasTag(created.Tags, dropKey) {
		t.Fatalf("CreateIPSpace: tags = %v, want both %s and %s", created.Tags, keepKey, dropKey)
	}

	// ipam.IPSpace.Name is a required, non-pointer field and always rides along on a
	// patch (see ipspace_adapter.go's Diff).
	if _, err := client.IPAM.UpdateIPSpace(ctx, id, ipam.IPSpace{
		Name: spaceName,
		Tags: testTags(map[string]string{keepKey: keepValue}),
	}); err != nil {
		t.Fatalf("UpdateIPSpace with %s removed from tags: %v", dropKey, err)
	}

	got, err := client.IPAM.GetIPSpace(ctx, id)
	if err != nil {
		t.Fatalf("GetIPSpace after the tag patch: %v", err)
	}
	if !hasTag(got.Tags, keepKey) || !hasTag(got.Tags, tagRunKey) {
		t.Fatalf("GetIPSpace after the tag patch: tags = %v, want %s and %s kept", got.Tags, keepKey, tagRunKey)
	}

	if hasTag(got.Tags, dropKey) {
		t.Logf("VERDICT: PATCH of tags merges into the map (%s survived a patch that omitted it; tags = %v); "+
			"tag removal in internal/tags.ForPatch does not work against this tenant", dropKey, got.Tags)
		return
	}
	t.Logf("VERDICT: PATCH of tags replaces the map (%s is gone after a patch that omitted it; tags = %v)", dropKey, got.Tags)
}
