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

// TestIPSpace exercises the IPSpace lifecycle: create with tags, read back, the
// natural-key filter ipspaceAdapter.Find builds (name=="..."), the run's tfilter, a
// comment patch and delete. What happens to the natural-key lookup after delete is
// answered separately in recyclebin_test.go.
func TestIPSpace(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	spaceName := objName("ipspace", "1")
	created, err := client.IPAM.CreateIPSpace(ctx, ipam.IPSpace{
		Name: spaceName,
		Tags: testTags(nil),
	})
	if err != nil {
		t.Fatalf("CreateIPSpace: %v", err)
	}
	id := strv(created.Id)
	registerDelete(t, "ip space", id, client.IPAM.DeleteIPSpace)

	got, err := client.IPAM.GetIPSpace(ctx, id)
	if err != nil {
		t.Fatalf("GetIPSpace: %v", err)
	}
	if got.Name != spaceName {
		t.Fatalf("GetIPSpace: name = %q, want %q", got.Name, spaceName)
	}

	byName, err := client.IPAM.ListIPSpaces(ctx, uddi.ListOptions{Filter: `name=="` + spaceName + `"`, Limit: 1})
	if err != nil {
		t.Fatalf("ListIPSpaces by natural key: %v", err)
	}
	if !containsID(byName, id, func(s ipam.IPSpace) string { return strv(s.Id) }) {
		t.Fatalf("ListIPSpaces by natural key: created ip space %s not among %d results", id, len(byName))
	}

	byTag, err := client.IPAM.ListIPSpaces(ctx, uddi.ListOptions{TFilter: uddi.TagFilter(map[string]string{tagRunKey: runID}), Limit: 100})
	if err != nil {
		t.Fatalf("ListIPSpaces by tfilter: %v", err)
	}
	if !containsID(byTag, id, func(s ipam.IPSpace) string { return strv(s.Id) }) {
		t.Fatalf("ListIPSpaces by tfilter: created ip space %s not among %d results", id, len(byTag))
	}

	// ipam.IPSpace.Name is a required, non-pointer field and always rides along on a
	// patch (see the comment in view_test.go's TestView, and ipspace_adapter.go's Diff).
	const comment = "patched by uddi-operator integration test"
	updated, err := client.IPAM.UpdateIPSpace(ctx, id, ipam.IPSpace{Name: spaceName, Comment: ptr(comment)})
	if err != nil {
		t.Fatalf("UpdateIPSpace: %v", err)
	}
	if strv(updated.Comment) != comment {
		t.Fatalf("UpdateIPSpace: comment = %q, want %q", strv(updated.Comment), comment)
	}

	if err := client.IPAM.DeleteIPSpace(ctx, id); err != nil {
		t.Fatalf("DeleteIPSpace: %v", err)
	}
}
