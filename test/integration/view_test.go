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

	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"

	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// TestView exercises the View lifecycle: create with tags, read back, the natural-key
// filter viewAdapter.Find builds (name=="..."), the run's tfilter, a comment patch and
// delete. What happens to the natural-key lookup after delete is answered separately in
// recyclebin_test.go.
func TestView(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	viewName := objName("view", "1")
	created, err := client.DNSConfig.CreateView(ctx, dnsconfig.View{
		Name: viewName,
		Tags: testTags(nil),
	})
	if err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	id := strv(created.Id)
	registerDelete(t, "view", id, client.DNSConfig.DeleteView)

	got, err := client.DNSConfig.GetView(ctx, id)
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if got.Name != viewName {
		t.Fatalf("GetView: name = %q, want %q", got.Name, viewName)
	}

	byName, err := client.DNSConfig.ListViews(ctx, uddi.ListOptions{Filter: `name=="` + viewName + `"`, Limit: 1})
	if err != nil {
		t.Fatalf("ListViews by natural key: %v", err)
	}
	if !containsID(byName, id, func(v dnsconfig.View) string { return strv(v.Id) }) {
		t.Fatalf("ListViews by natural key: created view %s not among %d results", id, len(byName))
	}

	byTag, err := client.DNSConfig.ListViews(ctx, uddi.ListOptions{TFilter: uddi.TagFilter(map[string]string{tagRunKey: runID}), Limit: 100})
	if err != nil {
		t.Fatalf("ListViews by tfilter: %v", err)
	}
	if !containsID(byTag, id, func(v dnsconfig.View) string { return strv(v.Id) }) {
		t.Fatalf("ListViews by tfilter: created view %s not among %d results", id, len(byTag))
	}

	// dnsconfig.View.Name is a required, non-pointer field: the generated MarshalJSON
	// always includes "name" (ToMap has no IsNil guard on it, unlike every *string
	// field), so a patch that omits Name would send name="" and rename the view. Every
	// production adapter that patches a View or IPSpace carries the current name along
	// for exactly this reason (view_adapter.go, ipspace_adapter.go); this test mirrors
	// that rather than asserting the omit-it behavior against a real tenant.
	const comment = "patched by uddi-operator integration test"
	updated, err := client.DNSConfig.UpdateView(ctx, id, dnsconfig.View{Name: viewName, Comment: ptr(comment)})
	if err != nil {
		t.Fatalf("UpdateView: %v", err)
	}
	if strv(updated.Comment) != comment {
		t.Fatalf("UpdateView: comment = %q, want %q", strv(updated.Comment), comment)
	}

	if err := client.DNSConfig.DeleteView(ctx, id); err != nil {
		t.Fatalf("DeleteView: %v", err)
	}
}
