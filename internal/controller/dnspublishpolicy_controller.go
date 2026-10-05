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
	"time"

	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/publish"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
)

// requeueNotReady mirrors uddisync.RequeueNotReady: how long the loop waits before
// retrying while the policy is not Ready. Field indexes and watches usually wake the
// reconciler sooner; this is only a safety net.
const requeueNotReady = 30 * time.Second

// maxSkipped bounds DNSPublishPolicyStatus.Skipped, matching the CRD's own MaxItems.
const maxSkipped = 50

// maxSkipExamples is how many example sources a Skipped event names per reason.
const maxSkipExamples = 3

// DNSPublishPolicyReconciler turns a DNSPublishPolicy into the Records described by
// DESIGN.md section 4.7: it resolves the policy's zoneRefs and selected sources
// (Routes, Ingresses, Services), calls internal/publish.Plan, applies the resulting
// Records with server-side apply, prunes the ones the policy no longer wants, and
// reports status.
type DNSPublishPolicyReconciler struct {
	client.Client
	// Runtime carries the event recorder shared by every object-layer controller.
	// Its Client and Connection resolver are not used here: a DNSPublishPolicy never
	// talks to Universal DDI directly, only through the Records it manages.
	Runtime *uddisync.Runtime
	// RoutesAvailable reports whether the cluster serves the OpenShift Route API
	// (internal/routeapi.Available, checked once at manager startup). When false the
	// Route source is ignored rather than erroring.
	RoutesAvailable bool
}

// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=dnspublishpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=dnspublishpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=dnspublishpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=records,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=authzones,verbs=get;list;watch
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile brings the Records owned by one DNSPublishPolicy into agreement with its
// selected sources, per DESIGN.md section 4.7.
//
//nolint:gocyclo // the loop is a single linear pipeline; splitting it hides the order.
func (r *DNSPublishPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	policy := &uddiv1alpha1.DNSPublishPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !policy.GetDeletionTimestamp().IsZero() {
		// The policy carries no finalizer: owned Records are garbage-collected by
		// Kubernetes through their controller ownerReference, and each Record's own
		// finalizer removes its Universal DDI counterpart.
		return ctrl.Result{}, nil
	}

	// Step 2: zones.
	zones, err := r.resolveZones(ctx, policy)
	if err != nil {
		return ctrl.Result{}, err
	}
	anyZoneReady := false
	for _, z := range zones {
		if z.Ready {
			anyZoneReady = true
			break
		}
	}

	// Step 3: sources.
	sel, err := selectorFor(policy)
	if err != nil {
		// A malformed selector cannot be fixed by retrying.
		log.Error(err, "invalid spec.selector")
		return ctrl.Result{}, nil
	}
	opts := publish.SourceOptions{
		Mode:              publish.TargetMode(policy.Spec.Target.Mode),
		StaticAddresses:   policy.Spec.Target.Addresses,
		CanonicalHostname: policy.Spec.Target.CanonicalHostname,
	}
	endpoints, skips, err := r.collectEndpoints(ctx, policy, opts, sel)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Steps 4-5: plan, then apply and prune.
	desired, planSkips := publish.Plan(publish.PlanInput{Policy: policy, Endpoints: endpoints, Zones: zones})
	skips = append(skips, planSkips...)
	sortSkips(skips)

	// The Records this policy currently owns, read before applying so pruning and the
	// Published event both compare against the same "before" snapshot.
	var existing uddiv1alpha1.RecordList
	if err := r.List(ctx, &existing, client.InNamespace(policy.Namespace),
		client.MatchingLabels{publish.LabelPolicy: policy.Name}); err != nil {
		return ctrl.Result{}, err
	}
	priorNames := make(map[string]bool, len(existing.Items))
	for _, it := range existing.Items {
		priorNames[it.Name] = true
	}

	desiredNames := make(map[string]bool, len(desired))
	for _, dr := range desired {
		desiredNames[dr.Name] = true

		rec := desiredRecordObject(policy, dr)
		if err := controllerutil.SetControllerReference(policy, rec, r.Scheme()); err != nil {
			return ctrl.Result{}, err
		}
		//nolint:staticcheck // client.Apply is the typed server-side apply Patch; see DESIGN.md 4.7 step 4.
		if err := r.Patch(ctx, rec, client.Apply, client.FieldOwner(publish.FieldManager), client.ForceOwnership); err != nil {
			return ctrl.Result{}, fmt.Errorf("applying Record %s: %w", dr.Name, err)
		}
	}

	// Prune: every Record this policy owns but no longer wants.
	for i := range existing.Items {
		item := &existing.Items[i]
		if desiredNames[item.Name] {
			continue
		}
		if err := r.Delete(ctx, item); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	// Step 5 continued: ready is computed from the Records as they stand now, after
	// apply and prune, so a Ready flip that happened between our List above and now
	// (RecordReconciler racing us) is not missed.
	readyCount := 0
	for _, dr := range desired {
		rec := &uddiv1alpha1.Record{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: policy.Namespace, Name: dr.Name}, rec); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return ctrl.Result{}, err
		}
		if cond := meta.FindStatusCondition(rec.Status.Conditions, uddiv1alpha1.ConditionReady); cond != nil && cond.Status == metav1.ConditionTrue {
			readyCount++
		}
	}

	r.emitEvents(policy, skips, priorNames, desiredNames, len(desired))

	// Step 6: status.
	hasZoneNotReadySkip := false
	for _, sk := range skips {
		if sk.Reason == publish.SkipZoneNotReady {
			hasZoneNotReadySkip = true
			break
		}
	}

	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.Published = int32(len(desired))
	policy.Status.Ready = int32(readyCount)
	policy.Status.Skipped = toSkippedSources(skips)

	cond := metav1.Condition{Type: uddiv1alpha1.ConditionReady, ObservedGeneration: policy.Generation}
	switch {
	case !anyZoneReady:
		cond.Status = metav1.ConditionFalse
		cond.Reason = uddiv1alpha1.ReasonZonesNotReady
		cond.Message = "no zoneRef resolves to a Ready AuthZone"
	case readyCount == len(desired) && !hasZoneNotReadySkip:
		cond.Status = metav1.ConditionTrue
		cond.Reason = uddiv1alpha1.ReasonPublished
		cond.Message = fmt.Sprintf("%d of %d records ready", readyCount, len(desired))
	default:
		cond.Status = metav1.ConditionFalse
		cond.Reason = uddiv1alpha1.ReasonRecordsNotReady
		cond.Message = fmt.Sprintf("%d of %d records ready", readyCount, len(desired))
	}
	meta.SetStatusCondition(&policy.Status.Conditions, cond)

	if err := r.Status().Update(ctx, policy); err != nil {
		if apierrors.IsConflict(err) {
			// Another writer (most likely RecordReconciler touching a different
			// field) won the race. The next pass sees the fresh object.
			log.V(1).Info("conflict while writing the DNSPublishPolicy status, requeueing")
			return ctrl.Result{RequeueAfter: uddisync.RequeueSoon}, nil
		}
		return ctrl.Result{}, err
	}

	if cond.Status != metav1.ConditionTrue {
		return ctrl.Result{RequeueAfter: requeueNotReady}, nil
	}
	// Ready: rely on watches (owned Records, the sources, and the zoneRefs) rather
	// than polling.
	return ctrl.Result{}, nil
}

