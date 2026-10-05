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
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	uddiapply "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1/applyconfiguration/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/egressip"
	"github.com/IngmarVG-IB/openshiftoperator/internal/ipalloc"
	"github.com/IngmarVG-IB/openshiftoperator/internal/metallb"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
)

const (
	// MetalLBPoolsFinalizer is set on an IPAllocationPolicy while it mirrors its pools into
	// MetalLB IPAddressPools. Those live in another namespace and cannot be owned by the
	// policy, so the finalizer deletes them when the policy is deleted or mirroring is
	// turned off.
	MetalLBPoolsFinalizer = "uddi.infoblox.com/metallb-pools"
	// MetalLBFieldManager is the server-side apply field manager for mirrored IPAddressPools.
	MetalLBFieldManager = "uddi-operator/ipallocationpolicy-metallb"
)

// IPAllocationPolicyReconciler turns an IPAllocationPolicy into the Addresses and
// assignments described by DESIGN.md section 4.7: it views the selected Services and
// EgressIPs through internal/ipalloc, calls ipalloc.Compute, applies the resulting
// Addresses with server-side apply, writes the allocated addresses back onto their
// targets, releases allocations that are no longer wanted, mirrors the pools it uses into
// MetalLB when asked to, and reports status.
type IPAllocationPolicyReconciler struct {
	client.Client
	// Runtime carries the event recorder shared by every object-layer controller. Its
	// Client and Connection resolver are not used here: an IPAllocationPolicy never
	// talks to Universal DDI directly, only through the Addresses it manages.
	Runtime *uddisync.Runtime
	// EgressIPAvailable reports whether the cluster serves the k8s.ovn.org/v1 EgressIP API
	// (egressip.Available, checked once at manager startup). When false the EgressIP
	// target is inert: no EgressIP is listed or watched, Addresses already allocated for
	// EgressIPs are left alone, and a policy listing the target reports it in
	// status.skipped.
	EgressIPAvailable bool
	// MetalLBAvailable reports whether the cluster serves the metallb.io/v1beta1
	// IPAddressPool API (metallb.Available, checked once at manager startup). When false
	// spec.metallb.mirrorPools is inert and reported in status.skipped, and the policy never
	// keeps MetalLBPoolsFinalizer, so deleting it never waits.
	MetalLBAvailable bool
}

// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ipallocationpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ipallocationpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ipallocationpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=addresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=subnets,verbs=get;list;watch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=ranges,verbs=get;list;watch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=addressblocks,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=k8s.ovn.org,resources=egressips,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=metallb.io,resources=ipaddresspools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile brings the Addresses and assignments owned by one IPAllocationPolicy into
// agreement with its selected Services and EgressIPs, and its mirrored MetalLB pools into
// agreement with the pools it uses, per DESIGN.md section 4.7.
//
//nolint:gocyclo // the loop is a single linear pipeline; splitting it hides the order.
func (r *IPAllocationPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	policy := &uddiv1alpha1.IPAllocationPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !policy.GetDeletionTimestamp().IsZero() {
		// Addresses are owned by their Services and EgressIPs (not the policy) and keep
		// working, and the targets keep whatever addresses they were already assigned.
		// Only the mirrored MetalLB pools go, through the finalizer.
		return ctrl.Result{}, r.unmirrorPools(ctx, policy)
	}

	via := ipalloc.AssignVia{
		Mode:       ipalloc.AssignMode(policy.Spec.AssignVia.Mode),
		Annotation: policy.Spec.AssignVia.Annotation,
	}

	// Step 1: claims, from the Services in the policy namespace and, when the policy lists
	// the EgressIP target and the cluster serves it, the EgressIPs cluster-wide; both
	// matched against spec.selector.
	sel, err := serviceSelectorFor(policy)
	if err != nil {
		// A malformed selector cannot be fixed by retrying.
		log.Error(err, "invalid spec.selector")
		return ctrl.Result{}, nil
	}
	var claims []ipalloc.Claim
	svcByName := map[string]*corev1.Service{}
	if ipalloc.TargetEnabled(policy, ipalloc.TargetService) {
		var svcList corev1.ServiceList
		if err := r.List(ctx, &svcList, client.InNamespace(policy.Namespace), client.MatchingLabelsSelector{Selector: sel}); err != nil {
			return ctrl.Result{}, err
		}
		for i := range svcList.Items {
			svc := &svcList.Items[i]
			svcByName[svc.Name] = svc
			claims = append(claims, ipalloc.ViewService(svc, via, ""))
		}
	}
	eipByName := map[string]*unstructured.Unstructured{}
	var apiSkips []policySkip
	if ipalloc.TargetEnabled(policy, ipalloc.TargetEgressIP) {
		if r.EgressIPAvailable {
			eipList := egressip.NewList()
			if err := r.List(ctx, eipList, client.MatchingLabelsSelector{Selector: sel}); err != nil {
				return ctrl.Result{}, err
			}
			for i := range eipList.Items {
				eip := &eipList.Items[i]
				// Patches and event references need the kind, which list items need not carry.
				eip.SetGroupVersionKind(egressip.GVK)
				eipByName[eip.GetName()] = eip
				claims = append(claims, ipalloc.ViewEgressIP(eip, ""))
			}
		} else {
			apiSkips = append(apiSkips, policySkip{
				Name:   egressip.GVK.Kind,
				Reason: uddiv1alpha1.SkipReasonAPIUnavailable,
				Message: fmt.Sprintf("the cluster does not serve %s %s; the EgressIP target is inert",
					egressip.GVK.GroupVersion(), egressip.GVK.Kind),
			})
		}
	}

	// Step 2: Addresses this policy already owns.
	var addrList uddiv1alpha1.AddressList
	if err := r.List(ctx, &addrList, client.InNamespace(policy.Namespace),
		client.MatchingLabels{ipalloc.LabelPolicy: policy.Name}); err != nil {
		return ctrl.Result{}, err
	}
	liveByName := make(map[string]*uddiv1alpha1.Address, len(addrList.Items))
	existing := make([]ipalloc.AddressState, 0, len(addrList.Items))
	for i := range addrList.Items {
		a := &addrList.Items[i]
		liveByName[a.Name] = a
		existing = append(existing, ipalloc.AddressStateOf(a))
	}

	// Step 3: pools. allocateFrom, allocateFromIPv6, every distinct allocate-from override
	// and the pools existing Addresses were allocated from, each with its family.
	poolRefs := ipalloc.PoolRefs(policy, claims, existing)
	resolved := make(map[ipalloc.PoolRef]resolvedPool, len(poolRefs))
	pools := make(map[ipalloc.PoolRef]ipalloc.PoolState, len(poolRefs))
	for _, ref := range poolRefs {
		rp, err := r.resolvePool(ctx, policy.Namespace, ref)
		if err != nil {
			return ctrl.Result{}, err
		}
		resolved[ref] = rp
		pools[ref] = rp.state
	}

	// Step 4: plan.
	plan := ipalloc.Compute(ipalloc.PlanInput{Policy: policy, Claims: claims, Pools: pools, Existing: existing})
	if !r.EgressIPAvailable {
		// No EgressIP was listed, so the planner reads every EgressIP Address as
		// abandoned; an inert target leaves them alone instead.
		withoutEgressIPReleases(&plan)
	}

	// Step 5: Ensure.
	for _, da := range plan.Ensure {
		related, owner, err := r.addressOwner(da.Target, svcByName, eipByName)
		if err != nil {
			return ctrl.Result{}, err
		}
		if owner == nil {
			// The target disappeared between the List above and now; the next
			// reconcile (woken by the delete) releases it instead.
			continue
		}
		live := liveByName[da.Name]
		if live != nil {
			keepAllocatedFields(&da.Spec, live)
		}
		addr, err := desiredAddressApplyConfiguration(policy, da, owner)
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Apply(ctx, addr, client.FieldOwner(ipalloc.FieldManager), client.ForceOwnership); err != nil {
			return ctrl.Result{}, fmt.Errorf("applying Address %s: %w", da.Name, err)
		}
		if live == nil {
			r.Runtime.Eventf(policy, related, corev1.EventTypeNormal, "Allocated", uddisync.ActionAllocate,
				"allocated Address %s for %s %s", da.Name, da.Target.Kind, da.Target.Name)
		}
	}

	// Step 6: Assign, once every slot of a claim is Ready.
	for _, as := range plan.Assign {
		var related client.Object
		switch as.Target.Kind {
		case ipalloc.TargetService:
			svc, ok := svcByName[as.Target.Name]
			if !ok {
				continue
			}
			if err := r.Patch(ctx, svc, client.RawPatch(types.MergePatchType, ipalloc.AssignmentPatch(via, as.Value()))); err != nil {
				return ctrl.Result{}, err
			}
			related = svc
		case ipalloc.TargetEgressIP:
			eip, ok := eipByName[as.Target.Name]
			if !ok {
				continue
			}
			if err := r.Patch(ctx, eip, client.RawPatch(types.MergePatchType, ipalloc.EgressIPsPatch(as.Values))); err != nil {
				return ctrl.Result{}, err
			}
			related = eip
		default:
			continue
		}
		r.Runtime.Eventf(policy, related, corev1.EventTypeNormal, "Assigned", uddisync.ActionAssign,
			"assigned %s to %s %s", as.Value(), as.Target.Kind, as.Target.Name)
	}

	// Step 7: release. First clear the assignment of every target released as a whole,
	// if it still holds only its own addresses, then delete the Addresses.
	for _, un := range plan.Unassign {
		if err := r.unassign(ctx, policy.Namespace, via, un); err != nil {
			return ctrl.Result{}, err
		}
	}
	for _, rel := range plan.Release {
		addr := &uddiv1alpha1.Address{ObjectMeta: metav1.ObjectMeta{Name: rel.AddressName, Namespace: policy.Namespace}}
		if err := r.Delete(ctx, addr); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		r.Runtime.Eventf(policy, nil, corev1.EventTypeNormal, "Released", uddisync.ActionRelease,
			"released Address %s for %s %s", rel.AddressName, rel.Target.Kind, rel.Target.Name)
	}

	// Step 8: MetalLB pool mirroring.
	mirrorSkips, err := r.mirrorPools(ctx, policy, poolRefs, resolved)
	if err != nil {
		return ctrl.Result{}, err
	}

	skips := make([]policySkip, 0, len(plan.Skips)+len(apiSkips)+len(mirrorSkips))
	skips = append(skips, planSkips(plan.Skips)...)
	skips = append(skips, apiSkips...)
	skips = append(skips, mirrorSkips...)
	r.emitSkipEvents(policy, skips)

	// Step 9: status. plan.Assigned only counts claims whose assignment field already
	// matched their Addresses before this reconcile; plan.Assign is exactly the claims
	// just written in step 6 above, so they are assigned too by the time this status is
	// written. The skips added in this controller (APIUnavailable and the MetalLB ones)
	// do not change Ready.
	assigned := plan.Assigned + int32(len(plan.Assign))

	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.Allocated = plan.Allocated
	policy.Status.Assigned = assigned
	policy.Status.Skipped = toSkippedServices(skips)

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
	// Ready: rely on watches (Services, EgressIPs, owned Addresses and the pool kinds)
	// rather than polling.
	return ctrl.Result{}, nil
}

