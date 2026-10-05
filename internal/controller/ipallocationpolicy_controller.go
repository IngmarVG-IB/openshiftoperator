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
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/ipalloc"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
)

// IPAllocationPolicyReconciler turns an IPAllocationPolicy into the Addresses and
// Service assignments described by DESIGN.md section 4.7: it views the selected
// Services through internal/ipalloc, calls ipalloc.Compute, applies the resulting
// Addresses with server-side apply, patches Services to carry their allocated
// address, releases allocations that are no longer wanted, and reports status.
type IPAllocationPolicyReconciler struct {
	client.Client
	// Runtime carries the event recorder shared by every object-layer controller. Its
	// Client and Connection resolver are not used here: an IPAllocationPolicy never
	// talks to Universal DDI directly, only through the Addresses it manages.
	Runtime *uddisync.Runtime
}

// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ipallocationpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ipallocationpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ipallocationpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=addresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=subnets,verbs=get;list;watch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ranges,verbs=get;list;watch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=addressblocks,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile brings the Addresses and Service assignments owned by one
// IPAllocationPolicy into agreement with its selected Services, per DESIGN.md
// section 4.7.
//
//nolint:gocyclo // the loop is a single linear pipeline; splitting it hides the order.
func (r *IPAllocationPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	policy := &uddiv1alpha1.IPAllocationPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !policy.GetDeletionTimestamp().IsZero() {
		// No finalizer on the policy: Addresses are owned by their Services (not the
		// policy) and keep working, and the Services keep whatever address they were
		// already assigned.
		return ctrl.Result{}, nil
	}

	via := ipalloc.AssignVia{
		Mode:       ipalloc.AssignMode(policy.Spec.AssignVia.Mode),
		Annotation: policy.Spec.AssignVia.Annotation,
	}

	// Step 3: Services.
	sel, err := serviceSelectorFor(policy)
	if err != nil {
		// A malformed selector cannot be fixed by retrying.
		log.Error(err, "invalid spec.selector")
		return ctrl.Result{}, nil
	}
	var svcList corev1.ServiceList
	if err := r.List(ctx, &svcList, client.InNamespace(policy.Namespace), client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return ctrl.Result{}, err
	}
	svcByName := make(map[string]*corev1.Service, len(svcList.Items))
	views := make([]ipalloc.ServiceView, 0, len(svcList.Items))
	for i := range svcList.Items {
		svc := &svcList.Items[i]
		svcByName[svc.Name] = svc
		views = append(views, ipalloc.ViewService(svc, via, ""))
	}

	// Step 4: Pools. The policy's own pool plus every distinct per-Service override.
	poolRefs := map[ipalloc.PoolRef]bool{ipalloc.PolicyPool(policy): true}
	for _, v := range views {
		if v.Pool != nil {
			poolRefs[*v.Pool] = true
		}
	}
	pools := make(map[ipalloc.PoolRef]ipalloc.PoolState, len(poolRefs))
	for ref := range poolRefs {
		state, err := r.resolvePool(ctx, policy.Namespace, ref)
		if err != nil {
			return ctrl.Result{}, err
		}
		pools[ref] = state
	}

	// Step 5: Addresses this policy already owns.
	var addrList uddiv1alpha1.AddressList
	if err := r.List(ctx, &addrList, client.InNamespace(policy.Namespace),
		client.MatchingLabels{ipalloc.LabelPolicy: policy.Name}); err != nil {
		return ctrl.Result{}, err
	}
	priorAddressNames := make(map[string]bool, len(addrList.Items))
	existing := make([]ipalloc.AddressState, 0, len(addrList.Items))
	for i := range addrList.Items {
		a := &addrList.Items[i]
		priorAddressNames[a.Name] = true
		st := ipalloc.AddressState{
			Name:    a.Name,
			Service: a.Labels[ipalloc.LabelService],
			Address: a.Status.Address,
		}
		if cond := meta.FindStatusCondition(a.Status.Conditions, uddiv1alpha1.ConditionReady); cond != nil {
			st.Ready = cond.Status == metav1.ConditionTrue
			if !st.Ready {
				st.Reason = cond.Reason
				st.Message = cond.Message
			}
		}
		existing = append(existing, st)
	}

	// Step 6: plan.
	plan := ipalloc.Compute(ipalloc.PlanInput{Policy: policy, Services: views, Pools: pools, Existing: existing})

	// Step 7: Ensure.
	for _, da := range plan.Ensure {
		svc, ok := svcByName[da.Service.Name]
		if !ok {
			// The Service disappeared between the List above and now; the next
			// reconcile (woken by the delete) releases it instead.
			continue
		}
		addr := &uddiv1alpha1.Address{
			TypeMeta: metav1.TypeMeta{
				APIVersion: uddiv1alpha1.GroupVersion.String(),
				Kind:       "Address",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:        da.Name,
				Namespace:   policy.Namespace,
				Labels:      da.Labels,
				Annotations: da.Annotations,
			},
			Spec: da.Spec,
		}
		if err := controllerutil.SetControllerReference(svc, addr, r.Scheme()); err != nil {
			return ctrl.Result{}, err
		}
		//nolint:staticcheck // see PLAN.md
		if err := r.Patch(ctx, addr, client.Apply, client.FieldOwner(ipalloc.FieldManager), client.ForceOwnership); err != nil {
			return ctrl.Result{}, fmt.Errorf("applying Address %s: %w", da.Name, err)
		}
		if !priorAddressNames[da.Name] {
			r.event(policy, corev1.EventTypeNormal, "Allocated", "allocated Address %s for Service %s", da.Name, da.Service.Name)
		}
	}

	// Step 8: Assign.
	for _, as := range plan.Assign {
		svc, ok := svcByName[as.Service.Name]
		if !ok {
			continue
		}
		if err := r.Patch(ctx, svc, client.RawPatch(types.MergePatchType, ipalloc.AssignmentPatch(via, as.Value))); err != nil {
			return ctrl.Result{}, err
		}
		r.event(policy, corev1.EventTypeNormal, "Assigned", "assigned %s to Service %s", as.Value, as.Service.Name)
	}

	// Step 9: Release.
	for _, rel := range plan.Release {
		var svc corev1.Service
		err := r.Get(ctx, client.ObjectKey{Namespace: policy.Namespace, Name: rel.Service.Name}, &svc)
		switch {
		case apierrors.IsNotFound(err):
			// The Service is gone; nothing left to clear.
		case err != nil:
			return ctrl.Result{}, err
		default:
			if ipalloc.AssignmentMatches(&svc, via, rel.ClearValue) {
				if err := r.Patch(ctx, &svc, client.RawPatch(types.MergePatchType, ipalloc.ClearAssignmentPatch(via))); err != nil {
					return ctrl.Result{}, err
				}
			}
		}
		addr := &uddiv1alpha1.Address{ObjectMeta: metav1.ObjectMeta{Name: rel.AddressName, Namespace: policy.Namespace}}
		if err := r.Delete(ctx, addr); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		r.event(policy, corev1.EventTypeNormal, "Released", "released Address %s for Service %s", rel.AddressName, rel.Service.Name)
	}

	r.emitSkipEvents(policy, plan.Skips)

	// Step 10: status. plan.Assigned only counts Services whose assignment field
	// already matched their Address before this reconcile; plan.Assign is exactly the
	// Services just patched in step 8 above, so they are assigned too by the time this
	// status is written.
	assigned := plan.Assigned + int32(len(plan.Assign))

	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.Allocated = plan.Allocated
	policy.Status.Assigned = assigned
	policy.Status.Skipped = toSkippedServices(plan.Skips)

	hasPoolNotReady := false
	hasAddressPending := false
	for _, sk := range plan.Skips {
		switch sk.Reason {
		case ipalloc.SkipPoolNotReady:
			hasPoolNotReady = true
		case ipalloc.SkipAddressPending:
			hasAddressPending = true
		}
	}
	policyPool := ipalloc.PolicyPool(policy)
	policyPoolReady := pools[policyPool].Ready

	cond := metav1.Condition{Type: uddiv1alpha1.ConditionReady, ObservedGeneration: policy.Generation}
	switch {
	case assigned == plan.Allocated && !hasPoolNotReady && !hasAddressPending:
		cond.Status = metav1.ConditionTrue
		cond.Reason = uddiv1alpha1.ReasonAssigned
		cond.Message = fmt.Sprintf("%d of %d addresses assigned", assigned, plan.Allocated)
	case !policyPoolReady:
		cond.Status = metav1.ConditionFalse
		cond.Reason = uddiv1alpha1.ReasonPoolNotReady
		cond.Message = fmt.Sprintf("pool %s has no status.id yet", policyPool.String())
	default:
		cond.Status = metav1.ConditionFalse
		cond.Reason = uddiv1alpha1.ReasonAddressesPending
		cond.Message = fmt.Sprintf("%d of %d addresses assigned", assigned, plan.Allocated)
	}
	meta.SetStatusCondition(&policy.Status.Conditions, cond)

	if err := r.Status().Update(ctx, policy); err != nil {
		if apierrors.IsConflict(err) {
			// Another writer won the race. The next pass sees the fresh object.
			log.V(1).Info("conflict while writing the IPAllocationPolicy status, requeueing")
			return ctrl.Result{RequeueAfter: uddisync.RequeueSoon}, nil
		}
		return ctrl.Result{}, err
	}

	if cond.Status != metav1.ConditionTrue {
		return ctrl.Result{RequeueAfter: requeueNotReady}, nil
	}
	// Ready: rely on watches (Services, owned Addresses and the pool kinds) rather than
	// polling.
	return ctrl.Result{}, nil
}

