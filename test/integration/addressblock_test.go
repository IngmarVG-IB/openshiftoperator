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
	"fmt"
	"testing"

	"github.com/infobloxopen/universal-ddi-go-client/ipam"

	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// TestAddressBlock exercises the AddressBlock lifecycle: create under a fresh IPSpace,
// read back, the natural-key filter addressblockAdapter.Find builds (address, cidr and
// space), the run's tfilter, a comment patch and delete. Address and Cidr are *string /
// *int64 on ipam.AddressBlock (guarded by IsNil in the generated MarshalJSON), so unlike
// View.Name or ipam.Address.Address, a comment-only patch does not need to carry them
// forward.
func TestAddressBlock(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	space, err := client.IPAM.CreateIPSpace(ctx, ipam.IPSpace{Name: objName("ab-space"), Tags: testTags(nil)})
	if err != nil {
		t.Fatalf("CreateIPSpace (dependency): %v", err)
	}
	spaceID := strv(space.Id)
	registerDelete(t, "ip space", spaceID, client.IPAM.DeleteIPSpace)

	const address = "10.60.0.0"
	const cidr = int64(24)
	created, err := client.IPAM.CreateAddressBlock(ctx, ipam.AddressBlock{
		Address: ptr(address),
		Cidr:    ptr(cidr),
		Space:   &spaceID,
		Tags:    testTags(nil),
	})
	if err != nil {
		t.Fatalf("CreateAddressBlock: %v", err)
	}
	id := strv(created.Id)
	registerDelete(t, "address block", id, client.IPAM.DeleteAddressBlock)

	got, err := client.IPAM.GetAddressBlock(ctx, id)
	if err != nil {
		t.Fatalf("GetAddressBlock: %v", err)
	}
	if strv(got.Address) != address {
		t.Fatalf("GetAddressBlock: address = %q, want %q", strv(got.Address), address)
	}

	natFilter := fmt.Sprintf(`address=="%s" and cidr==%d and space=="%s"`, address, cidr, spaceID)
	byNat, err := client.IPAM.ListAddressBlocks(ctx, uddi.ListOptions{Filter: natFilter, Limit: 1})
	if err != nil {
		t.Fatalf("ListAddressBlocks by natural key: %v", err)
	}
	if !containsID(byNat, id, func(b ipam.AddressBlock) string { return strv(b.Id) }) {
		t.Fatalf("ListAddressBlocks by natural key (%s): created block %s not among %d results", natFilter, id, len(byNat))
	}

	byTag, err := client.IPAM.ListAddressBlocks(ctx, uddi.ListOptions{TFilter: uddi.TagFilter(map[string]string{tagRunKey: runID}), Limit: 100})
	if err != nil {
		t.Fatalf("ListAddressBlocks by tfilter: %v", err)
	}
	if !containsID(byTag, id, func(b ipam.AddressBlock) string { return strv(b.Id) }) {
		t.Fatalf("ListAddressBlocks by tfilter: created block %s not among %d results", id, len(byTag))
	}

	const comment = "patched by uddi-operator integration test"
	updated, err := client.IPAM.UpdateAddressBlock(ctx, id, ipam.AddressBlock{Comment: ptr(comment)})
	if err != nil {
		t.Fatalf("UpdateAddressBlock: %v", err)
	}
	if strv(updated.Comment) != comment {
		t.Fatalf("UpdateAddressBlock: comment = %q, want %q", strv(updated.Comment), comment)
	}

	if err := client.IPAM.DeleteAddressBlock(ctx, id); err != nil {
		t.Fatalf("DeleteAddressBlock: %v", err)
	}
}