// addressOwner returns the target a DesiredAddress is generated for, as the related
// object of its events, and the controller ownerReference the Address carries: the
// Service, or the EgressIP, whose reference is built by hand from egressip.GVK because
// EgressIPs are read as unstructured data. The owner is nil when the target is not among
// the listed ones.
func (r *IPAllocationPolicyReconciler) addressOwner(
	t ipalloc.TargetRef,
	svcByName map[string]*corev1.Service,
	eipByName map[string]*unstructured.Unstructured,
) (client.Object, *metav1ac.OwnerReferenceApplyConfiguration, error) {
	switch t.Kind {
	case ipalloc.TargetService:
		svc, ok := svcByName[t.Name]
		if !ok {
			return nil, nil, nil
		}
		owner, err := controllerOwnerReference(svc, r.Scheme(), false)
		if err != nil {
			return nil, nil, err
		}
		return svc, owner, nil
	case ipalloc.TargetEgressIP:
		eip, ok := eipByName[t.Name]
		if !ok {
			return nil, nil, nil
		}
		return eip, egressIPOwnerReference(eip.GetName(), eip.GetUID()), nil
	default:
		return nil, nil, nil
	}
}

// egressIPOwnerReference is the controller ownerReference of an Address generated for a
// slot of the named EgressIP, so Kubernetes garbage-collects it with the EgressIP. A
// namespaced Address may name the cluster-scoped EgressIP as its owner.
func egressIPOwnerReference(name string, uid types.UID) *metav1ac.OwnerReferenceApplyConfiguration {
	return controllerReference(egressip.GVK.GroupVersion().String(), egressip.GVK.Kind, name, uid, false)
}

