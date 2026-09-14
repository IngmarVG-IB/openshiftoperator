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

// TestNextAvailableAddress answers docs/uddi-api-notes.md's next-available question for
// mechanism 2 (AddressAPI.Create with address = "<parent id>/nextavailableip"), the
// mechanism the operator actually uses because it is atomic with tags: two sequential
// allocations from the same subnet succeed, return different addresses, and carry the
// tags sent in the same request body.
func TestNextAvailableAddress(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	space, err := client.IPAM.CreateIPSpace(ctx, ipam.IPSpace{Name: objName("nextip-space"), Tags: testTags(nil)})
	if err != nil {
		t.Fatalf("CreateIPSpace (dependency): %v", err)
	}
	spaceID := strv(space.Id)
	registerDelete(t, "ip space", spaceID, client.IPAM.DeleteIPSpace)

	subnet, err := client.IPAM.CreateSubnet(ctx, ipam.Subnet{
		Address: ptr("10.70.0.0"),
		Cidr:    ptr(int64(24)),
		Space:   &spaceID,
		Tags:    testTags(nil),
	})
	if err != nil {
		t.Fatalf("CreateSubnet (dependency): %v", err)
	}
	subnetID := strv(subnet.Id)
	registerDelete(t, "subnet", subnetID, client.IPAM.DeleteSubnet)

	first, err := client.IPAM.CreateAddress(ctx, ipam.Address{
		Address: subnetID + uddi.NextAvailableIPSuffix,
		Space:   &spaceID,
		Names:   []ipam.Name{{Name: "svc-first", Type: "user"}},
		Tags:    testTags(map[string]string{"uddi_operator_it_alloc": "first"}),
	})
	if err != nil {
		t.Fatalf("VERDICT: next-available Address create failed: %v", err)
	}
	registerDelete(t, "address", strv(first.Id), client.IPAM.DeleteAddress)
	if first.Address == "" {
		t.Fatal("VERDICT: next-available Address create returned an empty address")
	}
	if !hasTag(first.Tags, "uddi_operator_it_alloc") {
		t.Fatal("VERDICT: next-available Address create response did not carry the tags sent in the same body")
	}

	second, err := client.IPAM.CreateAddress(ctx, ipam.Address{
		Address: subnetID + uddi.NextAvailableIPSuffix,
		Space:   &spaceID,
		Names:   []ipam.Name{{Name: "svc-second", Type: "user"}},
		Tags:    testTags(map[string]string{"uddi_operator_it_alloc": "second"}),
	})
	if err != nil {
		t.Fatalf("VERDICT: second sequential next-available Address create failed: %v", err)
	}
	registerDelete(t, "address", strv(second.Id), client.IPAM.DeleteAddress)

	if first.Address == second.Address {
		t.Fatalf("VERDICT: two sequential next-available allocations returned the same address %q", first.Address)
	}
	t.Logf("VERDICT: sequential next-available Address creates (mechanism 2, AddressAPI.Create "+
		"with address=\"<subnet id>/nextavailableip\") succeed, return distinct addresses (%s, then %s), "+
		"and the response carries the tags sent in the same body", first.Address, second.Address)
}

// TestNextAvailableSubnetFromBlock answers the same next-available question for a
// Subnet allocated from an AddressBlock via mechanism 2 (SubnetAPI.Create with
// address = "<address block id>/nextavailablesubnet" plus cidr): two sequential
// allocations succeed and return distinct subnets.
func TestNextAvailableSubnetFromBlock(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	space, err := client.IPAM.CreateIPSpace(ctx, ipam.IPSpace{Name: objName("nextsubnet-space"), Tags: testTags(nil)})
	if err != nil {
		t.Fatalf("CreateIPSpace (dependency): %v", err)
	}
	spaceID := strv(space.Id)
	registerDelete(t, "ip space", spaceID, client.IPAM.DeleteIPSpace)

	block, err := client.IPAM.CreateAddressBlock(ctx, ipam.AddressBlock{
		Address: ptr("10.71.0.0"),
		Cidr:    ptr(int64(16)),
		Space:   &spaceID,
		Tags:    testTags(nil),
	})
	if err != nil {
		t.Fatalf("CreateAddressBlock (dependency): %v", err)
	}
	blockID := strv(block.Id)
	registerDelete(t, "address block", blockID, client.IPAM.DeleteAddressBlock)

	first, err := client.IPAM.CreateSubnet(ctx, ipam.Subnet{
		Address: ptr(blockID + uddi.NextAvailableSubnetSuffix),
		Cidr:    ptr(int64(24)),
		Space:   &spaceID,
		Tags:    testTags(nil),
	})
	if err != nil {
		t.Fatalf("VERDICT: next-available Subnet create failed: %v", err)
	}
	registerDelete(t, "subnet", strv(first.Id), client.IPAM.DeleteSubnet)
	if strv(first.Address) == "" {
		t.Fatal("VERDICT: next-available Subnet create returned an empty address")
	}

	second, err := client.IPAM.CreateSubnet(ctx, ipam.Subnet{
		Address: ptr(blockID + uddi.NextAvailableSubnetSuffix),
		Cidr:    ptr(int64(24)),
		Space:   &spaceID,
		Tags:    testTags(nil),
	})
	if err != nil {
		t.Fatalf("VERDICT: second sequential next-available Subnet create failed: %v", err)
	}
	registerDelete(t, "subnet", strv(second.Id), client.IPAM.DeleteSubnet)

	if strv(first.Address) == strv(second.Address) {
		t.Fatalf("VERDICT: two sequential next-available subnet allocations returned the same address %q", strv(first.Address))
	}
	t.Logf("VERDICT: sequential next-available Subnet creates from an AddressBlock (mechanism 2, "+
		"SubnetAPI.Create with address=\"<address block id>/nextavailablesubnet\" plus cidr) succeed "+
		"and return distinct subnets (%s, then %s)", strv(first.Address), strv(second.Address))
}
