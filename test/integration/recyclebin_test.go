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
	"github.com/infobloxopen/universal-ddi-go-client/ipam"

	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// TestRecycleBinIPSpace answers DESIGN.md section 14 open question 6 for IPSpace: after
// delete, does a natural-key List still return the object, and does re-creating the same
// name succeed immediately (as opposed to conflicting with a recycled object)?
func TestRecycleBinIPSpace(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	spaceName := objName("recycle-ipspace")
	created, err := client.IPAM.CreateIPSpace(ctx, ipam.IPSpace{Name: spaceName, Tags: testTags(nil)})
	if err != nil {
		t.Fatalf("CreateIPSpace: %v", err)
	}
	id := strv(created.Id)

	if err := client.IPAM.DeleteIPSpace(ctx, id); err != nil {
		t.Fatalf("DeleteIPSpace: %v", err)
	}

	afterDelete, err := client.IPAM.ListIPSpaces(ctx, uddi.ListOptions{Filter: `name=="` + spaceName + `"`, Limit: 10})
	if err != nil {
		t.Fatalf("ListIPSpaces by natural key after delete: %v", err)
	}
	stillVisible := containsID(afterDelete, id, func(s ipam.IPSpace) string { return strv(s.Id) })
	t.Logf("VERDICT: after deleting an IPSpace, a natural-key List still returns it: %v", stillVisible)

	recreated, err := client.IPAM.CreateIPSpace(ctx, ipam.IPSpace{Name: spaceName, Tags: testTags(nil)})
	if err != nil {
		t.Logf("VERDICT: re-creating an IPSpace with the same name immediately after delete failed: %v", err)
		return
	}
	registerDelete(t, "ip space", strv(recreated.Id), client.IPAM.DeleteIPSpace)
	t.Log("VERDICT: re-creating an IPSpace with the same name immediately after delete succeeds")
}

// TestRecycleBinView answers the same question for View.
func TestRecycleBinView(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	viewName := objName("recycle-view")
	created, err := client.DNSConfig.CreateView(ctx, dnsconfig.View{Name: viewName, Tags: testTags(nil)})
	if err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	id := strv(created.Id)

	if err := client.DNSConfig.DeleteView(ctx, id); err != nil {
		t.Fatalf("DeleteView: %v", err)
	}

	afterDelete, err := client.DNSConfig.ListViews(ctx, uddi.ListOptions{Filter: `name=="` + viewName + `"`, Limit: 10})
	if err != nil {
		t.Fatalf("ListViews by natural key after delete: %v", err)
	}
	stillVisible := containsID(afterDelete, id, func(v dnsconfig.View) string { return strv(v.Id) })
	t.Logf("VERDICT: after deleting a View, a natural-key List still returns it: %v", stillVisible)

	recreated, err := client.DNSConfig.CreateView(ctx, dnsconfig.View{Name: viewName, Tags: testTags(nil)})
	if err != nil {
		t.Logf("VERDICT: re-creating a View with the same name immediately after delete failed: %v", err)
		return
	}
	registerDelete(t, "view", strv(recreated.Id), client.DNSConfig.DeleteView)
	t.Log("VERDICT: re-creating a View with the same name immediately after delete succeeds")
}
