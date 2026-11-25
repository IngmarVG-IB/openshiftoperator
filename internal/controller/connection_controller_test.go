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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/connection"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
)

// requestFor builds a reconcile request for an object.
func requestFor(obj client.Object) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}}
}

func readyCondition(conn *uddiv1alpha1.Connection) *metav1.Condition {
	return meta.FindStatusCondition(conn.Status.Conditions, uddiv1alpha1.ConditionReady)
}

var _ = Describe("Connection controller", func() {
	var (
		ns         string
		reconciler *ConnectionReconciler
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		reconciler = &ConnectionReconciler{
			Client:      k8sClient,
			Connections: testResolver,
			Recorder:    testRecorder,
		}
	})

	It("reports SecretNotFound when the referenced Secret does not exist", func() {
		conn := &uddiv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns},
			Spec: uddiv1alpha1.ConnectionSpec{
				SecretRef: uddiv1alpha1.SecretKeyRef{Name: "missing-secret"},
			},
		}
		Expect(k8sClient.Create(ctx, conn)).To(Succeed())

		res, err := reconciler.Reconcile(ctx, requestFor(conn))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(connection.DefaultResyncInterval))

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(conn), conn)).To(Succeed())
		cond := readyCondition(conn)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonSecretNotFound))
		Expect(conn.Status.LastVerifiedTime).To(BeNil())
	})

	It("reports SecretKeyMissing when the Secret has no data under the configured key", func() {
		makeSecret(ns, "creds", "wrongKey", "value")
		conn := &uddiv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns},
			Spec: uddiv1alpha1.ConnectionSpec{
				SecretRef: uddiv1alpha1.SecretKeyRef{Name: "creds"},
			},
		}
		Expect(k8sClient.Create(ctx, conn)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, requestFor(conn))
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(conn), conn)).To(Succeed())
		cond := readyCondition(conn)
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonSecretKeyMissing))
	})

	It("reports APIError when Universal DDI rejects the key, without leaking it", func() {
		makeSecret(ns, "creds", uddiv1alpha1.DefaultSecretKey, "super-secret-key-value")
		conn := &uddiv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns},
			Spec: uddiv1alpha1.ConnectionSpec{
				SecretRef: uddiv1alpha1.SecretKeyRef{Name: "creds"},
			},
		}
		Expect(k8sClient.Create(ctx, conn)).To(Succeed())
		testFake.PushStatus("ListViews", http.StatusUnauthorized, `{"message":"invalid api key"}`)

		_, err := reconciler.Reconcile(ctx, requestFor(conn))
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(conn), conn)).To(Succeed())
		cond := readyCondition(conn)
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonAPIError))
		Expect(cond.Message).To(ContainSubstring("invalid api key"))
		Expect(cond.Message).NotTo(ContainSubstring("super-secret-key-value"))
		Expect(connection.IsReady(conn)).To(BeFalse())

		events := collectEvents()
		Expect(events).To(ContainElement(ContainSubstring("VerificationFailed")))
		for _, e := range events {
			Expect(e).NotTo(ContainSubstring("super-secret-key-value"))
		}
	})

	It("verifies the key with a limit-1 view list and reports Ready", func() {
		makeSecret(ns, "creds", uddiv1alpha1.DefaultSecretKey, "good-key")
		conn := &uddiv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns},
			Spec: uddiv1alpha1.ConnectionSpec{
				SecretRef:      uddiv1alpha1.SecretKeyRef{Name: "creds"},
				ResyncInterval: &metav1.Duration{Duration: connection.DefaultResyncInterval},
			},
		}
		Expect(k8sClient.Create(ctx, conn)).To(Succeed())

		before := testFake.CallCount("ListViews")
		res, err := reconciler.Reconcile(ctx, requestFor(conn))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(connection.DefaultResyncInterval))
		Expect(testFake.CallCount("ListViews")).To(Equal(before + 1))

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(conn), conn)).To(Succeed())
		cond := readyCondition(conn)
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonVerified))
		Expect(conn.Status.LastVerifiedTime).NotTo(BeNil())
		Expect(conn.Status.ObservedGeneration).To(Equal(conn.Generation))
		Expect(connection.IsReady(conn)).To(BeTrue())
	})

	It("applies the documented defaults", func() {
		makeSecret(ns, "creds", uddiv1alpha1.DefaultSecretKey, "good-key")
		conn := &uddiv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns},
			Spec: uddiv1alpha1.ConnectionSpec{
				SecretRef: uddiv1alpha1.SecretKeyRef{Name: "creds"},
			},
		}
		Expect(k8sClient.Create(ctx, conn)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(conn), conn)).To(Succeed())

		Expect(conn.Spec.CSPURL).To(Equal(uddiv1alpha1.DefaultCSPURL))
		Expect(conn.Spec.ClientName).To(Equal(uddiv1alpha1.DefaultClientName))
		Expect(conn.Spec.SecretRef.Key).To(Equal(uddiv1alpha1.DefaultSecretKey))
		Expect(conn.Spec.ResyncInterval).NotTo(BeNil())
		Expect(conn.Spec.ResyncInterval.Duration).To(Equal(connection.DefaultResyncInterval))
		Expect(conn.Spec.RateLimit).To(BeNil())
	})

	It("rejects a non-https or decorated cspURL and a resyncInterval under 30s at admission", func() {
		secretRef := uddiv1alpha1.SecretKeyRef{Name: "creds"}
		for i, spec := range []uddiv1alpha1.ConnectionSpec{
			{CSPURL: "http://csp.infoblox.com", SecretRef: secretRef},
			{CSPURL: "https://evil.example.com/#", SecretRef: secretRef},
			{CSPURL: "https://evil.example.com?", SecretRef: secretRef},
			{CSPURL: "https://user@evil.example.com", SecretRef: secretRef},
			{CSPURL: "https://csp.infoblox.com/api/ddi/v1", SecretRef: secretRef},
			{ResyncInterval: &metav1.Duration{Duration: time.Second}, SecretRef: secretRef},
		} {
			conn := &uddiv1alpha1.Connection{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("bad-%d", i), Namespace: ns},
				Spec:       spec,
			}
			err := k8sClient.Create(ctx, conn)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "spec %d (%+v): got %v", i, spec, err)
		}

		Expect(k8sClient.Create(ctx, &uddiv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "good", Namespace: ns},
			Spec: uddiv1alpha1.ConnectionSpec{
				CSPURL:         "https://CSP.eu.infoblox.com:443/",
				ResyncInterval: &metav1.Duration{Duration: 30 * time.Second},
				SecretRef:      secretRef,
			},
		})).To(Succeed())
	})

	It("reports EndpointNotAllowed and sends nothing to a host outside the allowlist", func() {
		var hits atomic.Int32
		srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			hits.Add(1)
		}))
		defer srv.Close()

		makeSecret(ns, "creds", uddiv1alpha1.DefaultSecretKey, "super-secret-key-value")
		conn := &uddiv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns},
			Spec: uddiv1alpha1.ConnectionSpec{
				CSPURL:    srv.URL,
				SecretRef: uddiv1alpha1.SecretKeyRef{Name: "creds"},
			},
		}
		Expect(k8sClient.Create(ctx, conn)).To(Succeed())

		// The real SDK client factory: if the allowlist did not hold, the key would reach srv.
		r := &ConnectionReconciler{
			Client:      k8sClient,
			Connections: &connection.Resolver{Client: k8sClient},
			Recorder:    testRecorder,
		}
		res, err := r.Reconcile(ctx, requestFor(conn))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(connection.DefaultResyncInterval))

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(conn), conn)).To(Succeed())
		cond := readyCondition(conn)
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonEndpointNotAllowed))
		Expect(cond.Message).To(ContainSubstring("127.0.0.1"))
		Expect(cond.Message).NotTo(ContainSubstring("super-secret-key-value"))
		Expect(hits.Load()).To(BeZero())
		for _, e := range collectEvents() {
			Expect(e).NotTo(ContainSubstring("super-secret-key-value"))
		}
	})

	It("reports InvalidEndpoint for a cspURL the CRD pattern lets through but the operator refuses", func() {
		makeSecret(ns, "creds", uddiv1alpha1.DefaultSecretKey, "super-secret-key-value")
		conn := &uddiv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns},
			Spec: uddiv1alpha1.ConnectionSpec{
				CSPURL:    "https://csp.infoblox.com:0",
				SecretRef: uddiv1alpha1.SecretKeyRef{Name: "creds"},
			},
		}
		Expect(k8sClient.Create(ctx, conn)).To(Succeed())
		listsBefore := testFake.CallCount("ListViews")

		_, err := reconciler.Reconcile(ctx, requestFor(conn))
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(conn), conn)).To(Succeed())
		cond := readyCondition(conn)
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonInvalidEndpoint))
		Expect(cond.Message).NotTo(ContainSubstring("super-secret-key-value"))
		Expect(testFake.CallCount("ListViews")).To(Equal(listsBefore))
	})

	It("holds object reconcilers off a Connection whose spec changed until it is re-verified", func() {
		conn := makeReadyConnection(ns)
		v := &uddiv1alpha1.View{
			ObjectMeta: metav1.ObjectMeta{Name: "stale-view", Namespace: ns},
			Spec:       uddiv1alpha1.ViewSpec{Name: "stale-view"},
		}
		Expect(k8sClient.Create(ctx, v)).To(Succeed())

		patch := client.MergeFrom(conn.DeepCopy())
		conn.Spec.DefaultTags = map[string]string{"env": "changed"}
		Expect(k8sClient.Patch(ctx, conn, patch)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(conn), conn)).To(Succeed())
		Expect(conn.Status.ObservedGeneration).To(BeNumerically("<", conn.Generation))
		Expect(readyCondition(conn).Status).To(Equal(metav1.ConditionTrue))
		Expect(connection.IsReady(conn)).To(BeFalse())

		_, err := testResolver.Resolve(ctx, ns, conn.Name)
		Expect(errors.Is(err, connection.ErrConnectionNotReady)).To(BeTrue(), "got %v", err)

		createsBefore := testFake.CallCount("CreateView")
		vr := &ViewReconciler{Client: k8sClient, Runtime: testRuntime}
		res, err := vr.Reconcile(ctx, requestFor(v))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(uddisync.RequeueNotReady))
		Expect(testFake.CallCount("CreateView")).To(Equal(createsBefore))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(v), v)).To(Succeed())
		Expect(meta.FindStatusCondition(v.Status.Conditions, uddiv1alpha1.ConditionReady).Reason).
			To(Equal(uddiv1alpha1.ReasonConnectionNotReady))

		// Re-verification at the new generation makes it usable again.
		_, err = reconciler.Reconcile(ctx, requestFor(conn))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(conn), conn)).To(Succeed())
		Expect(connection.IsReady(conn)).To(BeTrue())
		Expect(readyCondition(conn).ObservedGeneration).To(Equal(conn.Generation))
	})
})
