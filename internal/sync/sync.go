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

package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/connection"
	"github.com/IngmarVG-IB/openshiftoperator/internal/tags"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// RequeueNotReady is how long the loop waits before retrying when the Connection or a
// dependency is not ready. Field indexers usually wake the reconciler sooner; this is
// only a safety net.
const RequeueNotReady = 30 * time.Second

// RequeueSoon is the short delay used when the loop has to run again right away: a write
// conflict with another writer, or a Universal DDI object that vanished mid-update. It
// replaces controller-runtime's deprecated Result.Requeue.
const RequeueSoon = 200 * time.Millisecond

// Event reasons emitted by the loop. They are the same for every object-layer kind.
const (
	// EventCreated: a Universal DDI object was created for this CR.
	EventCreated = "Created"
	// EventAdopted: an existing Universal DDI object was taken over, either because it
	// carries this CR's uid tag or because lifecycle.adoptExisting is set.
	EventAdopted = "Adopted"
	// EventUpdated: managed fields were patched in Universal DDI.
	EventUpdated = "Updated"
	// EventDeleted: the Universal DDI object was deleted during finalization.
	EventDeleted = "Deleted"
	// EventOrphaned: the Universal DDI object was deliberately left in place, because
	// deletionPolicy is Orphan or because the ownership uid tag did not match.
	EventOrphaned = "Orphaned"
	// EventConflict: a matching Universal DDI object exists that this CR may not take
	// over. The Ready condition carries reason AlreadyExists with the same message.
	EventConflict = "Conflict"
	// EventRecreated: the Universal DDI object disappeared out of band.
	EventRecreated = "Recreated"
	// EventConnectionMissing: the CR is being deleted but its Connection CR is gone, so
	// the Universal DDI object was orphaned rather than deleted.
	EventConnectionMissing = "ConnectionMissing"
	// EventDeleteBlocked: cleanup failed and will be retried with backoff.
	EventDeleteBlocked = "DeleteBlocked"
	// EventAPIError: a Universal DDI API call failed.
	EventAPIError = "APIError"
)

// Runtime holds everything the loop needs that is not per-kind.
type Runtime struct {
	// Client is the Kubernetes client.
	Client client.Client
	// Scheme is used to resolve the CR's kind for events.
	Scheme *runtime.Scheme
	// Recorder emits Kubernetes events.
	Recorder record.EventRecorder
	// Connections resolves Connection CRs into Universal DDI clients.
	Connections *connection.Resolver
	// ClusterID goes into the uddi_operator_cluster ownership tag.
	ClusterID string
}

func (rt *Runtime) event(obj client.Object, eventType, reason, format string, args ...any) {
	if rt.Recorder == nil {
		return
	}
	rt.Recorder.Eventf(obj, eventType, reason, format, args...)
}

// Reconcile runs the object-layer reconcile loop for one CR.
//
// Steps, per DESIGN.md section 5:
//
//  1. resolve the Connection (Ready=False/ConnectionNotReady when it is missing or unverified)
//  2. handle deletion, per DESIGN.md section 7
//  3. ensure the finalizer
//  4. resolve dependencies (Ready=False/DependencyNotReady)
//  5. adopt or create when status.id is empty
//  6. read, recreate on an out-of-band 404, then diff and patch
//  7. write status and requeue after the Connection's resyncInterval
func Reconcile[S any, U any](ctx context.Context, rt *Runtime, obj Object[S], ad Adapter[S, U]) (ctrl.Result, error) {
	before := obj.DeepCopyObject().(client.Object)

	res, writeStatus, err := reconcile(ctx, rt, obj, ad)

	if writeStatus {
		if serr := rt.Client.Status().Patch(ctx, obj, client.MergeFrom(before)); serr != nil {
			if err == nil {
				return ctrl.Result{}, serr
			}
			logf.FromContext(ctx).Error(serr, "unable to write status")
		}
	}
	return res, err
}

