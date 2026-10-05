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

// depIPSpaceName is the Deps key holding the effective Universal DDI IP space name.
const depIPSpaceName = "name"

// ipspaceAdapter maps the IPSpace CR onto ipam.IPSpace. It follows the shape of
// viewAdapter, the reference implementation in view_adapter.go.
type ipspaceAdapter struct{}

var _ uddisync.Adapter[uddiv1alpha1.IPSpaceSpec, ipam.IPSpace] = ipspaceAdapter{}

// Kind returns the CR kind name.
func (ipspaceAdapter) Kind() string { return "IPSpace" }

// ipSpaceCRName returns the effective Universal DDI IP space name for a CR, defaulting
// spec.name to metadata.name the same way View defaults spec.name.
func ipSpaceCRName(obj client.Object, spec *uddiv1alpha1.IPSpaceSpec) string {
	if spec.Name != "" {
		return spec.Name
	}
	return obj.GetName()
}

// Resolve defaults the IP space name. IPSpace has no dependencies on other object-layer
// kinds.
func (ipspaceAdapter) Resolve(_ context.Context, _ client.Client, obj client.Object, spec *uddiv1alpha1.IPSpaceSpec) (uddisync.Deps, error) {
	return uddisync.Deps{depIPSpaceName: ipSpaceCRName(obj, spec)}, nil
}

// Find looks an IP space up by its natural key, the name.
func (ipspaceAdapter) Find(ctx context.Context, uc *uddi.Client, _ *uddiv1alpha1.IPSpaceSpec, deps uddisync.Deps) (*ipam.IPSpace, error) {
	name := deps[depIPSpaceName]
	if name == "" {
		return nil, nil
	}
	res, err := uc.IPAM.ListIPSpaces(ctx, uddi.ListOptions{
		Filter: "name==" + quoteFilterValue(name),
		Limit:  1,
	})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// FindByUID looks an IP space up by the ownership uid tag.
func (ipspaceAdapter) FindByUID(ctx context.Context, uc *uddi.Client, uid string) (*ipam.IPSpace, error) {
	if uid == "" {
		return nil, nil
	}
	res, err := uc.IPAM.ListIPSpaces(ctx, uddi.ListOptions{
		TFilter: tags.UIDFilter(uid),
		Limit:   1,
	})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// Create makes the Universal DDI IP space.
func (ipspaceAdapter) Create(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.IPSpaceSpec, deps uddisync.Deps, t map[string]string) (*ipam.IPSpace, error) {
	in := ipam.IPSpace{Name: deps[depIPSpaceName]}
	if spec.Comment != "" {
		in.Comment = &spec.Comment
	}
	in.DhcpOptions = toOptionItems(spec.DHCPOptions)
	in.Tags = uddi.ToAPITags(t)
	return uc.IPAM.CreateIPSpace(ctx, in)
}

// Get reads an IP space by id.
func (ipspaceAdapter) Get(ctx context.Context, uc *uddi.Client, id string) (*ipam.IPSpace, error) {
	return uc.IPAM.GetIPSpace(ctx, id)
}

// Diff compares the managed fields and builds a partial patch. Name is mutable in the
// API, so it is treated as managed and patched like comment, dhcpOptions and tags.
func (ipspaceAdapter) Diff(spec *uddiv1alpha1.IPSpaceSpec, deps uddisync.Deps, t tags.Desired, current *ipam.IPSpace) (*ipam.IPSpace, bool) {
	// ipam.IPSpace.Name is a required, non-pointer field, so it always rides along on a
	// patch, exactly like dnsconfig.View.Name in the reference adapter.
	patch := &ipam.IPSpace{Name: deps[depIPSpaceName]}
	changed := current.Name != deps[depIPSpaceName]

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
func (ipspaceAdapter) Update(ctx context.Context, uc *uddi.Client, id string, patch *ipam.IPSpace) (*ipam.IPSpace, error) {
	return uc.IPAM.UpdateIPSpace(ctx, id, *patch)
}

// Delete removes the IP space.
func (ipspaceAdapter) Delete(ctx context.Context, uc *uddi.Client, id string) error {
	return uc.IPAM.DeleteIPSpace(ctx, id)
}

// Observe copies observed fields into the CR status.
func (ipspaceAdapter) Observe(obj client.Object, current *ipam.IPSpace) {
	s, ok := obj.(*uddiv1alpha1.IPSpace)
	if !ok || current == nil {
		return
	}
	s.Status.Name = current.Name
	s.Status.Utilization = utilizationFrom(current.Utilization)
}

// IDOf returns the Universal DDI resource id.
func (ipspaceAdapter) IDOf(current *ipam.IPSpace) string { return strPtr(current.Id) }

// TagsOf returns the tags currently set on the IP space.
func (ipspaceAdapter) TagsOf(current *ipam.IPSpace) map[string]string {
	return uddi.FromAPITags(current.Tags)
}

// i64Ptr dereferences an int64 pointer, treating nil as zero. Shared by the IPAM
// adapters (ipspace, addressblock, subnet) for utilisation and cidr fields.
func i64Ptr(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// utilizationFrom converts the SDK's read-only utilisation block into the CRD's mirror
// type. Shared by every IPAM kind whose status carries a Utilization.
func utilizationFrom(u *ipam.Utilization) *uddiv1alpha1.Utilization {
	if u == nil {
		return nil
	}
	return &uddiv1alpha1.Utilization{
		AbandonUtilization: i64Ptr(u.AbandonUtilization),
		Abandoned:          strPtr(u.Abandoned),
		Dynamic:            strPtr(u.Dynamic),
		Free:               strPtr(u.Free),
		Static:             strPtr(u.Static),
		Total:              strPtr(u.Total),
		Used:               strPtr(u.Used),
		Utilization:        i64Ptr(u.Utilization),
	}
}
