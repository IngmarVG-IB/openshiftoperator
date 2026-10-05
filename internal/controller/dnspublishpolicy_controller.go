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
	"slices"
	"sort"
	"strings"
	"time"

	operatoringressv1 "github.com/openshift/api/operatoringress/v1"
	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	uddiapply "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1/applyconfiguration/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/gatewayapi"
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
// (Routes, Ingresses, Services, Nodes, DNSRecords, HTTPRoutes), calls
// internal/publish.Plan, applies the resulting Records with server-side apply, prunes the
// ones the policy no longer wants, and reports status.
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
	// DNSRecordsAvailable reports whether the cluster serves the OpenShift ingress
	// operator DNSRecord API (internal/ingressapi.Available, checked once at manager
	// startup). When false the DNSRecord source is ignored rather than erroring.
	DNSRecordsAvailable bool
	// GatewayAPIAvailable reports whether the cluster serves the Gateway API Gateway and
	// HTTPRoute kinds (internal/gatewayapi.Available, checked once at manager startup).
	// When false the HTTPRoute source is ignored rather than erroring.
	GatewayAPIAvailable bool
}

// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=dnspublishpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=dnspublishpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=dnspublishpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=records,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=authzones,verbs=get;list;watch
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=ingress.operator.openshift.io,resources=dnsrecords,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes;gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

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
	zones, missingZones, err := r.resolveZones(ctx, policy)
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
		ExternalDNS:       policy.Spec.Compatibility.ExternalDNS,
		NodeAddressType:   string(policy.Spec.NodeAddressType),
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
	existingByName := make(map[string]*uddiv1alpha1.Record, len(existing.Items))
	for i := range existing.Items {
		priorNames[existing.Items[i].Name] = true
		existingByName[existing.Items[i].Name] = &existing.Items[i]
	}

	desiredNames := make(map[string]bool, len(desired))
	replacing := make(map[string]bool)
	for _, dr := range desired {
		desiredNames[dr.Name] = true

		// A Record's zoneRef and name cannot change in place. When a desired Record keeps its
		// name but lands in another zone (a zoneRef moved from an AuthZone name to a zone id,
		// or a longer zone now matches), delete the old one and let the owned-Record watch
		// create the replacement once it is gone, instead of failing the apply forever.
		if cur := existingByName[dr.Name]; cur != nil && recordPlacementDiffers(cur, dr) {
			log.Info("replacing a Record whose zone or name changed", "record", dr.Name)
			replacing[dr.Name] = true
			if cur.GetDeletionTimestamp().IsZero() {
				if err := r.Delete(ctx, cur); err != nil && !apierrors.IsNotFound(err) {
					return ctrl.Result{}, err
				}
			}
			continue
		}

		rec, err := desiredRecordApplyConfiguration(policy, dr, r.Scheme())
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Apply(ctx, rec, client.FieldOwner(publish.FieldManager), client.ForceOwnership); err != nil {
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
		if replacing[dr.Name] {
			continue
		}
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

	r.emitEvents(policy, skips, missingZones, priorNames, desiredNames, len(desired))

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
	// ZonesResolved is reported next to Ready and never feeds into it.
	meta.SetStatusCondition(&policy.Status.Conditions, zonesResolvedCondition(missingZones, policy.Generation))

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
// per spec.zoneRefs entry, in order. A zoneRef given by id (id plus fqdn) is Ready and
// resolved as it stands, with its FQDN taken from the spec and no read at all. A zoneRef
// naming an AuthZone CR that does not exist yields a Zone with no FQDN (so it can never
// match a hostname) and Ready false, and its name is returned in missing, sorted and
// without duplicates, for the ZonesResolved condition. Any other read error is returned
// so the caller retries with backoff.
func (r *DNSPublishPolicyReconciler) resolveZones(ctx context.Context, policy *uddiv1alpha1.DNSPublishPolicy) (zones []publish.Zone, missing []string, err error) {
	log := logf.FromContext(ctx)
	zones = make([]publish.Zone, 0, len(policy.Spec.ZoneRefs))
	seenMissing := map[string]bool{}
	for _, ref := range policy.Spec.ZoneRefs {
		if ref.ByID() {
			zones = append(zones, publish.Zone{
				ID:    ref.ID,
				FQDN:  publish.CanonicalHostname(ref.FQDN),
				Ready: true,
			})
			continue
		}
		if ref.Name == "" {
			continue
		}
		zone := &uddiv1alpha1.AuthZone{}
		gerr := r.Get(ctx, client.ObjectKey{Namespace: policy.Namespace, Name: ref.Name}, zone)
		switch {
		case apierrors.IsNotFound(gerr):
			log.V(1).Info("AuthZone not found", "zone", ref.Name)
			zones = append(zones, publish.Zone{Name: ref.Name, Ready: false})
			if !seenMissing[ref.Name] {
				seenMissing[ref.Name] = true
				missing = append(missing, ref.Name)
			}
		case gerr != nil:
			return nil, nil, gerr
		default:
			zones = append(zones, publish.Zone{
				Name:  zone.Name,
				FQDN:  publish.CanonicalHostname(zone.Spec.FQDN),
				Ready: zone.Status.ID != "",
			})
		}
	}
	sort.Strings(missing)
	return zones, missing, nil
}

// zonesResolvedCondition builds the ZonesResolved condition from the sorted names of
// the zoneRefs that name no existing AuthZone CR.
func zonesResolvedCondition(missing []string, generation int64) metav1.Condition {
	if len(missing) == 0 {
		return metav1.Condition{
			Type:               uddiv1alpha1.ConditionZonesResolved,
			Status:             metav1.ConditionTrue,
			Reason:             uddiv1alpha1.ReasonZonesFound,
			Message:            "every zoneRef names an existing AuthZone",
			ObservedGeneration: generation,
		}
	}
	return metav1.Condition{
		Type:               uddiv1alpha1.ConditionZonesResolved,
		Status:             metav1.ConditionFalse,
		Reason:             uddiv1alpha1.ReasonZoneNotFound,
		Message:            zonesNotFoundMessage(missing),
		ObservedGeneration: generation,
	}
}

// zonesNotFoundMessage is the ZonesResolved=False message and the ZoneNotFound event
// text.
func zonesNotFoundMessage(missing []string) string {
	return "zoneRefs not found: " + strings.Join(missing, ", ")
}

// collectEndpoints lists every selected source kind and turns each matching object
// into Endpoints and Skips, per DESIGN.md 4.7 step 1 and step 3's "Sources" paragraph.
// Routes, Ingresses, Services and HTTPRoutes are read from the policy's namespace, Nodes
// cluster-wide, and DNSRecords from spec.dnsRecordNamespace. A source whose API the
// cluster does not serve (RoutesAvailable, DNSRecordsAvailable or GatewayAPIAvailable
// false) is silently ignored and logged at V(1).
//
//nolint:gocyclo // one flat switch over the source kinds; splitting it hides the list.
func (r *DNSPublishPolicyReconciler) collectEndpoints(
	ctx context.Context,
	policy *uddiv1alpha1.DNSPublishPolicy,
	opts publish.SourceOptions,
	sel labels.Selector,
) ([]publish.Endpoint, []publish.Skip, error) {
	log := logf.FromContext(ctx)
	kinds := policy.Spec.Sources
	if len(kinds) == 0 {
		kinds = []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindRoute, uddiv1alpha1.SourceKindIngress, uddiv1alpha1.SourceKindService}
	}
	selector := client.MatchingLabelsSelector{Selector: sel}
	listOpts := []client.ListOption{client.InNamespace(policy.Namespace), selector}

	var endpoints []publish.Endpoint
	var skips []publish.Skip

	for _, kind := range kinds {
		switch kind {
		case uddiv1alpha1.SourceKindNode:
			// Nodes are cluster-scoped: no namespace option.
			var list corev1.NodeList
			if err := r.List(ctx, &list, selector); err != nil {
				return nil, nil, err
			}
			for i := range list.Items {
				eps, sk := publish.EndpointsFromNode(&list.Items[i], opts)
				endpoints = append(endpoints, eps...)
				skips = append(skips, sk...)
			}
		case uddiv1alpha1.SourceKindDNSRecord:
			if !r.DNSRecordsAvailable {
				log.V(1).Info("ignoring DNSRecord source: the ingress operator DNSRecord API is not available on this cluster")
				continue
			}
			var list operatoringressv1.DNSRecordList
			if err := r.List(ctx, &list, client.InNamespace(dnsRecordNamespace(policy)), selector); err != nil {
				return nil, nil, err
			}
			for i := range list.Items {
				eps, sk := publish.EndpointsFromDNSRecord(&list.Items[i], opts)
				endpoints = append(endpoints, eps...)
				skips = append(skips, sk...)
			}
		case uddiv1alpha1.SourceKindHTTPRoute:
			if !r.GatewayAPIAvailable {
				log.V(1).Info("ignoring HTTPRoute source: the Gateway API is not available on this cluster")
				continue
			}
			eps, sk, err := r.httpRouteEndpoints(ctx, listOpts, opts)
			if err != nil {
				return nil, nil, err
			}
			endpoints = append(endpoints, eps...)
			skips = append(skips, sk...)
		case uddiv1alpha1.SourceKindRoute:
			if !r.RoutesAvailable {
				log.V(1).Info("ignoring Route source: the Route API is not available on this cluster")
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

// httpRouteEndpoints lists the HTTPRoutes the policy selects (as unstructured data, per
// DESIGN.md 4.7, so the operator takes no Gateway API module dependency) and turns them
// into Endpoints and Skips. The Gateways each route's status.parents report as accepting
// it are read once each into the map EndpointsFromHTTPRoute resolves addresses from; a
// Gateway that no longer exists is simply absent from it and contributes no target.
func (r *DNSPublishPolicyReconciler) httpRouteEndpoints(
	ctx context.Context,
	listOpts []client.ListOption,
	opts publish.SourceOptions,
) ([]publish.Endpoint, []publish.Skip, error) {
	routes := &unstructured.UnstructuredList{}
	routes.SetGroupVersionKind(gatewayapi.HTTPRouteGVK.GroupVersion().WithKind(gatewayapi.HTTPRouteGVK.Kind + "List"))
	if err := r.List(ctx, routes, listOpts...); err != nil {
		return nil, nil, err
	}

	var endpoints []publish.Endpoint
	var skips []publish.Skip
	gateways := map[types.NamespacedName]*unstructured.Unstructured{}
	fetched := map[types.NamespacedName]bool{}
	for i := range routes.Items {
		route := &routes.Items[i]
		for _, key := range gatewayapi.AcceptedGateways(route) {
			if fetched[key] {
				continue
			}
			fetched[key] = true
			gw := &unstructured.Unstructured{}
			gw.SetGroupVersionKind(gatewayapi.GatewayGVK)
			switch err := r.Get(ctx, key, gw); {
			case apierrors.IsNotFound(err):
				logf.FromContext(ctx).V(1).Info("accepting Gateway not found", "gateway", key.String())
			case err != nil:
				return nil, nil, err
			default:
				gateways[key] = gw
			}
		}
		eps, sk := publish.EndpointsFromHTTPRoute(route, gateways, opts)
		endpoints = append(endpoints, eps...)
		skips = append(skips, sk...)
	}
	return endpoints, skips, nil
}

// dnsRecordNamespace is the namespace the DNSRecord source reads for policy: its
// spec.dnsRecordNamespace, else where the OpenShift ingress operator creates its records.
func dnsRecordNamespace(policy *uddiv1alpha1.DNSPublishPolicy) string {
	if ns := policy.Spec.DNSRecordNamespace; ns != "" {
		return ns
	}
	return uddiv1alpha1.DefaultDNSRecordNamespace
}

// recordPlacementDiffers reports whether the desired Record would put an existing Record
// in another zone or under another name, both of which a Record rejects changing.
func recordPlacementDiffers(cur *uddiv1alpha1.Record, dr publish.DesiredRecord) bool {
	return cur.Spec.ZoneRef != dr.Spec.ZoneRef || apexName(cur.Spec.Name) != apexName(dr.Spec.Name)
}

// apexName returns the name of a record in its zone with the omitted name spelled as the
// apex "@", which is how a Record treats it.
func apexName(name string) string {
	if name == "" {
		return "@"
	}
	return name
}

// desiredRecordApplyConfiguration builds the server-side apply configuration a
// DesiredRecord is applied as: name, namespace, labels, annotations, the policy as
// controller owner, and the spec. It is a pure declaration of intent, not a
// read-modify-write of the current object, so only these fields end up owned by the
// policy's field manager.
func desiredRecordApplyConfiguration(
	policy *uddiv1alpha1.DNSPublishPolicy,
	dr publish.DesiredRecord,
	scheme *runtime.Scheme,
) (*uddiapply.RecordApplyConfiguration, error) {
	spec, err := specApplyConfiguration[uddiapply.RecordSpecApplyConfiguration](dr.Spec)
	if err != nil {
		return nil, err
	}
	owner, err := controllerOwnerReference(policy, scheme, true)
	if err != nil {
		return nil, err
	}
	return uddiapply.Record(dr.Name, policy.Namespace).
		WithLabels(dr.Labels).
		WithAnnotations(dr.Annotations).
		WithOwnerReferences(owner).
		WithSpec(spec), nil
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

// emitEvents records one Warning "ZoneNotFound" event naming every zoneRef without an
// AuthZone CR, one Warning "Skipped" event per distinct skip reason (with the count and
// up to three example sources), and a Normal "Published" event whenever the applied
// Record set differs from the one this policy owned before this reconcile.
func (r *DNSPublishPolicyReconciler) emitEvents(
	policy *uddiv1alpha1.DNSPublishPolicy,
	skips []publish.Skip,
	missingZones []string,
	priorNames, desiredNames map[string]bool,
	desiredCount int,
) {
	if r.Runtime == nil || r.Runtime.Recorder == nil {
		return
	}

	if len(missingZones) > 0 {
		r.Runtime.Eventf(policy, nil, corev1.EventTypeWarning, uddiv1alpha1.ReasonZoneNotFound, uddisync.ActionPublish,
			"%s", zonesNotFoundMessage(missingZones))
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
		r.Runtime.Eventf(policy, nil, corev1.EventTypeWarning, "Skipped", uddisync.ActionPublish,
			"%d source(s) skipped: %s (e.g. %s)", len(list), reason, strings.Join(examples, ", "))
	}

	if setsDiffer(priorNames, desiredNames) {
		r.Runtime.Eventf(policy, nil, corev1.EventTypeNormal, "Published", uddisync.ActionPublish,
			"published %d record(s)", desiredCount)
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

// zoneRefNames are the AuthZone CR names a policy depends on, for the dependency index.
// A zoneRef given by id has no CR to wait for and is not indexed.
func zoneRefNames(policy *uddiv1alpha1.DNSPublishPolicy) []string {
	names := make([]string, 0, len(policy.Spec.ZoneRefs))
	for _, z := range policy.Spec.ZoneRefs {
		if !z.ByID() && z.Name != "" {
			names = append(names, z.Name)
		}
	}
	return names
}

// policyRelevance decides whether a changed source object can affect a policy that
// lists the object's kind among its sources.
type policyRelevance func(policy *uddiv1alpha1.DNSPublishPolicy, obj client.Object) bool

// inObjectNamespace holds for a policy in the changed object's own namespace, which is
// where Routes, Ingresses, Services and HTTPRoutes are read from.
func inObjectNamespace(policy *uddiv1alpha1.DNSPublishPolicy, obj client.Object) bool {
	return policy.Namespace == obj.GetNamespace()
}

// anywhere holds for every policy: Nodes are cluster-scoped, and a Gateway may live in
// another namespace than the routes attached to it.
func anywhere(*uddiv1alpha1.DNSPublishPolicy, client.Object) bool { return true }

// readsObjectNamespace holds for a policy whose DNSRecord source reads the changed
// DNSRecord's namespace.
func readsObjectNamespace(policy *uddiv1alpha1.DNSPublishPolicy, obj client.Object) bool {
	return dnsRecordNamespace(policy) == obj.GetNamespace()
}

// enqueuePoliciesListingSource maps a changed source object onto every DNSPublishPolicy,
// cluster-wide, that lists kind among its sources and for which relevant holds. A
// policy's selector is not evaluated here (that would re-run the matching the reconciler
// already does), so the change wakes the policy, which decides for itself.
func enqueuePoliciesListingSource(c client.Client, kind uddiv1alpha1.SourceKind, relevant policyRelevance) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list uddiv1alpha1.DNSPublishPolicyList
		if err := c.List(ctx, &list); err != nil {
			return nil
		}
		var out []reconcile.Request
		for i := range list.Items {
			p := &list.Items[i]
			if slices.Contains(p.Spec.Sources, kind) && relevant(p, obj) {
				out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(p)})
			}
		}
		return out
	}
}

// SetupWithManager registers the controller, the AuthZone dependency index, and the
// watches DESIGN.md 4.7 lists: owned Records, Ingresses and Services mapped to every
// policy in their namespace, Routes the same way when RoutesAvailable, Nodes mapped to
// every policy that lists Node, DNSRecords (when DNSRecordsAvailable) to every policy
// that lists DNSRecord and reads their namespace, HTTPRoutes (when GatewayAPIAvailable) to
// the HTTPRoute policies in their namespace, Gateways to every policy that lists
// HTTPRoute, and AuthZones through the zoneRefs dependency index. Only zoneRefs by name
// are indexed: a zone given by id has no CR to wait for.
func (r *DNSPublishPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()

	if err := RegisterDependencyIndex(ctx, mgr, &uddiv1alpha1.DNSPublishPolicy{}, func(o client.Object) []string {
		return zoneRefNames(o.(*uddiv1alpha1.DNSPublishPolicy))
	}); err != nil {
		return err
	}

	c := mgr.GetClient()
	newPolicyList := func() client.ObjectList { return &uddiv1alpha1.DNSPublishPolicyList{} }
	toNamespacePolicies := handler.EnqueueRequestsFromMapFunc(enqueueNamespacePolicies(c))
	policies := func(kind uddiv1alpha1.SourceKind, relevant policyRelevance) handler.EventHandler {
		return handler.EnqueueRequestsFromMapFunc(enqueuePoliciesListingSource(c, kind, relevant))
	}

	bldr := ctrl.NewControllerManagedBy(mgr).
		For(&uddiv1alpha1.DNSPublishPolicy{}).
		Owns(&uddiv1alpha1.Record{}).
		Watches(&networkingv1.Ingress{}, toNamespacePolicies).
		Watches(&corev1.Service{}, toNamespacePolicies).
		Watches(&corev1.Node{}, policies(uddiv1alpha1.SourceKindNode, anywhere)).
		Watches(
			&uddiv1alpha1.AuthZone{},
			handler.EnqueueRequestsFromMapFunc(EnqueueForIndex(c, IndexDependencyRefs, newPolicyList)),
		)

	if r.RoutesAvailable {
		bldr = bldr.Watches(&routev1.Route{}, toNamespacePolicies)
	}
	if r.DNSRecordsAvailable {
		bldr = bldr.Watches(&operatoringressv1.DNSRecord{}, policies(uddiv1alpha1.SourceKindDNSRecord, readsObjectNamespace))
	}
	if r.GatewayAPIAvailable {
		route := &unstructured.Unstructured{}
		route.SetGroupVersionKind(gatewayapi.HTTPRouteGVK)
		gateway := &unstructured.Unstructured{}
		gateway.SetGroupVersionKind(gatewayapi.GatewayGVK)
		bldr = bldr.
			Watches(route, policies(uddiv1alpha1.SourceKindHTTPRoute, inObjectNamespace)).
			Watches(gateway, policies(uddiv1alpha1.SourceKindHTTPRoute, anywhere))
	}

	return bldr.Named("dnspublishpolicy").Complete(r)
}