// selectorFor converts spec.selector to a labels.Selector. Per DESIGN.md 4.7 and the
// field's own doc comment, a nil selector means "every object of the configured
// source kinds", which is the opposite of what metav1.LabelSelectorAsSelector(nil)
// returns (labels.Nothing()), so the nil case is special-cased here; a non-nil but
// empty selector already means "everything" in the standard semantics.
func selectorFor(policy *uddiv1alpha1.DNSPublishPolicy) (labels.Selector, error) {
	if policy.Spec.Selector == nil {
		return labels.Everything(), nil
	}
	return metav1.LabelSelectorAsSelector(policy.Spec.Selector)
}

// resolveZones builds the publish.Zone list DESIGN.md 4.7 step 2 describes: one entry
// per spec.zoneRefs[].name, in order. A zoneRef whose AuthZone CR does not exist
// yields a Zone with no FQDN (so it can never match a hostname) and Ready false; any
// other read error is returned so the caller retries with backoff.
func (r *DNSPublishPolicyReconciler) resolveZones(ctx context.Context, policy *uddiv1alpha1.DNSPublishPolicy) ([]publish.Zone, error) {
	log := logf.FromContext(ctx)
	zones := make([]publish.Zone, 0, len(policy.Spec.ZoneRefs))
	for _, ref := range policy.Spec.ZoneRefs {
		if ref.Name == "" {
			continue
		}
		zone := &uddiv1alpha1.AuthZone{}
		err := r.Get(ctx, client.ObjectKey{Namespace: policy.Namespace, Name: ref.Name}, zone)
		switch {
		case apierrors.IsNotFound(err):
			log.V(1).Info("AuthZone not found", "zone", ref.Name)
			zones = append(zones, publish.Zone{Name: ref.Name, Ready: false})
		case err != nil:
			return nil, err
		default:
			zones = append(zones, publish.Zone{
				Name:  zone.Name,
				FQDN:  publish.CanonicalHostname(zone.Spec.FQDN),
				Ready: zone.Status.ID != "",
			})
		}
	}
	return zones, nil
}