// desiredAddressApplyConfiguration builds the server-side apply configuration a
// DesiredAddress is applied as: name, namespace, labels, annotations, owner (the
// controller reference to the Service or EgressIP the slot belongs to) and the spec. It
// is a pure declaration of intent, so only these fields end up owned by the policy's
// field manager.
func desiredAddressApplyConfiguration(
	policy *uddiv1alpha1.IPAllocationPolicy,
	da ipalloc.DesiredAddress,
	owner *metav1ac.OwnerReferenceApplyConfiguration,
) (*uddiapply.AddressApplyConfiguration, error) {
	spec, err := specApplyConfiguration[uddiapply.AddressSpecApplyConfiguration](da.Spec)
	if err != nil {
		return nil, err
	}
	return uddiapply.Address(da.Name, policy.Namespace).
		WithLabels(da.Labels).
		WithAnnotations(da.Annotations).
		WithOwnerReferences(owner).
		WithSpec(spec), nil
}

// keepAllocatedFields copies onto the desired spec of a slot whose Address already exists
// the fields that pin what it allocated: address and nextAvailableFrom, which the Address
// CRD makes immutable, and the spaceRef they live in. A later change of allocateFrom,
// allocateFromIPv6, allocate-from or the address annotation therefore only affects new
// slots (DESIGN.md section 4.7) instead of failing the apply; release and re-opt-in to
// move an allocated address.
func keepAllocatedFields(spec *uddiv1alpha1.AddressSpec, live *uddiv1alpha1.Address) {
	spec.Address = live.Spec.Address
	spec.NextAvailableFrom = live.Spec.NextAvailableFrom.DeepCopy()
	spec.SpaceRef = live.Spec.SpaceRef
}

// withoutEgressIPReleases drops the Unassigns and Releases of EgressIP targets from plan,
// for a cluster that does not serve the EgressIP API.
func withoutEgressIPReleases(plan *ipalloc.Plan) {
	unassign := plan.Unassign[:0]
	for _, un := range plan.Unassign {
		if un.Target.Kind != ipalloc.TargetEgressIP {
			unassign = append(unassign, un)
		}
	}
	plan.Unassign = unassign
	release := plan.Release[:0]
	for _, rel := range plan.Release {
		if rel.Target.Kind != ipalloc.TargetEgressIP {
			release = append(release, rel)
		}
	}
	plan.Release = release
}

// unassign clears the assignment field of a target whose whole claim is released, but
// only while it still holds nothing but the claim's addresses (Unassign.Applies): a
// Service's annotation or spec.loadBalancerIP, or an EgressIP's spec.egressIPs. A target
// that is gone has nothing left to clear, and a malformed spec.egressIPs is not this
// policy's to clear.
func (r *IPAllocationPolicyReconciler) unassign(ctx context.Context, namespace string, via ipalloc.AssignVia, un ipalloc.Unassign) error {
	switch un.Target.Kind {
	case ipalloc.TargetService:
		var svc corev1.Service
		err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: un.Target.Name}, &svc)
		switch {
		case apierrors.IsNotFound(err):
			return nil
		case err != nil:
			return err
		case !un.Applies(ipalloc.CurrentAssignmentValues(&svc, via)):
			return nil
		}
		return r.Patch(ctx, &svc, client.RawPatch(types.MergePatchType, ipalloc.ClearAssignmentPatch(via)))
	case ipalloc.TargetEgressIP:
		eip := egressip.New()
		err := r.Get(ctx, client.ObjectKey{Name: un.Target.Name}, eip)
		switch {
		case apierrors.IsNotFound(err):
			return nil
		case err != nil:
			return err
		}
		current, _, err := egressip.EgressIPs(eip)
		if err != nil || !un.Applies(current) {
			return nil
		}
		return r.Patch(ctx, eip, client.RawPatch(types.MergePatchType, ipalloc.ClearEgressIPsPatch()))
	default:
		return nil
	}
}

