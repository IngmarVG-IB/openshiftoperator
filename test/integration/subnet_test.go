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

// TestSubnet exercises the Subnet lifecycle: create directly under a fresh IPSpace (no
// parent AddressBlock; next-available creation from a block is covered separately in
// nextavailable_test.go), read back, the natural-key filter subnetAdapter.Find builds
// (address, cidr and space), the run's tfilter, a comment patch and delete.
func TestSubnet(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	space, err := client.IPAM.CreateIPSpace(ctx, ipam.IPSpace{Name: objName("subnet-space"), Tags: testTags(nil)})
	if err != nil {
		t.Fatalf("CreateIPSpace (dependency): %v", err)
	}
	spaceID := strv(space.Id)
	registerDelete(t, "ip space", spaceID, client.IPAM.DeleteIPSpace)

	const address = "10.61.0.0"
	const cidr = int64(24)
	created, err := client.IPAM.CreateSubnet(ctx, ipam.Subnet{
		Address: ptr(address),
		Cidr:    ptr(cidr),
		Space:   &spaceID,
		Tags:    testTags(nil),
	})
	if err != nil {
		t.Fatalf("CreateSubnet: %v", err)
	}
	id := strv(created.Id)
	registerDelete(t, "subnet", id, client.IPAM.DeleteSubnet)

	got, err := client.IPAM.GetSubnet(ctx, id)
	if err != nil {
		t.Fatalf("GetSubnet: %v", err)
	}
	if strv(got.Address) != address {
		t.Fatalf("GetSubnet: address = %q, want %q", strv(got.Address), address)
	}

	natFilter := fmt.Sprintf(`address=="%s" and cidr==%d and space=="%s"`, address, cidr, spaceID)
	byNat, err := client.IPAM.ListSubnets(ctx, uddi.ListOptions{Filter: natFilter, Limit: 1})
	if err != nil {
		t.Fatalf("ListSubnets by natural key: %v", err)
	}
	if !containsID(byNat, id, func(s ipam.Subnet) string { return strv(s.Id) }) {
		t.Fatalf("ListSubnets by natural key (%s): created subnet %s not among %d results", natFilter, id, len(byNat))
	}

	byTag, err := client.IPAM.ListSubnets(ctx, uddi.ListOptions{TFilter: uddi.TagFilter(map[string]string{tagRunKey: runID}), Limit: 100})
	if err != nil {
		t.Fatalf("ListSubnets by tfilter: %v", err)
	}
	if !containsID(byTag, id, func(s ipam.Subnet) string { return strv(s.Id) }) {
		t.Fatalf("ListSubnets by tfilter: created subnet %s not among %d results", id, len(byTag))
	}

	const comment = "patched by uddi-operator integration test"
	updated, err := client.IPAM.UpdateSubnet(ctx, id, ipam.Subnet{Comment: ptr(comment)})
	if err != nil {
		t.Fatalf("UpdateSubnet: %v", err)
	}
	if strv(updated.Comment) != comment {
		t.Fatalf("UpdateSubnet: comment = %q, want %q", strv(updated.Comment), comment)
	}

	if err := client.IPAM.DeleteSubnet(ctx, id); err != nil {
		t.Fatalf("DeleteSubnet: %v", err)
	}
}