// collectEndpoints lists every selected source kind in the policy's namespace and
// turns each matching object into Endpoints and Skips, per DESIGN.md 4.7 step 1 and
// step 3's "Sources" paragraph. Routes are silently ignored (logged once at V(1))
// when RoutesAvailable is false.
func (r *DNSPublishPolicyReconciler) collectEndpoints(
	ctx context.Context,
	policy *uddiv1alpha1.DNSPublishPolicy,
	opts publish.SourceOptions,
	sel labels.Selector,
) ([]publish.Endpoint, []publish.Skip, error) {
	kinds := policy.Spec.Sources
	if len(kinds) == 0 {
		kinds = []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindRoute, uddiv1alpha1.SourceKindIngress, uddiv1alpha1.SourceKindService}
	}
	listOpts := []client.ListOption{client.InNamespace(policy.Namespace), client.MatchingLabelsSelector{Selector: sel}}

	var endpoints []publish.Endpoint
	var skips []publish.Skip

	for _, kind := range kinds {
		switch kind {
		case uddiv1alpha1.SourceKindRoute:
			if !r.RoutesAvailable {
				logf.FromContext(ctx).V(1).Info("ignoring Route source: the Route API is not available on this cluster")
				continue
			}
			var list routev1.RouteList
			if err := r.List(ctx, &list, listOpts...); err != nil {
				return nil, nil, err
			}
			for i := range list.Items {
				eps, sk := publish.EndpointsFromRoute(&list.Items[i], opts)
				endpoints = append(endpoints, eps...)
				skips = append(skips, sk...)
			}
		case uddiv1alpha1.SourceKindIngress:
			var list networkingv1.IngressList
			if err := r.List(ctx, &list, listOpts...); err != nil {
				return nil, nil, err
			}
			for i := range list.Items {
				eps, sk := publish.EndpointsFromIngress(&list.Items[i], opts)
				endpoints = append(endpoints, eps...)
				skips = append(skips, sk...)
			}
		case uddiv1alpha1.SourceKindService:
			var list corev1.ServiceList
			if err := r.List(ctx, &list, listOpts...); err != nil {
				return nil, nil, err
			}
			for i := range list.Items {
				eps, sk := publish.EndpointsFromService(&list.Items[i], opts)
				endpoints = append(endpoints, eps...)
				skips = append(skips, sk...)
			}
		}
	}
	return endpoints, skips, nil
}

// desiredRecordObject builds the typed Record CR a DesiredRecord applies as. TypeMeta
// is set explicitly (apiVersion/kind), and resourceVersion and managedFields are left
// zero, as server-side apply requires: the object is a pure declaration of intent, not
// a read-modify-write of the current one.
func desiredRecordObject(policy *uddiv1alpha1.DNSPublishPolicy, dr publish.DesiredRecord) *uddiv1alpha1.Record {
	return &uddiv1alpha1.Record{
		TypeMeta: metav1.TypeMeta{
			APIVersion: uddiv1alpha1.GroupVersion.String(),
			Kind:       "Record",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        dr.Name,
			Namespace:   policy.Namespace,
			Labels:      dr.Labels,
			Annotations: dr.Annotations,
		},
		Spec: dr.Spec,
	}
}

// sortSkips orders skips by source then hostname, matching the order
// internal/publish.Plan already uses for the skips it returns, so combining skips
// from the source extractors with Plan's own skips yields one consistently sorted
// list.
func sortSkips(skips []publish.Skip) {
	sort.Slice(skips, func(i, j int) bool {
		si, sj := skips[i].Source.String(), skips[j].Source.String()
		if si != sj {
			return si < sj
		}
		return skips[i].Hostname < skips[j].Hostname
	})
}

// toSkippedSources converts already-sorted skips into the status field, capped at
// DNSPublishPolicyStatus.Skipped's own MaxItems.
func toSkippedSources(skips []publish.Skip) []uddiv1alpha1.SkippedSource {
	if len(skips) == 0 {
		return nil
	}
	if len(skips) > maxSkipped {
		skips = skips[:maxSkipped]
	}
	out := make([]uddiv1alpha1.SkippedSource, 0, len(skips))
	for _, sk := range skips {
		out = append(out, uddiv1alpha1.SkippedSource{
			Source:   sk.Source.String(),
			Hostname: sk.Hostname,
			Reason:   string(sk.Reason),
			Message:  sk.Message,
		})
	}
	return out
}

