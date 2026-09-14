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

	"github.com/infobloxopen/universal-ddi-go-client/ipam"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
	"github.com/IngmarVG-IB/openshiftoperator/internal/tags"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// fixedAddressDepSpace is the Deps key holding the resolved IP space id.
const fixedAddressDepSpace = "space"

// fixedaddressAdapter maps the FixedAddress CR onto ipam.FixedAddress.
type fixedaddressAdapter struct{}

var _ uddisync.Adapter[uddiv1alpha1.FixedAddressSpec, ipam.FixedAddress] = fixedaddressAdapter{}

// Kind returns the CR kind name.
func (fixedaddressAdapter) Kind() string { return "FixedAddress" }

// Resolve turns spec.spaceRef into a Universal DDI ip space id.
func (fixedaddressAdapter) Resolve(ctx context.Context, c client.Client, obj client.Object, spec *uddiv1alpha1.FixedAddressSpec) (uddisync.Deps, error) {
	spaceID, err := uddisync.ResolveRef(ctx, c, obj.GetNamespace(), spec.SpaceRef, &uddiv1alpha1.IPSpace{}, "IPSpace")
	if err != nil {
		return nil, err
	}
	return uddisync.Deps{fixedAddressDepSpace: spaceID}, nil
}

// Find looks a fixed address up by its natural key: address plus ip space.
func (fixedaddressAdapter) Find(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.FixedAddressSpec, deps uddisync.Deps) (*ipam.FixedAddress, error) {
	res, err := uc.DHCP.ListFixedAddresses(ctx, uddi.ListOptions{
		Filter: `address=="` + spec.Address + `" and ip_space=="` + deps[fixedAddressDepSpace] + `"`,
		Limit:  1,
	})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// FindByUID looks a fixed address up by the ownership uid tag.
func (fixedaddressAdapter) FindByUID(ctx context.Context, uc *uddi.Client, uid string) (*ipam.FixedAddress, error) {
	if uid == "" {
		return nil, nil
	}
	res, err := uc.DHCP.ListFixedAddresses(ctx, uddi.ListOptions{
		TFilter: tags.UIDFilter(uid),
		Limit:   1,
	})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// Create makes the Universal DDI DHCP reservation.
func (fixedaddressAdapter) Create(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.FixedAddressSpec, deps uddisync.Deps, t map[string]string) (*ipam.FixedAddress, error) {
	space := deps[fixedAddressDepSpace]
	in := ipam.FixedAddress{
		Address:     spec.Address,
		IpSpace:     &space,
		MatchType:   string(spec.MatchType),
		MatchValue:  spec.MatchValue,
		Name:        optStr(spec.Name),
		Hostname:    optStr(spec.Hostname),
		Comment:     optStr(spec.Comment),
		DhcpOptions: toOptionItems(spec.DHCPOptions),
		Tags:        uddi.ToAPITags(t),
	}
	if spec.DisableDHCP {
		disable := true
		in.DisableDhcp = &disable
	}
	return uc.DHCP.CreateFixedAddress(ctx, in)
}

// Get reads a fixed address by id.
func (fixedaddressAdapter) Get(ctx context.Context, uc *uddi.Client, id string) (*ipam.FixedAddress, error) {
	return uc.DHCP.GetFixedAddress(ctx, id)
}

// Diff compares the managed fields and builds a partial patch. Address, space and
// matchType are immutable and ride along on every patch because ipam.FixedAddress models
// address, matchType and matchValue as required, non-pointer fields; the address and
// matchType values sent always equal what Universal DDI already has, so this is
// harmless, the same way view_adapter.go handles dnsconfig.View.Name.
func (fixedaddressAdapter) Diff(spec *uddiv1alpha1.FixedAddressSpec, deps uddisync.Deps, t map[string]string, current *ipam.FixedAddress) (*ipam.FixedAddress, bool) {
	patch := &ipam.FixedAddress{
		Address:    current.Address,
		MatchType:  current.MatchType,
		MatchValue: current.MatchValue,
	}
	changed := false

	if current.MatchValue != spec.MatchValue {
		patch.MatchValue = spec.MatchValue
		changed = true
	}
	if strPtr(current.Name) != spec.Name {
		patch.Name = optStr(spec.Name)
		changed = true
	}
	if strPtr(current.Hostname) != spec.Hostname {
		patch.Hostname = optStr(spec.Hostname)
		changed = true
	}
	if strPtr(current.Comment) != spec.Comment {
		patch.Comment = optStr(spec.Comment)
		changed = true
	}
	if boolPtr(current.DisableDhcp) != spec.DisableDHCP {
		disable := spec.DisableDHCP
		patch.DisableDhcp = &disable
		changed = true
	}
	if !sameOptionItems(current.DhcpOptions, spec.DHCPOptions) {
		patch.DhcpOptions = toOptionItems(spec.DHCPOptions)
		changed = true
	}
	if merged, tagsChanged := tags.ForPatch(uddi.FromAPITags(current.Tags), t); tagsChanged {
		patch.Tags = uddi.ToAPITags(merged)
		changed = true
	}
	return patch, changed
}

// Update applies a partial patch.
func (fixedaddressAdapter) Update(ctx context.Context, uc *uddi.Client, id string, patch *ipam.FixedAddress) (*ipam.FixedAddress, error) {
	return uc.DHCP.UpdateFixedAddress(ctx, id, *patch)
}

// Delete removes the DHCP reservation.
func (fixedaddressAdapter) Delete(ctx context.Context, uc *uddi.Client, id string) error {
	return uc.DHCP.DeleteFixedAddress(ctx, id)
}

// Observe copies observed fields into the CR status.
func (fixedaddressAdapter) Observe(obj client.Object, current *ipam.FixedAddress) {
	f, ok := obj.(*uddiv1alpha1.FixedAddress)
	if !ok {
		return
	}
	f.Status.Address = current.Address
	f.Status.IPSpace = strPtr(current.IpSpace)
	f.Status.Parent = strPtr(current.Parent)
}

// IDOf returns the Universal DDI resource id.
func (fixedaddressAdapter) IDOf(current *ipam.FixedAddress) string { return strPtr(current.Id) }

// TagsOf returns the tags currently set on the fixed address.
func (fixedaddressAdapter) TagsOf(current *ipam.FixedAddress) map[string]string {
	return uddi.FromAPITags(current.Tags)
}
