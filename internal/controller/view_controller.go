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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
)

// ViewReconciler reconciles a View object through the generic object-layer loop in
// internal/sync. It is the reference wiring for every other object-layer kind.
type ViewReconciler struct {
	client.Client
	// Runtime carries the Kubernetes client, event recorder, Connection resolver and
	// cluster id shared by every object-layer controller.
	Runtime *uddisync.Runtime
}

// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=views,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=views/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=views/finalizers,verbs=update
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ipspaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=connections,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile brings one View CR and its Universal DDI counterpart into agreement.
func (r *ViewReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	view := &uddiv1alpha1.View{}
	if err := r.Get(ctx, req.NamespacedName, view); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	res, err := uddisync.Reconcile(ctx, r.Runtime, view, viewAdapter{})
	if apierrors.IsConflict(err) {
		// Another writer won the race. The next pass sees the fresh object.
		logf.FromContext(ctx).V(1).Info("conflict while writing the View, requeueing")
		return ctrl.Result{RequeueAfter: uddisync.RequeueSoon}, nil
	}
	if apierrors.IsNotFound(err) {
		// The CR went away mid-reconcile; there is nothing left to do.
		return ctrl.Result{}, nil
	}
	return res, err
}

// SetupWithManager registers the controller, its field indexes and the watches that
// requeue a View when the Connection it uses or an IPSpace it references changes.
func (r *ViewReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()

	if err := RegisterConnectionIndex(ctx, mgr, &uddiv1alpha1.View{}, func(o client.Object) string {
		return o.(*uddiv1alpha1.View).Spec.ConnectionRef.Name
	}); err != nil {
		return err
	}
	if err := RegisterDependencyIndex(ctx, mgr, &uddiv1alpha1.View{}, func(o client.Object) []string {
		v := o.(*uddiv1alpha1.View)
		refs := make([]*uddiv1alpha1.ObjectRef, 0, len(v.Spec.IPSpaces))
		for i := range v.Spec.IPSpaces {
			refs = append(refs, &v.Spec.IPSpaces[i])
		}
		return RefNames(refs...)
	}); err != nil {
		return err
	}

	newViewList := func() client.ObjectList { return &uddiv1alpha1.ViewList{} }

	return ctrl.NewControllerManagedBy(mgr).
		For(&uddiv1alpha1.View{}).
		Watches(
			&uddiv1alpha1.Connection{},
			handler.EnqueueRequestsFromMapFunc(EnqueueForIndex(mgr.GetClient(), IndexConnectionRef, newViewList)),
		).
		Watches(
			&uddiv1alpha1.IPSpace{},
			handler.EnqueueRequestsFromMapFunc(EnqueueForIndex(mgr.GetClient(), IndexDependencyRefs, newViewList)),
		).
		Named("view").
		Complete(r)
}
