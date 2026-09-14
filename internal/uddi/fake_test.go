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

package uddi

import (
	"context"
	"sync"
	"testing"

	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"
	"github.com/infobloxopen/universal-ddi-go-client/ipam"
)

func ptr[T any](v T) *T { return &v }

// seedSubnet builds a space plus a subnet and returns their ids.
func seedSubnet(t *testing.T, f *Fake, address string, cidr int64) (spaceID, subnetID string) {
	t.Helper()
	ctx := context.Background()

	space, err := f.CreateIPSpace(ctx, ipam.IPSpace{Name: "corp"})
	if err != nil {
		t.Fatalf("CreateIPSpace: %v", err)
	}
	subnet, err := f.CreateSubnet(ctx, ipam.Subnet{
		Address: ptr(address),
		Cidr:    ptr(cidr),
		Space:   space.Id,
	})
	if err != nil {
		t.Fatalf("CreateSubnet: %v", err)
	}
	return *space.Id, *subnet.Id
}

func TestNextAvailableIPSkipsNetworkAndTakenAddresses(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	spaceID, subnetID := seedSubnet(t, f, "10.0.10.0", 24)

	// The first allocation skips the network address 10.0.10.0.
	first, err := f.CreateAddress(ctx, ipam.Address{
		Address: subnetID + NextAvailableIPSuffix,
		Space:   &spaceID,
	})
	if err != nil {
		t.Fatalf("CreateAddress: %v", err)
	}
	if first.Address != "10.0.10.1" {
		t.Fatalf("first allocation = %q, want 10.0.10.1", first.Address)
	}
	if first.Parent == nil || *first.Parent != subnetID {
		t.Fatalf("parent = %v, want %q", first.Parent, subnetID)
	}

	// A literal reservation in the middle is respected.
	if _, err := f.CreateAddress(ctx, ipam.Address{Address: "10.0.10.2", Space: &spaceID}); err != nil {
		t.Fatalf("literal CreateAddress: %v", err)
	}
	third, err := f.CreateAddress(ctx, ipam.Address{
		Address: subnetID + NextAvailableIPSuffix,
		Space:   &spaceID,
	})
	if err != nil {
		t.Fatalf("CreateAddress: %v", err)
	}
	if third.Address != "10.0.10.3" {
		t.Fatalf("third allocation = %q, want 10.0.10.3", third.Address)
	}

	// Releasing an address makes it the lowest free one again.
	if err := f.DeleteAddress(ctx, first.GetId()); err != nil {
		t.Fatalf("DeleteAddress: %v", err)
	}
	reused, err := f.CreateAddress(ctx, ipam.Address{
		Address: subnetID + NextAvailableIPSuffix,
		Space:   &spaceID,
	})
	if err != nil {
		t.Fatalf("CreateAddress: %v", err)
	}
	if reused.Address != "10.0.10.1" {
		t.Fatalf("reused allocation = %q, want 10.0.10.1", reused.Address)
	}
}

func TestNextAvailableIPCarriesTagsInTheSameCall(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	spaceID, subnetID := seedSubnet(t, f, "10.0.10.0", 24)

	got, err := f.CreateAddress(ctx, ipam.Address{
		Address: subnetID + NextAvailableIPSuffix,
		Space:   &spaceID,
		Names:   []ipam.Name{{Name: "svc-web", Type: "user"}},
		Tags:    ToAPITags(map[string]string{"uddi_operator_uid": "uid-1"}),
	})
	if err != nil {
		t.Fatalf("CreateAddress: %v", err)
	}
	if FromAPITags(got.Tags)["uddi_operator_uid"] != "uid-1" {
		t.Fatalf("tags = %v, want the uid tag to survive the allocation", got.Tags)
	}
	if len(got.Names) != 1 || got.Names[0].Name != "svc-web" {
		t.Fatalf("names = %v, want svc-web", got.Names)
	}
}

