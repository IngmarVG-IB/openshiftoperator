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

// IPSpaceReconciler reconciles an IPSpace object through the generic object-layer loop
// in internal/sync. It follows view_controller.go, the reference wiring.
type IPSpaceReconciler struct {
	client.Client
	// Runtime carries the Kubernetes client, event recorder, Connection resolver and
	// cluster id shared by every object-layer controller.
	Runtime *uddisync.Runtime
}

// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ipspaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ipspaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ipspaces/finalizers,verbs=update
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=connections,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile brings one IPSpace CR and its Universal DDI counterpart into agreement.
func (r *IPSpaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	space := &uddiv1alpha1.IPSpace{}
	if err := r.Get(ctx, req.NamespacedName, space); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	res, err := uddisync.Reconcile(ctx, r.Runtime, space, ipspaceAdapter{})
	if apierrors.IsConflict(err) {
		// Another writer won the race. The next pass sees the fresh object.
		logf.FromContext(ctx).V(1).Info("conflict while writing the IPSpace, requeueing")
		return ctrl.Result{RequeueAfter: uddisync.RequeueSoon}, nil
	}
	if apierrors.IsNotFound(err) {
		// The CR went away mid-reconcile; there is nothing left to do.
		return ctrl.Result{}, nil
	}
	return res, err
}

// SetupWithManager registers the controller, its Connection field index and the watch
// that requeues an IPSpace when the Connection it uses changes. IPSpace has no
// dependencies on other object-layer kinds.
func (r *IPSpaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()

	if err := RegisterConnectionIndex(ctx, mgr, &uddiv1alpha1.IPSpace{}, func(o client.Object) string {
		return o.(*uddiv1alpha1.IPSpace).Spec.ConnectionRef.Name
	}); err != nil {
		return err
	}

	newIPSpaceList := func() client.ObjectList { return &uddiv1alpha1.IPSpaceList{} }

	return ctrl.NewControllerManagedBy(mgr).
		For(&uddiv1alpha1.IPSpace{}).
		Watches(
			&uddiv1alpha1.Connection{},
			handler.EnqueueRequestsFromMapFunc(EnqueueForIndex(mgr.GetClient(), IndexConnectionRef, newIPSpaceList)),
		).
		Named("ipspace").
		Complete(r)
}
