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

// TestGetAfterDelete answers DESIGN.md section 14 open question 7: Get on a deleted id
// returns 404, recognised by uddi.IsNotFound.
func TestGetAfterDelete(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	created, err := client.IPAM.CreateIPSpace(ctx, ipam.IPSpace{
		Name: objName("get-after-delete-ipspace"),
		Tags: testTags(nil),
	})
	if err != nil {
		t.Fatalf("CreateIPSpace: %v", err)
	}
	id := strv(created.Id)

	if err := client.IPAM.DeleteIPSpace(ctx, id); err != nil {
		t.Fatalf("DeleteIPSpace: %v", err)
	}

	_, err = client.IPAM.GetIPSpace(ctx, id)
	if err == nil {
		t.Fatal("VERDICT: Get on a deleted IPSpace id succeeded; expected 404")
	}
	if !uddi.IsNotFound(err) {
		t.Fatalf("VERDICT: Get on a deleted IPSpace id failed with a status uddi.IsNotFound does not recognise: %v", err)
	}
	t.Logf("VERDICT: Get on a deleted IPSpace id returns 404, recognised by uddi.IsNotFound (%v)", err)
}