//nolint:gocyclo // the loop is a single linear state machine; splitting it hides the order.
func reconcile[S any, U any](ctx context.Context, rt *Runtime, obj Object[S], ad Adapter[S, U]) (ctrl.Result, bool, error) {
	log := logf.FromContext(ctx)
	status := obj.StatusOf()
	lifecycle := obj.LifecycleOf()
	deleting := !obj.GetDeletionTimestamp().IsZero()

	// Orphan needs no Universal DDI call at all, so it runs before the Connection is
	// resolved. That keeps a CR from hanging when its Connection is already gone.
	if deleting && lifecycle.Policy() == uddiv1alpha1.DeletionPolicyOrphan {
		if !controllerutil.ContainsFinalizer(obj, uddiv1alpha1.Finalizer) {
			return ctrl.Result{}, false, nil
		}
		rt.event(obj, corev1.EventTypeNormal, EventOrphaned,
			"deletionPolicy is Orphan, leaving Universal DDI %s %s in place", ad.Kind(), status.ID)
		return ctrl.Result{}, false, removeFinalizer(ctx, rt, obj)
	}

	// 1. Connection.
	conn, err := rt.Connections.Resolve(ctx, obj.GetNamespace(), obj.ConnectionName())
	if err != nil {
		if isTerminalConnectionError(err) {
			if deleting {
				// The Connection CR itself is gone, so nothing will ever make this CR
				// deletable again: orphan the Universal DDI object instead of wedging
				// on Ready=False/Deleting forever. A Connection that exists but is not
				// Ready (or whose Secret is temporarily missing) is transient, so it
				// keeps blocking through the branch below.
				if errors.Is(err, connection.ErrConnectionNotFound) {
					return orphanMissingConnection(ctx, rt, obj, ad.Kind(), status.ID)
				}
				setCondition(status, uddiv1alpha1.ConditionReady, metav1.ConditionFalse,
					uddiv1alpha1.ReasonDeleting, "cannot reach Universal DDI to clean up: "+err.Error(), obj.GetGeneration())
				return ctrl.Result{RequeueAfter: RequeueNotReady}, true, nil
			}
			setCondition(status, uddiv1alpha1.ConditionReady, metav1.ConditionFalse,
				uddiv1alpha1.ReasonConnectionNotReady, err.Error(), obj.GetGeneration())
			setCondition(status, uddiv1alpha1.ConditionSynced, metav1.ConditionFalse,
				uddiv1alpha1.ReasonConnectionNotReady, err.Error(), obj.GetGeneration())
			return ctrl.Result{RequeueAfter: RequeueNotReady}, true, nil
		}
		return ctrl.Result{}, false, err
	}
	uc := conn.Client

	// 2. Deletion.
	if deleting {
		if !controllerutil.ContainsFinalizer(obj, uddiv1alpha1.Finalizer) {
			return ctrl.Result{}, false, nil
		}
		return finalize(ctx, rt, obj, ad, uc)
	}

	// 3. Finalizer.
	if !controllerutil.ContainsFinalizer(obj, uddiv1alpha1.Finalizer) {
		patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
		controllerutil.AddFinalizer(obj, uddiv1alpha1.Finalizer)
		if err := rt.Client.Patch(ctx, obj, patch); err != nil {
			return ctrl.Result{}, false, err
		}
	}

	// 4. Dependencies.
	spec := obj.SpecOf()
	deps, err := ad.Resolve(ctx, rt.Client, obj, spec)
	if err != nil {
		if errors.Is(err, ErrDependencyNotReady) {
			setCondition(status, uddiv1alpha1.ConditionReady, metav1.ConditionFalse,
				uddiv1alpha1.ReasonDependencyNotReady, err.Error(), obj.GetGeneration())
			setCondition(status, uddiv1alpha1.ConditionSynced, metav1.ConditionFalse,
				uddiv1alpha1.ReasonDependencyNotReady, err.Error(), obj.GetGeneration())
			return ctrl.Result{RequeueAfter: RequeueNotReady}, true, nil
		}
		return ctrl.Result{}, false, err
	}

	uid := string(obj.GetUID())
	owner := tags.Ownership(rt.ClusterID, obj.GetNamespace(), ad.Kind(), obj.GetName(), uid)
	merged := tags.Merge(owner, conn.DefaultTags, obj.SpecTags())

	// 5 and 6. Read the object, recreating it if it vanished out of band.
	var current *U
	if status.ID != "" {
		current, err = ad.Get(ctx, uc, status.ID)
		switch {
		case uddi.IsNotFound(err):
			log.Info("Universal DDI object disappeared, recreating", "kind", ad.Kind(), "id", status.ID)
			rt.event(obj, corev1.EventTypeWarning, EventRecreated,
				"Universal DDI %s %s no longer exists, recreating it", ad.Kind(), status.ID)
			status.ID = ""
			current = nil
		case err != nil:
			return apiFailure(rt, obj, status, err)
		}
	}

	if current == nil {
		// Recover an object created by a previous pass whose status write was lost.
		current, err = ad.FindByUID(ctx, uc, uid)
		if err != nil {
			return apiFailure(rt, obj, status, err)
		}
		if current != nil {
			rt.event(obj, corev1.EventTypeNormal, EventAdopted,
				"recovered Universal DDI %s %s by ownership tag", ad.Kind(), ad.IDOf(current))
		}
	}

	if current == nil {
		// Natural-key lookup, for adoption and to keep creates idempotent.
		found, ferr := ad.Find(ctx, uc, spec, deps)
		if ferr != nil {
			if errors.Is(ferr, ErrConflict) {
				// The adapter found a match that can never be adopted, regardless of
				// lifecycle.adoptExisting (for example a non-STATIC Record). Report the
				// conflict with the adapter's message and stop: no create, no adopt, no
				// patch, no delete.
				msg := ferr.Error()
				rt.event(obj, corev1.EventTypeWarning, EventConflict, "%s", msg)
				setCondition(status, uddiv1alpha1.ConditionReady, metav1.ConditionFalse,
					uddiv1alpha1.ReasonAlreadyExists, msg, obj.GetGeneration())
				setCondition(status, uddiv1alpha1.ConditionSynced, metav1.ConditionFalse,
					uddiv1alpha1.ReasonAlreadyExists, msg, obj.GetGeneration())
				return ctrl.Result{RequeueAfter: conn.ResyncInterval}, true, nil
			}
			return apiFailure(rt, obj, status, ferr)
		}
		switch {
		case found == nil:
			setCondition(status, uddiv1alpha1.ConditionReady, metav1.ConditionFalse,
				uddiv1alpha1.ReasonCreating, "creating the Universal DDI "+ad.Kind(), obj.GetGeneration())
			created, cerr := ad.Create(ctx, uc, spec, deps, merged)
			if cerr != nil {
				return apiFailure(rt, obj, status, cerr)
			}
			current = created
			rt.event(obj, corev1.EventTypeNormal, EventCreated,
				"created Universal DDI %s %s", ad.Kind(), ad.IDOf(current))
		case tags.OwnedBy(ad.TagsOf(found), uid), lifecycle.AdoptExisting:
			current = found
			rt.event(obj, corev1.EventTypeNormal, EventAdopted,
				"adopted existing Universal DDI %s %s", ad.Kind(), ad.IDOf(current))
		default:
			msg := fmt.Sprintf("Universal DDI %s %s already exists and is not managed by this operator; "+
				"set spec.lifecycle.adoptExisting to take it over", ad.Kind(), ad.IDOf(found))
			rt.event(obj, corev1.EventTypeWarning, EventConflict, "%s", msg)
			setCondition(status, uddiv1alpha1.ConditionReady, metav1.ConditionFalse,
				uddiv1alpha1.ReasonAlreadyExists, msg, obj.GetGeneration())
			setCondition(status, uddiv1alpha1.ConditionSynced, metav1.ConditionFalse,
				uddiv1alpha1.ReasonAlreadyExists, msg, obj.GetGeneration())
			return ctrl.Result{RequeueAfter: conn.ResyncInterval}, true, nil
		}
	}

	// From here on the object exists, whether it was recovered, adopted or created.
	status.ID = ad.IDOf(current)

	// Diff and patch. Only fields the CR manages are sent.
	if patch, changed := ad.Diff(spec, deps, merged, current); changed {
		updated, uerr := ad.Update(ctx, uc, status.ID, patch)
		if uerr != nil {
			if uddi.IsNotFound(uerr) {
				rt.event(obj, corev1.EventTypeWarning, EventRecreated,
					"Universal DDI %s %s vanished during update, recreating it", ad.Kind(), status.ID)
				status.ID = ""
				return ctrl.Result{RequeueAfter: RequeueSoon}, true, nil
			}
			return apiFailure(rt, obj, status, uerr)
		}
		current = updated
		rt.event(obj, corev1.EventTypeNormal, EventUpdated,
			"updated Universal DDI %s %s", ad.Kind(), status.ID)
	}

	// 7. Status.
	ad.Observe(obj, current)
	status.ID = ad.IDOf(current)
	status.ObservedGeneration = obj.GetGeneration()
	now := metav1.Now()
	status.LastSyncTime = &now
	setCondition(status, uddiv1alpha1.ConditionSynced, metav1.ConditionTrue,
		uddiv1alpha1.ReasonSynced, "Universal DDI "+ad.Kind()+" matches spec", obj.GetGeneration())
	setCondition(status, uddiv1alpha1.ConditionReady, metav1.ConditionTrue,
		uddiv1alpha1.ReasonSynced, "Universal DDI "+ad.Kind()+" "+status.ID+" is in sync", obj.GetGeneration())

	return ctrl.Result{RequeueAfter: conn.ResyncInterval}, true, nil
}