// resolvedPool is one pool CR as the reconcile needs it: the planner's view and, when the
// CR exists, the source its MetalLB mirror is built from.
type resolvedPool struct {
	state ipalloc.PoolState
	// mirror is nil when the pool CR does not exist.
	mirror *metallb.Source
}

// resolvePool reads the Subnet, Range or AddressBlock CR a PoolRef names in namespace and
// builds the ipalloc.PoolState the planner needs, family included, plus the
// metallb.Source of its mirror. A missing CR is Ready: false with a zero SpaceRef, per
// DESIGN.md section 4.7 step 3, and has no mirror source.
func (r *IPAllocationPolicyReconciler) resolvePool(ctx context.Context, namespace string, ref ipalloc.PoolRef) (resolvedPool, error) {
	key := client.ObjectKey{Namespace: namespace, Name: ref.Name}
	missing := resolvedPool{state: ipalloc.PoolState{Ref: ref}}
	src := &metallb.Source{PoolKind: string(ref.Kind), PoolName: ref.Name}
	switch ref.Kind {
	case ipalloc.PoolSubnet:
		var s uddiv1alpha1.Subnet
		if err := r.Get(ctx, key, &s); err != nil {
			if apierrors.IsNotFound(err) {
				return missing, nil
			}
			return resolvedPool{}, err
		}
		src.CIDR = poolCIDR(s.Status.Address, s.Status.CIDR, s.Spec.Address, s.Spec.CIDR)
		return resolvedPool{state: ipalloc.SubnetPoolState(&s), mirror: src}, nil
	case ipalloc.PoolRange:
		var rg uddiv1alpha1.Range
		if err := r.Get(ctx, key, &rg); err != nil {
			if apierrors.IsNotFound(err) {
				return missing, nil
			}
			return resolvedPool{}, err
		}
		src.Start, src.End = rg.Spec.Start, rg.Spec.End
		if src.Start == "" || src.End == "" {
			src.Start, src.End = rg.Status.Start, rg.Status.End
		}
		return resolvedPool{state: ipalloc.RangePoolState(&rg), mirror: src}, nil
	case ipalloc.PoolAddressBlock:
		var b uddiv1alpha1.AddressBlock
		if err := r.Get(ctx, key, &b); err != nil {
			if apierrors.IsNotFound(err) {
				return missing, nil
			}
			return resolvedPool{}, err
		}
		src.CIDR = poolCIDR(b.Status.Address, b.Status.CIDR, b.Spec.Address, b.Spec.CIDR)
		return resolvedPool{state: ipalloc.AddressBlockPoolState(&b), mirror: src}, nil
	default:
		return missing, nil
	}
}

// poolCIDR renders a Subnet's or AddressBlock's network as "<address>/<prefix length>":
// from status once Universal DDI reported it, otherwise from spec; empty while neither
// carries an address (a Subnet still waiting for its next-available allocation).
func poolCIDR(statusAddress string, statusCIDR int32, specAddress string, specCIDR int32) string {
	switch {
	case statusAddress != "" && statusCIDR > 0:
		return fmt.Sprintf("%s/%d", statusAddress, statusCIDR)
	case specAddress != "":
		return fmt.Sprintf("%s/%d", specAddress, specCIDR)
	default:
		return ""
	}
}

