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

// HostRegistrationPolicyReconciler will turn a HostRegistrationPolicy into the
// Addresses and FixedAddresses described by DESIGN.md section 4.7, mirroring how
// IPAllocationPolicyReconciler drives internal/ipalloc from its own planner. This is a
// milestone 4 wave 1 placeholder: the struct and registration exist so the manager
// wires up cleanly, but Reconcile is a no-op. Milestone 4 wave 2 implements the
// pipeline (Node/NMState views from internal/hostreg, hostreg.Compute, apply/release
// via server-side apply, and status) and adds the watches on Nodes, NodeNetworkStates,
// owned Addresses/FixedAddresses and the IPSpace kind that DESIGN.md lists.
type HostRegistrationPolicyReconciler struct {
	client.Client
	// Runtime carries the event recorder, Connection resolver and cluster id shared by
	// every object-layer controller. Not used yet by this placeholder.
	Runtime *uddisync.Runtime
}

// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=hostregistrationpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=hostregistrationpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=hostregistrationpolicies/finalizers,verbs=update

// Reconcile is a milestone 4 wave 1 no-op placeholder. Milestone 4 wave 2 implements
// the pipeline described in DESIGN.md section 4.7.
func (r *HostRegistrationPolicyReconciler) Reconcile(_ context.Context, _ ctrl.Request) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

// SetupWithManager registers the controller. Milestone 4 wave 2 adds the watches on
// Nodes, NodeNetworkStates (when the API is present), owned Addresses/FixedAddresses
// and the IPSpace kind that DESIGN.md section 4.7 lists.
func (r *HostRegistrationPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&uddiv1alpha1.HostRegistrationPolicy{}).
		Named("hostregistrationpolicy").
		Complete(r)
}
