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

// IPAllocationPolicyReconciler will turn an IPAllocationPolicy into the Addresses and
// Service assignments described by DESIGN.md section 4.7, mirroring how
// DNSPublishPolicyReconciler drives internal/publish from internal/ipalloc. This is a
// milestone 3 wave 1 placeholder: the struct and registration exist so the manager
// wires up cleanly, but Reconcile is a no-op. Milestone 3 wave 2 implements the
// pipeline (internal/ipalloc.Compute plus apply/assign/release/status) and adds the
// watches on Services, owned Addresses and the pool kinds that DESIGN.md lists.
type IPAllocationPolicyReconciler struct {
	client.Client
	// Runtime carries the event recorder, Connection resolver and cluster id shared by
	// every object-layer controller. Not used yet by this placeholder.
	Runtime *uddisync.Runtime
}

// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ipallocationpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ipallocationpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ipallocationpolicies/finalizers,verbs=update

// Reconcile is a milestone 3 wave 1 no-op placeholder. Milestone 3 wave 2 implements
// the pipeline described in DESIGN.md section 4.7.
func (r *IPAllocationPolicyReconciler) Reconcile(_ context.Context, _ ctrl.Request) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

// SetupWithManager registers the controller. Milestone 3 wave 2 adds the watches on
// Services, owned Addresses and the pool kinds (Subnet, Range, AddressBlock) that
// DESIGN.md section 4.7 lists.
func (r *IPAllocationPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&uddiv1alpha1.IPAllocationPolicy{}).
		Named("ipallocationpolicy").
		Complete(r)
}
