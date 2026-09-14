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

// rangeDepSpace is the Deps key holding the resolved IP space id.
const rangeDepSpace = "space"

// rangeAdapter maps the Range CR onto ipam.Range. The parent subnet is derived by
// Universal DDI from start and end, so it is never resolved here; it only ever flows
// back through Observe.
type rangeAdapter struct{}

var _ uddisync.Adapter[uddiv1alpha1.RangeSpec, ipam.Range] = rangeAdapter{}

// Kind returns the CR kind name.
func (rangeAdapter) Kind() string { return "Range" }

// Resolve turns spec.spaceRef into a Universal DDI ip space id.
func (rangeAdapter) Resolve(ctx context.Context, c client.Client, obj client.Object, spec *uddiv1alpha1.RangeSpec) (uddisync.Deps, error) {
	spaceID, err := uddisync.ResolveRef(ctx, c, obj.GetNamespace(), spec.SpaceRef, &uddiv1alpha1.IPSpace{}, "IPSpace")
	if err != nil {
		return nil, err
	}
	return uddisync.Deps{rangeDepSpace: spaceID}, nil
}

// Find looks a range up by its natural key: start plus end plus space.
func (rangeAdapter) Find(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.RangeSpec, deps uddisync.Deps) (*ipam.Range, error) {
	res, err := uc.IPAM.ListRanges(ctx, uddi.ListOptions{
		Filter: `start=="` + spec.Start + `" and end=="` + spec.End + `" and space=="` + deps[rangeDepSpace] + `"`,
		Limit:  1,
	})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// FindByUID looks a range up by the ownership uid tag.
func (rangeAdapter) FindByUID(ctx context.Context, uc *uddi.Client, uid string) (*ipam.Range, error) {
	if uid == "" {
		return nil, nil
	}
	res, err := uc.IPAM.ListRanges(ctx, uddi.ListOptions{
		TFilter: tags.UIDFilter(uid),
		Limit:   1,
	})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// Create makes the Universal DDI range.
func (rangeAdapter) Create(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.RangeSpec, deps uddisync.Deps, t map[string]string) (*ipam.Range, error) {
	space := deps[rangeDepSpace]
	in := ipam.Range{
		Start:           spec.Start,
		End:             spec.End,
		Space:           &space,
		Name:            optStr(spec.Name),
		Comment:         optStr(spec.Comment),
		DhcpHost:        optStr(spec.DHCPHost),
		DhcpOptions:     toOptionItems(spec.DHCPOptions),
		ExclusionRanges: toExclusionRanges(spec.ExclusionRanges),
		Tags:            uddi.ToAPITags(t),
	}
	return uc.IPAM.CreateRange(ctx, in)
}

// Get reads a range by id.
func (rangeAdapter) Get(ctx context.Context, uc *uddi.Client, id string) (*ipam.Range, error) {
	return uc.IPAM.GetRange(ctx, id)
}

// Diff compares the managed fields and builds a partial patch. Start and end are
// immutable (enforced by CEL on the CRD) and ride along on every patch because
// ipam.Range models them as required, non-pointer fields; the value sent always equals
// what Universal DDI already has, so this is harmless, the same way view_adapter.go
// handles dnsconfig.View.Name.
func (rangeAdapter) Diff(spec *uddiv1alpha1.RangeSpec, deps uddisync.Deps, t map[string]string, current *ipam.Range) (*ipam.Range, bool) {
	patch := &ipam.Range{Start: current.Start, End: current.End}
	changed := false

	if strPtr(current.Name) != spec.Name {
		patch.Name = optStr(spec.Name)
		changed = true
	}
	if strPtr(current.Comment) != spec.Comment {
		patch.Comment = optStr(spec.Comment)
		changed = true
	}
	if strPtr(current.DhcpHost) != spec.DHCPHost {
		patch.DhcpHost = optStr(spec.DHCPHost)
		changed = true
	}
	if !sameOptionItems(current.DhcpOptions, spec.DHCPOptions) {
		patch.DhcpOptions = toOptionItems(spec.DHCPOptions)
		changed = true
	}
	if !sameExclusionRanges(current.ExclusionRanges, spec.ExclusionRanges) {
		patch.ExclusionRanges = toExclusionRanges(spec.ExclusionRanges)
		changed = true
	}
	if merged, tagsChanged := tags.ForPatch(uddi.FromAPITags(current.Tags), t); tagsChanged {
		patch.Tags = uddi.ToAPITags(merged)
		changed = true
	}
	return patch, changed
}

// Update applies a partial patch.
func (rangeAdapter) Update(ctx context.Context, uc *uddi.Client, id string, patch *ipam.Range) (*ipam.Range, error) {
	return uc.IPAM.UpdateRange(ctx, id, *patch)
}

// Delete removes the range.
func (rangeAdapter) Delete(ctx context.Context, uc *uddi.Client, id string) error {
	return uc.IPAM.DeleteRange(ctx, id)
}

// Observe copies observed fields into the CR status.
func (rangeAdapter) Observe(obj client.Object, current *ipam.Range) {
	r, ok := obj.(*uddiv1alpha1.Range)
	if !ok {
		return
	}
	r.Status.Start = current.Start
	r.Status.End = current.End
	r.Status.Space = strPtr(current.Space)
	r.Status.Parent = strPtr(current.Parent)
	r.Status.Utilization = toUtilization(current.Utilization)
}

// IDOf returns the Universal DDI resource id.
func (rangeAdapter) IDOf(current *ipam.Range) string { return strPtr(current.Id) }

// TagsOf returns the tags currently set on the range.
func (rangeAdapter) TagsOf(current *ipam.Range) map[string]string {
	return uddi.FromAPITags(current.Tags)
}

// optStr returns nil for an empty string, otherwise a pointer to a copy of s. Shared by
// the Range, Address and FixedAddress adapters for optional string fields the SDK models
// as *string.
func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// toUtilization converts the SDK's read-only utilisation block into the CRD's mirror
// type, field for field. It returns nil when Universal DDI reports none.
func toUtilization(in *ipam.Utilization) *uddiv1alpha1.Utilization {
	if in == nil {
		return nil
	}
	return &uddiv1alpha1.Utilization{
		AbandonUtilization: i64Ptr(in.AbandonUtilization),
		Abandoned:          strPtr(in.Abandoned),
		Dynamic:            strPtr(in.Dynamic),
		Free:               strPtr(in.Free),
		Static:             strPtr(in.Static),
		Total:              strPtr(in.Total),
		Used:               strPtr(in.Used),
		Utilization:        i64Ptr(in.Utilization),
	}
}

// toExclusionRanges converts CR exclusion ranges into the SDK's ipam.ExclusionRange
// list. A nil or empty input yields an empty (non-nil) slice, mirroring toOptionItems in
// dhcpoptions.go, so a patch can clear the list in Universal DDI.
func toExclusionRanges(in []uddiv1alpha1.ExclusionRange) []ipam.ExclusionRange {
	out := make([]ipam.ExclusionRange, 0, len(in))
	for i := range in {
		e := in[i]
		out = append(out, ipam.ExclusionRange{
			Start:   e.Start,
			End:     e.End,
			Comment: optStr(e.Comment),
		})
	}
	return out
}

// sameExclusionRanges reports whether the observed exclusion ranges match the desired
// ones. Order is significant, mirroring sameOptionItems.
func sameExclusionRanges(current []ipam.ExclusionRange, desired []uddiv1alpha1.ExclusionRange) bool {
	if len(current) != len(desired) {
		return false
	}
	for i := range desired {
		d := desired[i]
		c := current[i]
		if c.Start != d.Start || c.End != d.End || strPtr(c.Comment) != d.Comment {
			return false
		}
	}
	return true
}
