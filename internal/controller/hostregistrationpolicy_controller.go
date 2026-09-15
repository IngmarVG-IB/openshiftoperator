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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/hostreg"
	"github.com/IngmarVG-IB/openshiftoperator/internal/nmstateapi"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
)

// HostRegistrationPolicyReconciler turns a HostRegistrationPolicy into the Addresses and
// FixedAddresses described by DESIGN.md section 4.7: it views the selected Nodes (and,
// when available, their NodeNetworkState) through internal/hostreg, calls
// hostreg.Compute, applies the resulting objects with server-side apply, releases
// registrations that are no longer wanted, and reports status.
type HostRegistrationPolicyReconciler struct {
	client.Client
	// Runtime carries the event recorder shared by every object-layer controller. Its
	// Client and Connection resolver are not used here: a HostRegistrationPolicy never
	// talks to Universal DDI directly, only through the Addresses and FixedAddresses it
	// manages.
	Runtime *uddisync.Runtime
	// NMStateAvailable reports whether the cluster serves the nmstate.io/v1beta1
	// NodeNetworkState API (nmstateapi.Available, checked once at manager startup).
	// When false, MAC resolution never reads NodeNetworkState even when a policy's
	// spec.macSource.nmstate.enabled is true.
	NMStateAvailable bool
}

// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=hostregistrationpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=hostregistrationpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=hostregistrationpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=addresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=fixedaddresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ipspaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=nmstate.io,resources=nodenetworkstates,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile brings the Addresses and FixedAddresses owned by one HostRegistrationPolicy
// into agreement with its selected Nodes, per DESIGN.md section 4.7.
//
//nolint:gocyclo // the loop is a single linear pipeline; splitting it hides the order.
func (r *HostRegistrationPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	policy := &uddiv1alpha1.HostRegistrationPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !policy.GetDeletionTimestamp().IsZero() {
		// No finalizer on the policy: generated objects are owned by their Nodes (not
		// the policy) and keep their ownership tags, so they survive the policy going
		// away and can be re-adopted later.
		return ctrl.Result{}, nil
	}

	// Step 2: space.
	space, err := r.resolveSpace(ctx, policy)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Step 3: MAC source.
	nmstateEnabled := true
	if e := policy.Spec.MACSource.NMState.Enabled; e != nil {
		nmstateEnabled = *e
	}
	src := hostreg.MACSource{
		NodeAnnotation:   policy.Spec.MACSource.NodeAnnotation,
		NMState:          nmstateEnabled,
		NMStateAvailable: r.NMStateAvailable,
		NMStateInterface: policy.Spec.MACSource.NMState.Interface,
	}

	addrType := hostreg.AddressType(policy.Spec.AddressType)
	if addrType == "" {
		addrType = hostreg.AddressTypeInternal
	}

	// Step 4: Nodes.
	sel, err := nodeSelectorFor(policy)
	if err != nil {
		// A malformed selector cannot be fixed by retrying.
		log.Error(err, "invalid spec.selector")
		return ctrl.Result{}, nil
	}
	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList, client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return ctrl.Result{}, err
	}
	views := make([]hostreg.NodeView, 0, len(nodeList.Items))
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		view := hostreg.ViewNode(node, addrType, src, "")
		if view.MAC == "" && src.NMState && src.NMStateAvailable {
			mac, ok, err := r.macFromNMState(ctx, node.Name, src.NMStateInterface, view.Address)
			if err != nil {
				return ctrl.Result{}, err
			}
			if ok {
				view.MAC = mac
				view.MACFrom = "nmstate"
			}
		}
		views = append(views, view)
	}

	// Step 5: existing registrations.
	existing, err := r.listExisting(ctx, policy)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Step 6: plan.
	plan := hostreg.Compute(hostreg.PlanInput{Policy: policy, Nodes: views, Space: space, Existing: existing})

	// Step 7: ensure.
	prior := make(map[registrationKey]bool, len(existing))
	for _, e := range existing {
		prior[registrationKey{e.Kind, e.Name}] = true
	}
	// created counts objects that did not exist before this reconcile. The planner only
	// counts registrations it could observe, so these are added to pending below; without
	// that the first reconcile would report Ready with nothing registered and flap.
	created := int32(0)
	for _, d := range plan.Ensure {
		obj, err := desiredRegistrationObject(policy, d)
		if err != nil {
			return ctrl.Result{}, err
		}
		//nolint:staticcheck // see PLAN.md
		if err := r.Patch(ctx, obj, client.Apply, client.FieldOwner(hostreg.FieldManager), client.ForceOwnership); err != nil {
			return ctrl.Result{}, fmt.Errorf("applying %s %s: %w", d.Kind, d.Name, err)
		}
		if !prior[registrationKey{d.Kind, d.Name}] {
			created++
			r.event(policy, corev1.EventTypeNormal, "Registered", "registered %s %s for Node %s", d.Kind, d.Name, d.Node.Name)
		}
	}

	// Step 8: release.
	for _, rel := range plan.Release {
		if err := r.deleteRegistration(ctx, policy.Namespace, rel); err != nil {
			return ctrl.Result{}, err
		}
		r.event(policy, corev1.EventTypeNormal, "Released", "released %s %s for Node %s", rel.Kind, rel.Name, rel.Node.Name)
	}

	r.emitSkipEvents(policy, plan.Skips)

	// Step 9: status.
	policy.Status.ObservedGeneration = policy.Generation
	pending := plan.Pending + created
	policy.Status.Registered = plan.Registered
	policy.Status.Pending = pending
	policy.Status.Skipped = toSkippedNodes(plan.Skips)

	cond := metav1.Condition{Type: uddiv1alpha1.ConditionReady, ObservedGeneration: policy.Generation}
	switch {
	case !space.Ready:
		cond.Status = metav1.ConditionFalse
		cond.Reason = uddiv1alpha1.ReasonSpaceNotReady
		cond.Message = fmt.Sprintf("space %s has no status.id yet", space.Name)
	case pending > 0:
		cond.Status = metav1.ConditionFalse
		cond.Reason = uddiv1alpha1.ReasonRegistrationsPending
		cond.Message = fmt.Sprintf("%d pending", pending)
	default:
		cond.Status = metav1.ConditionTrue
		cond.Reason = uddiv1alpha1.ReasonRegistered
		cond.Message = fmt.Sprintf("%d registered", plan.Registered)
	}
	meta.SetStatusCondition(&policy.Status.Conditions, cond)

	if err := r.Status().Update(ctx, policy); err != nil {
		if apierrors.IsConflict(err) {
			// Another writer won the race. The next pass sees the fresh object.
			log.V(1).Info("conflict while writing the HostRegistrationPolicy status, requeueing")
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	if cond.Status != metav1.ConditionTrue {
		return ctrl.Result{RequeueAfter: requeueNotReady}, nil
	}
	// Ready: rely on watches (Nodes, NodeNetworkStates, owned Addresses/FixedAddresses
	// and the IPSpace) rather than polling.
	return ctrl.Result{}, nil
}

// event records a Kubernetes event when a Recorder is wired up, matching
// uddisync.Runtime's own nil-safe helper.
func (r *HostRegistrationPolicyReconciler) event(obj client.Object, eventType, reason, format string, args ...any) {
	if r.Runtime == nil || r.Runtime.Recorder == nil {
		return
	}
	r.Runtime.Recorder.Eventf(obj, eventType, reason, format, args...)
}

// resolveSpace reads the IPSpace CR spec.spaceRef.name names in the policy's namespace
// and builds the hostreg.SpaceState the planner needs. A missing CR is Ready: false,
// per DESIGN.md section 4.7 step 2.
func (r *HostRegistrationPolicyReconciler) resolveSpace(ctx context.Context, policy *uddiv1alpha1.HostRegistrationPolicy) (hostreg.SpaceState, error) {
	name := policy.Spec.SpaceRef.Name
	var space uddiv1alpha1.IPSpace
	switch err := r.Get(ctx, client.ObjectKey{Namespace: policy.Namespace, Name: name}, &space); {
	case apierrors.IsNotFound(err):
		return hostreg.SpaceState{Name: name}, nil
	case err != nil:
		return hostreg.SpaceState{}, err
	default:
		return hostreg.SpaceState{Name: name, Ready: space.Status.ID != ""}, nil
	}
}

// macFromNMState reads the NodeNetworkState named after node (as unstructured data, per
// DESIGN.md 4.7 step 3) and extracts the MAC of iface, or of the interface carrying
// address when iface is empty. A missing NodeNetworkState is not an error: ok is simply
// false, and the caller's Node keeps whatever MAC (if any) it already had.
func (r *HostRegistrationPolicyReconciler) macFromNMState(ctx context.Context, nodeName, iface, address string) (mac string, ok bool, err error) {
	nns := &unstructured.Unstructured{}
	nns.SetGroupVersionKind(nmstateapi.GVK)
	switch getErr := r.Get(ctx, client.ObjectKey{Name: nodeName}, nns); {
	case apierrors.IsNotFound(getErr):
		return "", false, nil
	case getErr != nil:
		return "", false, getErr
	}

	cs, ok := nmstateapi.CurrentState(nns)
	if !ok {
		return "", false, nil
	}
	mac, _, ok = hostreg.MACFromNodeNetworkState(cs, iface, address)
	return mac, ok, nil
}

// listExisting lists the Addresses and FixedAddresses this policy already owns
// (carrying its LabelPolicy label) and builds the hostreg.RegistrationState list the
// planner needs, per DESIGN.md section 4.7 step 5.
func (r *HostRegistrationPolicyReconciler) listExisting(ctx context.Context, policy *uddiv1alpha1.HostRegistrationPolicy) ([]hostreg.RegistrationState, error) {
	var addrList uddiv1alpha1.AddressList
	if err := r.List(ctx, &addrList, client.InNamespace(policy.Namespace),
		client.MatchingLabels{hostreg.LabelPolicy: policy.Name}); err != nil {
		return nil, err
	}
	var fixedList uddiv1alpha1.FixedAddressList
	if err := r.List(ctx, &fixedList, client.InNamespace(policy.Namespace),
		client.MatchingLabels{hostreg.LabelPolicy: policy.Name}); err != nil {
		return nil, err
	}

	out := make([]hostreg.RegistrationState, 0, len(addrList.Items)+len(fixedList.Items))
	for i := range addrList.Items {
		a := &addrList.Items[i]
		out = append(out, registrationStateOf(a.Name, hostreg.KindAddress, a.Labels[hostreg.LabelNode], a.Status.Conditions))
	}
	for i := range fixedList.Items {
		f := &fixedList.Items[i]
		out = append(out, registrationStateOf(f.Name, hostreg.KindFixedAddress, f.Labels[hostreg.LabelNode], f.Status.Conditions))
	}
	return out, nil
}

// registrationStateOf builds one hostreg.RegistrationState from a generated object's
// name, kind, node label and Ready condition.
func registrationStateOf(name string, kind hostreg.RegistrationKind, node string, conditions []metav1.Condition) hostreg.RegistrationState {
	st := hostreg.RegistrationState{Name: name, Kind: kind, Node: node}
	if cond := meta.FindStatusCondition(conditions, uddiv1alpha1.ConditionReady); cond != nil {
		st.Ready = cond.Status == metav1.ConditionTrue
		if !st.Ready {
			st.Reason = cond.Reason
			st.Message = cond.Message
		}
	}
	return st
}

// registrationKey identifies one generated object by kind and name, used to tell
// whether an Ensure entry already existed before this reconcile (kind matters because
// releasing an Address and ensuring a FixedAddress under the same name is a kind
// change, not a no-op).
type registrationKey struct {
	Kind hostreg.RegistrationKind
	Name string
}

// desiredRegistrationObject builds the typed Address or FixedAddress CR a
// DesiredRegistration applies as. TypeMeta is set explicitly, and resourceVersion and
// managedFields are left zero, as server-side apply requires. The ownerReference points
// at the Node by hand (APIVersion "v1", Kind "Node") rather than through
// controllerutil.SetControllerReference: a cluster-scoped Node owning a namespaced
// Address or FixedAddress is a case Kubernetes garbage collection supports, per
// DESIGN.md section 4.7.
func desiredRegistrationObject(policy *uddiv1alpha1.HostRegistrationPolicy, d hostreg.DesiredRegistration) (client.Object, error) {
	owner := nodeOwnerReference(d.Node)
	objMeta := metav1.ObjectMeta{
		Name:            d.Name,
		Namespace:       policy.Namespace,
		Labels:          d.Labels,
		Annotations:     d.Annotations,
		OwnerReferences: []metav1.OwnerReference{owner},
	}

	switch d.Kind {
	case hostreg.KindFixedAddress:
		if d.FixedAddressSpec == nil {
			return nil, fmt.Errorf("desired FixedAddress %s has no spec", d.Name)
		}
		return &uddiv1alpha1.FixedAddress{
			TypeMeta:   metav1.TypeMeta{APIVersion: uddiv1alpha1.GroupVersion.String(), Kind: string(hostreg.KindFixedAddress)},
			ObjectMeta: objMeta,
			Spec:       *d.FixedAddressSpec,
		}, nil
	case hostreg.KindAddress:
		if d.AddressSpec == nil {
			return nil, fmt.Errorf("desired Address %s has no spec", d.Name)
		}
		return &uddiv1alpha1.Address{
			TypeMeta:   metav1.TypeMeta{APIVersion: uddiv1alpha1.GroupVersion.String(), Kind: string(hostreg.KindAddress)},
			ObjectMeta: objMeta,
			Spec:       *d.AddressSpec,
		}, nil
	default:
		return nil, fmt.Errorf("unknown registration kind %q", d.Kind)
	}
}

// nodeOwnerReference builds a controller ownerReference to the cluster-scoped Node a
// generated Address or FixedAddress belongs to.
func nodeOwnerReference(node hostreg.NodeRef) metav1.OwnerReference {
	isController := true
	blockOwnerDeletion := true
	return metav1.OwnerReference{
		APIVersion:         "v1",
		Kind:               "Node",
		Name:               node.Name,
		UID:                node.UID,
		Controller:         &isController,
		BlockOwnerDeletion: &blockOwnerDeletion,
	}
}

// deleteRegistration deletes the Address or FixedAddress a Release names, ignoring
// NotFound.
func (r *HostRegistrationPolicyReconciler) deleteRegistration(ctx context.Context, namespace string, rel hostreg.Release) error {
	var obj client.Object
	switch rel.Kind {
	case hostreg.KindFixedAddress:
		obj = &uddiv1alpha1.FixedAddress{ObjectMeta: metav1.ObjectMeta{Name: rel.Name, Namespace: namespace}}
	case hostreg.KindAddress:
		obj = &uddiv1alpha1.Address{ObjectMeta: metav1.ObjectMeta{Name: rel.Name, Namespace: namespace}}
	default:
		return fmt.Errorf("unknown registration kind %q", rel.Kind)
	}
	if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// nodeSelectorFor converts spec.selector to a labels.Selector. A nil selector means
// "every Node in the cluster", the opposite of what metav1.LabelSelectorAsSelector(nil)
// returns (labels.Nothing()), matching serviceSelectorFor and selectorFor.
func nodeSelectorFor(policy *uddiv1alpha1.HostRegistrationPolicy) (labels.Selector, error) {
	if policy.Spec.Selector == nil {
		return labels.Everything(), nil
	}
	return metav1.LabelSelectorAsSelector(policy.Spec.Selector)
}

// toSkippedNodes converts already-sorted skips into the status field, capped at
// HostRegistrationPolicyStatus.Skipped's own MaxItems.
func toSkippedNodes(skips []hostreg.Skip) []uddiv1alpha1.SkippedNode {
	if len(skips) == 0 {
		return nil
	}
	if len(skips) > maxSkipped {
		skips = skips[:maxSkipped]
	}
	out := make([]uddiv1alpha1.SkippedNode, 0, len(skips))
	for _, sk := range skips {
		out = append(out, uddiv1alpha1.SkippedNode{
			Node:    sk.Node.Name,
			Reason:  string(sk.Reason),
			Message: sk.Message,
		})
	}
	return out
}

// emitSkipEvents records one Warning "Skipped" event per distinct skip reason present
// this reconcile, with a count and up to three example Nodes, mirroring
// IPAllocationPolicyReconciler.emitSkipEvents.
func (r *HostRegistrationPolicyReconciler) emitSkipEvents(policy *uddiv1alpha1.HostRegistrationPolicy, skips []hostreg.Skip) {
	if len(skips) == 0 || r.Runtime == nil || r.Runtime.Recorder == nil {
		return
	}

	byReason := make(map[hostreg.SkipReason][]hostreg.Skip, len(skips))
	for _, sk := range skips {
		byReason[sk.Reason] = append(byReason[sk.Reason], sk)
	}
	reasons := make([]string, 0, len(byReason))
	for reason := range byReason {
		reasons = append(reasons, string(reason))
	}
	sort.Strings(reasons)
	for _, reasonStr := range reasons {
		reason := hostreg.SkipReason(reasonStr)
		list := byReason[reason]
		examples := exampleNodes(list, maxSkipExamples)
		r.Runtime.Recorder.Eventf(policy, corev1.EventTypeWarning, "Skipped",
			"%d node(s) skipped: %s (e.g. %s)", len(list), reason, strings.Join(examples, ", "))
	}
}

// exampleNodes returns up to max distinct Node references from skips, in order.
func exampleNodes(skips []hostreg.Skip, max int) []string {
	seen := make(map[string]bool, max)
	out := make([]string, 0, max)
	for _, sk := range skips {
		s := sk.Node.String()
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

// enqueueClusterHostRegistrationPolicies maps a changed cluster-scoped object (a Node,
// or a NodeNetworkState) onto every HostRegistrationPolicy in the cluster: Nodes carry
// no namespace to match against, and a policy's selector is not evaluated here, so the
// change simply wakes every policy, which then decides for itself whether the Node is
// still relevant.
func enqueueClusterHostRegistrationPolicies(c client.Client) handler.MapFunc {
	return func(ctx context.Context, _ client.Object) []reconcile.Request {
		var list uddiv1alpha1.HostRegistrationPolicyList
		if err := c.List(ctx, &list); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0, len(list.Items))
		for i := range list.Items {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
		return out
	}
}

// enqueueForHostRegistrationPolicyLabel maps a changed Address or FixedAddress carrying
// the policy label straight to the one HostRegistrationPolicy that owns it, without a
// List call.
func enqueueForHostRegistrationPolicyLabel() handler.MapFunc {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		policy := obj.GetLabels()[hostreg.LabelPolicy]
		if policy == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: policy}}}
	}
}