func TestNextAvailableIPFromARange(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	spaceID, subnetID := seedSubnet(t, f, "10.0.10.0", 24)

	rng, err := f.CreateRange(ctx, ipam.Range{
		Start: "10.0.10.100",
		End:   "10.0.10.102",
		Space: &spaceID,
	})
	if err != nil {
		t.Fatalf("CreateRange: %v", err)
	}
	if rng.Parent == nil || *rng.Parent != subnetID {
		t.Fatalf("range parent = %v, want %q", rng.Parent, subnetID)
	}

	for _, want := range []string{"10.0.10.100", "10.0.10.101", "10.0.10.102"} {
		got, err := f.CreateAddress(ctx, ipam.Address{
			Address: rng.GetId() + NextAvailableIPSuffix,
			Space:   &spaceID,
		})
		if err != nil {
			t.Fatalf("CreateAddress: %v", err)
		}
		if got.Address != want {
			t.Fatalf("allocation = %q, want %q", got.Address, want)
		}
	}

	// The range is now full.
	if _, err := f.CreateAddress(ctx, ipam.Address{
		Address: rng.GetId() + NextAvailableIPSuffix,
		Space:   &spaceID,
	}); err == nil {
		t.Fatal("CreateAddress on an exhausted range succeeded, want an error")
	}
}

func TestNextAvailableIPOnASlashThirtyOne(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	spaceID, subnetID := seedSubnet(t, f, "10.0.20.0", 31)

	// On a /31 both addresses are usable.
	for _, want := range []string{"10.0.20.0", "10.0.20.1"} {
		got, err := f.CreateAddress(ctx, ipam.Address{
			Address: subnetID + NextAvailableIPSuffix,
			Space:   &spaceID,
		})
		if err != nil {
			t.Fatalf("CreateAddress: %v", err)
		}
		if got.Address != want {
			t.Fatalf("allocation = %q, want %q", got.Address, want)
		}
	}
}

func TestNextAvailableSubnetAndAddressBlock(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	space, err := f.CreateIPSpace(ctx, ipam.IPSpace{Name: "corp"})
	if err != nil {
		t.Fatalf("CreateIPSpace: %v", err)
	}
	parent, err := f.CreateAddressBlock(ctx, ipam.AddressBlock{
		Address: ptr("10.0.0.0"), Cidr: ptr(int64(16)), Space: space.Id,
	})
	if err != nil {
		t.Fatalf("CreateAddressBlock: %v", err)
	}

	// A child block takes the bottom of the parent.
	child, err := f.CreateAddressBlock(ctx, ipam.AddressBlock{
		Address: ptr(parent.GetId() + NextAvailableBlockSuffix),
		Cidr:    ptr(int64(20)),
		Space:   space.Id,
	})
	if err != nil {
		t.Fatalf("next-available AddressBlock: %v", err)
	}
	if child.GetAddress() != "10.0.0.0" {
		t.Fatalf("child block = %q, want 10.0.0.0", child.GetAddress())
	}

	// A next-available subnet must skip the child block.
	sub, err := f.CreateSubnet(ctx, ipam.Subnet{
		Address: ptr(parent.GetId() + NextAvailableSubnetSuffix),
		Cidr:    ptr(int64(24)),
		Space:   space.Id,
	})
	if err != nil {
		t.Fatalf("next-available Subnet: %v", err)
	}
	if sub.GetAddress() != "10.0.16.0" {
		t.Fatalf("subnet = %q, want 10.0.16.0", sub.GetAddress())
	}

	sub2, err := f.CreateSubnet(ctx, ipam.Subnet{
		Address: ptr(parent.GetId() + NextAvailableSubnetSuffix),
		Cidr:    ptr(int64(24)),
		Space:   space.Id,
	})
	if err != nil {
		t.Fatalf("next-available Subnet: %v", err)
	}
	if sub2.GetAddress() != "10.0.17.0" {
		t.Fatalf("second subnet = %q, want 10.0.17.0", sub2.GetAddress())
	}
}