// mirrorPools implements DESIGN.md section 4.7 "MetalLB pool mirroring" for one reconcile
// and returns the pools it could not mirror as skips. While spec.metallb.mirrorPools is
// true and the cluster serves MetalLB, the policy keeps MetalLBPoolsFinalizer and every
// pool in refs is mirrored into an IPAddressPool in spec.metallb.namespace by server-side
// apply, skipping writes the live pool already matches and leaving alone a pool of the
// same name that the policy did not create. Then the policy's mirrored pools that are no
// longer wanted are pruned. A pool still in refs keeps an existing mirror even while it
// cannot be rebuilt, so a transient problem never withdraws an announced range.
func (r *IPAllocationPolicyReconciler) mirrorPools(
	ctx context.Context,
	policy *uddiv1alpha1.IPAllocationPolicy,
	refs []ipalloc.PoolRef,
	pools map[ipalloc.PoolRef]resolvedPool,
) ([]policySkip, error) {
	if policy.Spec.MetalLB == nil || !policy.Spec.MetalLB.MirrorPools {
		return nil, r.unmirrorPools(ctx, policy)
	}
	if !r.MetalLBAvailable {
		if err := r.setMetalLBFinalizer(ctx, policy, false); err != nil {
			return nil, err
		}
		return []policySkip{{
			Name:   metallb.GVK.Kind,
			Reason: uddiv1alpha1.SkipReasonAPIUnavailable,
			Message: fmt.Sprintf("the cluster does not serve %s %s; spec.metallb.mirrorPools is inert",
				metallb.GVK.GroupVersion(), metallb.GVK.Kind),
		}}, nil
	}
	if err := r.setMetalLBFinalizer(ctx, policy, true); err != nil {
		return nil, err
	}

	mirrorNS := policy.Spec.MetalLB.Namespace
	if mirrorNS == "" {
		mirrorNS = uddiv1alpha1.DefaultMetalLBNamespace
	}
	keep := make(map[string]bool, len(refs))
	var skips []policySkip
	for _, ref := range refs {
		keep[metallb.PoolName(policy.Namespace, policy.Name, string(ref.Kind), ref.Name)] = true
		src := pools[ref].mirror
		if src == nil {
			// The pool CR does not exist; PoolNotReady reports it where it matters.
			continue
		}
		skip, err := r.mirrorPool(ctx, policy, mirrorNS, ref, *src)
		if err != nil {
			return nil, err
		}
		if skip != nil {
			skips = append(skips, *skip)
		}
	}
	return skips, r.pruneMirroredPools(ctx, policy, mirrorNS, keep)
}

// mirrorPool creates or updates the IPAddressPool mirroring one pool, returning a skip
// when the pool cannot be built or the name is taken by a pool the policy did not create.
func (r *IPAllocationPolicyReconciler) mirrorPool(
	ctx context.Context,
	policy *uddiv1alpha1.IPAllocationPolicy,
	mirrorNS string,
	ref ipalloc.PoolRef,
	src metallb.Source,
) (*policySkip, error) {
	entry := string(ref.Kind) + "/" + ref.Name
	desired, err := metallb.Desired(policy.Namespace, policy.Name, mirrorNS, src)
	if err != nil {
		return &policySkip{Name: entry, Reason: uddiv1alpha1.SkipReasonMetalLBPoolInvalid, Message: err.Error()}, nil
	}
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(metallb.GVK)
	switch err := r.Get(ctx, client.ObjectKeyFromObject(desired), live); {
	case apierrors.IsNotFound(err):
	case err != nil:
		return nil, err
	case !metallb.OwnedBy(live, policy.Namespace, policy.Name):
		return &policySkip{
			Name:   entry,
			Reason: uddiv1alpha1.SkipReasonMetalLBPoolConflict,
			Message: fmt.Sprintf("IPAddressPool %s/%s already exists without this policy's labels and is left alone",
				mirrorNS, desired.GetName()),
		}, nil
	case metallb.Equal(desired, live):
		return nil, nil
	}
	if err := r.Apply(ctx, client.ApplyConfigurationFromUnstructured(desired),
		client.FieldOwner(MetalLBFieldManager), client.ForceOwnership); err != nil {
		return nil, fmt.Errorf("applying IPAddressPool %s/%s: %w", mirrorNS, desired.GetName(), err)
	}
	return nil, nil
}

// pruneMirroredPools deletes the policy's mirrored pools that are no longer wanted: those
// in mirrorNS that metallb.Stale returns for keep, and every one in another namespace,
// left behind when spec.metallb.namespace changed. An empty mirrorNS with a nil keep
// deletes them all. Pools are listed cluster-wide with metallb.Selector, so a pool
// somebody else created is never touched.
func (r *IPAllocationPolicyReconciler) pruneMirroredPools(
	ctx context.Context,
	policy *uddiv1alpha1.IPAllocationPolicy,
	mirrorNS string,
	keep map[string]bool,
) error {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(metallb.ListGVK)
	if err := r.List(ctx, list, client.MatchingLabelsSelector{Selector: metallb.Selector(policy.Namespace, policy.Name)}); err != nil {
		return err
	}
	var inMirror []unstructured.Unstructured
	var stale []client.ObjectKey
	for i := range list.Items {
		pool := &list.Items[i]
		switch {
		case pool.GetNamespace() == mirrorNS:
			inMirror = append(inMirror, *pool)
		case metallb.OwnedBy(pool, policy.Namespace, policy.Name):
			stale = append(stale, client.ObjectKeyFromObject(pool))
		}
	}
	for _, name := range metallb.Stale(policy.Namespace, policy.Name, inMirror, keep) {
		stale = append(stale, client.ObjectKey{Namespace: mirrorNS, Name: name})
	}
	for _, key := range stale {
		pool := &unstructured.Unstructured{}
		pool.SetGroupVersionKind(metallb.GVK)
		pool.SetNamespace(key.Namespace)
		pool.SetName(key.Name)
		if err := r.Delete(ctx, pool); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting IPAddressPool %s: %w", key, err)
		}
	}
	return nil
}