// SetupWithManager registers the controller, the spaceRef dependency index, and the
// watches DESIGN.md section 4.7 lists: Nodes and (when the API is present)
// NodeNetworkStates mapped to every HostRegistrationPolicy in the cluster, owned
// Addresses and FixedAddresses mapped to their policy, and the IPSpace through the
// dependency index.
func (r *HostRegistrationPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()

	if err := RegisterDependencyIndex(ctx, mgr, &uddiv1alpha1.HostRegistrationPolicy{}, func(o client.Object) []string {
		p := o.(*uddiv1alpha1.HostRegistrationPolicy)
		return RefNames(&p.Spec.SpaceRef)
	}); err != nil {
		return err
	}

	newPolicyList := func() client.ObjectList { return &uddiv1alpha1.HostRegistrationPolicyList{} }
	toClusterPolicies := handler.EnqueueRequestsFromMapFunc(enqueueClusterHostRegistrationPolicies(mgr.GetClient()))
	toOwningPolicy := handler.EnqueueRequestsFromMapFunc(enqueueForHostRegistrationPolicyLabel())

	bldr := ctrl.NewControllerManagedBy(mgr).
		For(&uddiv1alpha1.HostRegistrationPolicy{}).
		Watches(&corev1.Node{}, toClusterPolicies).
		Watches(&uddiv1alpha1.Address{}, toOwningPolicy).
		Watches(&uddiv1alpha1.FixedAddress{}, toOwningPolicy).
		Watches(
			&uddiv1alpha1.IPSpace{},
			handler.EnqueueRequestsFromMapFunc(EnqueueForIndex(mgr.GetClient(), IndexDependencyRefs, newPolicyList)),
		)

	if r.NMStateAvailable {
		nns := &unstructured.Unstructured{}
		nns.SetGroupVersionKind(nmstateapi.GVK)
		bldr = bldr.Watches(nns, toClusterPolicies)
	}

	return bldr.Named("hostregistrationpolicy").Complete(r)
}