func TestNaturalKeyUniqueness(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	if _, err := f.CreateView(ctx, dnsconfig.View{Name: "default"}); err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	_, err := f.CreateView(ctx, dnsconfig.View{Name: "default"})
	if !IsConflict(err) {
		t.Fatalf("duplicate CreateView error = %v, want a conflict", err)
	}

	space, _ := f.CreateIPSpace(ctx, ipam.IPSpace{Name: "corp"})
	if _, err := f.CreateAddress(ctx, ipam.Address{Address: "10.0.0.5", Space: space.Id}); err != nil {
		t.Fatalf("CreateAddress: %v", err)
	}
	_, err = f.CreateAddress(ctx, ipam.Address{Address: "10.0.0.5", Space: space.Id})
	if !IsConflict(err) {
		t.Fatalf("duplicate CreateAddress error = %v, want a conflict", err)
	}

	// The same address in a different space is fine.
	other, _ := f.CreateIPSpace(ctx, ipam.IPSpace{Name: "lab"})
	if _, err := f.CreateAddress(ctx, ipam.Address{Address: "10.0.0.5", Space: other.Id}); err != nil {
		t.Fatalf("CreateAddress in another space: %v", err)
	}
}

func TestTagFilterLookup(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	wanted, err := f.CreateView(ctx, dnsconfig.View{
		Name: "tagged",
		Tags: ToAPITags(map[string]string{"uddi_operator_uid": "uid-1", "uddi_operator_managed": "true"}),
	})
	if err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	if _, err := f.CreateView(ctx, dnsconfig.View{
		Name: "other",
		Tags: ToAPITags(map[string]string{"uddi_operator_uid": "uid-2"}),
	}); err != nil {
		t.Fatalf("CreateView: %v", err)
	}

	got, err := f.ListViews(ctx, ListOptions{
		TFilter: TagFilter(map[string]string{"uddi_operator_uid": "uid-1", "uddi_operator_managed": "true"}),
	})
	if err != nil {
		t.Fatalf("ListViews: %v", err)
	}
	if len(got) != 1 || got[0].GetId() != wanted.GetId() {
		t.Fatalf("tag lookup returned %d results, want exactly the tagged view", len(got))
	}

	none, err := f.ListViews(ctx, ListOptions{TFilter: TagFilter(map[string]string{"uddi_operator_uid": "uid-3"})})
	if err != nil {
		t.Fatalf("ListViews: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("tag lookup for an unknown uid returned %d results, want 0", len(none))
	}
}

func TestFieldFilterLookup(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	space, _ := f.CreateIPSpace(ctx, ipam.IPSpace{Name: "corp"})
	if _, err := f.CreateSubnet(ctx, ipam.Subnet{
		Address: ptr("10.0.10.0"), Cidr: ptr(int64(24)), Space: space.Id,
	}); err != nil {
		t.Fatalf("CreateSubnet: %v", err)
	}
	if _, err := f.CreateSubnet(ctx, ipam.Subnet{
		Address: ptr("10.0.11.0"), Cidr: ptr(int64(24)), Space: space.Id,
	}); err != nil {
		t.Fatalf("CreateSubnet: %v", err)
	}

	got, err := f.ListSubnets(ctx, ListOptions{
		Filter: `address=="10.0.11.0" and cidr==24`,
	})
	if err != nil {
		t.Fatalf("ListSubnets: %v", err)
	}
	if len(got) != 1 || got[0].GetAddress() != "10.0.11.0" {
		t.Fatalf("filter returned %d results, want exactly 10.0.11.0", len(got))
	}

	if _, err := f.ListSubnets(ctx, ListOptions{Filter: `address==`}); err == nil {
		t.Fatal("a malformed filter was accepted, want a 400")
	}
}

func TestErrorInjectionAndCallCounts(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	f.PushStatus("ListViews", 401, `{"message":"invalid api key"}`)
	if _, err := f.ListViews(ctx, ListOptions{Limit: 1}); !IsUnauthorized(err) {
		t.Fatalf("ListViews error = %v, want 401", err)
	}
	// The queued error is consumed, so the next call succeeds.
	if _, err := f.ListViews(ctx, ListOptions{Limit: 1}); err != nil {
		t.Fatalf("ListViews: %v", err)
	}
	if got := f.CallCount("ListViews"); got != 2 {
		t.Fatalf("CallCount = %d, want 2", got)
	}
}

func TestNotFoundOnMissingObjects(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	if _, err := f.GetView(ctx, PrefixView+"/nope"); !IsNotFound(err) {
		t.Fatalf("GetView error = %v, want 404", err)
	}
	if err := f.DeleteView(ctx, PrefixView+"/nope"); !IsNotFound(err) {
		t.Fatalf("DeleteView error = %v, want 404", err)
	}
}

func TestConcurrentAllocationHandsOutDistinctAddresses(t *testing.T) {
	ctx := context.Background()
	f := NewFake()
	spaceID, subnetID := seedSubnet(t, f, "10.0.30.0", 24)

	const workers = 25
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[string]bool{}

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			got, err := f.CreateAddress(ctx, ipam.Address{
				Address: subnetID + NextAvailableIPSuffix,
				Space:   &spaceID,
			})
			if err != nil {
				t.Errorf("CreateAddress: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[got.Address] {
				t.Errorf("address %s handed out twice", got.Address)
			}
			seen[got.Address] = true
		}()
	}
	wg.Wait()

	if len(seen) != workers {
		t.Fatalf("got %d distinct addresses, want %d", len(seen), workers)
	}
}