// finalize implements DESIGN.md section 7 for deletionPolicy Delete.
func finalize[S any, U any](
	ctx context.Context,
	rt *Runtime,
	obj Object[S],
	ad Adapter[S, U],
	uc *uddi.Client,
) (ctrl.Result, bool, error) {
	status := obj.StatusOf()
	uid := string(obj.GetUID())

	id := status.ID
	if id == "" {
		// The CR may have created an object whose id never made it into status.
		found, err := ad.FindByUID(ctx, uc, uid)
		if err != nil {
			return deleteFailure(rt, obj, status, err)
		}
		if found == nil {
			return ctrl.Result{}, false, removeFinalizer(ctx, rt, obj)
		}
		id = ad.IDOf(found)
	}

	current, err := ad.Get(ctx, uc, id)
	switch {
	case uddi.IsNotFound(err):
		// Already gone. A 404 counts as done.
		return ctrl.Result{}, false, removeFinalizer(ctx, rt, obj)
	case err != nil:
		return deleteFailure(rt, obj, status, err)
	}

	if !tags.OwnedBy(ad.TagsOf(current), uid) {
		rt.event(obj, corev1.EventTypeWarning, EventOrphaned,
			"Universal DDI %s %s does not carry this object's ownership tag, leaving it in place", ad.Kind(), id)
		logf.FromContext(ctx).Info("ownership tag mismatch on delete, orphaning",
			"kind", ad.Kind(), "id", id)
		return ctrl.Result{}, false, removeFinalizer(ctx, rt, obj)
	}

	if derr := ad.Delete(ctx, uc, id); derr != nil {
		if uddi.IsNotFound(derr) {
			return ctrl.Result{}, false, removeFinalizer(ctx, rt, obj)
		}
		return deleteFailure(rt, obj, status, derr)
	}

	rt.event(obj, corev1.EventTypeNormal, EventDeleted, "deleted Universal DDI %s %s", ad.Kind(), id)
	return ctrl.Result{}, false, removeFinalizer(ctx, rt, obj)
}

