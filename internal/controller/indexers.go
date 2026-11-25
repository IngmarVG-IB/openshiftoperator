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

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

// Field index names. Object-layer controllers register these so a CR is requeued as
// soon as the thing it waits for becomes usable.
const (
	// IndexConnectionRef indexes an object-layer CR by the Connection it uses, with the
	// empty ref already defaulted to "default".
	IndexConnectionRef = "spec.connectionRef.name"
	// IndexDependencyRefs indexes an object-layer CR by the names of every other
	// object-layer CR it references, so it is requeued when one of them gains a
	// status.id.
	IndexDependencyRefs = "spec.dependencyRefs"
)

// RegisterConnectionIndex indexes objects of the given kind by the Connection they use.
// extract must return spec.connectionRef.name, already defaulted.
func RegisterConnectionIndex(ctx context.Context, mgr ctrl.Manager, obj client.Object, extract func(client.Object) string) error {
	return mgr.GetFieldIndexer().IndexField(ctx, obj, IndexConnectionRef, func(o client.Object) []string {
		name := extract(o)
		if name == "" {
			name = uddiv1alpha1.DefaultConnectionName
		}
		return []string{name}
	})
}

// RegisterDependencyIndex indexes objects of the given kind by the names of the
// object-layer CRs they reference. extract returns those names; refs that point
// straight at a Universal DDI id contribute nothing.
//
// Each object-layer kind calls this with its own extractor (AuthZone -> viewRef, Record ->
// zoneRef, the IPAM kinds -> spaceRef and parentRef, and so on).
func RegisterDependencyIndex(ctx context.Context, mgr ctrl.Manager, obj client.Object, extract func(client.Object) []string) error {
	return mgr.GetFieldIndexer().IndexField(ctx, obj, IndexDependencyRefs, func(o client.Object) []string {
		return extract(o)
	})
}

// RefNames collects the CR names out of a set of ObjectRefs, skipping refs that carry a
// Universal DDI id instead. It is the building block for RegisterDependencyIndex
// extractors.
func RefNames(refs ...*uddiv1alpha1.ObjectRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if r != nil && r.Name != "" {
			out = append(out, r.Name)
		}
	}
	return out
}

// EnqueueForIndex returns a handler that maps a changed object onto every CR in the
// same namespace whose index field matches the changed object's name. list must be an
// empty list of the dependent kind, and the handler reads it fresh on every event.
func EnqueueForIndex(c client.Client, index string, newList func() client.ObjectList) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		list := newList()
		if err := c.List(ctx, list,
			client.InNamespace(obj.GetNamespace()),
			client.MatchingFields{index: obj.GetName()},
		); err != nil {
			return nil
		}
		var out []reconcile.Request
		_ = meta.EachListItem(list, func(o runtime.Object) error {
			item, ok := o.(client.Object)
			if !ok {
				return nil
			}
			out = append(out, reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: item.GetNamespace(), Name: item.GetName()},
			})
			return nil
		})
		return out
	}
}
