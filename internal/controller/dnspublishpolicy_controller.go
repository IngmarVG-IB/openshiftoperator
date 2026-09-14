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
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
)

// DNSPublishPolicyReconciler is scaffolding for the DNSPublishPolicy controller. The
// struct shape matches every other object-layer reconciler (client.Client plus the
// shared Runtime) so wave 2 can wire it into the generic pieces already in
// internal/sync and internal/publish without changing this signature.
//
// Milestone 2 wave 2 implements the actual pipeline described in DESIGN.md section
// 4.7: resolving sources (Routes, Ingresses, Services) and zoneRefs, calling
// internal/publish.Plan, applying and pruning the resulting Records, and writing
// status. For now Reconcile is a no-op placeholder.
type DNSPublishPolicyReconciler struct {
	client.Client
	// Runtime carries the Kubernetes client, event recorder, Connection resolver and
	// cluster id shared by every object-layer controller.
	Runtime *uddisync.Runtime
}

// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=dnspublishpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=dnspublishpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=dnspublishpolicies/finalizers,verbs=update

// Reconcile is a placeholder. Milestone 2 wave 2 implements the DNSPublishPolicy
// pipeline; until then this controller does nothing.
func (r *DNSPublishPolicyReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	_ = logf.FromContext(ctx)
	return ctrl.Result{}, nil
}

// SetupWithManager registers the controller. Milestone 2 wave 2 adds the field
// indexes and watches (owned Records, Routes, Ingresses, Services, AuthZones) that
// the real pipeline needs.
func (r *DNSPublishPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&uddiv1alpha1.DNSPublishPolicy{}).
		Named("dnspublishpolicy").
		Complete(r)
}