// unmirrorPools deletes every pool the policy mirrored and removes MetalLBPoolsFinalizer,
// when mirroring is turned off or the policy is deleted. A policy without the finalizer
// has nothing mirrored. When the cluster does not serve MetalLB there is nothing to list,
// so only the finalizer goes and deletion never hangs.
func (r *IPAllocationPolicyReconciler) unmirrorPools(ctx context.Context, policy *uddiv1alpha1.IPAllocationPolicy) error {
	if !controllerutil.ContainsFinalizer(policy, MetalLBPoolsFinalizer) {
		return nil
	}
	if r.MetalLBAvailable {
		if err := r.pruneMirroredPools(ctx, policy, "", nil); err != nil {
			return err
		}
	}
	return r.setMetalLBFinalizer(ctx, policy, false)
}

// setMetalLBFinalizer adds or removes MetalLBPoolsFinalizer, patching only when that
// changes something.
func (r *IPAllocationPolicyReconciler) setMetalLBFinalizer(ctx context.Context, policy *uddiv1alpha1.IPAllocationPolicy, want bool) error {
	if controllerutil.ContainsFinalizer(policy, MetalLBPoolsFinalizer) == want {
		return nil
	}
	patch := client.MergeFromWithOptions(policy.DeepCopy(), client.MergeFromWithOptimisticLock{})
	if want {
		controllerutil.AddFinalizer(policy, MetalLBPoolsFinalizer)
	} else {
		controllerutil.RemoveFinalizer(policy, MetalLBPoolsFinalizer)
	}
	return r.Patch(ctx, policy, patch)
}

// serviceSelectorFor converts spec.selector to a labels.Selector, which selects both the
// Services and the EgressIPs a policy considers. A nil selector means "every one", the
// opposite of what metav1.LabelSelectorAsSelector(nil) returns (labels.Nothing()),
// matching DNSPublishPolicyReconciler's own selectorFor.
func serviceSelectorFor(policy *uddiv1alpha1.IPAllocationPolicy) (labels.Selector, error) {
	if policy.Spec.Selector == nil {
		return labels.Everything(), nil
	}
	return metav1.LabelSelectorAsSelector(policy.Spec.Selector)
}

// policySkip is one entry of status.skipped and of the Skipped events: a claim the
// planner skipped, an inert target or mirroring, or a pool that cannot be mirrored.
type policySkip struct {
	// Name is the status.skipped service value: a Service's name, "EgressIP/<name>",
	// "<pool kind>/<pool name>", or a bare kind for an API the cluster does not serve.
	Name    string
	Reason  string
	Message string
}

// planSkips converts the planner's skips. A Service is named by its name alone, as
// before milestone 7; other targets by "<kind>/<name>".
func planSkips(skips []ipalloc.Skip) []policySkip {
	out := make([]policySkip, 0, len(skips))
	for _, sk := range skips {
		name := sk.Target.Name
		if sk.Target.Kind != ipalloc.TargetService {
			name = sk.Target.String()
		}
		out = append(out, policySkip{Name: name, Reason: string(sk.Reason), Message: sk.Message})
	}
	return out
}

// sortedSkips returns skips sorted by name, reason, then message.
func sortedSkips(skips []policySkip) []policySkip {
	out := append([]policySkip(nil), skips...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		return a.Message < b.Message
	})
	return out
}

