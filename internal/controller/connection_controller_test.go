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
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/connection"
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
	})
})
