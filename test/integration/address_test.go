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

// TestAddress exercises the Address lifecycle for an explicit (non next-available)
// address: create inside a Subnet of a fresh IPSpace, read back, the natural-key filter
// addressAdapter.Find builds (address and space), the run's tfilter, a comment patch and
// delete. Next-available allocation is covered separately in nextavailable_test.go.
func TestAddress(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	space, err := client.IPAM.CreateIPSpace(ctx, ipam.IPSpace{Name: objName("address-space"), Tags: testTags(nil)})
	if err != nil {
		t.Fatalf("CreateIPSpace (dependency): %v", err)
	}
	spaceID := strv(space.Id)
	registerDelete(t, "ip space", spaceID, client.IPAM.DeleteIPSpace)

	const address = "10.63.0.5"
	mustParentSubnet(ctx, t, spaceID, "10.63.0.0")
	created, err := client.IPAM.CreateAddress(ctx, ipam.Address{
		Address: address,
		Space:   &spaceID,
		Names:   []ipam.Name{{Name: "svc-a", Type: "user"}},
		Tags:    testTags(nil),
	})
	if err != nil {
		t.Fatalf("CreateAddress: %v", err)
	}
	id := strv(created.Id)
	registerDelete(t, "address", id, client.IPAM.DeleteAddress)

	got, err := client.IPAM.GetAddress(ctx, id)
	if err != nil {
		t.Fatalf("GetAddress: %v", err)
	}
	if got.Address != address {
		t.Fatalf("GetAddress: address = %q, want %q", got.Address, address)
	}

	natFilter := `address=="` + address + `" and space=="` + spaceID + `"`
	byNat, err := client.IPAM.ListAddresses(ctx, uddi.ListOptions{Filter: natFilter, Limit: 1})
	if err != nil {
		t.Fatalf("ListAddresses by natural key: %v", err)
	}
	if !containsID(byNat, id, func(a ipam.Address) string { return strv(a.Id) }) {
		t.Fatalf("ListAddresses by natural key (%s): created address %s not among %d results", natFilter, id, len(byNat))
	}

	byTag, err := client.IPAM.ListAddresses(ctx, uddi.ListOptions{TFilter: uddi.TagFilter(map[string]string{tagRunKey: runID}), Limit: 100})
	if err != nil {
		t.Fatalf("ListAddresses by tfilter: %v", err)
	}
	if !containsID(byTag, id, func(a ipam.Address) string { return strv(a.Id) }) {
		t.Fatalf("ListAddresses by tfilter: created address %s not among %d results", id, len(byTag))
	}

	// ipam.Address.Address is a required, non-pointer field with no omitempty guard in
	// the generated MarshalJSON, so (like View.Name) it must ride along on every patch.
	// addressAdapter.Diff already does this; mirror it here.
	const comment = "patched by uddi-operator integration test"
	updated, err := client.IPAM.UpdateAddress(ctx, id, ipam.Address{Address: address, Comment: ptr(comment)})
	if err != nil {
		t.Fatalf("UpdateAddress: %v", err)
	}
	if strv(updated.Comment) != comment {
		t.Fatalf("UpdateAddress: comment = %q, want %q", strv(updated.Comment), comment)
	}

	if err := client.IPAM.DeleteAddress(ctx, id); err != nil {
		t.Fatalf("DeleteAddress: %v", err)
	}
}