func TestKeyedMutexSerialisesPerKey(t *testing.T) {
	var km KeyedMutex
	var wg sync.WaitGroup
	counters := map[string]int{"a": 0, "b": 0}
	var mu sync.Mutex

	for _, key := range []string{"a", "b"} {
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(k string) {
				defer wg.Done()
				km.Lock(k)
				defer km.Unlock(k)
				mu.Lock()
				counters[k]++
				mu.Unlock()
			}(key)
		}
	}
	wg.Wait()

	if counters["a"] != 50 || counters["b"] != 50 {
		t.Fatalf("counters = %v, want 50 each", counters)
	}
}

func TestBareAndQualifyID(t *testing.T) {
	if got := BareID("dns/view/abc-123"); got != "abc-123" {
		t.Fatalf("BareID = %q, want abc-123", got)
	}
	if got := BareID("abc-123"); got != "abc-123" {
		t.Fatalf("BareID = %q, want abc-123", got)
	}
	if got := QualifyID(PrefixView, "abc-123"); got != "dns/view/abc-123" {
		t.Fatalf("QualifyID = %q, want dns/view/abc-123", got)
	}
	if got := QualifyID(PrefixView, "dns/view/abc-123"); got != "dns/view/abc-123" {
		t.Fatalf("QualifyID = %q, want the input unchanged", got)
	}
}

func TestParentOfNextAvailable(t *testing.T) {
	cases := []struct {
		in     string
		parent string
		suffix string
		ok     bool
	}{
		{"ipam/subnet/x" + NextAvailableIPSuffix, "ipam/subnet/x", NextAvailableIPSuffix, true},
		{"ipam/address_block/x" + NextAvailableSubnetSuffix, "ipam/address_block/x", NextAvailableSubnetSuffix, true},
		{"ipam/address_block/x" + NextAvailableBlockSuffix, "ipam/address_block/x", NextAvailableBlockSuffix, true},
		{"10.0.0.1", "", "", false},
	}
	for _, tc := range cases {
		parent, suffix, ok := ParentOfNextAvailable(tc.in)
		if parent != tc.parent || suffix != tc.suffix || ok != tc.ok {
			t.Fatalf("ParentOfNextAvailable(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.in, parent, suffix, ok, tc.parent, tc.suffix, tc.ok)
		}
	}
}
