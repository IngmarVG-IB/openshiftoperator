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

// subnetAdapter maps the Subnet CR onto ipam.Subnet. It is the same shape as
// addressblockAdapter, plus dhcpHost and DHCP utilization observation (DESIGN.md 4.4).
type subnetAdapter struct{}

var _ uddisync.Adapter[uddiv1alpha1.SubnetSpec, ipam.Subnet] = subnetAdapter{}

// Kind returns the CR kind name.
func (subnetAdapter) Kind() string { return "Subnet" }

// Resolve turns spaceRef into a Universal DDI ip space id, and parentRef into a
// Universal DDI address block id when address is omitted (next-available allocation).
// The parent for a next-available subnet is always an AddressBlock.
func (subnetAdapter) Resolve(ctx context.Context, c client.Client, obj client.Object, spec *uddiv1alpha1.SubnetSpec) (uddisync.Deps, error) {
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

// Find looks a subnet up by its natural key: address, cidr and space. A next-available
// subnet (empty spec.address) has no natural key yet, so the loop relies on FindByUID
// for idempotent creates and lost-status recovery.
func (subnetAdapter) Find(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.SubnetSpec, deps uddisync.Deps) (*ipam.Subnet, error) {
	if spec.Address == "" {
		return nil, nil
	}
	filter := fmt.Sprintf("address==%s and cidr==%d and space==%s",
		quoteFilterValue(spec.Address), spec.CIDR, quoteFilterValue(deps[depSpace]))
	res, err := uc.IPAM.ListSubnets(ctx, uddi.ListOptions{Filter: filter, Limit: 1})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// FindByUID looks a subnet up by the ownership uid tag.
func (subnetAdapter) FindByUID(ctx context.Context, uc *uddi.Client, uid string) (*ipam.Subnet, error) {
	if uid == "" {
		return nil, nil
	}
	res, err := uc.IPAM.ListSubnets(ctx, uddi.ListOptions{TFilter: tags.UIDFilter(uid), Limit: 1})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// Create makes the Universal DDI subnet. When spec.address is empty, address is set to
// "<parent address block id>/nextavailablesubnet" so Universal DDI allocates the subnet
// atomically with the tags (DESIGN.md 4.4).
func (subnetAdapter) Create(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.SubnetSpec, deps uddisync.Deps, t map[string]string) (*ipam.Subnet, error) {
	address := spec.Address
	if address == "" {
		address = deps[depParent] + uddi.NextAvailableSubnetSuffix
	}
	cidr := int64(spec.CIDR)
	in := ipam.Subnet{
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
	if spec.DHCPHost != "" {
		in.DhcpHost = &spec.DHCPHost
	}
	in.DhcpOptions = toOptionItems(spec.DHCPOptions)
	if spec.DisableDHCP {
		disable := true
		in.DisableDhcp = &disable
	}
	in.Tags = uddi.ToAPITags(t)
	return uc.IPAM.CreateSubnet(ctx, in)
}

// Get reads a subnet by id.
func (subnetAdapter) Get(ctx context.Context, uc *uddi.Client, id string) (*ipam.Subnet, error) {
	return uc.IPAM.GetSubnet(ctx, id)
}

// Diff compares the managed fields and builds a partial patch. Address, cidr and space
// are immutable (CEL on address and parentRef, always on cidr) and are never patched.
// dhcpHost, dhcpOptions and disableDhcp, unlike address and cidr, are mutable.
func (subnetAdapter) Diff(spec *uddiv1alpha1.SubnetSpec, _ uddisync.Deps, t map[string]string, current *ipam.Subnet) (*ipam.Subnet, bool) {
	patch := &ipam.Subnet{}
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
	if strPtr(current.DhcpHost) != spec.DHCPHost {
		host := spec.DHCPHost
		patch.DhcpHost = &host
		changed = true
	}
	if !sameOptionItems(current.DhcpOptions, spec.DHCPOptions) {
		patch.DhcpOptions = toOptionItems(spec.DHCPOptions)
		changed = true
	}
	if boolPtr(current.DisableDhcp) != spec.DisableDHCP {
		disable := spec.DisableDHCP
		patch.DisableDhcp = &disable
		changed = true
	}
	if merged, tagsChanged := tags.ForPatch(uddi.FromAPITags(current.Tags), t); tagsChanged {
		patch.Tags = uddi.ToAPITags(merged)
		changed = true
	}
	return patch, changed
}

// Update applies a partial patch.
func (subnetAdapter) Update(ctx context.Context, uc *uddi.Client, id string, patch *ipam.Subnet) (*ipam.Subnet, error) {
	return uc.IPAM.UpdateSubnet(ctx, id, *patch)
}

// Delete removes the subnet.
func (subnetAdapter) Delete(ctx context.Context, uc *uddi.Client, id string) error {
	return uc.IPAM.DeleteSubnet(ctx, id)
}

// Observe copies observed fields into the CR status. Address and cidr are how a
// next-available allocation becomes visible to the user.
func (subnetAdapter) Observe(obj client.Object, current *ipam.Subnet) {
	s, ok := obj.(*uddiv1alpha1.Subnet)
	if !ok || current == nil {
		return
	}
	s.Status.Address = strPtr(current.Address)
	s.Status.CIDR = int32(i64Ptr(current.Cidr))
	s.Status.Space = strPtr(current.Space)
	s.Status.Utilization = utilizationFrom(current.Utilization)
	s.Status.DHCPUtilization = dhcpUtilizationFrom(current.DhcpUtilization)
}

// IDOf returns the Universal DDI resource id.
func (subnetAdapter) IDOf(current *ipam.Subnet) string { return strPtr(current.Id) }

// TagsOf returns the tags currently set on the subnet.
func (subnetAdapter) TagsOf(current *ipam.Subnet) map[string]string {
	return uddi.FromAPITags(current.Tags)
}

// dhcpUtilizationFrom converts the SDK's read-only DHCP utilisation block into the
// CRD's mirror type.
func dhcpUtilizationFrom(u *ipam.DHCPUtilization) *uddiv1alpha1.DHCPUtilization {
	if u == nil {
		return nil
	}
	return &uddiv1alpha1.DHCPUtilization{
		DHCPFree:        strPtr(u.DhcpFree),
		DHCPTotal:       strPtr(u.DhcpTotal),
		DHCPUsed:        strPtr(u.DhcpUsed),
		DHCPUtilization: i64Ptr(u.DhcpUtilization),
	}
}
