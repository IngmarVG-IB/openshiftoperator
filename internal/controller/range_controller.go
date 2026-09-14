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

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
)

// RangeReconciler reconciles a Range object.
//
// WAVE 2 PLACEHOLDER. The generic object-layer loop, the Connection resolver, the
// ownership tags and the field-index helpers are all in place; what is missing is the
// sync.Adapter[uddiv1alpha1.RangeSpec, <SDK type>] that maps this kind onto its
// Universal DDI counterpart. Follow view_adapter.go and view_controller.go: implement
// the adapter, then replace the body of Reconcile below with
//
//	obj := &uddiv1alpha1.Range{}
//	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
//		return ctrl.Result{}, client.IgnoreNotFound(err)
//	}
//	return uddisync.Reconcile(ctx, r.Runtime, obj, rangeAdapter{})
//
// and register the Connection and dependency indexes in SetupWithManager.
//
// Until then this controller intentionally does nothing: it never sets a finalizer and
// never touches Universal DDI, so a Range CR can be created and deleted freely.
type RangeReconciler struct {
	client.Client
	// Runtime is shared with every other object-layer controller.
	Runtime *uddisync.Runtime
}

// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ranges,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ranges/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ranges/finalizers,verbs=update

// Reconcile is a no-op until the wave 2 adapter lands.
func (r *RangeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = ctx
	_ = req
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RangeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&uddiv1alpha1.Range{}).
		Named("range").
		Complete(r)
}
