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

// TestFixedAddress exercises the FixedAddress (DHCP reservation) lifecycle for a mac
// match type: create inside a Subnet of a fresh IPSpace, read back, the natural-key filter
// fixedaddressAdapter.Find builds (address and ip_space), the run's tfilter, a comment
// patch and delete.
func TestFixedAddress(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	space, err := client.IPAM.CreateIPSpace(ctx, ipam.IPSpace{Name: objName("fa-space"), Tags: testTags(nil)})
	if err != nil {
		t.Fatalf("CreateIPSpace (dependency): %v", err)
	}
	spaceID := strv(space.Id)
	registerDelete(t, "ip space", spaceID, client.IPAM.DeleteIPSpace)

	const address = "10.64.0.5"
	const matchType = "mac"
	const matchValue = "aa:bb:cc:00:00:01"
	mustParentSubnet(ctx, t, spaceID, "10.64.0.0")
	created, err := client.DHCP.CreateFixedAddress(ctx, ipam.FixedAddress{
		Address:    address,
		MatchType:  matchType,
		MatchValue: matchValue,
		IpSpace:    &spaceID,
		Tags:       testTags(nil),
	})
	if err != nil {
		t.Fatalf("CreateFixedAddress: %v", err)
	}
	id := strv(created.Id)
	registerDelete(t, "fixed address", id, client.DHCP.DeleteFixedAddress)

	got, err := client.DHCP.GetFixedAddress(ctx, id)
	if err != nil {
		t.Fatalf("GetFixedAddress: %v", err)
	}
	if got.Address != address || got.MatchValue != matchValue {
		t.Fatalf("GetFixedAddress: address/match_value = %q/%q, want %q/%q", got.Address, got.MatchValue, address, matchValue)
	}

	natFilter := `address=="` + address + `" and ip_space=="` + spaceID + `"`
	byNat, err := client.DHCP.ListFixedAddresses(ctx, uddi.ListOptions{Filter: natFilter, Limit: 1})
	if err != nil {
		t.Fatalf("ListFixedAddresses by natural key: %v", err)
	}
	if !containsID(byNat, id, func(a ipam.FixedAddress) string { return strv(a.Id) }) {
		t.Fatalf("ListFixedAddresses by natural key (%s): created reservation %s not among %d results", natFilter, id, len(byNat))
	}

	byTag, err := client.DHCP.ListFixedAddresses(ctx, uddi.ListOptions{TFilter: uddi.TagFilter(map[string]string{tagRunKey: runID}), Limit: 100})
	if err != nil {
		t.Fatalf("ListFixedAddresses by tfilter: %v", err)
	}
	if !containsID(byTag, id, func(a ipam.FixedAddress) string { return strv(a.Id) }) {
		t.Fatalf("ListFixedAddresses by tfilter: created reservation %s not among %d results", id, len(byTag))
	}

	// ipam.FixedAddress.Address, .MatchType and .MatchValue are required, non-pointer
	// fields with no omitempty guard in the generated MarshalJSON, so (like
	// ipam.Address.Address) they must ride along on every patch. fixedaddressAdapter.Diff
	// already does this; mirror it here.
	const comment = "patched by uddi-operator integration test"
	updated, err := client.DHCP.UpdateFixedAddress(ctx, id, ipam.FixedAddress{
		Address:    address,
		MatchType:  matchType,
		MatchValue: matchValue,
		Comment:    ptr(comment),
	})
	if err != nil {
		t.Fatalf("UpdateFixedAddress: %v", err)
	}
	if strv(updated.Comment) != comment {
		t.Fatalf("UpdateFixedAddress: comment = %q, want %q", strv(updated.Comment), comment)
	}

	if err := client.DHCP.DeleteFixedAddress(ctx, id); err != nil {
		t.Fatalf("DeleteFixedAddress: %v", err)
	}
}
