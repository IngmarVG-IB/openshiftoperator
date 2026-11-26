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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/connection"
	"github.com/IngmarVG-IB/openshiftoperator/internal/egressip"
	"github.com/IngmarVG-IB/openshiftoperator/internal/gatewayapi"
	"github.com/IngmarVG-IB/openshiftoperator/internal/hostreg"
	"github.com/IngmarVG-IB/openshiftoperator/internal/ipalloc"
	"github.com/IngmarVG-IB/openshiftoperator/internal/metal3api"
	"github.com/IngmarVG-IB/openshiftoperator/internal/publish"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
		mgrRT     *uddisync.Runtime
		mgrCancel context.CancelFunc
		mgrDone   chan struct{}
	)

	// eventsFor lists the events.k8s.io/v1 Events the manager's recorder has written about
	// the named object in the manager's namespace.
	eventsFor := func(name string) func() []eventsv1.Event {
		return func() []eventsv1.Event {
			var list eventsv1.EventList
			if err := k8sClient.List(ctx, &list, client.InNamespace(mgrNS)); err != nil {
				return nil
			}
			var out []eventsv1.Event
			for _, e := range list.Items {
				if e.Regarding.Name == name {
					out = append(out, e)
				}
			}
			return out
		}
	}

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
			Client:       mgr.GetClient(),
			SecretReader: mgr.GetAPIReader(),
			Factory: func(uddi.Config) (*uddi.Client, error) {
				return mgrFake.Client(), nil
			},
		}
		rt := &uddisync.Runtime{
			Client:      mgr.GetClient(),
			Scheme:      mgr.GetScheme(),
			Recorder:    mgr.GetEventRecorder("uddi-operator"),
			Connections: resolver,
			ClusterID:   TestClusterID,
		}
		mgrRT = rt

		Expect((&ConnectionReconciler{
			Client:      mgr.GetClient(),
			Connections: resolver,
			Recorder:    mgr.GetEventRecorder("uddi-connection"),
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
		Expect((&DNSPublishPolicyReconciler{
			Client: mgr.GetClient(), Runtime: rt,
			RoutesAvailable: true, DNSRecordsAvailable: true, GatewayAPIAvailable: true,
		}).SetupWithManager(mgr)).To(Succeed())
		Expect((&IPAllocationPolicyReconciler{
			Client: mgr.GetClient(), Runtime: rt, EgressIPAvailable: true, MetalLBAvailable: true,
			ClusterTargetNamespaces: NamespaceSet{mgrNS: {}},
		}).SetupWithManager(mgr)).To(Succeed())
		Expect((&HostRegistrationPolicyReconciler{Client: mgr.GetClient(), Runtime: rt, NMStateAvailable: true, Metal3Available: true}).SetupWithManager(mgr)).To(Succeed())

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

	It("writes events.k8s.io/v1 Events that name what the loop did", func() {
		secretAndConnection("event-connection")
		v := &uddiv1alpha1.View{
			ObjectMeta: metav1.ObjectMeta{Name: "event-view", Namespace: mgrNS},
			Spec: uddiv1alpha1.ViewSpec{
				CommonSpec: uddiv1alpha1.CommonSpec{
					ConnectionRef: uddiv1alpha1.ConnectionRef{Name: "event-connection"},
				},
				Name: "event-view",
			},
		}
		Expect(k8sClient.Create(ctx, v)).To(Succeed())

		Eventually(eventsFor(v.Name)).WithTimeout(settle).WithPolling(poll).Should(ContainElement(And(
			HaveField("Reason", uddisync.EventCreated),
			HaveField("Action", uddisync.ActionCreate),
			HaveField("Type", corev1.EventTypeNormal),
			HaveField("ReportingController", "uddi-operator"),
			HaveField("Note", ContainSubstring("created Universal DDI View")),
		)))
	})

	It("cuts an over-long note to the 1024 bytes events.k8s.io/v1 accepts instead of losing the Event", func() {
		v := &uddiv1alpha1.View{
			ObjectMeta: metav1.ObjectMeta{Name: "note-view", Namespace: mgrNS},
			Spec:       uddiv1alpha1.ViewSpec{Name: "note-view"},
		}
		Expect(k8sClient.Create(ctx, v)).To(Succeed())

		// The API server rejects an Event whose note is longer than 1024 bytes, and a
		// Universal DDI error message can easily be.
		mgrRT.Eventf(v, nil, corev1.EventTypeWarning, uddisync.EventAPIError, uddisync.ActionReconcile,
			"%s", strings.Repeat("x", 5000))

		Eventually(eventsFor(v.Name)).WithTimeout(settle).WithPolling(poll).Should(ContainElement(And(
			HaveField("Reason", uddisync.EventAPIError),
			HaveField("Note", HaveLen(1024)),
		)))
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

	It("verifies a Connection through the metadata-only Secret watch when its Secret appears later", func() {
		conn := &uddiv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "secret-later", Namespace: mgrNS},
			Spec: uddiv1alpha1.ConnectionSpec{
				SecretRef: uddiv1alpha1.SecretKeyRef{Name: "secret-later-key"},
			},
		}
		Expect(k8sClient.Create(ctx, conn)).To(Succeed())
		Eventually(readyCondition(conn, func() []metav1.Condition { return conn.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Reason", uddiv1alpha1.ReasonSecretNotFound))

		// The Connection's own requeue is 10 minutes away, so only the Secret watch can
		// bring it to Ready inside settle.
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "secret-later-key", Namespace: mgrNS},
			Data:       map[string][]byte{uddiv1alpha1.DefaultSecretKey: []byte("not-a-real-key")},
		})).To(Succeed())
		Eventually(readyCondition(conn, func() []metav1.Condition { return conn.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Status", metav1.ConditionTrue))
	})

	It("re-verifies a Connection promptly after a spec edit", func() {
		conn := secretAndConnection("edited-connection")
		Eventually(func() bool {
			return k8sClient.Get(ctx, client.ObjectKeyFromObject(conn), conn) == nil && connection.IsReady(conn)
		}).WithTimeout(settle).WithPolling(poll).Should(BeTrue())

		patch := client.MergeFrom(conn.DeepCopy())
		conn.Spec.DefaultTags = map[string]string{"edited": "yes"}
		Expect(k8sClient.Patch(ctx, conn, patch)).To(Succeed())
		gen := conn.Generation

		Eventually(func() bool {
			return k8sClient.Get(ctx, client.ObjectKeyFromObject(conn), conn) == nil &&
				conn.Status.ObservedGeneration == gen && connection.IsReady(conn)
		}).WithTimeout(settle).WithPolling(poll).Should(BeTrue())
	})

	It("publishes a Record for an Ingress created after the policy, purely through the watch", func() {
		view := &uddiv1alpha1.View{
			ObjectMeta: metav1.ObjectMeta{Name: "publish-view", Namespace: mgrNS},
			Spec:       uddiv1alpha1.ViewSpec{Name: "publish-view"},
		}
		Expect(k8sClient.Create(ctx, view)).To(Succeed())
		Eventually(readyCondition(view, func() []metav1.Condition { return view.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Status", metav1.ConditionTrue))

		zone := &uddiv1alpha1.AuthZone{
			ObjectMeta: metav1.ObjectMeta{Name: "publish-zone", Namespace: mgrNS},
			Spec: uddiv1alpha1.AuthZoneSpec{
				FQDN:        "apps.publish.example.com.",
				ViewRef:     uddiv1alpha1.ObjectRef{Name: view.Name},
				PrimaryType: uddiv1alpha1.AuthZonePrimaryCloud,
			},
		}
		Expect(k8sClient.Create(ctx, zone)).To(Succeed())
		Eventually(func() string {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(zone), zone); err != nil {
				return ""
			}
			return zone.Status.ID
		}).WithTimeout(settle).WithPolling(poll).ShouldNot(BeEmpty())

		policy := &uddiv1alpha1.DNSPublishPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "watch-policy", Namespace: mgrNS},
			Spec: uddiv1alpha1.DNSPublishPolicySpec{
				Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindIngress},
				ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		ingress := &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "watched-ingress", Namespace: mgrNS},
			Spec: networkingv1.IngressSpec{
				Rules: []networkingv1.IngressRule{{Host: "www.apps.publish.example.com"}},
			},
		}
		Expect(k8sClient.Create(ctx, ingress)).To(Succeed())
		ingress.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.50"}}
		Expect(k8sClient.Status().Update(ctx, ingress)).To(Succeed())

		// No direct Reconcile call on DNSPublishPolicyReconciler: the Ingress watch
		// wakes it and it creates the Record on its own.
		Eventually(func() int {
			var list uddiv1alpha1.RecordList
			if err := k8sClient.List(ctx, &list, client.InNamespace(mgrNS),
				client.MatchingLabels{publish.LabelPolicy: policy.Name}); err != nil {
				return -1
			}
			return len(list.Items)
		}).WithTimeout(settle).WithPolling(poll).Should(Equal(1))
	})

	It("creates an Address for a Service created after the IPAllocationPolicy exists, purely through the watch", func() {
		space := &uddiv1alpha1.IPSpace{
			ObjectMeta: metav1.ObjectMeta{Name: "alloc-space", Namespace: mgrNS},
			Spec:       uddiv1alpha1.IPSpaceSpec{Name: "alloc-space"},
		}
		Expect(k8sClient.Create(ctx, space)).To(Succeed())
		Eventually(readyCondition(space, func() []metav1.Condition { return space.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Status", metav1.ConditionTrue))

		rg := &uddiv1alpha1.Range{
			ObjectMeta: metav1.ObjectMeta{Name: "alloc-range", Namespace: mgrNS},
			Spec: uddiv1alpha1.RangeSpec{
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
				Start:    "10.0.60.10",
				End:      "10.0.60.20",
			},
		}
		Expect(k8sClient.Create(ctx, rg)).To(Succeed())
		Eventually(readyCondition(rg, func() []metav1.Condition { return rg.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Status", metav1.ConditionTrue))

		policy := &uddiv1alpha1.IPAllocationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "watch-alloc-policy", Namespace: mgrNS},
			Spec: uddiv1alpha1.IPAllocationPolicySpec{
				AllocateFrom: uddiv1alpha1.AllocateFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: rg.Name}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "watched-service",
				Namespace:   mgrNS,
				Annotations: map[string]string{"uddi.infoblox.com/allocate": "true"},
			},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Ports: []corev1.ServicePort{{Port: 80}}},
		}
		Expect(k8sClient.Create(ctx, svc)).To(Succeed())

		// No direct Reconcile call on IPAllocationPolicyReconciler: the Service watch
		// wakes it and it creates the Address on its own.
		Eventually(func() int {
			var list uddiv1alpha1.AddressList
			if err := k8sClient.List(ctx, &list, client.InNamespace(mgrNS),
				client.MatchingLabels{ipalloc.LabelPolicy: policy.Name}); err != nil {
				return -1
			}
			return len(list.Items)
		}).WithTimeout(settle).WithPolling(poll).Should(Equal(1))

		// The policy's event names the Service it allocated for as the related object.
		Eventually(eventsFor(policy.Name)).WithTimeout(settle).WithPolling(poll).Should(ContainElement(And(
			HaveField("Reason", "Allocated"),
			HaveField("Action", uddisync.ActionAllocate),
			HaveField("Related.Name", svc.Name),
		)))
	})

	It("allocates for an EgressIP created after the IPAllocationPolicy exists, purely through the watch", func() {
		space := &uddiv1alpha1.IPSpace{
			ObjectMeta: metav1.ObjectMeta{Name: "egress-space", Namespace: mgrNS},
			Spec:       uddiv1alpha1.IPSpaceSpec{Name: "egress-space"},
		}
		Expect(k8sClient.Create(ctx, space)).To(Succeed())
		Eventually(readyCondition(space, func() []metav1.Condition { return space.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Status", metav1.ConditionTrue))

		rg := &uddiv1alpha1.Range{
			ObjectMeta: metav1.ObjectMeta{Name: "egress-range", Namespace: mgrNS},
			Spec: uddiv1alpha1.RangeSpec{
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
				Start:    "10.0.61.10",
				End:      "10.0.61.20",
			},
		}
		Expect(k8sClient.Create(ctx, rg)).To(Succeed())
		Eventually(readyCondition(rg, func() []metav1.Condition { return rg.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Status", metav1.ConditionTrue))

		// EgressIPs are cluster-scoped, so the selector keeps this policy to its own.
		policy := &uddiv1alpha1.IPAllocationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "watch-egress-policy", Namespace: mgrNS},
			Spec: uddiv1alpha1.IPAllocationPolicySpec{
				Targets:      []uddiv1alpha1.TargetKind{uddiv1alpha1.TargetKindEgressIP},
				Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"egress-watch-test": mgrNS}},
				AllocateFrom: uddiv1alpha1.AllocateFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: rg.Name}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		eip := egressip.New()
		eip.SetName("watch-egress-" + mgrNS)
		eip.SetLabels(map[string]string{"egress-watch-test": mgrNS})
		eip.SetAnnotations(map[string]string{
			"uddi.infoblox.com/allocate": "true",
			"uddi.infoblox.com/policy":   mgrNS + "/" + policy.Name,
		})
		Expect(unstructured.SetNestedField(eip.Object, map[string]interface{}{
			"egressIPs":         []interface{}{},
			"namespaceSelector": map[string]interface{}{},
		}, "spec")).To(Succeed())
		Expect(k8sClient.Create(ctx, eip)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, eip))).To(Succeed())
		})

		// No direct Reconcile call on IPAllocationPolicyReconciler: the EgressIP watch
		// wakes it, it creates the Address, and once the Address controller allocated it,
		// writes it into spec.egressIPs.
		Eventually(func() []string {
			fresh := egressip.New()
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(eip), fresh); err != nil {
				return nil
			}
			ips, _, _ := egressip.EgressIPs(fresh)
			return ips
		}).WithTimeout(settle).WithPolling(poll).Should(Equal([]string{"10.0.61.10"}))

		var addr uddiv1alpha1.Address
		Expect(k8sClient.Get(ctx, client.ObjectKey{
			Namespace: mgrNS,
			Name:      ipalloc.SlotAddressName(policy.Name, ipalloc.TargetRef{Kind: ipalloc.TargetEgressIP, Name: eip.GetName()}, 0),
		}, &addr)).To(Succeed())
		Expect(metav1.GetControllerOf(&addr)).To(HaveField("Kind", egressip.GVK.Kind))
	})

	It("registers a Node labelled after the HostRegistrationPolicy exists, purely through the watch", func() {
		space := &uddiv1alpha1.IPSpace{
			ObjectMeta: metav1.ObjectMeta{Name: "hostreg-watch-space", Namespace: mgrNS},
			Spec:       uddiv1alpha1.IPSpaceSpec{Name: "hostreg-watch-space"},
		}
		Expect(k8sClient.Create(ctx, space)).To(Succeed())
		Eventually(readyCondition(space, func() []metav1.Condition { return space.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Status", metav1.ConditionTrue))

		policy := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "watch-hostreg-policy", Namespace: mgrNS},
			Spec: uddiv1alpha1.HostRegistrationPolicySpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"hostreg-watch-test": "true"}},
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "watch-hostreg-node",
				Labels: map[string]string{"hostreg-watch-test": "true"},
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.0.2.123"}}
		Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, node))).To(Succeed())
		})

		// No direct Reconcile call on HostRegistrationPolicyReconciler: the Node watch
		// wakes it and it creates the Address on its own.
		Eventually(func() int {
			var list uddiv1alpha1.AddressList
			if err := k8sClient.List(ctx, &list, client.InNamespace(mgrNS),
				client.MatchingLabels{hostreg.LabelPolicy: policy.Name}); err != nil {
				return -1
			}
			return len(list.Items)
		}).WithTimeout(settle).WithPolling(poll).Should(Equal(1))

		// The policy's event names the Node it registered as the related object.
		Eventually(eventsFor(policy.Name)).WithTimeout(settle).WithPolling(poll).Should(ContainElement(And(
			HaveField("Reason", "Registered"),
			HaveField("Action", uddisync.ActionRegister),
			HaveField("Related.Name", node.Name),
		)))
	})

	// readyManagedZone creates a View and an AuthZone in the manager's namespace and waits
	// for the manager's own controllers to give the zone a status.id.
	readyManagedZone := func(name, fqdn string) *uddiv1alpha1.AuthZone {
		view := &uddiv1alpha1.View{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-view", Namespace: mgrNS},
			Spec:       uddiv1alpha1.ViewSpec{Name: name + "-view"},
		}
		Expect(k8sClient.Create(ctx, view)).To(Succeed())
		Eventually(readyCondition(view, func() []metav1.Condition { return view.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Status", metav1.ConditionTrue))

		zone := &uddiv1alpha1.AuthZone{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: mgrNS},
			Spec: uddiv1alpha1.AuthZoneSpec{
				FQDN:        fqdn,
				ViewRef:     uddiv1alpha1.ObjectRef{Name: view.Name},
				PrimaryType: uddiv1alpha1.AuthZonePrimaryCloud,
			},
		}
		Expect(k8sClient.Create(ctx, zone)).To(Succeed())
		Eventually(func() string {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(zone), zone); err != nil {
				return ""
			}
			return zone.Status.ID
		}).WithTimeout(settle).WithPolling(poll).ShouldNot(BeEmpty())
		return zone
	}

	// policyRecordCount counts the Records a DNSPublishPolicy owns in the manager's namespace.
	policyRecordCount := func(policy string) func() int {
		return func() int {
			var list uddiv1alpha1.RecordList
			if err := k8sClient.List(ctx, &list, client.InNamespace(mgrNS),
				client.MatchingLabels{publish.LabelPolicy: policy}); err != nil {
				return -1
			}
			return len(list.Items)
		}
	}

	It("publishes a Record for a labelled Node created after the DNSPublishPolicy, purely through the watch", func() {
		zone := readyManagedZone("node-watch-zone", "nodes.watch.example.com.")

		policy := &uddiv1alpha1.DNSPublishPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "watch-node-policy", Namespace: mgrNS},
			Spec: uddiv1alpha1.DNSPublishPolicySpec{
				Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindNode},
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"dnspub-watch-test": "true"}},
				ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "watch-dns-node.nodes.watch.example.com",
				Labels: map[string]string{"dnspub-watch-test": "true"},
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, node))).To(Succeed())
		})
		node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.0.2.124"}}
		Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())

		// No direct Reconcile call: the Node watch wakes the policy, which lists the Node.
		Eventually(policyRecordCount(policy.Name)).WithTimeout(settle).WithPolling(poll).Should(Equal(1))
	})

	It("publishes an HTTPRoute hostname once its Gateway reports an address, purely through the Gateway watch", func() {
		zone := readyManagedZone("gw-watch-zone", "gw.watch.example.com.")

		policy := &uddiv1alpha1.DNSPublishPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "watch-httproute-policy", Namespace: mgrNS},
			Spec: uddiv1alpha1.DNSPublishPolicySpec{
				Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindHTTPRoute},
				ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		gw := &unstructured.Unstructured{}
		gw.SetGroupVersionKind(gatewayapi.GatewayGVK)
		gw.SetNamespace(mgrNS)
		gw.SetName("watched-gateway")
		Expect(unstructured.SetNestedField(gw.Object, "example", "spec", "gatewayClassName")).To(Succeed())
		Expect(k8sClient.Create(ctx, gw)).To(Succeed())

		route := &unstructured.Unstructured{}
		route.SetGroupVersionKind(gatewayapi.HTTPRouteGVK)
		route.SetNamespace(mgrNS)
		route.SetName("watched-route")
		Expect(unstructured.SetNestedStringSlice(route.Object, []string{"web.gw.watch.example.com"}, "spec", "hostnames")).To(Succeed())
		Expect(unstructured.SetNestedSlice(route.Object, []interface{}{
			map[string]interface{}{"name": gw.GetName()},
		}, "spec", "parentRefs")).To(Succeed())
		Expect(k8sClient.Create(ctx, route)).To(Succeed())
		Expect(unstructured.SetNestedSlice(route.Object, []interface{}{map[string]interface{}{
			"parentRef":      map[string]interface{}{"name": gw.GetName()},
			"controllerName": "example.com/gateway-controller",
			"conditions": []interface{}{map[string]interface{}{
				"type": "Accepted", "status": "True", "reason": "Accepted", "message": "",
				"lastTransitionTime": "2026-10-04T00:00:00Z",
			}},
		}}, "status", "parents")).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, route)).To(Succeed())

		// The route is accepted but its Gateway has no address, so the policy skips it.
		Eventually(func() []uddiv1alpha1.SkippedSource {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy); err != nil {
				return nil
			}
			return policy.Status.Skipped
		}).WithTimeout(settle).WithPolling(poll).Should(ContainElement(And(
			HaveField("Source", "HTTPRoute/watched-route"),
			HaveField("Reason", "NoTarget"),
		)))
		Expect(policyRecordCount(policy.Name)()).To(Equal(0))

		// The address arrives on the Gateway alone; only the Gateway watch can notice.
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(gw), gw)).To(Succeed())
		Expect(unstructured.SetNestedSlice(gw.Object, []interface{}{
			map[string]interface{}{"type": "IPAddress", "value": "192.0.2.125"},
		}, "status", "addresses")).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, gw)).To(Succeed())

		Eventually(policyRecordCount(policy.Name)).WithTimeout(settle).WithPolling(poll).Should(Equal(1))
	})

	It("switches a registration from an Address to a FixedAddress when a BareMetalHost gains hardware details, purely through the watch", func() {
		space := &uddiv1alpha1.IPSpace{
			ObjectMeta: metav1.ObjectMeta{Name: "metal3-watch-space", Namespace: mgrNS},
			Spec:       uddiv1alpha1.IPSpaceSpec{Name: "metal3-watch-space"},
		}
		Expect(k8sClient.Create(ctx, space)).To(Succeed())
		Eventually(readyCondition(space, func() []metav1.Condition { return space.Status.Conditions })).
			WithTimeout(settle).WithPolling(poll).
			Should(HaveField("Status", metav1.ConditionTrue))

		// The manager's cache covers this file's namespace only, so the BareMetalHost lives
		// here and the policy reads it through macSource.metal3.namespace.
		policy := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "watch-metal3-policy", Namespace: mgrNS},
			Spec: uddiv1alpha1.HostRegistrationPolicySpec{
				Selector:  &metav1.LabelSelector{MatchLabels: map[string]string{"hostreg-metal3-watch": "true"}},
				SpaceRef:  uddiv1alpha1.ObjectRef{Name: space.Name},
				MACSource: uddiv1alpha1.MACSource{Metal3: uddiv1alpha1.Metal3Source{Namespace: mgrNS}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "watch-metal3-node",
				Labels: map[string]string{"hostreg-metal3-watch": "true"},
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.0.2.150"}}
		Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, node))).To(Succeed())
		})

		registrationName := hostreg.RegistrationName(policy.Name, node.Name)
		addressOf := func() error {
			return k8sClient.Get(ctx, client.ObjectKey{Namespace: mgrNS, Name: registrationName}, &uddiv1alpha1.Address{})
		}
		fixedOf := func() (*uddiv1alpha1.FixedAddress, error) {
			var fa uddiv1alpha1.FixedAddress
			err := k8sClient.Get(ctx, client.ObjectKey{Namespace: mgrNS, Name: registrationName}, &fa)
			return &fa, err
		}

		// No MAC is known yet, so the Node registers as an Address.
		Eventually(addressOf).WithTimeout(settle).WithPolling(poll).Should(Succeed())

		host := &unstructured.Unstructured{}
		host.SetGroupVersionKind(metal3api.GVK)
		host.SetNamespace(mgrNS)
		host.SetName("watch-metal3-host")
		Expect(k8sClient.Create(ctx, host)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, host))).To(Succeed())
		})
		hardware := map[string]interface{}{
			"nics": []interface{}{map[string]interface{}{"name": "eno1", "mac": "52:55:00:D1:55:F1", "ip": "192.0.2.150"}},
		}
		Expect(unstructured.SetNestedMap(host.Object, hardware, "status", "hardware")).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, host)).To(Succeed())

		// No direct Reconcile call: the BareMetalHost watch wakes the policy, which now finds
		// a MAC, releases the Address and applies a FixedAddress.
		Eventually(func() string {
			fa, err := fixedOf()
			if err != nil {
				return ""
			}
			return fa.Spec.MatchValue
		}).WithTimeout(settle).WithPolling(poll).Should(Equal("52:55:00:d1:55:f1"))
		Eventually(func() bool {
			err := addressOf()
			if apierrors.IsNotFound(err) {
				return true
			}
			// The Address controller's finalizer may still be removing it from Universal DDI.
			var a uddiv1alpha1.Address
			return k8sClient.Get(ctx, client.ObjectKey{Namespace: mgrNS, Name: registrationName}, &a) == nil &&
				!a.DeletionTimestamp.IsZero()
		}).WithTimeout(settle).WithPolling(poll).Should(BeTrue())
	})
})
