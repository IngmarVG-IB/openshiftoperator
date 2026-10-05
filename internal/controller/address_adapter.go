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

package controller

import (
	"context"
	"fmt"

	"github.com/infobloxopen/universal-ddi-go-client/ipam"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
	"github.com/IngmarVG-IB/openshiftoperator/internal/tags"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// Deps keys used by addressAdapter.
const (
	// addressDepSpace holds the resolved IP space id.
	addressDepSpace = "space"
	// addressDepParent holds the resolved id of the subnet, range or address block
	// nextAvailableFrom points at. It is only set when spec.address is empty.
	addressDepParent = "parent"
)

// addressDefaultNameType is the value Universal DDI names[].type defaults to. It is the
// only value confirmed writable (DESIGN.md section 4.4).
const addressDefaultNameType = "user"

// addressAdapter maps the Address CR onto ipam.Address. Allocation for a next-available
// address happens in Create, by setting Address to "<parent id>/nextavailableip"; the
// SDK client (internal/uddi/sdk.go) and the fake (internal/uddi/fake.go) both serialise
// that behind a per-parent lock.
type addressAdapter struct{}

var _ uddisync.Adapter[uddiv1alpha1.AddressSpec, ipam.Address] = addressAdapter{}

// Kind returns the CR kind name.
func (addressAdapter) Kind() string { return "Address" }

// Resolve turns spec.spaceRef into a Universal DDI ip space id and, when spec.address is
// empty, resolves spec.nextAvailableFrom's ref into the parent id used to build the
// "<parent id>/nextavailableip" create address. Exactly one of subnetRef, rangeRef or
// addressBlockRef is set; the CRD enforces that with CEL, but Resolve still guards it so
// a stale or hand-edited object cannot allocate against a garbage parent.
func (addressAdapter) Resolve(ctx context.Context, c client.Client, obj client.Object, spec *uddiv1alpha1.AddressSpec) (uddisync.Deps, error) {
	spaceID, err := uddisync.ResolveRef(ctx, c, obj.GetNamespace(), spec.SpaceRef, &uddiv1alpha1.IPSpace{}, "IPSpace")
	if err != nil {
		return nil, err
	}
	deps := uddisync.Deps{addressDepSpace: spaceID}

	if spec.Address != "" {
		return deps, nil
	}

	naf := spec.NextAvailableFrom
	if naf == nil {
		return nil, fmt.Errorf("%w: nextAvailableFrom is required when address is empty", uddisync.ErrDependencyNotReady)
	}

	var (
		parentID string
		perr     error
	)
	switch {
	case naf.SubnetRef != nil:
		parentID, perr = uddisync.ResolveRef(ctx, c, obj.GetNamespace(), *naf.SubnetRef, &uddiv1alpha1.Subnet{}, "Subnet")
	case naf.RangeRef != nil:
		parentID, perr = uddisync.ResolveRef(ctx, c, obj.GetNamespace(), *naf.RangeRef, &uddiv1alpha1.Range{}, "Range")
	case naf.AddressBlockRef != nil:
		parentID, perr = uddisync.ResolveRef(ctx, c, obj.GetNamespace(), *naf.AddressBlockRef, &uddiv1alpha1.AddressBlock{}, "AddressBlock")
	default:
		return nil, fmt.Errorf("%w: exactly one of subnetRef, rangeRef or addressBlockRef must be set", uddisync.ErrDependencyNotReady)
	}
	if perr != nil {
		return nil, perr
	}
	deps[addressDepParent] = parentID
	return deps, nil
}

// Find looks an address up by its natural key, address plus space. When spec.address is
// empty (next-available allocation) the natural key is not known in advance, so Find
// returns (nil, nil) and the generic loop relies on FindByUID to recover an address
// created by a previous pass whose status write was lost, per DESIGN.md section 4.4.
func (addressAdapter) Find(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.AddressSpec, deps uddisync.Deps) (*ipam.Address, error) {
	if spec.Address == "" {
		return nil, nil
	}
	res, err := uc.IPAM.ListAddresses(ctx, uddi.ListOptions{
		Filter: "address==" + quoteFilterValue(spec.Address) +
			" and space==" + quoteFilterValue(deps[addressDepSpace]),
		Limit: 1,
	})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// FindByUID looks an address up by the ownership uid tag.
func (addressAdapter) FindByUID(ctx context.Context, uc *uddi.Client, uid string) (*ipam.Address, error) {
	if uid == "" {
		return nil, nil
	}
	res, err := uc.IPAM.ListAddresses(ctx, uddi.ListOptions{
		TFilter: tags.UIDFilter(uid),
		Limit:   1,
	})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// Create reserves the address: a literal one, or the next available one inside
// deps[addressDepParent]. Names, comment, hwaddr, interface and the ownership tags land
// in the same request, which is why this is atomic instead of allocate-then-tag.
func (addressAdapter) Create(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.AddressSpec, deps uddisync.Deps, t map[string]string) (*ipam.Address, error) {
	address := spec.Address
	if address == "" {
		address = deps[addressDepParent] + uddi.NextAvailableIPSuffix
	}
	space := deps[addressDepSpace]
	in := ipam.Address{
		Address:   address,
		Space:     &space,
		Names:     toAddressNames(spec.Names),
		Hwaddr:    optStr(spec.HWAddr),
		Interface: optStr(spec.Interface),
		Comment:   optStr(spec.Comment),
		Tags:      uddi.ToAPITags(t),
	}
	return uc.IPAM.CreateAddress(ctx, in)
}

// Get reads an address by id.
func (addressAdapter) Get(ctx context.Context, uc *uddi.Client, id string) (*ipam.Address, error) {
	return uc.IPAM.GetAddress(ctx, id)
}

// Diff compares the managed fields and builds a partial patch. Address and space are
// immutable and ride along on every patch because ipam.Address models address as a
// required, non-pointer field; the value sent always equals what Universal DDI already
// has, so this is harmless, the same way view_adapter.go handles dnsconfig.View.Name.
func (addressAdapter) Diff(spec *uddiv1alpha1.AddressSpec, deps uddisync.Deps, t tags.Desired, current *ipam.Address) (*ipam.Address, bool) {
	patch := &ipam.Address{Address: current.Address}
	changed := false

	if !sameAddressNames(current.Names, spec.Names) {
		patch.Names = toAddressNames(spec.Names)
		changed = true
	}
	if strPtr(current.Hwaddr) != spec.HWAddr {
		patch.Hwaddr = optStr(spec.HWAddr)
		changed = true
	}
	if strPtr(current.Interface) != spec.Interface {
		patch.Interface = optStr(spec.Interface)
		changed = true
	}
	if strPtr(current.Comment) != spec.Comment {
		patch.Comment = optStr(spec.Comment)
		changed = true
	}
	if merged, tagsChanged := tags.ForPatchAPI(current.Tags, t); tagsChanged {
		patch.Tags = merged
		changed = true
	}
	return patch, changed
}

// Update applies a partial patch.
func (addressAdapter) Update(ctx context.Context, uc *uddi.Client, id string, patch *ipam.Address) (*ipam.Address, error) {
	return uc.IPAM.UpdateAddress(ctx, id, *patch)
}

// Delete releases the address.
func (addressAdapter) Delete(ctx context.Context, uc *uddi.Client, id string) error {
	return uc.IPAM.DeleteAddress(ctx, id)
}

// Observe copies observed fields into the CR status. status.address is the allocated
// address and the value consumers read (DESIGN.md section 4.4).
func (addressAdapter) Observe(obj client.Object, current *ipam.Address) {
	a, ok := obj.(*uddiv1alpha1.Address)
	if !ok || current == nil {
		return
	}
	a.Status.Address = current.Address
	a.Status.Space = strPtr(current.Space)
	a.Status.State = strPtr(current.State)
	a.Status.Parent = strPtr(current.Parent)
}

// IDOf returns the Universal DDI resource id.
func (addressAdapter) IDOf(current *ipam.Address) string { return strPtr(current.Id) }

// TagsOf returns the tags currently set on the address.
func (addressAdapter) TagsOf(current *ipam.Address) map[string]string {
	return uddi.FromAPITags(current.Tags)
}

// toAddressNames converts CR names into the SDK's ipam.Name list, defaulting an empty
// type to "user". A nil or empty input yields an empty (non-nil) slice so a patch can
// clear the list in Universal DDI.
func toAddressNames(in []uddiv1alpha1.AddressName) []ipam.Name {
	out := make([]ipam.Name, 0, len(in))
	for i := range in {
		n := in[i]
		typ := n.Type
		if typ == "" {
			typ = addressDefaultNameType
		}
		out = append(out, ipam.Name{Name: n.Name, Type: typ})
	}
	return out
}

// sameAddressNames reports whether the observed names match the desired ones. Order is
// significant, mirroring sameOptionItems in dhcpoptions.go.
func sameAddressNames(current []ipam.Name, desired []uddiv1alpha1.AddressName) bool {
	if len(current) != len(desired) {
		return false
	}
	for i := range desired {
		d := desired[i]
		typ := d.Type
		if typ == "" {
			typ = addressDefaultNameType
		}
		if current[i].Name != d.Name || current[i].Type != typ {
			return false
		}
	}
	return true
}
