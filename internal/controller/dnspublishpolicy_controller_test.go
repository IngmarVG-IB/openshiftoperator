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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	routev1 "github.com/openshift/api/route/v1"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/publish"
)

// testRouterCanonicalHostname is the routerCanonicalHostname every admittedRouteIngress
// in this suite reports, matching the CNAME target the Route specs below expect.
const testRouterCanonicalHostname = "router-default.apps.example.com"

// admittedRouteIngress builds a RouteIngress admitted by a router, the shape
// routeAdmittedCanonicalHostname (internal/publish/sources_route.go) looks for.
func admittedRouteIngress() routev1.RouteIngress {
	return routev1.RouteIngress{
		RouterName:              "default",
		RouterCanonicalHostname: testRouterCanonicalHostname,
		Conditions: []routev1.RouteIngressCondition{{
			Type:   routev1.RouteAdmitted,
			Status: corev1.ConditionTrue,
		}},
	}
}

var _ = Describe("DNSPublishPolicy controller", func() {
	var (
		ns         string
		reconciler *DNSPublishPolicyReconciler
		viewRec    *ViewReconciler
		zoneRec    *AuthZoneReconciler
		recordRec  *RecordReconciler
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		reconciler = &DNSPublishPolicyReconciler{Client: k8sClient, Runtime: testRuntime, RoutesAvailable: true}
		viewRec = &ViewReconciler{Client: k8sClient, Runtime: testRuntime}
		zoneRec = &AuthZoneReconciler{Client: k8sClient, Runtime: testRuntime}
		recordRec = &RecordReconciler{Client: k8sClient, Runtime: testRuntime}
	})

	// makeZone creates a View and an AuthZone CR for it but does not reconcile the
	// zone, so it starts life without a status.id.
	makeZone := func(name, fqdn string) *uddiv1alpha1.AuthZone {
		v := &uddiv1alpha1.View{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-view", Namespace: ns},
			Spec:       uddiv1alpha1.ViewSpec{Name: name + "-view"},
		}
		Expect(k8sClient.Create(ctx, v)).To(Succeed())
		_, err := viewRec.Reconcile(ctx, requestFor(v))
		Expect(err).NotTo(HaveOccurred())

		z := &uddiv1alpha1.AuthZone{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.AuthZoneSpec{
				FQDN:        fqdn,
				ViewRef:     uddiv1alpha1.ObjectRef{Name: v.Name},
				PrimaryType: uddiv1alpha1.AuthZonePrimaryCloud,
			},
		}
		Expect(k8sClient.Create(ctx, z)).To(Succeed())
		return z
	}

	// readyZone is makeZone plus a reconcile, so the zone carries a status.id.
	readyZone := func(name, fqdn string) *uddiv1alpha1.AuthZone {
		z := makeZone(name, fqdn)
		_, err := zoneRec.Reconcile(ctx, requestFor(z))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(z), z)).To(Succeed())
		return z
	}

	reconcilePolicy := func(p *uddiv1alpha1.DNSPublishPolicy) error {
		_, err := reconciler.Reconcile(ctx, requestFor(p))
		if err == nil {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
		}
		return err
	}

	policyRecords := func(p *uddiv1alpha1.DNSPublishPolicy) []uddiv1alpha1.Record {
		var list uddiv1alpha1.RecordList
		Expect(k8sClient.List(ctx, &list, client.InNamespace(ns),
			client.MatchingLabels{publish.LabelPolicy: p.Name})).To(Succeed())
		return list.Items
	}

	readyConditionOf := func(p *uddiv1alpha1.DNSPublishPolicy) *metav1.Condition {
		return meta.FindStatusCondition(p.Status.Conditions, uddiv1alpha1.ConditionReady)
	}

	Context("with a Ready Connection and zone", func() {
		var zone *uddiv1alpha1.AuthZone

		BeforeEach(func() {
			makeReadyConnection(ns)
			zone = readyZone("apps-zone", "apps.example.com.")
			drainEvents()
		})

		It("publishes a CNAME Record for an admitted Route and becomes Ready once the Record syncs", func() {
			route := &routev1.Route{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
				Spec: routev1.RouteSpec{
					Host: "web.apps.example.com",
					To:   routev1.RouteTargetReference{Kind: "Service", Name: "web"},
				},
			}
			Expect(k8sClient.Create(ctx, route)).To(Succeed())
			route.Status.Ingress = []routev1.RouteIngress{admittedRouteIngress()}
			Expect(k8sClient.Status().Update(ctx, route)).To(Succeed())

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "web-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindRoute},
					ZoneRefs: []uddiv1alpha1.ObjectRef{{Name: zone.Name}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			Expect(reconcilePolicy(policy)).To(Succeed())
			Expect(policy.Status.Published).To(Equal(int32(1)))
			Expect(policy.Status.Ready).To(Equal(int32(0)))
			Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionFalse))
			Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonRecordsNotReady))

			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			rec := records[0]

			Expect(rec.Labels[publish.LabelPolicy]).To(Equal(policy.Name))
			Expect(rec.Labels[publish.LabelSourceKind]).To(Equal(string(publish.SourceRoute)))
			Expect(rec.Labels[publish.LabelSourceName]).To(Equal(route.Name))

			owner := metav1.GetControllerOf(&rec)
			Expect(owner).NotTo(BeNil())
			Expect(owner.Name).To(Equal(policy.Name))
			Expect(owner.Controller).NotTo(BeNil())
			Expect(*owner.Controller).To(BeTrue())
			Expect(owner.BlockOwnerDeletion).NotTo(BeNil())
			Expect(*owner.BlockOwnerDeletion).To(BeTrue())

			Expect(rec.Spec.ZoneRef.Name).To(Equal(zone.Name))
			Expect(rec.Spec.Name).To(Equal("web"))
			Expect(rec.Spec.Type).To(Equal(uddiv1alpha1.RecordType("CNAME")))
			Expect(rec.Spec.RData.CNAME).NotTo(BeNil())
			Expect(rec.Spec.RData.CNAME.CNAME).To(Equal("router-default.apps.example.com."))
			Expect(rec.Spec.TTL).NotTo(BeNil())
			Expect(*rec.Spec.TTL).To(Equal(int32(300)))

			_, err := recordRec.Reconcile(ctx, requestFor(&rec))
			Expect(err).NotTo(HaveOccurred())

			Expect(reconcilePolicy(policy)).To(Succeed())
			Expect(policy.Status.Ready).To(Equal(int32(1)))
			Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionTrue))
			Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonPublished))
		})

		It("publishes one A Record per Ingress load balancer IP and prunes a Record when one is removed", func() {
			ingress := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
				Spec: networkingv1.IngressSpec{
					Rules: []networkingv1.IngressRule{{Host: "www.apps.example.com"}},
				},
			}
			Expect(k8sClient.Create(ctx, ingress)).To(Succeed())
			ingress.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{
				{IP: "192.0.2.10"}, {IP: "192.0.2.11"},
			}
			Expect(k8sClient.Status().Update(ctx, ingress)).To(Succeed())

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "ingress-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindIngress},
					ZoneRefs: []uddiv1alpha1.ObjectRef{{Name: zone.Name}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			Expect(policy.Status.Published).To(Equal(int32(2)))
			Expect(policyRecords(policy)).To(HaveLen(2))

			ingress.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.10"}}
			Expect(k8sClient.Status().Update(ctx, ingress)).To(Succeed())

			Expect(reconcilePolicy(policy)).To(Succeed())
			Expect(policy.Status.Published).To(Equal(int32(1)))
			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			Expect(records[0].Spec.RData.A.Address).To(Equal("192.0.2.10"))
		})

		It("publishes a LoadBalancer Service with the hostname annotation and skips the others", func() {
			lbWithHostname := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "lb-with-host",
					Namespace:   ns,
					Annotations: map[string]string{"uddi.infoblox.com/hostname": "svc.apps.example.com"},
				},
				Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Ports: []corev1.ServicePort{{Port: 80}}},
			}
			Expect(k8sClient.Create(ctx, lbWithHostname)).To(Succeed())
			lbWithHostname.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "192.0.2.20"}}
			Expect(k8sClient.Status().Update(ctx, lbWithHostname)).To(Succeed())

			lbNoHostname := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "lb-no-host", Namespace: ns},
				Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Ports: []corev1.ServicePort{{Port: 80}}},
			}
			Expect(k8sClient.Create(ctx, lbNoHostname)).To(Succeed())

			clusterIP := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "cluster-ip",
					Namespace:   ns,
					Annotations: map[string]string{"uddi.infoblox.com/hostname": "internal.apps.example.com"},
				},
				Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 80}}},
			}
			Expect(k8sClient.Create(ctx, clusterIP)).To(Succeed())

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "svc-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindService},
					ZoneRefs: []uddiv1alpha1.ObjectRef{{Name: zone.Name}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			Expect(policy.Status.Published).To(Equal(int32(1)))
			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			Expect(records[0].Labels[publish.LabelSourceName]).To(Equal("lb-with-host"))

			reasons := map[string]string{}
			for _, sk := range policy.Status.Skipped {
				reasons[sk.Source] = sk.Reason
			}
			Expect(reasons).To(HaveKeyWithValue("Service/lb-no-host", "NoHostname"))
			Expect(reasons).To(HaveKeyWithValue("Service/cluster-ip", "NotLoadBalancer"))
		})

		It("excludes a source annotated publish=false and skips a hostname outside every zone", func() {
			excluded := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "excluded",
					Namespace:   ns,
					Annotations: map[string]string{"uddi.infoblox.com/publish": "false"},
				},
				Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "excluded.apps.example.com"}}},
			}
			Expect(k8sClient.Create(ctx, excluded)).To(Succeed())

			outside := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: "outside", Namespace: ns},
				Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "outside.other.example.com"}}},
			}
			Expect(k8sClient.Create(ctx, outside)).To(Succeed())
			outside.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.30"}}
			Expect(k8sClient.Status().Update(ctx, outside)).To(Succeed())

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "excl-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindIngress},
					ZoneRefs: []uddiv1alpha1.ObjectRef{{Name: zone.Name}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			Expect(policy.Status.Published).To(Equal(int32(0)))
			reasons := make([]string, 0, len(policy.Status.Skipped))
			for _, sk := range policy.Status.Skipped {
				reasons = append(reasons, sk.Reason)
			}
			Expect(reasons).To(ContainElements("Excluded", "NoMatchingZone"))

			Expect(collectEvents()).To(ContainElement(ContainSubstring("Skipped")))
		})

		It("ignores a source that does not match spec.selector", func() {
			labeled := &routev1.Route{
				ObjectMeta: metav1.ObjectMeta{Name: "labeled", Namespace: ns, Labels: map[string]string{"dns": "public"}},
				Spec: routev1.RouteSpec{
					Host: "labeled.apps.example.com",
					To:   routev1.RouteTargetReference{Kind: "Service", Name: "labeled"},
				},
			}
			Expect(k8sClient.Create(ctx, labeled)).To(Succeed())
			labeled.Status.Ingress = []routev1.RouteIngress{admittedRouteIngress()}
			Expect(k8sClient.Status().Update(ctx, labeled)).To(Succeed())

			unlabeled := &routev1.Route{
				ObjectMeta: metav1.ObjectMeta{Name: "unlabeled", Namespace: ns},
				Spec: routev1.RouteSpec{
					Host: "unlabeled.apps.example.com",
					To:   routev1.RouteTargetReference{Kind: "Service", Name: "unlabeled"},
				},
			}
			Expect(k8sClient.Create(ctx, unlabeled)).To(Succeed())
			unlabeled.Status.Ingress = []routev1.RouteIngress{admittedRouteIngress()}
			Expect(k8sClient.Status().Update(ctx, unlabeled)).To(Succeed())

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "sel-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindRoute},
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"dns": "public"}},
					ZoneRefs: []uddiv1alpha1.ObjectRef{{Name: zone.Name}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			Expect(policy.Status.Published).To(Equal(int32(1)))
			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			Expect(records[0].Labels[publish.LabelSourceName]).To(Equal("labeled"))
		})

		It("ignores Route sources without error when RoutesAvailable is false", func() {
			route := &routev1.Route{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
				Spec: routev1.RouteSpec{
					Host: "web.apps.example.com",
					To:   routev1.RouteTargetReference{Kind: "Service", Name: "web"},
				},
			}
			Expect(k8sClient.Create(ctx, route)).To(Succeed())
			route.Status.Ingress = []routev1.RouteIngress{admittedRouteIngress()}
			Expect(k8sClient.Status().Update(ctx, route)).To(Succeed())

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "noroute-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					ZoneRefs: []uddiv1alpha1.ObjectRef{{Name: zone.Name}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			noRoutes := &DNSPublishPolicyReconciler{Client: k8sClient, Runtime: testRuntime, RoutesAvailable: false}
			_, err := noRoutes.Reconcile(ctx, requestFor(policy))
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)).To(Succeed())

			Expect(policy.Status.Published).To(Equal(int32(0)))
			Expect(policyRecords(policy)).To(BeEmpty())
		})

		It("uses target.addresses for a Route instead of the canonical hostname CNAME", func() {
			route := &routev1.Route{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
				Spec: routev1.RouteSpec{
					Host: "web.apps.example.com",
					To:   routev1.RouteTargetReference{Kind: "Service", Name: "web"},
				},
			}
			Expect(k8sClient.Create(ctx, route)).To(Succeed())
			route.Status.Ingress = []routev1.RouteIngress{admittedRouteIngress()}
			Expect(k8sClient.Status().Update(ctx, route)).To(Succeed())

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "static-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindRoute},
					ZoneRefs: []uddiv1alpha1.ObjectRef{{Name: zone.Name}},
					Target:   uddiv1alpha1.DNSPublishTarget{Addresses: []string{"192.0.2.99"}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			Expect(records[0].Spec.Type).To(Equal(uddiv1alpha1.RecordType("A")))
			Expect(records[0].Spec.RData.A).NotTo(BeNil())
			Expect(records[0].Spec.RData.A.Address).To(Equal("192.0.2.99"))
		})

		It("re-applies a Record via server-side apply when the policy's TTL changes", func() {
			ingress := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
				Spec: networkingv1.IngressSpec{
					Rules: []networkingv1.IngressRule{{Host: "www.apps.example.com"}},
				},
			}
			Expect(k8sClient.Create(ctx, ingress)).To(Succeed())
			ingress.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.50"}}
			Expect(k8sClient.Status().Update(ctx, ingress)).To(Succeed())

			ttl := int32(120)
			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "ttl-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindIngress},
					ZoneRefs: []uddiv1alpha1.ObjectRef{{Name: zone.Name}},
					TTL:      &ttl,
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			Expect(*records[0].Spec.TTL).To(Equal(int32(120)))
			recordName := records[0].Name

			newTTL := int32(600)
			policy.Spec.TTL = &newTTL
			Expect(k8sClient.Update(ctx, policy)).To(Succeed())

			Expect(reconcilePolicy(policy)).To(Succeed())

			var updated uddiv1alpha1.Record
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: recordName}, &updated)).To(Succeed())
			Expect(*updated.Spec.TTL).To(Equal(int32(600)))
		})
	})

	It("reports ZonesNotReady and no Records while the AuthZone has no status.id, then publishes once it does", func() {
		makeReadyConnection(ns)
		zone := makeZone("pending-zone", "apps.example.com.")

		ingress := &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
			Spec: networkingv1.IngressSpec{
				Rules: []networkingv1.IngressRule{{Host: "www.apps.example.com"}},
			},
		}
		Expect(k8sClient.Create(ctx, ingress)).To(Succeed())
		ingress.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.40"}}
		Expect(k8sClient.Status().Update(ctx, ingress)).To(Succeed())

		policy := &uddiv1alpha1.DNSPublishPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "pending-policy", Namespace: ns},
			Spec: uddiv1alpha1.DNSPublishPolicySpec{
				Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindIngress},
				ZoneRefs: []uddiv1alpha1.ObjectRef{{Name: zone.Name}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		Expect(policy.Status.Published).To(Equal(int32(0)))
		Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionFalse))
		Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonZonesNotReady))
		Expect(policyRecords(policy)).To(BeEmpty())

		_, err := zoneRec.Reconcile(ctx, requestFor(zone))
		Expect(err).NotTo(HaveOccurred())

		Expect(reconcilePolicy(policy)).To(Succeed())
		Expect(policy.Status.Published).To(Equal(int32(1)))
		Expect(policyRecords(policy)).To(HaveLen(1))
	})

	Context("ZonesResolved", func() {
		zonesResolvedOf := func(p *uddiv1alpha1.DNSPublishPolicy) *metav1.Condition {
			return meta.FindStatusCondition(p.Status.Conditions, uddiv1alpha1.ConditionZonesResolved)
		}

		makeIngress := func(name, host, ip string) {
			ing := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: host}}},
			}
			Expect(k8sClient.Create(ctx, ing)).To(Succeed())
			ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: ip}}
			Expect(k8sClient.Status().Update(ctx, ing)).To(Succeed())
		}

		It("names a missing zoneRef without changing Ready, and turns True once the AuthZone exists", func() {
			makeReadyConnection(ns)
			zone := readyZone("apps-zone", "apps.example.com.")
			makeIngress("web", "www.apps.example.com", "192.0.2.60")
			drainEvents()

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "partial-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindIngress},
					ZoneRefs: []uddiv1alpha1.ObjectRef{{Name: zone.Name}, {Name: "corp-zone"}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			// Ready follows the working zone exactly as it would without the bad ref.
			Expect(policy.Status.Published).To(Equal(int32(1)))
			Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionFalse))
			Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonRecordsNotReady))

			resolved := zonesResolvedOf(policy)
			Expect(resolved).NotTo(BeNil())
			Expect(resolved.Status).To(Equal(metav1.ConditionFalse))
			Expect(resolved.Reason).To(Equal(uddiv1alpha1.ReasonZoneNotFound))
			Expect(resolved.Message).To(Equal("zoneRefs not found: corp-zone"))
			Expect(resolved.ObservedGeneration).To(Equal(policy.Generation))

			events := collectEvents()
			Expect(events).To(ContainElement(And(
				ContainSubstring("Warning"),
				ContainSubstring(uddiv1alpha1.ReasonZoneNotFound),
				ContainSubstring("corp-zone"),
			)))

			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			_, err := recordRec.Reconcile(ctx, requestFor(&records[0]))
			Expect(err).NotTo(HaveOccurred())

			Expect(reconcilePolicy(policy)).To(Succeed())
			Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionTrue))
			Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonPublished))
			Expect(zonesResolvedOf(policy).Status).To(Equal(metav1.ConditionFalse))

			// The AuthZone only has to exist; it does not have to be Ready yet.
			makeZone("corp-zone", "corp.example.com.")
			drainEvents()

			Expect(reconcilePolicy(policy)).To(Succeed())
			resolved = zonesResolvedOf(policy)
			Expect(resolved.Status).To(Equal(metav1.ConditionTrue))
			Expect(resolved.Reason).To(Equal(uddiv1alpha1.ReasonZonesFound))
			Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionTrue))
			Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonPublished))
			Expect(collectEvents()).NotTo(ContainElement(ContainSubstring(uddiv1alpha1.ReasonZoneNotFound)))
		})

		It("lists every missing zoneRef once, sorted, and keeps Ready=False/ZonesNotReady when none resolves", func() {
			makeReadyConnection(ns)
			drainEvents()

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "missing-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources: []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindIngress},
					ZoneRefs: []uddiv1alpha1.ObjectRef{
						{Name: "zz-zone"}, {Name: "aa-zone"}, {Name: "zz-zone"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionFalse))
			Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonZonesNotReady))

			resolved := zonesResolvedOf(policy)
			Expect(resolved.Status).To(Equal(metav1.ConditionFalse))
			Expect(resolved.Message).To(Equal("zoneRefs not found: aa-zone, zz-zone"))

			var zoneNotFound []string
			for _, e := range collectEvents() {
				if strings.Contains(e, uddiv1alpha1.ReasonZoneNotFound) {
					zoneNotFound = append(zoneNotFound, e)
				}
			}
			Expect(zoneNotFound).To(HaveLen(1))
			Expect(zoneNotFound[0]).To(ContainSubstring("zoneRefs not found: aa-zone, zz-zone"))
		})
	})
})
