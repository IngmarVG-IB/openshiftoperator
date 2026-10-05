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
	operatoringressv1 "github.com/openshift/api/operatoringress/v1"
	routev1 "github.com/openshift/api/route/v1"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/gatewayapi"
	"github.com/IngmarVG-IB/openshiftoperator/internal/labelvalue"
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
		reconciler = &DNSPublishPolicyReconciler{
			Client: k8sClient, Runtime: testRuntime,
			RoutesAvailable: true, DNSRecordsAvailable: true, GatewayAPIAvailable: true,
		}
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
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
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
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
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

		It("removes a field from a generated Record when the policy stops setting it", func() {
			ingress := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: "ssa", Namespace: ns},
				Spec: networkingv1.IngressSpec{
					Rules: []networkingv1.IngressRule{{Host: "ssa.apps.example.com"}},
				},
			}
			Expect(k8sClient.Create(ctx, ingress)).To(Succeed())
			ingress.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.40"}}
			Expect(k8sClient.Status().Update(ctx, ingress)).To(Succeed())

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "ssa-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindIngress},
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
					Tags:     map[string]string{"team": "net", "env": "prod"},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			recordName := records[0].Name
			Expect(records[0].Spec.Tags).To(HaveKeyWithValue("env", "prod"))
			Expect(appliedBy(&records[0], publish.FieldManager)).To(BeTrue())

			// The policy drops a tag. Server-side apply removes exactly the fields this field
			// manager owned and no longer sends, and leaves the rest alone.
			policy.Spec.Tags = map[string]string{"team": "net"}
			Expect(k8sClient.Update(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			records = policyRecords(policy)
			Expect(records).To(HaveLen(1))
			Expect(records[0].Name).To(Equal(recordName))
			Expect(records[0].Spec.Tags).To(HaveKeyWithValue("team", "net"))
			Expect(records[0].Spec.Tags).NotTo(HaveKey("env"))
			Expect(records[0].Spec.Tags).To(HaveKey(publish.TagSource))
		})

		It("rejects a policy name longer than a label value", func() {
			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("p", 64), Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
				},
			}
			err := k8sClient.Create(ctx, policy)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("limited to 63 characters"))
		})

		It("publishes a source whose name is longer than a label value", func() {
			longName := strings.Repeat("very-long-ingress-name-", 4) + "x"
			Expect(len(longName)).To(BeNumerically(">", 63))
			ingress := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: longName, Namespace: ns},
				Spec: networkingv1.IngressSpec{
					Rules: []networkingv1.IngressRule{{Host: "long.apps.example.com"}},
				},
			}
			Expect(k8sClient.Create(ctx, ingress)).To(Succeed())
			ingress.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.42"}}
			Expect(k8sClient.Status().Update(ctx, ingress)).To(Succeed())

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "long-source", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindIngress},
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			label := records[0].Labels[publish.LabelSourceName]
			Expect(len(label)).To(BeNumerically("<=", 63))
			Expect(label).To(HavePrefix("very-long-ingress-name-"))
		})

		It("clears connectionRef and lifecycle on generated Records when the policy stops setting them", func() {
			// On Kubernetes 1.29 an apply that sent connectionRef: {} after an earlier apply set
			// connectionRef.name failed with a 422; the envtest floor lane proves it no longer does.
			ingress := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: "clear", Namespace: ns},
				Spec: networkingv1.IngressSpec{
					Rules: []networkingv1.IngressRule{{Host: "clear.apps.example.com"}},
				},
			}
			Expect(k8sClient.Create(ctx, ingress)).To(Succeed())
			ingress.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.41"}}
			Expect(k8sClient.Status().Update(ctx, ingress)).To(Succeed())

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "clear-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					ConnectionRef:   uddiv1alpha1.ConnectionRef{Name: uddiv1alpha1.DefaultConnectionName},
					Sources:         []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindIngress},
					ZoneRefs:        []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
					RecordLifecycle: uddiv1alpha1.Lifecycle{DeletionPolicy: uddiv1alpha1.DeletionPolicyOrphan},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			Expect(records[0].Spec.ConnectionRef.Name).To(Equal(uddiv1alpha1.DefaultConnectionName))
			Expect(records[0].Spec.Lifecycle.DeletionPolicy).To(Equal(uddiv1alpha1.DeletionPolicyOrphan))

			policy.Spec.ConnectionRef = uddiv1alpha1.ConnectionRef{}
			policy.Spec.RecordLifecycle = uddiv1alpha1.Lifecycle{}
			Expect(k8sClient.Update(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			records = policyRecords(policy)
			Expect(records).To(HaveLen(1))
			Expect(records[0].Spec.ConnectionRef.Name).To(BeEmpty())
			Expect(records[0].Spec.Lifecycle.DeletionPolicy).NotTo(Equal(uddiv1alpha1.DeletionPolicyOrphan))
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
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
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
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
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
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
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
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
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
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
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
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
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
				ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
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
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}, {Name: "corp-zone"}},
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
					ZoneRefs: []uddiv1alpha1.ZoneRef{
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

	// skipReasons maps "Kind/name" to the reason of its skip, for the specs below.
	skipReasons := func(p *uddiv1alpha1.DNSPublishPolicy) map[string]string {
		out := map[string]string{}
		for _, sk := range p.Status.Skipped {
			out[sk.Source] = sk.Reason
		}
		return out
	}

	// recordsByType indexes a policy's Records by DNS record type.
	recordsByType := func(p *uddiv1alpha1.DNSPublishPolicy) map[uddiv1alpha1.RecordType]uddiv1alpha1.Record {
		out := map[uddiv1alpha1.RecordType]uddiv1alpha1.Record{}
		for _, rec := range policyRecords(p) {
			out[rec.Spec.Type] = rec
		}
		return out
	}

	Context("Node source", func() {
		// makeNode creates a Node with the given addresses. Nodes are cluster-scoped and
		// outlive the namespace, so every spec labels its own and deletes it again.
		makeNode := func(name string, lbls map[string]string, addresses ...corev1.NodeAddress) {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls}}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, node))).To(Succeed())
			})
			node.Status.Addresses = addresses
			Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
		}

		It("publishes an A and an AAAA Record with createPtr for a dual-stack Node, and follows nodeAddressType", func() {
			makeReadyConnection(ns)
			zone := readyZone("nodes-zone", "nodes.example.com.")
			selector := map[string]string{"dnspub-node-test": ns}
			nodeName := "worker-" + ns + ".nodes.example.com"
			makeNode(nodeName, selector,
				corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "192.0.2.21"},
				corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "2001:db8::21"},
				corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "203.0.113.9"},
				corev1.NodeAddress{Type: corev1.NodeHostName, Address: "worker"})
			// A Node name without a dot is not a DNS name and has no hostname annotation.
			makeNode("short-"+ns, selector, corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "192.0.2.22"})
			// A Node the selector does not match is not listed at all.
			makeNode("other-"+ns+".nodes.example.com", map[string]string{"dnspub-node-test": "other"},
				corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "192.0.2.23"})

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "node-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:   []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindNode},
					Selector:  &metav1.LabelSelector{MatchLabels: selector},
					ZoneRefs:  []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
					CreatePtr: true,
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			Expect(policy.Status.Published).To(Equal(int32(2)))
			records := recordsByType(policy)
			Expect(records).To(HaveLen(2))
			a, aaaa := records[uddiv1alpha1.RecordType("A")], records[uddiv1alpha1.RecordType("AAAA")]
			Expect(a.Spec.RData.A).NotTo(BeNil())
			Expect(a.Spec.RData.A.Address).To(Equal("192.0.2.21"))
			Expect(aaaa.Spec.RData.AAAA).NotTo(BeNil())
			Expect(aaaa.Spec.RData.AAAA.Address).To(Equal("2001:db8::21"))
			for _, rec := range []uddiv1alpha1.Record{a, aaaa} {
				Expect(rec.Spec.Name).To(Equal("worker-" + ns))
				Expect(rec.Spec.ZoneRef.Name).To(Equal(zone.Name))
				Expect(rec.Spec.CreatePtr).NotTo(BeNil())
				Expect(*rec.Spec.CreatePtr).To(BeTrue())
				Expect(rec.Labels[publish.LabelSourceKind]).To(Equal(string(publish.SourceNode)))
				Expect(rec.Labels[publish.LabelSourceName]).To(Equal(labelvalue.For(nodeName)))
			}
			Expect(skipReasons(policy)).To(Equal(map[string]string{"Node/short-" + ns: "NoHostname"}))

			// ExternalIP selects the other address, and the Records of the first pass are pruned.
			policy.Spec.NodeAddressType = uddiv1alpha1.NodeAddressTypeExternalIP
			Expect(k8sClient.Update(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			Expect(policy.Status.Published).To(Equal(int32(1)))
			records = recordsByType(policy)
			Expect(records).To(HaveLen(1))
			Expect(records[uddiv1alpha1.RecordType("A")].Spec.RData.A.Address).To(Equal("203.0.113.9"))
		})

		It("leaves createPtr unset on the Records when the policy does not ask for it", func() {
			makeReadyConnection(ns)
			zone := readyZone("nodes-zone", "nodes.example.com.")
			selector := map[string]string{"dnspub-node-test": ns}
			makeNode("plain-"+ns+".nodes.example.com", selector,
				corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "192.0.2.24"})

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "node-noptr-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindNode},
					Selector: &metav1.LabelSelector{MatchLabels: selector},
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			Expect(records[0].Spec.CreatePtr).To(BeNil())
		})
	})

	Context("DNSRecord source", func() {
		// makeDNSRecord creates an ingress operator DNSRecord, creating its namespace first
		// when no other spec has, and deletes it again.
		makeDNSRecord := func(namespace, name string, lbls map[string]string, spec operatoringressv1.DNSRecordSpec) {
			Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx,
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))).To(Succeed())
			rec := &operatoringressv1.DNSRecord{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: lbls},
				Spec:       spec,
			}
			Expect(k8sClient.Create(ctx, rec)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, rec))).To(Succeed())
			})
		}

		wildcardSpec := operatoringressv1.DNSRecordSpec{
			DNSName:    "*.apps.example.com.",
			Targets:    []string{"192.0.2.10"},
			RecordType: operatoringressv1.ARecordType,
			RecordTTL:  30,
		}

		It("publishes the default router wildcard from openshift-ingress-operator as a wildcard Record", func() {
			makeReadyConnection(ns)
			zone := readyZone("apps-zone", "apps.example.com.")
			selector := map[string]string{"dnspub-dnsrecord-test": ns}
			makeDNSRecord(uddiv1alpha1.DefaultDNSRecordNamespace, "default-wildcard", selector, wildcardSpec)

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "dnsrecord-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindDNSRecord},
					Selector: &metav1.LabelSelector{MatchLabels: selector},
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			// The CRD default names the namespace the ingress operator uses.
			Expect(policy.Spec.DNSRecordNamespace).To(Equal(uddiv1alpha1.DefaultDNSRecordNamespace))
			Expect(reconcilePolicy(policy)).To(Succeed())

			Expect(policy.Status.Published).To(Equal(int32(1)))
			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			rec := records[0]
			Expect(rec.Spec.Name).To(Equal("*"))
			Expect(rec.Spec.ZoneRef.Name).To(Equal(zone.Name))
			Expect(rec.Spec.Type).To(Equal(uddiv1alpha1.RecordType("A")))
			Expect(rec.Spec.RData.A.Address).To(Equal("192.0.2.10"))
			// spec.recordTTL is used because no annotation overrides it.
			Expect(rec.Spec.TTL).NotTo(BeNil())
			Expect(*rec.Spec.TTL).To(Equal(int32(30)))
			Expect(rec.Labels[publish.LabelSourceKind]).To(Equal(string(publish.SourceDNSRecord)))
			Expect(rec.Labels[publish.LabelSourceName]).To(Equal("default-wildcard"))

			// The wildcard Record syncs like any other.
			_, err := recordRec.Reconcile(ctx, requestFor(&rec))
			Expect(err).NotTo(HaveOccurred())
			Expect(reconcilePolicy(policy)).To(Succeed())
			Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionTrue))
		})

		It("reads spec.dnsRecordNamespace instead of the default", func() {
			makeReadyConnection(ns)
			zone := readyZone("apps-zone", "apps.example.com.")
			makeDNSRecord(ns, "own-wildcard", nil, wildcardSpec)

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "dnsrecord-ns-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:            []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindDNSRecord},
					ZoneRefs:           []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
					DNSRecordNamespace: ns,
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			Expect(records[0].Labels[publish.LabelSourceName]).To(Equal("own-wildcard"))
		})

		It("is inert, without an error, when DNSRecordsAvailable is false", func() {
			makeReadyConnection(ns)
			zone := readyZone("apps-zone", "apps.example.com.")
			makeDNSRecord(ns, "inert-wildcard", nil, wildcardSpec)

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "dnsrecord-inert-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:            []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindDNSRecord},
					ZoneRefs:           []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
					DNSRecordNamespace: ns,
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())

			inert := &DNSPublishPolicyReconciler{Client: k8sClient, Runtime: testRuntime, DNSRecordsAvailable: false}
			_, err := inert.Reconcile(ctx, requestFor(policy))
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)).To(Succeed())

			Expect(policy.Status.Published).To(Equal(int32(0)))
			Expect(policy.Status.Skipped).To(BeEmpty())
			Expect(policyRecords(policy)).To(BeEmpty())
		})
	})

	Context("HTTPRoute source", func() {
		// makeGateway creates a Gateway, with status.addresses when there are any.
		makeGateway := func(namespace, name string, addresses ...interface{}) {
			gw := &unstructured.Unstructured{}
			gw.SetGroupVersionKind(gatewayapi.GatewayGVK)
			gw.SetNamespace(namespace)
			gw.SetName(name)
			Expect(unstructured.SetNestedField(gw.Object, "example", "spec", "gatewayClassName")).To(Succeed())
			Expect(k8sClient.Create(ctx, gw)).To(Succeed())
			if len(addresses) == 0 {
				return
			}
			Expect(unstructured.SetNestedSlice(gw.Object, addresses, "status", "addresses")).To(Succeed())
			Expect(k8sClient.Status().Update(ctx, gw)).To(Succeed())
		}

		// makeHTTPRoute creates an HTTPRoute in ns attached to the named Gateway, whose
		// status.parents entry says accepted or not.
		makeHTTPRoute := func(name string, hostnames []string, gwNamespace, gwName string, accepted bool) {
			route := &unstructured.Unstructured{}
			route.SetGroupVersionKind(gatewayapi.HTTPRouteGVK)
			route.SetNamespace(ns)
			route.SetName(name)
			parent := map[string]interface{}{"name": gwName, "namespace": gwNamespace}
			Expect(unstructured.SetNestedStringSlice(route.Object, hostnames, "spec", "hostnames")).To(Succeed())
			Expect(unstructured.SetNestedSlice(route.Object, []interface{}{parent}, "spec", "parentRefs")).To(Succeed())
			Expect(k8sClient.Create(ctx, route)).To(Succeed())

			status := "False"
			if accepted {
				status = "True"
			}
			Expect(unstructured.SetNestedSlice(route.Object, []interface{}{map[string]interface{}{
				"parentRef": map[string]interface{}{
					"group": gatewayapi.Group, "kind": "Gateway", "name": gwName, "namespace": gwNamespace,
				},
				"controllerName": "example.com/gateway-controller",
				"conditions": []interface{}{map[string]interface{}{
					"type": "Accepted", "status": status, "reason": "Test", "message": "",
					"lastTransitionTime": "2026-10-04T00:00:00Z",
				}},
			}}, "status", "parents")).To(Succeed())
			Expect(k8sClient.Status().Update(ctx, route)).To(Succeed())
		}

		newHTTPRoutePolicy := func(zone *uddiv1alpha1.AuthZone) *uddiv1alpha1.DNSPublishPolicy {
			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "httproute-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindHTTPRoute},
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			return policy
		}

		It("publishes a route hostname to the addresses of its accepting Gateway and skips an unaccepted route", func() {
			makeReadyConnection(ns)
			zone := readyZone("apps-zone", "apps.example.com.")
			// The Gateway lives in another namespace, as a platform team would run it.
			gwNS := newNamespace()
			makeGateway(gwNS, "shared",
				map[string]interface{}{"type": "IPAddress", "value": "192.0.2.80"},
				map[string]interface{}{"type": "IPAddress", "value": "2001:db8::80"})
			makeHTTPRoute("web", []string{"web.apps.example.com"}, gwNS, "shared", true)
			makeHTTPRoute("pending", []string{"pending.apps.example.com"}, gwNS, "shared", false)

			policy := newHTTPRoutePolicy(zone)
			Expect(reconcilePolicy(policy)).To(Succeed())

			Expect(policy.Status.Published).To(Equal(int32(2)))
			records := recordsByType(policy)
			Expect(records).To(HaveLen(2))
			Expect(records[uddiv1alpha1.RecordType("A")].Spec.RData.A.Address).To(Equal("192.0.2.80"))
			Expect(records[uddiv1alpha1.RecordType("AAAA")].Spec.RData.AAAA.Address).To(Equal("2001:db8::80"))
			for _, rec := range records {
				Expect(rec.Spec.Name).To(Equal("web"))
				Expect(rec.Labels[publish.LabelSourceKind]).To(Equal(string(publish.SourceHTTPRoute)))
				Expect(rec.Labels[publish.LabelSourceName]).To(Equal("web"))
			}

			Expect(policy.Status.Skipped).To(ContainElement(And(
				HaveField("Source", "HTTPRoute/pending"),
				HaveField("Hostname", "pending.apps.example.com."),
				HaveField("Reason", "NotAdmitted"),
			)))
		})

		It("skips an accepted route whose Gateway has no address yet, then publishes once it has one", func() {
			makeReadyConnection(ns)
			zone := readyZone("apps-zone", "apps.example.com.")
			makeGateway(ns, "late")
			makeHTTPRoute("web", []string{"web.apps.example.com"}, ns, "late", true)

			policy := newHTTPRoutePolicy(zone)
			Expect(reconcilePolicy(policy)).To(Succeed())
			Expect(policy.Status.Published).To(Equal(int32(0)))
			Expect(skipReasons(policy)).To(HaveKeyWithValue("HTTPRoute/web", "NoTarget"))

			gw := &unstructured.Unstructured{}
			gw.SetGroupVersionKind(gatewayapi.GatewayGVK)
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: "late"}, gw)).To(Succeed())
			Expect(unstructured.SetNestedSlice(gw.Object, []interface{}{
				map[string]interface{}{"type": "Hostname", "value": "lb.example.net"},
			}, "status", "addresses")).To(Succeed())
			Expect(k8sClient.Status().Update(ctx, gw)).To(Succeed())

			Expect(reconcilePolicy(policy)).To(Succeed())
			Expect(policy.Status.Published).To(Equal(int32(1)))
			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			Expect(records[0].Spec.Type).To(Equal(uddiv1alpha1.RecordType("CNAME")))
			Expect(records[0].Spec.RData.CNAME.CNAME).To(Equal("lb.example.net."))
		})

		It("is inert, without an error, when GatewayAPIAvailable is false", func() {
			makeReadyConnection(ns)
			zone := readyZone("apps-zone", "apps.example.com.")
			makeGateway(ns, "shared", map[string]interface{}{"type": "IPAddress", "value": "192.0.2.80"})
			makeHTTPRoute("web", []string{"web.apps.example.com"}, ns, "shared", true)

			policy := newHTTPRoutePolicy(zone)
			inert := &DNSPublishPolicyReconciler{Client: k8sClient, Runtime: testRuntime, GatewayAPIAvailable: false}
			_, err := inert.Reconcile(ctx, requestFor(policy))
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)).To(Succeed())

			Expect(policy.Status.Published).To(Equal(int32(0)))
			Expect(policy.Status.Skipped).To(BeEmpty())
			Expect(policyRecords(policy)).To(BeEmpty())
		})
	})

	Context("external-dns compatibility", func() {
		It("reads the external-dns annotations of a LoadBalancer Service only while compatibility is on", func() {
			makeReadyConnection(ns)
			zone := readyZone("apps-zone", "apps.example.com.")
			svc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ext",
					Namespace: ns,
					Annotations: map[string]string{
						publish.ExternalDNSHostname: "ext.apps.example.com",
						publish.ExternalDNSTTL:      "120",
					},
				},
				Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Ports: []corev1.ServicePort{{Port: 80}}},
			}
			Expect(k8sClient.Create(ctx, svc)).To(Succeed())
			svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "192.0.2.90"}}
			Expect(k8sClient.Status().Update(ctx, svc)).To(Succeed())

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "extdns-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:       []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindService},
					ZoneRefs:      []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
					Compatibility: uddiv1alpha1.DNSPublishCompatibility{ExternalDNS: true},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			Expect(policy.Status.Published).To(Equal(int32(1)))
			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			Expect(records[0].Spec.Name).To(Equal("ext"))
			Expect(records[0].Spec.RData.A.Address).To(Equal("192.0.2.90"))
			Expect(records[0].Spec.TTL).NotTo(BeNil())
			Expect(*records[0].Spec.TTL).To(Equal(int32(120)))

			// Without compatibility the Service has no hostname, and its Record goes away.
			policy.Spec.Compatibility.ExternalDNS = false
			Expect(k8sClient.Update(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			Expect(policy.Status.Published).To(Equal(int32(0)))
			Expect(policyRecords(policy)).To(BeEmpty())
			Expect(skipReasons(policy)).To(HaveKeyWithValue("Service/ext", "NoHostname"))
		})
	})

	Context("zones given by id", func() {
		It("publishes Records that reference the zone id when it is the policy's only zone", func() {
			makeReadyConnection(ns)
			const zoneID = "dns/auth_zone/not-adopted-in-this-namespace"
			ing := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
				Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "www.apps.example.com"}}},
			}
			Expect(k8sClient.Create(ctx, ing)).To(Succeed())
			ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.61"}}
			Expect(k8sClient.Status().Update(ctx, ing)).To(Succeed())

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "byid-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources: []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindIngress},
					// No trailing dot and mixed case: the controller canonicalises the fqdn.
					ZoneRefs: []uddiv1alpha1.ZoneRef{{ID: zoneID, FQDN: "Apps.Example.com"}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			Expect(policy.Status.Published).To(Equal(int32(1)))
			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			Expect(records[0].Spec.ZoneRef).To(Equal(uddiv1alpha1.ObjectRef{ID: zoneID}))
			Expect(records[0].Spec.Name).To(Equal("www"))

			// A zone given by id is resolved and ready as it stands.
			Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonRecordsNotReady))
			resolved := meta.FindStatusCondition(policy.Status.Conditions, uddiv1alpha1.ConditionZonesResolved)
			Expect(resolved).NotTo(BeNil())
			Expect(resolved.Status).To(Equal(metav1.ConditionTrue))
		})

		It("lets a policy in another namespace publish into a zone adopted once in a platform namespace", func() {
			makeReadyConnection(ns)
			platform := readyZone("platform-zone", "apps.example.com.")
			Expect(platform.Status.ID).NotTo(BeEmpty())

			tenantNS := newNamespace()
			makeReadyConnection(tenantNS)
			ing := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: tenantNS},
				Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "www.apps.example.com"}}},
			}
			Expect(k8sClient.Create(ctx, ing)).To(Succeed())
			ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.62"}}
			Expect(k8sClient.Status().Update(ctx, ing)).To(Succeed())

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "tenant-policy", Namespace: tenantNS},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindIngress},
					ZoneRefs: []uddiv1alpha1.ZoneRef{{ID: platform.Status.ID, FQDN: platform.Spec.FQDN}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, requestFor(policy))
			Expect(err).NotTo(HaveOccurred())

			var list uddiv1alpha1.RecordList
			Expect(k8sClient.List(ctx, &list, client.InNamespace(tenantNS))).To(Succeed())
			Expect(list.Items).To(HaveLen(1))
			rec := list.Items[0]
			Expect(rec.Spec.ZoneRef.ID).To(Equal(platform.Status.ID))

			// The Record syncs against the zone through its id alone, and the policy turns Ready.
			_, err = recordRec.Reconcile(ctx, requestFor(&rec))
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&rec), &rec)).To(Succeed())
			Expect(rec.Status.Zone).To(Equal(platform.Status.ID))

			_, err = reconciler.Reconcile(ctx, requestFor(policy))
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)).To(Succeed())
			Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionTrue))
			Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonPublished))
		})

		It("matches the longest fqdn across zones given by name and by id", func() {
			makeReadyConnection(ns)
			parent := readyZone("example-zone", "example.com.")
			const childID = "dns/auth_zone/child-zone"
			for host, ip := range map[string]string{"www.apps.example.com": "192.0.2.63", "www.example.com": "192.0.2.64"} {
				name := "ing-" + strings.ReplaceAll(host, ".", "-")
				ing := &networkingv1.Ingress{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
					Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: host}}},
				}
				Expect(k8sClient.Create(ctx, ing)).To(Succeed())
				ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: ip}}
				Expect(k8sClient.Status().Update(ctx, ing)).To(Succeed())
			}

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "mixed-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindIngress},
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: parent.Name}, {ID: childID, FQDN: "apps.example.com."}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())

			zoneRefs := map[string]uddiv1alpha1.ObjectRef{}
			for _, rec := range policyRecords(policy) {
				zoneRefs[rec.Annotations[publish.AnnotationRecordHost]] = rec.Spec.ZoneRef
			}
			Expect(zoneRefs).To(Equal(map[string]uddiv1alpha1.ObjectRef{
				"www.apps.example.com.": {ID: childID},
				"www.example.com.":      {Name: parent.Name},
			}))
		})

		It("replaces a Record when its zoneRef moves from an AuthZone name to the zone id", func() {
			makeReadyConnection(ns)
			zone := readyZone("apps-zone", "apps.example.com.")
			ing := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
				Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "www.apps.example.com"}}},
			}
			Expect(k8sClient.Create(ctx, ing)).To(Succeed())
			ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.65"}}
			Expect(k8sClient.Status().Update(ctx, ing)).To(Succeed())

			policy := &uddiv1alpha1.DNSPublishPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "switch-policy", Namespace: ns},
				Spec: uddiv1alpha1.DNSPublishPolicySpec{
					Sources:  []uddiv1alpha1.SourceKind{uddiv1alpha1.SourceKindIngress},
					ZoneRefs: []uddiv1alpha1.ZoneRef{{Name: zone.Name}},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			Expect(reconcilePolicy(policy)).To(Succeed())
			records := policyRecords(policy)
			Expect(records).To(HaveLen(1))
			Expect(records[0].Spec.ZoneRef.Name).To(Equal(zone.Name))
			recordName := records[0].Name

			policy.Spec.ZoneRefs = []uddiv1alpha1.ZoneRef{{ID: zone.Status.ID, FQDN: zone.Spec.FQDN}}
			Expect(k8sClient.Update(ctx, policy)).To(Succeed())

			// zoneRef is immutable on a Record, so the first pass deletes the old Record
			// instead of failing an apply; the owned-Record watch brings the next pass.
			Expect(reconcilePolicy(policy)).To(Succeed())
			Expect(policyRecords(policy)).To(BeEmpty())

			Expect(reconcilePolicy(policy)).To(Succeed())
			records = policyRecords(policy)
			Expect(records).To(HaveLen(1))
			Expect(records[0].Name).To(Equal(recordName))
			Expect(records[0].Spec.ZoneRef).To(Equal(uddiv1alpha1.ObjectRef{ID: zone.Status.ID}))
		})

		It("indexes only the zoneRefs given by name", func() {
			p := &uddiv1alpha1.DNSPublishPolicy{Spec: uddiv1alpha1.DNSPublishPolicySpec{
				ZoneRefs: []uddiv1alpha1.ZoneRef{
					{Name: "a-zone"},
					{ID: "dns/auth_zone/x", FQDN: "x.example.com."},
					{Name: "b-zone"},
				},
			}}
			Expect(zoneRefNames(p)).To(Equal([]string{"a-zone", "b-zone"}))
		})

		It("validates every zoneRefs entry", func() {
			const id = "dns/auth_zone/validation"
			create := func(name string, refs ...uddiv1alpha1.ZoneRef) error {
				return k8sClient.Create(ctx, &uddiv1alpha1.DNSPublishPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
					Spec:       uddiv1alpha1.DNSPublishPolicySpec{ZoneRefs: refs},
				})
			}

			Expect(create("by-name", uddiv1alpha1.ZoneRef{Name: "apps-zone"})).To(Succeed())
			Expect(create("by-id", uddiv1alpha1.ZoneRef{ID: id, FQDN: "apps.example.com"})).To(Succeed())
			Expect(create("by-id-dot", uddiv1alpha1.ZoneRef{ID: id, FQDN: "apps.example.com."})).To(Succeed())
			Expect(create("both-kinds", uddiv1alpha1.ZoneRef{Name: "apps-zone"},
				uddiv1alpha1.ZoneRef{ID: id, FQDN: "apps.example.com."})).To(Succeed())

			Expect(create("empty", uddiv1alpha1.ZoneRef{})).To(MatchError(ContainSubstring("exactly one of name or id must be set")))
			Expect(create("name-and-id", uddiv1alpha1.ZoneRef{Name: "apps-zone", ID: id, FQDN: "apps.example.com."})).
				To(MatchError(ContainSubstring("exactly one of name or id must be set")))
			Expect(create("id-without-fqdn", uddiv1alpha1.ZoneRef{ID: id})).
				To(MatchError(ContainSubstring("fqdn is required with id, and only valid with id")))
			Expect(create("name-with-fqdn", uddiv1alpha1.ZoneRef{Name: "apps-zone", FQDN: "apps.example.com."})).
				To(MatchError(ContainSubstring("fqdn is required with id, and only valid with id")))
			Expect(create("bad-fqdn", uddiv1alpha1.ZoneRef{ID: id, FQDN: "not a dns name"})).
				To(MatchError(ContainSubstring("should match")))
			Expect(create("no-refs")).To(HaveOccurred())
		})
	})
})
