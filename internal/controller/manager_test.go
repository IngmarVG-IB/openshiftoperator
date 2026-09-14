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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/connection"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// The other suites drive Reconcile by hand, which proves the loop but not the wiring.
// These specs start a real controller-runtime manager with every controller's
// SetupWithManager, so the field indexes, the watches and the requeues are exercised
// exactly as they are in cmd/main.go. Nothing here calls Reconcile directly.
//
// The manager's cache is restricted to this file's namespace so it cannot reconcile the
// CRs the other suites leave behind, and it talks to its own in-memory Universal DDI
// fake rather than the shared testFake.
var _ = Describe("Every controller under a real manager", Ordered, func() {
	const (
		// Both requeue paths under test have a 30s RequeueNotReady safety net behind
		// them, so the timeout stays well under that: passing here means the watch and
		// the field index did the work, not the fallback.
		settle = 20 * time.Second
		poll   = 200 * time.Millisecond
	)

	var (
		mgrNS     string
		mgrFake   *uddi.Fake
		mgrCancel context.CancelFunc
		mgrDone   chan struct{}
	)

	// secretAndConnection creates a Secret plus a Connection and leaves both to the
	// manager's Connection controller to verify.
	secretAndConnection := func(name string) *uddiv1alpha1.Connection {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-key", Namespace: mgrNS},
			Data:       map[string][]byte{uddiv1alpha1.DefaultSecretKey: []byte("not-a-real-key")},
		})).To(Succeed())
		conn := &uddiv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: mgrNS},
			Spec: uddiv1alpha1.ConnectionSpec{
				SecretRef: uddiv1alpha1.SecretKeyRef{Name: name + "-key"},
			},
		}
		Expect(k8sClient.Create(ctx, conn)).To(Succeed())
		return conn
	}

	// readyCondition re-reads an object and returns its Ready condition, or nil.
	readyCondition := func(obj client.Object, conditions func() []metav1.Condition) func() *metav1.Condition {
		return func() *metav1.Condition {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
				return nil
			}
			return meta.FindStatusCondition(conditions(), uddiv1alpha1.ConditionReady)
		}
	}

	BeforeAll(func() {
		mgrNS = newNamespace()
		mgrFake = uddi.NewFake()

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                 scheme.Scheme,
			Metrics:                metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress: "0",
			LeaderElection:         false,
			Cache: cache.Options{
				DefaultNamespaces: map[string]cache.Config{mgrNS: {}},
			},
		})
		Expect(err).NotTo(HaveOccurred())

		resolver := &connection.Resolver{
			Client: mgr.GetClient(),
			Factory: func(uddi.Config) (*uddi.Client, error) {
				return mgrFake.Client(), nil
			},
		}
		rt := &uddisync.Runtime{
			Client:      mgr.GetClient(),
			Scheme:      mgr.GetScheme(),
			Recorder:    mgr.GetEventRecorderFor("uddi-operator"), //nolint:staticcheck // legacy record.EventRecorder API kept until Runtime moves to events.EventRecorder
			Connections: resolver,
			ClusterID:   TestClusterID,
		}

		Expect((&ConnectionReconciler{
			Client:      mgr.GetClient(),
			Connections: resolver,
			Recorder:    mgr.GetEventRecorderFor("uddi-connection"), //nolint:staticcheck // legacy record.EventRecorder API kept until Runtime moves to events.EventRecorder
		}).SetupWithManager(mgr)).To(Succeed())
		Expect((&ViewReconciler{Client: mgr.GetClient(), Runtime: rt}).SetupWithManager(mgr)).To(Succeed())
		Expect((&AuthZoneReconciler{Client: mgr.GetClient(), Runtime: rt}).SetupWithManager(mgr)).To(Succeed())
		Expect((&RecordReconciler{Client: mgr.GetClient(), Runtime: rt}).SetupWithManager(mgr)).To(Succeed())
		Expect((&IPSpaceReconciler{Client: mgr.GetClient(), Runtime: rt}).SetupWithManager(mgr)).To(Succeed())
		Expect((&AddressBlockReconciler{Client: mgr.GetClient(), Runtime: rt}).SetupWithManager(mgr)).To(Succeed())
		Expect((&SubnetReconciler{Client: mgr.GetClient(), Runtime: rt}).SetupWithManager(mgr)).To(Succeed())
		Expect((&RangeReconciler{Client: mgr.GetClient(), Runtime: rt}).SetupWithManager(mgr)).To(Succeed())
		Expect((&AddressReconciler{Client: mgr.GetClient(), Runtime: rt}).SetupWithManager(mgr)).To(Succeed())
		Expect((&FixedAddressReconciler{Client: mgr.GetClient(), Runtime: rt}).SetupWithManager(mgr)).To(Succeed())

		var mgrCtx context.Context
		mgrCtx, mgrCancel = context.WithCancel(ctx)
		mgrDone = make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(mgrDone)
			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()
		Expect(mgr.GetCache().WaitForCacheSync(mgrCtx)).To(BeTrue())
	})

	AfterAll(func() {
		if mgrCancel != nil {
			mgrCancel()
		}
		if mgrDone != nil {
			Eventually(mgrDone).WithTimeout(settle).Should(BeClosed())
		}
	})

	It("drives a View to Ready with no direct Reconcile call", func() {
		secretAndConnection(uddiv1alpha1.DefaultConnectionName)

		v := &uddiv1alpha1.View{
			ObjectMeta: metav1.ObjectMeta{Name: "managed-view", Namespace: mgrNS},
			Spec:       uddiv1alpha1.ViewSpec{Name: "managed-view"},
		}
		Expect(k8sClient.Create(ctx, v)).To(Succeed())

		Eventually(readyCondition(v, func() []metav1.Condition { return v.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Status", metav1.ConditionTrue))
		Expect(v.Status.ID).To(HavePrefix(uddi.PrefixView + "/"))
	})

	It("requeues a Record through the dependency index when its AuthZone gains a status.id", func() {
		view := &uddiv1alpha1.View{
			ObjectMeta: metav1.ObjectMeta{Name: "late-view", Namespace: mgrNS},
			Spec:       uddiv1alpha1.ViewSpec{Name: "late-view"},
		}
		Expect(k8sClient.Create(ctx, view)).To(Succeed())
		Eventually(readyCondition(view, func() []metav1.Condition { return view.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Status", metav1.ConditionTrue))

		// The zone the Record points at does not exist yet.
		rec := &uddiv1alpha1.Record{
			ObjectMeta: metav1.ObjectMeta{Name: "late-record", Namespace: mgrNS},
			Spec: uddiv1alpha1.RecordSpec{
				ZoneRef: uddiv1alpha1.ObjectRef{Name: "late-zone"},
				Name:    "www",
				Type:    "A",
				RData:   uddiv1alpha1.RecordRData{A: &uddiv1alpha1.ARData{Address: "192.0.2.10"}},
			},
		}
		Expect(k8sClient.Create(ctx, rec)).To(Succeed())
		Eventually(readyCondition(rec, func() []metav1.Condition { return rec.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Reason", uddiv1alpha1.ReasonDependencyNotReady))

		zone := &uddiv1alpha1.AuthZone{
			ObjectMeta: metav1.ObjectMeta{Name: "late-zone", Namespace: mgrNS},
			Spec: uddiv1alpha1.AuthZoneSpec{
				FQDN:        "apps.example.com.",
				ViewRef:     uddiv1alpha1.ObjectRef{Name: view.Name},
				PrimaryType: uddiv1alpha1.AuthZonePrimaryCloud,
			},
		}
		Expect(k8sClient.Create(ctx, zone)).To(Succeed())

		// No further nudge: the AuthZone watch plus the dependency index wake the
		// Record as soon as the zone reports an id.
		Eventually(readyCondition(rec, func() []metav1.Condition { return rec.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Status", metav1.ConditionTrue))
		Expect(rec.Status.ID).To(HavePrefix(uddi.PrefixRecord + "/"))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(zone), zone)).To(Succeed())
		Expect(zone.Status.ID).To(HavePrefix(uddi.PrefixAuthZone + "/"))
		Expect(rec.Status.Zone).To(Equal(zone.Status.ID))
	})

	It("requeues a waiting View when its Connection turns Ready", func() {
		v := &uddiv1alpha1.View{
			ObjectMeta: metav1.ObjectMeta{Name: "waiting-view", Namespace: mgrNS},
			Spec: uddiv1alpha1.ViewSpec{
				CommonSpec: uddiv1alpha1.CommonSpec{
					ConnectionRef: uddiv1alpha1.ConnectionRef{Name: "late-connection"},
				},
				Name: "waiting-view",
			},
		}
		Expect(k8sClient.Create(ctx, v)).To(Succeed())
		Eventually(readyCondition(v, func() []metav1.Condition { return v.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Reason", uddiv1alpha1.ReasonConnectionNotReady))

		secretAndConnection("late-connection")

		Eventually(readyCondition(v, func() []metav1.Condition { return v.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Status", metav1.ConditionTrue))
		Expect(v.Status.ID).To(HavePrefix(uddi.PrefixView + "/"))
	})
})
