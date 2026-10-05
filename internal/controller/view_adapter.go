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
	"strings"

	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
	"github.com/IngmarVG-IB/openshiftoperator/internal/tags"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

const (
	// depIPSpaces is the Deps key holding the resolved IP space ids, comma separated.
	// Universal DDI resource ids never contain a comma.
	depIPSpaces = "ipSpaces"
	// depName is the Deps key holding the effective Universal DDI view name. Resolve is
	// the only adapter method that sees the CR, so it is where defaulting spec.name to
	// metadata.name happens.
	depName = "name"
)

// viewAdapter maps the View CR onto dnsconfig.View. It is the reference implementation
// of sync.Adapter; every other kind follows the same shape.
type viewAdapter struct{}

var _ uddisync.Adapter[uddiv1alpha1.ViewSpec, dnsconfig.View] = viewAdapter{}

// Kind returns the CR kind name.
func (viewAdapter) Kind() string { return "View" }

// viewName returns the effective Universal DDI view name for a CR.
func viewName(obj client.Object, spec *uddiv1alpha1.ViewSpec) string {
	if spec.Name != "" {
		return spec.Name
	}
	return obj.GetName()
}

// Resolve defaults the view name and turns spec.ipSpaces into Universal DDI ip space ids.
func (viewAdapter) Resolve(ctx context.Context, c client.Client, obj client.Object, spec *uddiv1alpha1.ViewSpec) (uddisync.Deps, error) {
	deps := uddisync.Deps{depName: viewName(obj, spec)}
	if len(spec.IPSpaces) == 0 {
		return deps, nil
	}
	ids := make([]string, 0, len(spec.IPSpaces))
	for i := range spec.IPSpaces {
		id, err := uddisync.ResolveRef(ctx, c, obj.GetNamespace(), spec.IPSpaces[i], &uddiv1alpha1.IPSpace{}, "IPSpace")
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	deps[depIPSpaces] = strings.Join(ids, ",")
	return deps, nil
}

func ipSpacesOf(deps uddisync.Deps) []string {
	joined := deps[depIPSpaces]
	if joined == "" {
		return nil
	}
	return strings.Split(joined, ",")
}

// Find looks a view up by its natural key, the view name.
func (viewAdapter) Find(ctx context.Context, uc *uddi.Client, _ *uddiv1alpha1.ViewSpec, deps uddisync.Deps) (*dnsconfig.View, error) {
	name := deps[depName]
	if name == "" {
		return nil, nil
	}
	res, err := uc.DNSConfig.ListViews(ctx, uddi.ListOptions{
		Filter: "name==" + quoteFilterValue(name),
		Limit:  1,
	})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// FindByUID looks a view up by the ownership uid tag.
func (viewAdapter) FindByUID(ctx context.Context, uc *uddi.Client, uid string) (*dnsconfig.View, error) {
	if uid == "" {
		return nil, nil
	}
	res, err := uc.DNSConfig.ListViews(ctx, uddi.ListOptions{
		TFilter: tags.UIDFilter(uid),
		Limit:   1,
	})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// Create makes the Universal DDI view.
func (viewAdapter) Create(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.ViewSpec, deps uddisync.Deps, t map[string]string) (*dnsconfig.View, error) {
	in := dnsconfig.View{Name: deps[depName]}
	if spec.Comment != "" {
		in.Comment = &spec.Comment
	}
	if spec.Disabled {
		disabled := true
		in.Disabled = &disabled
	}
	in.IpSpaces = ipSpacesOf(deps)
	in.Tags = uddi.ToAPITags(t)
	return uc.DNSConfig.CreateView(ctx, in)
}

// Get reads a view by id.
func (viewAdapter) Get(ctx context.Context, uc *uddi.Client, id string) (*dnsconfig.View, error) {
	return uc.DNSConfig.GetView(ctx, id)
}

// Diff compares the managed fields and builds a partial patch.
func (viewAdapter) Diff(spec *uddiv1alpha1.ViewSpec, deps uddisync.Deps, t tags.Desired, current *dnsconfig.View) (*dnsconfig.View, bool) {
	// dnsconfig.View.Name is a required, non-pointer field, so it always rides along on
	// a patch. That is harmless: the name is mutable and the value sent is the desired one.
	patch := &dnsconfig.View{Name: deps[depName]}
	changed := current.Name != deps[depName]

	if strPtr(current.Comment) != spec.Comment {
		comment := spec.Comment
		patch.Comment = &comment
		changed = true
	}
	if boolPtr(current.Disabled) != spec.Disabled {
		disabled := spec.Disabled
		patch.Disabled = &disabled
		changed = true
	}
	if want := ipSpacesOf(deps); !sameStrings(current.IpSpaces, want) {
		patch.IpSpaces = want
		if want == nil {
			// An explicit empty list clears the binding; nil would be omitted.
			patch.IpSpaces = []string{}
		}
		changed = true
	}
	if merged, tagsChanged := tags.ForPatch(uddi.FromAPITags(current.Tags), t); tagsChanged {
		patch.Tags = uddi.ToAPITags(merged)
		changed = true
	}
	return patch, changed
}

// Update applies a partial patch.
func (viewAdapter) Update(ctx context.Context, uc *uddi.Client, id string, patch *dnsconfig.View) (*dnsconfig.View, error) {
	return uc.DNSConfig.UpdateView(ctx, id, *patch)
}

// Delete removes the view.
func (viewAdapter) Delete(ctx context.Context, uc *uddi.Client, id string) error {
	return uc.DNSConfig.DeleteView(ctx, id)
}

// Observe copies observed fields into the CR status.
func (viewAdapter) Observe(obj client.Object, current *dnsconfig.View) {
	v, ok := obj.(*uddiv1alpha1.View)
	if !ok || current == nil {
		return
	}
	v.Status.Name = current.Name
}

// IDOf returns the Universal DDI resource id.
func (viewAdapter) IDOf(current *dnsconfig.View) string { return strPtr(current.Id) }

// TagsOf returns the tags currently set on the view.
func (viewAdapter) TagsOf(current *dnsconfig.View) map[string]string {
	return uddi.FromAPITags(current.Tags)
}

func strPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func boolPtr(p *bool) bool {
	return p != nil && *p
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