// event records a Kubernetes event when a Recorder is wired up, matching
// uddisync.Runtime's own nil-safe helper.
func (r *IPAllocationPolicyReconciler) event(obj client.Object, eventType, reason, format string, args ...any) {
	if r.Runtime == nil || r.Runtime.Recorder == nil {
		return
	}
	r.Runtime.Recorder.Eventf(obj, eventType, reason, format, args...)
}

// resolvePool reads the Subnet, Range or AddressBlock CR a PoolRef names in namespace
// and builds the ipalloc.PoolState the planner needs. A missing CR is Ready: false with
// a zero SpaceRef, per DESIGN.md section 4.7 step 3.
func (r *IPAllocationPolicyReconciler) resolvePool(ctx context.Context, namespace string, ref ipalloc.PoolRef) (ipalloc.PoolState, error) {
	key := client.ObjectKey{Namespace: namespace, Name: ref.Name}
	switch ref.Kind {
	case ipalloc.PoolSubnet:
		var s uddiv1alpha1.Subnet
		switch err := r.Get(ctx, key, &s); {
		case apierrors.IsNotFound(err):
			return ipalloc.PoolState{Ref: ref}, nil
		case err != nil:
			return ipalloc.PoolState{}, err
		default:
			return ipalloc.PoolState{Ref: ref, Ready: s.Status.ID != "", SpaceRef: s.Spec.SpaceRef}, nil
		}
	case ipalloc.PoolRange:
		var rg uddiv1alpha1.Range
		switch err := r.Get(ctx, key, &rg); {
		case apierrors.IsNotFound(err):
			return ipalloc.PoolState{Ref: ref}, nil
		case err != nil:
			return ipalloc.PoolState{}, err
		default:
			return ipalloc.PoolState{Ref: ref, Ready: rg.Status.ID != "", SpaceRef: rg.Spec.SpaceRef}, nil
		}
	case ipalloc.PoolAddressBlock:
		var b uddiv1alpha1.AddressBlock
		switch err := r.Get(ctx, key, &b); {
		case apierrors.IsNotFound(err):
			return ipalloc.PoolState{Ref: ref}, nil
		case err != nil:
			return ipalloc.PoolState{}, err
		default:
			return ipalloc.PoolState{Ref: ref, Ready: b.Status.ID != "", SpaceRef: b.Spec.SpaceRef}, nil
		}
	default:
		return ipalloc.PoolState{Ref: ref}, nil
	}
}