// orphanMissingConnection drops the finalizer of a CR that is being deleted while the
// Connection it names no longer exists. The Universal DDI object cannot be reached, so
// it is left in place with its ownership tags and the user is told about it through a
// Warning event; a later CR with the same natural key re-adopts it.
func orphanMissingConnection(ctx context.Context, rt *Runtime, obj client.Object, kind, id string) (ctrl.Result, bool, error) {
	if !controllerutil.ContainsFinalizer(obj, uddiv1alpha1.Finalizer) {
		return ctrl.Result{}, false, nil
	}
	if id == "" {
		id = "(no id recorded)"
	}
	rt.event(obj, corev1.EventTypeWarning, EventConnectionMissing,
		"the Connection this object uses no longer exists, so Universal DDI cannot be reached; "+
			"%s %s was left in place with its ownership tags and has to be removed by hand or re-adopted by a new CR",
		kind, id)
	logf.FromContext(ctx).Info("Connection is gone while deleting, orphaning the Universal DDI object",
		"kind", kind, "id", id)
	return ctrl.Result{}, false, removeFinalizer(ctx, rt, obj)
}

func removeFinalizer(ctx context.Context, rt *Runtime, obj client.Object) error {
	if !controllerutil.ContainsFinalizer(obj, uddiv1alpha1.Finalizer) {
		return nil
	}
	patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
	controllerutil.RemoveFinalizer(obj, uddiv1alpha1.Finalizer)
	return rt.Client.Patch(ctx, obj, patch)
}

