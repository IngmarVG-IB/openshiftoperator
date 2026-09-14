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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/ipalloc"
)

var _ = Describe("IPAllocationPolicy controller", func() {
	var (
		ns         string
		reconciler *IPAllocationPolicyReconciler
		addrRec    *AddressReconciler
		spaceRec   *IPSpaceReconciler
		subnetRec  *SubnetReconciler
		rangeRec   *RangeReconciler
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		reconciler = &IPAllocationPolicyReconciler{Client: k8sClient, Runtime: testRuntime}
		addrRec = &AddressReconciler{Client: k8sClient, Runtime: testRuntime}
		spaceRec = &IPSpaceReconciler{Client: k8sClient, Runtime: testRuntime}
		subnetRec = &SubnetReconciler{Client: k8sClient, Runtime: testRuntime}
		rangeRec = &RangeReconciler{Client: k8sClient, Runtime: testRuntime}
		makeReadyConnection(ns)
		drainEvents()
	})

	// readySpace creates and reconciles an IPSpace CR so its status.id can back a pool.
	readySpace := func(name string) *uddiv1alpha1.IPSpace {
		sp := &uddiv1alpha1.IPSpace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       uddiv1alpha1.IPSpaceSpec{Name: name},
		}
		Expect(k8sClient.Create(ctx, sp)).To(Succeed())
		_, err := spaceRec.Reconcile(ctx, requestFor(sp))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
		Expect(sp.Status.ID).NotTo(BeEmpty())
		return sp
	}

	// readyRange creates a Range CR under space, naming it by CR name (not id), and
	// reconciles it so it carries status.id.
	readyRange := func(name, start, end string, space *uddiv1alpha1.IPSpace) *uddiv1alpha1.Range {
		rg := &uddiv1alpha1.Range{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.RangeSpec{
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
				Start:    start,
				End:      end,
			},
		}
		Expect(k8sClient.Create(ctx, rg)).To(Succeed())
		_, err := rangeRec.Reconcile(ctx, requestFor(rg))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rg), rg)).To(Succeed())
		Expect(rg.Status.ID).NotTo(BeEmpty())
		return rg
	}

	// pendingRange creates a Range CR but does not reconcile it, so it starts life
	// without a status.id.
	pendingRange := func(name, start, end string, space *uddiv1alpha1.IPSpace) *uddiv1alpha1.Range {
		rg := &uddiv1alpha1.Range{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.RangeSpec{
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
				Start:    start,
				End:      end,
			},
		}
		Expect(k8sClient.Create(ctx, rg)).To(Succeed())
		return rg
	}

	// readySubnet creates and reconciles a Subnet CR so it carries status.id.
	readySubnet := func(name, address string, cidr int32, space *uddiv1alpha1.IPSpace) *uddiv1alpha1.Subnet {
		s := &uddiv1alpha1.Subnet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.SubnetSpec{
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
				Address:  address,
				CIDR:     cidr,
			},
		}
		Expect(k8sClient.Create(ctx, s)).To(Succeed())
		_, err := subnetRec.Reconcile(ctx, requestFor(s))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(s), s)).To(Succeed())
		Expect(s.Status.ID).NotTo(BeEmpty())
		return s
	}

	// newLBService creates a type: LoadBalancer Service with the given annotations.
	newLBService := func(name string, annotations map[string]string) *corev1.Service {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Annotations: annotations},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Ports: []corev1.ServicePort{{Port: 80}}},
		}
		Expect(k8sClient.Create(ctx, svc)).To(Succeed())
		return svc
	}

	reconcilePolicy := func(p *uddiv1alpha1.IPAllocationPolicy) error {
		_, err := reconciler.Reconcile(ctx, requestFor(p))
		if err == nil {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
		}
		return err
	}

	readyConditionOf := func(p *uddiv1alpha1.IPAllocationPolicy) *metav1.Condition {
		return meta.FindStatusCondition(p.Status.Conditions, uddiv1alpha1.ConditionReady)
	}

	getAddress := func(policy *uddiv1alpha1.IPAllocationPolicy, service string) *uddiv1alpha1.Address {
		var addr uddiv1alpha1.Address
		Expect(k8sClient.Get(ctx, client.ObjectKey{
			Namespace: ns,
			Name:      ipalloc.AddressName(policy.Name, service),
		}, &addr)).To(Succeed())
		return &addr
	}

	addressExists := func(policy *uddiv1alpha1.IPAllocationPolicy, service string) bool {
		err := k8sClient.Get(ctx, client.ObjectKey{
			Namespace: ns,
			Name:      ipalloc.AddressName(policy.Name, service),
		}, &uddiv1alpha1.Address{})
		return err == nil
	}

	skipReasons := func(p *uddiv1alpha1.IPAllocationPolicy) map[string]string {
		out := map[string]string{}
		for _, sk := range p.Status.Skipped {
			out[sk.Service] = sk.Reason
		}
		return out
	}

	It("allocates and assigns an address for an opted-in LoadBalancer Service backed by a Ready Range", func() {
		space := readySpace("space-1")
		rg := readyRange("lb-pool-1", "10.0.10.100", "10.0.10.199", space)
		svc := newLBService("web", map[string]string{"uddi.infoblox.com/allocate": "true"})

		policy := &uddiv1alpha1.IPAllocationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "lb", Namespace: ns},
			Spec: uddiv1alpha1.IPAllocationPolicySpec{
				AllocateFrom: uddiv1alpha1.AllocateFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: rg.Name}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		Expect(policy.Status.Allocated).To(Equal(int32(1)))
		Expect(policy.Status.Assigned).To(Equal(int32(0)))
		Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionFalse))
		Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonAddressesPending))

		addr := getAddress(policy, svc.Name)
		Expect(addr.Labels[ipalloc.LabelPolicy]).To(Equal(policy.Name))
		Expect(addr.Labels[ipalloc.LabelService]).To(Equal(svc.Name))

		owner := metav1.GetControllerOf(addr)
		Expect(owner).NotTo(BeNil())
		Expect(owner.Kind).To(Equal("Service"))
		Expect(owner.Name).To(Equal(svc.Name))
		Expect(owner.UID).To(Equal(svc.UID))
		Expect(owner.Controller).NotTo(BeNil())
		Expect(*owner.Controller).To(BeTrue())
		Expect(owner.BlockOwnerDeletion).NotTo(BeNil())
		Expect(*owner.BlockOwnerDeletion).To(BeTrue())

		Expect(addr.Spec.NextAvailableFrom).NotTo(BeNil())
		Expect(addr.Spec.NextAvailableFrom.RangeRef).NotTo(BeNil())
		Expect(addr.Spec.NextAvailableFrom.RangeRef.Name).To(Equal(rg.Name))
		Expect(addr.Spec.Names).To(Equal([]uddiv1alpha1.AddressName{{Name: "web." + ns, Type: "user"}}))
		Expect(addr.Spec.Comment).To(Equal(fmt.Sprintf("Allocated by IPAllocationPolicy %s/%s for Service %s", ns, policy.Name, svc.Name)))
		Expect(addr.Spec.Tags[ipalloc.TagSource]).To(Equal(fmt.Sprintf("Service/%s/%s", ns, svc.Name)))

		_, err := addrRec.Reconcile(ctx, requestFor(addr))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(addr), addr)).To(Succeed())
		Expect(addr.Status.Address).NotTo(BeEmpty())

		Expect(reconcilePolicy(policy)).To(Succeed())
		Expect(policy.Status.Assigned).To(Equal(int32(1)))
		Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionTrue))
		Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonAssigned))

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(svc), svc)).To(Succeed())
		Expect(svc.Annotations["metallb.io/loadBalancerIPs"]).To(Equal(addr.Status.Address))

		Expect(collectEvents()).To(ContainElement(ContainSubstring("Assigned")))
	})

	It("allocates without the allocate annotation when requireAnnotation is false, and never allocates when allocate is false", func() {
		space := readySpace("space-2")
		rg := readyRange("lb-pool-2", "10.0.11.100", "10.0.11.199", space)

		unannotated := newLBService("auto", nil)
		optedOut := newLBService("optout", map[string]string{"uddi.infoblox.com/allocate": "false"})

		requireAnnotation := false
		policy := &uddiv1alpha1.IPAllocationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "auto-policy", Namespace: ns},
			Spec: uddiv1alpha1.IPAllocationPolicySpec{
				RequireAnnotation: &requireAnnotation,
				AllocateFrom:      uddiv1alpha1.AllocateFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: rg.Name}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		Expect(policy.Status.Allocated).To(Equal(int32(1)))
		Expect(addressExists(policy, unannotated.Name)).To(BeTrue())
		Expect(addressExists(policy, optedOut.Name)).To(BeFalse())
	})

	It("reports ManuallyAssigned and creates no Address when the assignment annotation already holds a foreign value", func() {
		space := readySpace("space-3")
		rg := readyRange("lb-pool-3", "10.0.12.100", "10.0.12.199", space)

		svc := newLBService("web", map[string]string{
			"uddi.infoblox.com/allocate": "true",
			"metallb.io/loadBalancerIPs": "192.0.2.99",
		})

		policy := &uddiv1alpha1.IPAllocationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "lb", Namespace: ns},
			Spec: uddiv1alpha1.IPAllocationPolicySpec{
				AllocateFrom: uddiv1alpha1.AllocateFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: rg.Name}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		Expect(policy.Status.Allocated).To(Equal(int32(0)))
		Expect(skipReasons(policy)).To(HaveKeyWithValue(svc.Name, string(ipalloc.SkipManuallyAssigned)))
		Expect(addressExists(policy, svc.Name)).To(BeFalse())
	})

	It("honours the address, hostname and allocate-from annotations", func() {
		space := readySpace("space-4")
		rg := readyRange("default-pool", "10.0.13.100", "10.0.13.199", space)
		subnet := readySubnet("override-subnet", "10.0.14.0", 24, space)

		svc := newLBService("custom", map[string]string{
			"uddi.infoblox.com/allocate":      "true",
			"uddi.infoblox.com/address":       "10.0.14.7",
			"uddi.infoblox.com/hostname":      "custom.example.com",
			"uddi.infoblox.com/allocate-from": "subnet/" + subnet.Name,
		})

		policy := &uddiv1alpha1.IPAllocationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "lb", Namespace: ns},
			Spec: uddiv1alpha1.IPAllocationPolicySpec{
				AllocateFrom: uddiv1alpha1.AllocateFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: rg.Name}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		addr := getAddress(policy, svc.Name)
		Expect(addr.Spec.Address).To(Equal("10.0.14.7"))
		Expect(addr.Spec.NextAvailableFrom).To(BeNil())
		Expect(addr.Spec.Names).To(Equal([]uddiv1alpha1.AddressName{{Name: "custom.example.com", Type: "user"}}))
		Expect(addr.Spec.SpaceRef).To(Equal(subnet.Spec.SpaceRef))
	})

	It("reports PoolNotReady while the pool has no status.id, then allocates once it does", func() {
		space := readySpace("space-5")
		rg := pendingRange("pending-pool", "10.0.15.100", "10.0.15.199", space)
		svc := newLBService("web", map[string]string{"uddi.infoblox.com/allocate": "true"})

		policy := &uddiv1alpha1.IPAllocationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "lb", Namespace: ns},
			Spec: uddiv1alpha1.IPAllocationPolicySpec{
				AllocateFrom: uddiv1alpha1.AllocateFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: rg.Name}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		Expect(policy.Status.Allocated).To(Equal(int32(0)))
		Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionFalse))
		Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonPoolNotReady))
		Expect(skipReasons(policy)).To(HaveKeyWithValue(svc.Name, string(ipalloc.SkipPoolNotReady)))
		Expect(addressExists(policy, svc.Name)).To(BeFalse())

		_, err := rangeRec.Reconcile(ctx, requestFor(rg))
		Expect(err).NotTo(HaveOccurred())

		Expect(reconcilePolicy(policy)).To(Succeed())
		Expect(policy.Status.Allocated).To(Equal(int32(1)))
		Expect(addressExists(policy, svc.Name)).To(BeTrue())
	})

	It("releases an assigned address when the Service opts out: clears the annotation and deletes the Address", func() {
		space := readySpace("space-6")
		rg := readyRange("lb-pool-6", "10.0.16.100", "10.0.16.199", space)
		svc := newLBService("web", map[string]string{"uddi.infoblox.com/allocate": "true"})

		policy := &uddiv1alpha1.IPAllocationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "lb", Namespace: ns},
			Spec: uddiv1alpha1.IPAllocationPolicySpec{
				AllocateFrom: uddiv1alpha1.AllocateFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: rg.Name}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		addr := getAddress(policy, svc.Name)
		_, err := addrRec.Reconcile(ctx, requestFor(addr))
		Expect(err).NotTo(HaveOccurred())
		Expect(reconcilePolicy(policy)).To(Succeed())
		Expect(policy.Status.Assigned).To(Equal(int32(1)))
		drainEvents()

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(svc), svc)).To(Succeed())
		svc.Annotations["uddi.infoblox.com/allocate"] = "false"
		Expect(k8sClient.Update(ctx, svc)).To(Succeed())

		Expect(reconcilePolicy(policy)).To(Succeed())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(svc), svc)).To(Succeed())
		Expect(svc.Annotations).NotTo(HaveKey("metallb.io/loadBalancerIPs"))
		Expect(policy.Status.Allocated).To(Equal(int32(0)))
		Expect(collectEvents()).To(ContainElement(ContainSubstring("Released")))

		// Deleting the Address only sets its deletionTimestamp: AddressReconciler still
		// has to remove the finalizer (and release the Universal DDI address) before the
		// CR actually disappears, exactly like address_controller_test.go's own delete spec.
		_, err = addrRec.Reconcile(ctx, requestFor(addr))
		Expect(err).NotTo(HaveOccurred())
		Expect(addressExists(policy, svc.Name)).To(BeFalse())
	})

	It("leaves a foreign assignment annotation alone during release", func() {
		space := readySpace("space-7")
		rg := readyRange("lb-pool-7", "10.0.17.100", "10.0.17.199", space)
		svc := newLBService("web", map[string]string{"uddi.infoblox.com/allocate": "true"})

		policy := &uddiv1alpha1.IPAllocationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "lb", Namespace: ns},
			Spec: uddiv1alpha1.IPAllocationPolicySpec{
				AllocateFrom: uddiv1alpha1.AllocateFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: rg.Name}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		addr := getAddress(policy, svc.Name)
		_, err := addrRec.Reconcile(ctx, requestFor(addr))
		Expect(err).NotTo(HaveOccurred())
		Expect(reconcilePolicy(policy)).To(Succeed())

		// Someone else takes over the annotation before the Service opts out.
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(svc), svc)).To(Succeed())
		svc.Annotations["metallb.io/loadBalancerIPs"] = "192.0.2.50"
		svc.Annotations["uddi.infoblox.com/allocate"] = "false"
		Expect(k8sClient.Update(ctx, svc)).To(Succeed())

		Expect(reconcilePolicy(policy)).To(Succeed())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(svc), svc)).To(Succeed())
		Expect(svc.Annotations["metallb.io/loadBalancerIPs"]).To(Equal("192.0.2.50"))

		// See the previous spec: deletion only completes once AddressReconciler removes
		// the finalizer.
		_, err = addrRec.Reconcile(ctx, requestFor(addr))
		Expect(err).NotTo(HaveOccurred())
		Expect(addressExists(policy, svc.Name)).To(BeFalse())
	})

	It("skips a ClusterIP Service as NotLoadBalancer and a malformed address annotation as InvalidAnnotation", func() {
		space := readySpace("space-8")
		rg := readyRange("lb-pool-8", "10.0.18.100", "10.0.18.199", space)

		clusterIP := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "cluster-svc",
				Namespace:   ns,
				Annotations: map[string]string{"uddi.infoblox.com/allocate": "true"},
			},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 80}}},
		}
		Expect(k8sClient.Create(ctx, clusterIP)).To(Succeed())

		badAddr := newLBService("bad-addr", map[string]string{
			"uddi.infoblox.com/allocate": "true",
			"uddi.infoblox.com/address":  "not-an-ip",
		})

		policy := &uddiv1alpha1.IPAllocationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "lb", Namespace: ns},
			Spec: uddiv1alpha1.IPAllocationPolicySpec{
				AllocateFrom: uddiv1alpha1.AllocateFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: rg.Name}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		reasons := skipReasons(policy)
		Expect(reasons).To(HaveKeyWithValue(clusterIP.Name, string(ipalloc.SkipNotLoadBalancer)))
		Expect(reasons).To(HaveKeyWithValue(badAddr.Name, string(ipalloc.SkipInvalidAnnotation)))

		Expect(collectEvents()).To(ContainElement(ContainSubstring("Skipped")))
	})

	It("writes spec.loadBalancerIP when assignVia.mode is LoadBalancerIP", func() {
		space := readySpace("space-9")
		rg := readyRange("lb-pool-9", "10.0.19.100", "10.0.19.199", space)
		svc := newLBService("web", map[string]string{"uddi.infoblox.com/allocate": "true"})

		policy := &uddiv1alpha1.IPAllocationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "lb", Namespace: ns},
			Spec: uddiv1alpha1.IPAllocationPolicySpec{
				AllocateFrom: uddiv1alpha1.AllocateFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: rg.Name}},
				AssignVia:    uddiv1alpha1.AssignVia{Mode: uddiv1alpha1.AssignModeLoadBalancerIP},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		addr := getAddress(policy, svc.Name)
		_, err := addrRec.Reconcile(ctx, requestFor(addr))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(addr), addr)).To(Succeed())

		Expect(reconcilePolicy(policy)).To(Succeed())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(svc), svc)).To(Succeed())
		Expect(svc.Spec.LoadBalancerIP).To(Equal(addr.Status.Address))
	})
})
