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
	"errors"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/connection"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// IndexConnectionSecret indexes Connection CRs by the Secret they reference.
const IndexConnectionSecret = "spec.secretRef.name"

// ConnectionReconciler verifies that a Connection's credentials work and publishes the
// result as the Ready condition, which every object-layer CR waits on.
type ConnectionReconciler struct {
	client.Client
	// Connections builds and caches Universal DDI clients.
	Connections *connection.Resolver
	// Recorder emits Kubernetes events.
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=connections,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=connections/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=uddi.infoblox.com,resources=connections/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile reads the Secret, builds a client and verifies it with a cheap read call.
//
// The API key never reaches a log line, an event or a condition: errors from the client
// layer are uddi.APIError values, which carry only the domain, method, HTTP status and
// a truncated response body.
func (r *ConnectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	conn := &uddiv1alpha1.Connection{}
	if err := r.Get(ctx, req.NamespacedName, conn); err != nil {
		if client.IgnoreNotFound(err) == nil {
			r.Connections.Invalidate(req.Namespace, req.Name)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !conn.GetDeletionTimestamp().IsZero() {
		r.Connections.Invalidate(conn.Namespace, conn.Name)
		return ctrl.Result{}, nil
	}

	before := conn.DeepCopy()
	requeue := connection.ResyncInterval(conn)

	uc, err := r.Connections.ClientFor(ctx, conn)
	if err != nil {
		reason := uddiv1alpha1.ReasonAPIError
		switch {
		case errors.Is(err, connection.ErrSecretNotFound):
			reason = uddiv1alpha1.ReasonSecretNotFound
		case errors.Is(err, connection.ErrSecretKeyMissing), errors.Is(err, uddi.ErrNoAPIKey):
			reason = uddiv1alpha1.ReasonSecretKeyMissing
		}
		log.Info("Connection is not usable", "reason", reason)
		r.setReady(conn, metav1.ConditionFalse, reason, err.Error())
		return ctrl.Result{RequeueAfter: requeue}, r.patchStatus(ctx, before, conn)
	}

	// Cheap read call: list one view. Every API key role can list views.
	if _, err := uc.DNSConfig.ListViews(ctx, uddi.ListOptions{Limit: 1}); err != nil {
		// A rejected key is not worth caching.
		if uddi.IsUnauthorized(err) {
			r.Connections.Invalidate(conn.Namespace, conn.Name)
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(conn, corev1.EventTypeWarning, "VerificationFailed", "%s", err.Error())
		}
		r.setReady(conn, metav1.ConditionFalse, uddiv1alpha1.ReasonAPIError, err.Error())
		return ctrl.Result{RequeueAfter: requeue}, r.patchStatus(ctx, before, conn)
	}

	now := metav1.Now()
	conn.Status.LastVerifiedTime = &now
	r.setReady(conn, metav1.ConditionTrue, uddiv1alpha1.ReasonVerified,
		"verified against "+conn.CSPURL())
	return ctrl.Result{RequeueAfter: requeue}, r.patchStatus(ctx, before, conn)
}

func (r *ConnectionReconciler) setReady(conn *uddiv1alpha1.Connection, status metav1.ConditionStatus, reason, message string) {
	conn.Status.ObservedGeneration = conn.Generation
	if len(message) > 1024 {
		message = message[:1024] + "..."
	}
	meta.SetStatusCondition(&conn.Status.Conditions, metav1.Condition{
		Type:               uddiv1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: conn.Generation,
	})
}

func (r *ConnectionReconciler) patchStatus(ctx context.Context, before, conn *uddiv1alpha1.Connection) error {
	return r.Status().Patch(ctx, conn, client.MergeFrom(before))
}

// SetupWithManager registers the controller and a watch that re-verifies a Connection
// as soon as its Secret changes, so a rotated key is picked up immediately.
func (r *ConnectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
	if err := mgr.GetFieldIndexer().IndexField(ctx, &uddiv1alpha1.Connection{}, IndexConnectionSecret,
		func(o client.Object) []string {
			return []string{o.(*uddiv1alpha1.Connection).Spec.SecretRef.Name}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&uddiv1alpha1.Connection{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, obj client.Object) []reconcile.Request {
				list := &uddiv1alpha1.ConnectionList{}
				if err := r.List(ctx, list,
					client.InNamespace(obj.GetNamespace()),
					client.MatchingFields{IndexConnectionSecret: obj.GetName()},
				); err != nil {
					return nil
				}
				out := make([]reconcile.Request, 0, len(list.Items))
				for i := range list.Items {
					out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{
						Namespace: list.Items[i].Namespace,
						Name:      list.Items[i].Name,
					}})
				}
				return out
			})).
		Named("connection").
		Complete(r)
}