// apiFailure records a Universal DDI API error on the CR and asks for a backed-off retry.
func apiFailure(rt *Runtime, obj client.Object, status *uddiv1alpha1.ObjectStatus, err error) (ctrl.Result, bool, error) {
	setCondition(status, uddiv1alpha1.ConditionReady, metav1.ConditionFalse,
		uddiv1alpha1.ReasonAPIError, err.Error(), obj.GetGeneration())
	setCondition(status, uddiv1alpha1.ConditionSynced, metav1.ConditionFalse,
		uddiv1alpha1.ReasonAPIError, err.Error(), obj.GetGeneration())
	rt.event(obj, corev1.EventTypeWarning, EventAPIError, "%s", err.Error())
	return ctrl.Result{}, true, err
}

// deleteFailure records a failure during cleanup. Dependents blocking a delete (a zone
// with records, a subnet with addresses) land here and are retried with backoff.
func deleteFailure(rt *Runtime, obj client.Object, status *uddiv1alpha1.ObjectStatus, err error) (ctrl.Result, bool, error) {
	setCondition(status, uddiv1alpha1.ConditionReady, metav1.ConditionFalse,
		uddiv1alpha1.ReasonDeleting, err.Error(), obj.GetGeneration())
	rt.event(obj, corev1.EventTypeWarning, EventDeleteBlocked, "%s", err.Error())
	return ctrl.Result{}, true, err
}

func setCondition(status *uddiv1alpha1.ObjectStatus, condType string, s metav1.ConditionStatus, reason, message string, generation int64) {
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             s,
		Reason:             reason,
		Message:            truncateMessage(message),
		ObservedGeneration: generation,
	})
}

// truncateMessage keeps condition messages inside the API server's 32 KiB limit and out
// of the way in kubectl output.
func truncateMessage(s string) string {
	const max = 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// isTerminalConnectionError reports whether the error means "the Connection is not
// usable right now", as opposed to a transient client-go failure worth returning.
func isTerminalConnectionError(err error) bool {
	return errors.Is(err, connection.ErrConnectionNotFound) ||
		errors.Is(err, connection.ErrConnectionNotReady) ||
		errors.Is(err, connection.ErrSecretNotFound) ||
		errors.Is(err, connection.ErrSecretKeyMissing) ||
		errors.Is(err, uddi.ErrNoAPIKey)
}
