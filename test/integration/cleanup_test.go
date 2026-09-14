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
	"context"
	"errors"
	"fmt"

	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// cleanupMatching deletes every object matching tfilter, in reverse dependency order:
// Records depend on AuthZones which depend on Views; FixedAddresses, Addresses and
// Ranges depend on the Subnets and AddressBlocks that may contain them, which in turn
// depend on IPSpaces. Deleting in this order means a parent is never deleted while the
// real API still considers it to have children (see fake.go's DeleteAuthZone and
// DeleteSubnet, which reject exactly that).
//
// requireRunTag additionally requires the object to carry tagRunKey (with a non-empty
// value) client-side before it is deleted:
//
//   - TestMain's leftover-from-earlier-runs pass matches on tagManagedKey alone, because
//     an earlier run's id is not known in advance, so requireRunTag=true is what stops
//     it from ever touching a hand-created object that merely happens to also carry
//     uddi_operator_managed=true.
//   - TestMain's end-of-run pass matches tagRunKey to this run's exact id, so every
//     result already carries the tag by construction and requireRunTag=false is just
//     avoiding a redundant check.
//
// Either way, nothing lacking the run tag is ever deleted.
func cleanupMatching(ctx context.Context, c *uddi.Client, tfilter string, requireRunTag bool) error {
	var errs []error
	del := func(kind, id string, err error) {
		if err != nil && !uddi.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("delete %s %s: %w", kind, id, err))
		}
	}

	if recs, err := c.DNSData.ListRecords(ctx, uddi.ListOptions{TFilter: tfilter, Limit: 1000}); err != nil {
		errs = append(errs, fmt.Errorf("list records: %w", err))
	} else {
		for _, r := range recs {
			if requireRunTag && !hasTag(r.Tags, tagRunKey) {
				continue
			}
			del("record", strv(r.Id), c.DNSData.DeleteRecord(ctx, strv(r.Id)))
		}
	}

	if zones, err := c.DNSConfig.ListAuthZones(ctx, uddi.ListOptions{TFilter: tfilter, Limit: 1000}); err != nil {
		errs = append(errs, fmt.Errorf("list auth zones: %w", err))
	} else {
		for _, z := range zones {
			if requireRunTag && !hasTag(z.Tags, tagRunKey) {
				continue
			}
			del("auth zone", strv(z.Id), c.DNSConfig.DeleteAuthZone(ctx, strv(z.Id)))
		}
	}

	if fixed, err := c.DHCP.ListFixedAddresses(ctx, uddi.ListOptions{TFilter: tfilter, Limit: 1000}); err != nil {
		errs = append(errs, fmt.Errorf("list fixed addresses: %w", err))
	} else {
		for _, a := range fixed {
			if requireRunTag && !hasTag(a.Tags, tagRunKey) {
				continue
			}
			del("fixed address", strv(a.Id), c.DHCP.DeleteFixedAddress(ctx, strv(a.Id)))
		}
	}

	if addrs, err := c.IPAM.ListAddresses(ctx, uddi.ListOptions{TFilter: tfilter, Limit: 1000}); err != nil {
		errs = append(errs, fmt.Errorf("list addresses: %w", err))
	} else {
		for _, a := range addrs {
			if requireRunTag && !hasTag(a.Tags, tagRunKey) {
				continue
			}
			del("address", strv(a.Id), c.IPAM.DeleteAddress(ctx, strv(a.Id)))
		}
	}

	if ranges, err := c.IPAM.ListRanges(ctx, uddi.ListOptions{TFilter: tfilter, Limit: 1000}); err != nil {
		errs = append(errs, fmt.Errorf("list ranges: %w", err))
	} else {
		for _, r := range ranges {
			if requireRunTag && !hasTag(r.Tags, tagRunKey) {
				continue
			}
			del("range", strv(r.Id), c.IPAM.DeleteRange(ctx, strv(r.Id)))
		}
	}

	if subnets, err := c.IPAM.ListSubnets(ctx, uddi.ListOptions{TFilter: tfilter, Limit: 1000}); err != nil {
		errs = append(errs, fmt.Errorf("list subnets: %w", err))
	} else {
		for _, s := range subnets {
			if requireRunTag && !hasTag(s.Tags, tagRunKey) {
				continue
			}
			del("subnet", strv(s.Id), c.IPAM.DeleteSubnet(ctx, strv(s.Id)))
		}
	}

	if blocks, err := c.IPAM.ListAddressBlocks(ctx, uddi.ListOptions{TFilter: tfilter, Limit: 1000}); err != nil {
		errs = append(errs, fmt.Errorf("list address blocks: %w", err))
	} else {
		for _, b := range blocks {
			if requireRunTag && !hasTag(b.Tags, tagRunKey) {
				continue
			}
			del("address block", strv(b.Id), c.IPAM.DeleteAddressBlock(ctx, strv(b.Id)))
		}
	}

	if spaces, err := c.IPAM.ListIPSpaces(ctx, uddi.ListOptions{TFilter: tfilter, Limit: 1000}); err != nil {
		errs = append(errs, fmt.Errorf("list ip spaces: %w", err))
	} else {
		for _, s := range spaces {
			if requireRunTag && !hasTag(s.Tags, tagRunKey) {
				continue
			}
			del("ip space", strv(s.Id), c.IPAM.DeleteIPSpace(ctx, strv(s.Id)))
		}
	}

	if views, err := c.DNSConfig.ListViews(ctx, uddi.ListOptions{TFilter: tfilter, Limit: 1000}); err != nil {
		errs = append(errs, fmt.Errorf("list views: %w", err))
	} else {
		for _, v := range views {
			if requireRunTag && !hasTag(v.Tags, tagRunKey) {
				continue
			}
			del("view", strv(v.Id), c.DNSConfig.DeleteView(ctx, strv(v.Id)))
		}
	}

	return errors.Join(errs...)
}