// toSkippedServices converts skips into the status field, sorted by service then reason
// and capped at IPAllocationPolicyStatus.Skipped's own MaxItems.
func toSkippedServices(skips []policySkip) []uddiv1alpha1.SkippedService {
	if len(skips) == 0 {
		return nil
	}
	sorted := sortedSkips(skips)
	if len(sorted) > maxSkipped {
		sorted = sorted[:maxSkipped]
	}
	out := make([]uddiv1alpha1.SkippedService, 0, len(sorted))
	for _, sk := range sorted {
		out = append(out, uddiv1alpha1.SkippedService{
			Service: sk.Name,
			Reason:  sk.Reason,
			Message: sk.Message,
		})
	}
	return out
}

// emitSkipEvents records one Warning "Skipped" event per distinct skip reason present
// this reconcile, with a count and up to three examples, mirroring
// DNSPublishPolicyReconciler.emitEvents.
func (r *IPAllocationPolicyReconciler) emitSkipEvents(policy *uddiv1alpha1.IPAllocationPolicy, skips []policySkip) {
	if len(skips) == 0 || r.Runtime == nil || r.Runtime.Recorder == nil {
		return
	}

	byReason := make(map[string][]policySkip, len(skips))
	for _, sk := range sortedSkips(skips) {
		byReason[sk.Reason] = append(byReason[sk.Reason], sk)
	}
	reasons := make([]string, 0, len(byReason))
	for reason := range byReason {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		list := byReason[reason]
		examples := exampleSkips(list, maxSkipExamples)
		r.Runtime.Eventf(policy, nil, corev1.EventTypeWarning, "Skipped", uddisync.ActionAllocate,
			"%d skipped: %s (e.g. %s)", len(list), reason, strings.Join(examples, ", "))
	}
}

// exampleSkips returns up to max distinct skip names from skips, in order.
func exampleSkips(skips []policySkip, max int) []string {
	seen := make(map[string]bool, max)
	out := make([]string, 0, max)
	for _, sk := range skips {
		if seen[sk.Name] {
			continue
		}
		seen[sk.Name] = true
		out = append(out, sk.Name)
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

// enqueueEgressIPPolicies maps a changed EgressIP onto every IPAllocationPolicy in the
// cluster whose spec.targets lists EgressIP: EgressIPs are cluster-scoped and the
// selector and annotations are not evaluated here, so each such policy decides for itself
// whether the EgressIP is still relevant.
func enqueueEgressIPPolicies(c client.Client) handler.MapFunc {
	return func(ctx context.Context, _ client.Object) []reconcile.Request {
		var list uddiv1alpha1.IPAllocationPolicyList
		if err := c.List(ctx, &list); err != nil {
			return nil
		}
		var out []reconcile.Request
		for i := range list.Items {
			if ipalloc.TargetEnabled(&list.Items[i], ipalloc.TargetEgressIP) {
				out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
			}
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

// SetupWithManager registers the controller, the dependency index on allocateFrom and
// allocateFromIPv6, and the watches DESIGN.md section 4.7 lists: Services and owned
// Addresses mapped to their policy, EgressIPs (when the cluster serves them) mapped to
// every policy listing the EgressIP target, and the pool kinds (Subnet, Range,
// AddressBlock) through the dependency index. Mirrored MetalLB pools are not watched:
// drift repair and pruning run on every reconcile.
func (r *IPAllocationPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()

	if err := RegisterDependencyIndex(ctx, mgr, &uddiv1alpha1.IPAllocationPolicy{}, func(o client.Object) []string {
		p := o.(*uddiv1alpha1.IPAllocationPolicy)
		af := p.Spec.AllocateFrom
		refs := []*uddiv1alpha1.ObjectRef{af.SubnetRef, af.RangeRef, af.AddressBlockRef}
		if v6 := p.Spec.AllocateFromIPv6; v6 != nil {
			refs = append(refs, v6.SubnetRef, v6.RangeRef, v6.AddressBlockRef)
		}
		return RefNames(refs...)
	}); err != nil {
		return err
	}

	newPolicyList := func() client.ObjectList { return &uddiv1alpha1.IPAllocationPolicyList{} }

	bldr := ctrl.NewControllerManagedBy(mgr).
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
		)

	if r.EgressIPAvailable {
		bldr = bldr.Watches(egressip.New(), handler.EnqueueRequestsFromMapFunc(enqueueEgressIPPolicies(mgr.GetClient())))
	}

	return bldr.Named("ipallocationpolicy").Complete(r)
}
