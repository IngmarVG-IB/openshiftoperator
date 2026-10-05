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

// Deps keys used by addressblockAdapter and, for the parent/space shape, subnetAdapter.
const (
	// depSpace holds the resolved Universal DDI ip space id.
	depSpace = "space"
	// depParent holds the resolved Universal DDI parent AddressBlock id. Present only
	// when spec.address is empty (next-available allocation).
	depParent = "parent"
)

// addressblockAdapter maps the AddressBlock CR onto ipam.AddressBlock. It follows the
// shape of viewAdapter, the reference implementation in view_adapter.go.
type addressblockAdapter struct{}

var _ uddisync.Adapter[uddiv1alpha1.AddressBlockSpec, ipam.AddressBlock] = addressblockAdapter{}

// Kind returns the CR kind name.
func (addressblockAdapter) Kind() string { return "AddressBlock" }

// Resolve turns spaceRef into a Universal DDI ip space id, and parentRef into a
// Universal DDI address block id when address is omitted (next-available allocation).
// parentRef is otherwise not needed and is left unresolved, matching DESIGN.md 4.4: it
// is required only when address is empty (CEL already enforces that combination).
func (addressblockAdapter) Resolve(ctx context.Context, c client.Client, obj client.Object, spec *uddiv1alpha1.AddressBlockSpec) (uddisync.Deps, error) {
	deps := uddisync.Deps{}

	spaceID, err := uddisync.ResolveRef(ctx, c, obj.GetNamespace(), spec.SpaceRef, &uddiv1alpha1.IPSpace{}, "IPSpace")
	if err != nil {
		return nil, err
	}
	deps[depSpace] = spaceID

	if spec.Address == "" {
		if spec.ParentRef == nil {
			return nil, fmt.Errorf("%w: parentRef is required when address is omitted", uddisync.ErrDependencyNotReady)
		}
		parentID, err := uddisync.ResolveRef(ctx, c, obj.GetNamespace(), *spec.ParentRef, &uddiv1alpha1.AddressBlock{}, "AddressBlock")
		if err != nil {
			return nil, err
		}
		deps[depParent] = parentID
	}
	return deps, nil
}

// Find looks an address block up by its natural key: address, cidr and space. A
// next-available block (empty spec.address) has no natural key yet, so the loop relies
// on FindByUID for idempotent creates and lost-status recovery.
func (addressblockAdapter) Find(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.AddressBlockSpec, deps uddisync.Deps) (*ipam.AddressBlock, error) {
	if spec.Address == "" {
		return nil, nil
	}
	filter := fmt.Sprintf("address==%s and cidr==%d and space==%s",
		quoteFilterValue(spec.Address), spec.CIDR, quoteFilterValue(deps[depSpace]))
	res, err := uc.IPAM.ListAddressBlocks(ctx, uddi.ListOptions{Filter: filter, Limit: 1})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// FindByUID looks an address block up by the ownership uid tag.
func (addressblockAdapter) FindByUID(ctx context.Context, uc *uddi.Client, uid string) (*ipam.AddressBlock, error) {
	if uid == "" {
		return nil, nil
	}
	res, err := uc.IPAM.ListAddressBlocks(ctx, uddi.ListOptions{TFilter: tags.UIDFilter(uid), Limit: 1})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// Create makes the Universal DDI address block. When spec.address is empty, address is
// set to "<parent id>/nextavailableaddressblock" so Universal DDI allocates the block
// atomically with the tags (DESIGN.md 4.4).
func (addressblockAdapter) Create(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.AddressBlockSpec, deps uddisync.Deps, t map[string]string) (*ipam.AddressBlock, error) {
	address := spec.Address
	if address == "" {
		address = deps[depParent] + uddi.NextAvailableBlockSuffix
	}
	cidr := int64(spec.CIDR)
	in := ipam.AddressBlock{
		Address: strp(address),
		Cidr:    &cidr,
		Space:   strp(deps[depSpace]),
	}
	if spec.Name != "" {
		in.Name = &spec.Name
	}
	if spec.Comment != "" {
		in.Comment = &spec.Comment
	}
	in.DhcpOptions = toOptionItems(spec.DHCPOptions)
	in.Tags = uddi.ToAPITags(t)
	return uc.IPAM.CreateAddressBlock(ctx, in)
}

// Get reads an address block by id.
func (addressblockAdapter) Get(ctx context.Context, uc *uddi.Client, id string) (*ipam.AddressBlock, error) {
	return uc.IPAM.GetAddressBlock(ctx, id)
}

// Diff compares the managed fields and builds a partial patch. Address, cidr and space
// are immutable (CEL on address and parentRef, always on cidr) and are never patched.
func (addressblockAdapter) Diff(spec *uddiv1alpha1.AddressBlockSpec, _ uddisync.Deps, t tags.Desired, current *ipam.AddressBlock) (*ipam.AddressBlock, bool) {
	patch := &ipam.AddressBlock{}
	changed := false

	if strPtr(current.Name) != spec.Name {
		name := spec.Name
		patch.Name = &name
		changed = true
	}
	if strPtr(current.Comment) != spec.Comment {
		comment := spec.Comment
		patch.Comment = &comment
		changed = true
	}
	if !sameOptionItems(current.DhcpOptions, spec.DHCPOptions) {
		patch.DhcpOptions = toOptionItems(spec.DHCPOptions)
		changed = true
	}
	if merged, tagsChanged := tags.ForPatchAPI(current.Tags, t); tagsChanged {
		patch.Tags = merged
		changed = true
	}
	return patch, changed
}

// Update applies a partial patch.
func (addressblockAdapter) Update(ctx context.Context, uc *uddi.Client, id string, patch *ipam.AddressBlock) (*ipam.AddressBlock, error) {
	return uc.IPAM.UpdateAddressBlock(ctx, id, *patch)
}

// Delete removes the address block.
func (addressblockAdapter) Delete(ctx context.Context, uc *uddi.Client, id string) error {
	return uc.IPAM.DeleteAddressBlock(ctx, id)
}

// Observe copies observed fields into the CR status. Address and cidr are how a
// next-available allocation becomes visible to the user.
func (addressblockAdapter) Observe(obj client.Object, current *ipam.AddressBlock) {
	b, ok := obj.(*uddiv1alpha1.AddressBlock)
	if !ok || current == nil {
		return
	}
	b.Status.Address = strPtr(current.Address)
	b.Status.CIDR = int32(i64Ptr(current.Cidr))
	b.Status.Space = strPtr(current.Space)
	b.Status.Utilization = utilizationFrom(current.Utilization)
}

// IDOf returns the Universal DDI resource id.
func (addressblockAdapter) IDOf(current *ipam.AddressBlock) string { return strPtr(current.Id) }

// TagsOf returns the tags currently set on the address block.
func (addressblockAdapter) TagsOf(current *ipam.AddressBlock) map[string]string {
	return uddi.FromAPITags(current.Tags)
}

// strp returns a pointer to a copy of s. Shared by the IPAM adapters (addressblock,
// subnet) whose SDK fields are string pointers.
func strp(s string) *string { return &s }