// emitEvents records one Warning "Skipped" event per distinct skip reason (with the
// count and up to three example sources) and a Normal "Published" event whenever the
// applied Record set differs from the one this policy owned before this reconcile.
func (r *DNSPublishPolicyReconciler) emitEvents(
	policy *uddiv1alpha1.DNSPublishPolicy,
	skips []publish.Skip,
	priorNames, desiredNames map[string]bool,
	desiredCount int,
) {
	if r.Runtime == nil || r.Runtime.Recorder == nil {
		return
	}

	byReason := make(map[publish.SkipReason][]publish.Skip, len(skips))
	for _, sk := range skips {
		byReason[sk.Reason] = append(byReason[sk.Reason], sk)
	}
	reasons := make([]string, 0, len(byReason))
	for reason := range byReason {
		reasons = append(reasons, string(reason))
	}
	sort.Strings(reasons)
	for _, reasonStr := range reasons {
		reason := publish.SkipReason(reasonStr)
		list := byReason[reason]
		examples := exampleSources(list, maxSkipExamples)
		r.Runtime.Recorder.Eventf(policy, corev1.EventTypeWarning, "Skipped",
			"%d source(s) skipped: %s (e.g. %s)", len(list), reason, strings.Join(examples, ", "))
	}

	if setsDiffer(priorNames, desiredNames) {
		r.Runtime.Recorder.Eventf(policy, corev1.EventTypeNormal, "Published", "published %d record(s)", desiredCount)
	}
}

// exampleSources returns up to max distinct source references from skips, in order.
func exampleSources(skips []publish.Skip, max int) []string {
	seen := make(map[string]bool, max)
	out := make([]string, 0, max)
	for _, sk := range skips {
		s := sk.Source.String()
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

// setsDiffer reports whether two name sets differ.
func setsDiffer(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return true
	}
	for k := range a {
		if !b[k] {
			return true
		}
	}
	return false
}

// enqueueNamespacePolicies maps a changed source object (Route, Ingress or Service)
// onto every DNSPublishPolicy in its namespace: the policy's selector is not
// evaluated here (that would need re-running the same matching logic the reconciler
// already does), so an object change simply wakes every policy in the namespace,
// which then decides for itself whether the object is still relevant.
func enqueueNamespacePolicies(c client.Client) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list uddiv1alpha1.DNSPublishPolicyList
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

// SetupWithManager registers the controller, the AuthZone dependency index, and the
// watches DESIGN.md 4.7 lists: owned Records, Ingresses and Services mapped to every
// policy in their namespace, Routes the same way when RoutesAvailable, and AuthZones
// through the zoneRefs dependency index.
func (r *DNSPublishPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()

	if err := RegisterDependencyIndex(ctx, mgr, &uddiv1alpha1.DNSPublishPolicy{}, func(o client.Object) []string {
		p := o.(*uddiv1alpha1.DNSPublishPolicy)
		refs := make([]*uddiv1alpha1.ObjectRef, 0, len(p.Spec.ZoneRefs))
		for i := range p.Spec.ZoneRefs {
			refs = append(refs, &p.Spec.ZoneRefs[i])
		}
		return RefNames(refs...)
	}); err != nil {
		return err
	}

	newPolicyList := func() client.ObjectList { return &uddiv1alpha1.DNSPublishPolicyList{} }
	toNamespacePolicies := handler.EnqueueRequestsFromMapFunc(enqueueNamespacePolicies(mgr.GetClient()))

	bldr := ctrl.NewControllerManagedBy(mgr).
		For(&uddiv1alpha1.DNSPublishPolicy{}).
		Owns(&uddiv1alpha1.Record{}).
		Watches(&networkingv1.Ingress{}, toNamespacePolicies).
		Watches(&corev1.Service{}, toNamespacePolicies).
		Watches(
			&uddiv1alpha1.AuthZone{},
			handler.EnqueueRequestsFromMapFunc(EnqueueForIndex(mgr.GetClient(), IndexDependencyRefs, newPolicyList)),
		)

	if r.RoutesAvailable {
		bldr = bldr.Watches(&routev1.Route{}, toNamespacePolicies)
	}

	return bldr.Named("dnspublishpolicy").Complete(r)
}