// serviceSelectorFor converts spec.selector to a labels.Selector. A nil selector means
// "every Service in the namespace", the opposite of what
// metav1.LabelSelectorAsSelector(nil) returns (labels.Nothing()), matching
// DNSPublishPolicyReconciler's own selectorFor.
func serviceSelectorFor(policy *uddiv1alpha1.IPAllocationPolicy) (labels.Selector, error) {
	if policy.Spec.Selector == nil {
		return labels.Everything(), nil
	}
	return metav1.LabelSelectorAsSelector(policy.Spec.Selector)
}

// toSkippedServices converts already-sorted skips into the status field, capped at
// IPAllocationPolicyStatus.Skipped's own MaxItems.
func toSkippedServices(skips []ipalloc.Skip) []uddiv1alpha1.SkippedService {
	if len(skips) == 0 {
		return nil
	}
	if len(skips) > maxSkipped {
		skips = skips[:maxSkipped]
	}
	out := make([]uddiv1alpha1.SkippedService, 0, len(skips))
	for _, sk := range skips {
		out = append(out, uddiv1alpha1.SkippedService{
			Service: sk.Service.Name,
			Reason:  string(sk.Reason),
			Message: sk.Message,
		})
	}
	return out
}

// emitSkipEvents records one Warning "Skipped" event per distinct skip reason present
// this reconcile, with a count and up to three example Services, mirroring
// DNSPublishPolicyReconciler.emitEvents.
func (r *IPAllocationPolicyReconciler) emitSkipEvents(policy *uddiv1alpha1.IPAllocationPolicy, skips []ipalloc.Skip) {
	if len(skips) == 0 || r.Runtime == nil || r.Runtime.Recorder == nil {
		return
	}

	byReason := make(map[ipalloc.SkipReason][]ipalloc.Skip, len(skips))
	for _, sk := range skips {
		byReason[sk.Reason] = append(byReason[sk.Reason], sk)
	}
	reasons := make([]string, 0, len(byReason))
	for reason := range byReason {
		reasons = append(reasons, string(reason))
	}
	sort.Strings(reasons)
	for _, reasonStr := range reasons {
		reason := ipalloc.SkipReason(reasonStr)
		list := byReason[reason]
		examples := exampleServices(list, maxSkipExamples)
		r.Runtime.Recorder.Eventf(policy, corev1.EventTypeWarning, "Skipped",
			"%d service(s) skipped: %s (e.g. %s)", len(list), reason, strings.Join(examples, ", "))
	}
}

// exampleServices returns up to max distinct Service references from skips, in order.
func exampleServices(skips []ipalloc.Skip, max int) []string {
	seen := make(map[string]bool, max)
	out := make([]string, 0, max)
	for _, sk := range skips {
		s := sk.Service.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) == max {
			break
		}
	}
	return out
}

// enqueueNamespaceIPAllocationPolicies maps a changed Service onto every
// IPAllocationPolicy in its namespace: the policy's selector and annotations are not
// evaluated here, so a Service change simply wakes every policy in the namespace, which
// then decides for itself whether the Service is still relevant.
func enqueueNamespaceIPAllocationPolicies(c client.Client) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list uddiv1alpha1.IPAllocationPolicyList
		if err := c.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0, len(list.Items))
		for i := range list.Items {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
		return out
	}
}

// enqueueForAddressPolicy maps a changed Address carrying the policy label straight to
// the one IPAllocationPolicy that owns it, without a List call.
func enqueueForAddressPolicy() handler.MapFunc {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		policy := obj.GetLabels()[ipalloc.LabelPolicy]
		if policy == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: policy}}}
	}
}

// SetupWithManager registers the controller, the allocateFrom dependency index, and the
// watches DESIGN.md section 4.7 lists: Services and owned Addresses mapped to their
// policy, and the pool kinds (Subnet, Range, AddressBlock) through the dependency index.
func (r *IPAllocationPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()

	if err := RegisterDependencyIndex(ctx, mgr, &uddiv1alpha1.IPAllocationPolicy{}, func(o client.Object) []string {
		p := o.(*uddiv1alpha1.IPAllocationPolicy)
		af := p.Spec.AllocateFrom
		return RefNames(af.SubnetRef, af.RangeRef, af.AddressBlockRef)
	}); err != nil {
		return err
	}

	newPolicyList := func() client.ObjectList { return &uddiv1alpha1.IPAllocationPolicyList{} }

	return ctrl.NewControllerManagedBy(mgr).
		For(&uddiv1alpha1.IPAllocationPolicy{}).
		Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(enqueueNamespaceIPAllocationPolicies(mgr.GetClient()))).
		Watches(&uddiv1alpha1.Address{}, handler.EnqueueRequestsFromMapFunc(enqueueForAddressPolicy())).
		Watches(
			&uddiv1alpha1.Subnet{},
			handler.EnqueueRequestsFromMapFunc(EnqueueForIndex(mgr.GetClient(), IndexDependencyRefs, newPolicyList)),
		).
		Watches(
			&uddiv1alpha1.Range{},
			handler.EnqueueRequestsFromMapFunc(EnqueueForIndex(mgr.GetClient(), IndexDependencyRefs, newPolicyList)),
		).
		Watches(
			&uddiv1alpha1.AddressBlock{},
			handler.EnqueueRequestsFromMapFunc(EnqueueForIndex(mgr.GetClient(), IndexDependencyRefs, newPolicyList)),
		).
		Named("ipallocationpolicy").
		Complete(r)
}
