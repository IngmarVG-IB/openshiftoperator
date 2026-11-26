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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/egressip"
	"github.com/IngmarVG-IB/openshiftoperator/internal/ipalloc"
	"github.com/IngmarVG-IB/openshiftoperator/internal/metallb"
)

// The milestone 7 extensions of IPAllocationPolicy (DESIGN.md section 4.7): dual-stack
// Services, EgressIP claims, the immutable fields of allocated slots and MetalLB pool
// mirroring, driven by direct Reconcile calls against envtest. The suite's API server has
// a dual-stack service CIDR, and the EgressIP and IPAddressPool CRD fixtures from
// test/crds are installed.
var _ = Describe("IPAllocationPolicy controller, milestone 7", func() {
	const (
		allocate         = "uddi.infoblox.com/allocate"
		policyAnnotation = "uddi.infoblox.com/policy"
		// selectLabel keeps a policy to the Services and EgressIPs of its own spec:
		// EgressIPs are cluster-scoped and outlive the namespace a spec creates.
		selectLabel = "ipalloc-m7-test"
	)

	var (
		ns         string
		reconciler *IPAllocationPolicyReconciler
		addrRec    *AddressReconciler
		spaceRec   *IPSpaceReconciler
		rangeRec   *RangeReconciler
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		// The spec's namespace is permitted cluster-scoped effects unless a spec says
		// otherwise; the specs that mirror set MetalLBNamespace through mirrorInto.
		reconciler = &IPAllocationPolicyReconciler{
			Client: k8sClient, Runtime: testRuntime, EgressIPAvailable: true, MetalLBAvailable: true,
			ClusterTargetNamespaces: NamespaceSet{ns: {}},
		}
		addrRec = &AddressReconciler{Client: k8sClient, Runtime: testRuntime}
		spaceRec = &IPSpaceReconciler{Client: k8sClient, Runtime: testRuntime}
		rangeRec = &RangeReconciler{Client: k8sClient, Runtime: testRuntime}
		makeReadyConnection(ns)
		drainEvents()
	})

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

	rangeRef := func(rg *uddiv1alpha1.Range) uddiv1alpha1.AllocateFrom {
		return uddiv1alpha1.AllocateFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: rg.Name}}
	}

	// newLBService creates a type: LoadBalancer Service carrying selectLabel and the given
	// annotations; mutate adjusts the spec before it is created.
	newLBService := func(name string, annotations map[string]string, mutate ...func(*corev1.Service)) *corev1.Service {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: ns, Labels: map[string]string{selectLabel: ns}, Annotations: annotations,
			},
			Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Ports: []corev1.ServicePort{{Port: 80}}},
		}
		for _, m := range mutate {
			m(svc)
		}
		Expect(k8sClient.Create(ctx, svc)).To(Succeed())
		return svc
	}

	dualStack := func(policy corev1.IPFamilyPolicy) func(*corev1.Service) {
		return func(svc *corev1.Service) {
			svc.Spec.IPFamilyPolicy = &policy
			svc.Spec.IPFamilies = []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol}
		}
	}

	// newEgressIP creates a cluster-scoped EgressIP carrying selectLabel, waiting for its
	// addresses (spec.egressIPs: []), and deletes it when the spec ends.
	newEgressIP := func(name string, annotations map[string]string) *unstructured.Unstructured {
		eip := egressip.New()
		eip.SetName(name)
		eip.SetLabels(map[string]string{selectLabel: ns})
		eip.SetAnnotations(annotations)
		Expect(unstructured.SetNestedField(eip.Object, map[string]interface{}{
			"egressIPs":         []interface{}{},
			"namespaceSelector": map[string]interface{}{},
		}, "spec")).To(Succeed())
		Expect(k8sClient.Create(ctx, eip)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, eip))).To(Succeed())
		})
		return eip
	}

	egressIPsOf := func(eip *unstructured.Unstructured) []string {
		fresh := egressip.New()
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(eip), fresh)).To(Succeed())
		ips, _, err := egressip.EgressIPs(fresh)
		Expect(err).NotTo(HaveOccurred())
		return ips
	}

	annotateEgressIP := func(eip *unstructured.Unstructured, annotations map[string]string) {
		fresh := egressip.New()
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(eip), fresh)).To(Succeed())
		merged := fresh.GetAnnotations()
		for k, v := range annotations {
			merged[k] = v
		}
		fresh.SetAnnotations(merged)
		Expect(k8sClient.Update(ctx, fresh)).To(Succeed())
	}

	newPolicy := func(name string, from uddiv1alpha1.AllocateFrom, mutate ...func(*uddiv1alpha1.IPAllocationPolicy)) *uddiv1alpha1.IPAllocationPolicy {
		p := &uddiv1alpha1.IPAllocationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       uddiv1alpha1.IPAllocationPolicySpec{AllocateFrom: from},
		}
		for _, m := range mutate {
			m(p)
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		return p
	}

	selectOwn := func(p *uddiv1alpha1.IPAllocationPolicy) {
		p.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{selectLabel: ns}}
	}

	// mirrorInto turns mirroring on and points the operator's MetalLB namespace at
	// namespace, so each spec mirrors into a namespace of its own.
	mirrorInto := func(namespace string) func(*uddiv1alpha1.IPAllocationPolicy) {
		return func(p *uddiv1alpha1.IPAllocationPolicy) {
			reconciler.MetalLBNamespace = namespace
			p.Spec.MetalLB = &uddiv1alpha1.MetalLBMirror{MirrorPools: true}
		}
	}

	// optedInFor is the annotations of an EgressIP that opts in and names the policy of the
	// spec's namespace allowed to manage it.
	optedInFor := func(policy string) map[string]string {
		return map[string]string{allocate: "true", policyAnnotation: ns + "/" + policy}
	}

	// updatePolicy re-reads the policy, applies change and writes it back.
	updatePolicy := func(p *uddiv1alpha1.IPAllocationPolicy, change func(*uddiv1alpha1.IPAllocationPolicy)) {
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
		change(p)
		Expect(k8sClient.Update(ctx, p)).To(Succeed())
	}

	reconcilePolicy := func(p *uddiv1alpha1.IPAllocationPolicy) {
		_, err := reconciler.Reconcile(ctx, requestFor(p))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
	}

	readyConditionOf := func(p *uddiv1alpha1.IPAllocationPolicy) *metav1.Condition {
		return meta.FindStatusCondition(p.Status.Conditions, uddiv1alpha1.ConditionReady)
	}

	skipReasons := func(p *uddiv1alpha1.IPAllocationPolicy) map[string]string {
		out := map[string]string{}
		for _, sk := range p.Status.Skipped {
			out[sk.Service] = sk.Reason
		}
		return out
	}

	getAddress := func(name string) *uddiv1alpha1.Address {
		var addr uddiv1alpha1.Address
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &addr)).To(Succeed())
		return &addr
	}

	addressGone := func(name string) bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &uddiv1alpha1.Address{}))
	}

	// allocateAddress runs the Address controller on an Address, which allocates it from
	// the fake, and returns it re-read.
	allocateAddress := func(name string) *uddiv1alpha1.Address {
		addr := getAddress(name)
		_, err := addrRec.Reconcile(ctx, requestFor(addr))
		Expect(err).NotTo(HaveOccurred())
		addr = getAddress(name)
		Expect(addr.Status.Address).NotTo(BeEmpty())
		return addr
	}

	// finishRelease completes the deletion of a released Address: its finalizer releases the
	// Universal DDI address once the Address controller runs.
	finishRelease := func(name string) {
		addr := getAddress(name)
		Expect(addr.DeletionTimestamp).NotTo(BeNil(), "Address %s should be released", name)
		_, err := addrRec.Reconcile(ctx, requestFor(addr))
		Expect(err).NotTo(HaveOccurred())
		Expect(addressGone(name)).To(BeTrue())
	}

	policyAddresses := func(p *uddiv1alpha1.IPAllocationPolicy) []uddiv1alpha1.Address {
		var list uddiv1alpha1.AddressList
		Expect(k8sClient.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{ipalloc.LabelPolicy: p.Name})).To(Succeed())
		return list.Items
	}

	getPool := func(namespace, name string) (*unstructured.Unstructured, error) {
		pool := &unstructured.Unstructured{}
		pool.SetGroupVersionKind(metallb.GVK)
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, pool)
		return pool, err
	}

	poolAddresses := func(pool *unstructured.Unstructured) []string {
		addrs, _, err := unstructured.NestedStringSlice(pool.Object, "spec", "addresses")
		Expect(err).NotTo(HaveOccurred())
		return addrs
	}

	serviceAllocationNamespaces := func(pool *unstructured.Unstructured) []string {
		namespaces, _, err := unstructured.NestedStringSlice(pool.Object, "spec", "serviceAllocation", "namespaces")
		Expect(err).NotTo(HaveOccurred())
		return namespaces
	}

	eipRef := func(eip *unstructured.Unstructured) ipalloc.TargetRef {
		return ipalloc.TargetRef{Kind: ipalloc.TargetEgressIP, Name: eip.GetName()}
	}

	It("gives a PreferDualStack Service one Address per family and writes both in family order once both are Ready", func() {
		space := readySpace("ds-space")
		v4 := readyRange("ds-v4", "10.0.70.10", "10.0.70.20", space)
		v6 := readyRange("ds-v6", "fd00:70::10", "fd00:70::20", space)
		svc := newLBService("web", map[string]string{allocate: "true"}, dualStack(corev1.IPFamilyPolicyPreferDualStack))
		Expect(svc.Spec.IPFamilies).To(Equal([]corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol}))

		policy := newPolicy("ds", rangeRef(v4), func(p *uddiv1alpha1.IPAllocationPolicy) {
			v6From := rangeRef(v6)
			p.Spec.AllocateFromIPv6 = &v6From
		})
		reconcilePolicy(policy)
		Expect(policy.Status.Allocated).To(Equal(int32(1)))

		// Slot 0 keeps the milestone 3 name; slot 1 takes the IPv6 pool.
		slot0Name := ipalloc.AddressName(policy.Name, svc.Name)
		Expect(slot0Name).To(Equal("ds-web"))
		slot1Name := ipalloc.SlotAddressName(policy.Name, ipalloc.TargetRef{Kind: ipalloc.TargetService, Name: svc.Name}, 1)
		Expect(slot1Name).To(Equal("ds-web-1"))
		slot0, slot1 := getAddress(slot0Name), getAddress(slot1Name)
		for i, a := range []*uddiv1alpha1.Address{slot0, slot1} {
			Expect(a.Labels).To(HaveKeyWithValue(ipalloc.LabelTargetKind, string(ipalloc.TargetService)))
			Expect(a.Labels).To(HaveKeyWithValue(ipalloc.LabelTargetName, svc.Name))
			Expect(a.Labels).To(HaveKeyWithValue(ipalloc.LabelService, svc.Name))
			Expect(a.Labels).To(HaveKeyWithValue(ipalloc.LabelSlot, []string{"0", "1"}[i]))
			Expect(metav1.GetControllerOf(a)).To(HaveField("UID", svc.UID))
		}
		Expect(slot0.Spec.NextAvailableFrom.RangeRef.Name).To(Equal(v4.Name))
		Expect(slot1.Spec.NextAvailableFrom.RangeRef.Name).To(Equal(v6.Name))

		// One family Ready is not enough: the assignment carries every slot or nothing.
		slot0 = allocateAddress(slot0Name)
		reconcilePolicy(policy)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(svc), svc)).To(Succeed())
		Expect(svc.Annotations).NotTo(HaveKey(ipalloc.AnnotationMetalLB))
		Expect(skipReasons(policy)).To(HaveKeyWithValue(svc.Name, string(ipalloc.SkipAddressPending)))
		Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonAddressesPending))

		slot1 = allocateAddress(slot1Name)
		Expect(slot0.Status.Address).To(Equal("10.0.70.10"))
		Expect(slot1.Status.Address).To(Equal("fd00:70::10"))
		reconcilePolicy(policy)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(svc), svc)).To(Succeed())
		Expect(svc.Annotations).To(HaveKeyWithValue(ipalloc.AnnotationMetalLB, "10.0.70.10,fd00:70::10"))
		Expect(policy.Status.Assigned).To(Equal(int32(1)))
		Expect(policy.Status.Skipped).To(BeEmpty())
		Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionTrue))
	})

	It("skips a RequireDualStack Service without an IPv6 pool as NoPoolForFamily and keeps its slot 0", func() {
		space := readySpace("rq-space")
		v4 := readyRange("rq-v4", "10.0.71.10", "10.0.71.20", space)
		svc := newLBService("web", map[string]string{allocate: "true"})
		policy := newPolicy("rq", rangeRef(v4))

		reconcilePolicy(policy)
		slot0Name := ipalloc.AddressName(policy.Name, svc.Name)
		slot0 := allocateAddress(slot0Name)
		reconcilePolicy(policy)
		Expect(policy.Status.Assigned).To(Equal(int32(1)))

		// The Service becomes dual-stack, but the policy has no IPv6 pool.
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(svc), svc)).To(Succeed())
		dualStack(corev1.IPFamilyPolicyRequireDualStack)(svc)
		Expect(k8sClient.Update(ctx, svc)).To(Succeed())

		reconcilePolicy(policy)
		Expect(skipReasons(policy)).To(HaveKeyWithValue(svc.Name, string(ipalloc.SkipNoPoolForFamily)))
		kept := getAddress(slot0Name)
		Expect(kept.DeletionTimestamp).To(BeNil())
		Expect(kept.Status.Address).To(Equal(slot0.Status.Address))
		Expect(policyAddresses(policy)).To(HaveLen(1))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(svc), svc)).To(Succeed())
		Expect(svc.Annotations).To(HaveKeyWithValue(ipalloc.AnnotationMetalLB, slot0.Status.Address))
	})

	It("adopts a milestone 3 Address as slot 0 without allocating again", func() {
		space := readySpace("m3-space")
		rg := readyRange("m3-pool", "10.0.72.10", "10.0.72.20", space)
		svc := newLBService("web", map[string]string{allocate: "true"})

		// What a milestone 3 policy left behind: only the policy and service labels.
		legacyName := ipalloc.AddressName("m3", svc.Name)
		legacy := &uddiv1alpha1.Address{
			ObjectMeta: metav1.ObjectMeta{
				Name:      legacyName,
				Namespace: ns,
				Labels:    map[string]string{ipalloc.LabelPolicy: "m3", ipalloc.LabelService: svc.Name},
			},
			Spec: uddiv1alpha1.AddressSpec{
				SpaceRef:          uddiv1alpha1.ObjectRef{Name: space.Name},
				NextAvailableFrom: &uddiv1alpha1.NextAvailableFrom{RangeRef: &uddiv1alpha1.ObjectRef{Name: rg.Name}},
				Names:             []uddiv1alpha1.AddressName{{Name: "web." + ns, Type: "user"}},
			},
		}
		Expect(k8sClient.Create(ctx, legacy)).To(Succeed())
		legacy = allocateAddress(legacyName)
		drainEvents()

		policy := newPolicy("m3", rangeRef(rg))
		reconcilePolicy(policy)

		addrs := policyAddresses(policy)
		Expect(addrs).To(HaveLen(1))
		adopted := addrs[0]
		Expect(adopted.Name).To(Equal(legacyName))
		Expect(adopted.Status.Address).To(Equal(legacy.Status.Address))
		Expect(adopted.Labels).To(HaveKeyWithValue(ipalloc.LabelTargetKind, string(ipalloc.TargetService)))
		Expect(adopted.Labels).To(HaveKeyWithValue(ipalloc.LabelSlot, "0"))
		Expect(metav1.GetControllerOf(&adopted)).To(HaveField("UID", svc.UID))

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(svc), svc)).To(Succeed())
		Expect(svc.Annotations).To(HaveKeyWithValue(ipalloc.AnnotationMetalLB, legacy.Status.Address))
		Expect(policy.Status.Allocated).To(Equal(int32(1)))
		Expect(policy.Status.Assigned).To(Equal(int32(1)))
		Expect(collectEvents()).NotTo(ContainElement(ContainSubstring("Allocated")))
	})

	It("allocates count slots for an EgressIP, writes spec.egressIPs once all are Ready, shrinks with the count and clears on opt-out", func() {
		space := readySpace("eip-space")
		rg := readyRange("eip-pool", "10.0.73.10", "10.0.73.20", space)
		policy := newPolicy("eip", rangeRef(rg), selectOwn, func(p *uddiv1alpha1.IPAllocationPolicy) {
			p.Spec.Targets = []uddiv1alpha1.TargetKind{uddiv1alpha1.TargetKindEgressIP}
		})
		ann := optedInFor(policy.Name)
		ann["uddi.infoblox.com/count"] = "2"
		eip := newEgressIP("egress-"+ns, ann)

		reconcilePolicy(policy)
		Expect(policy.Status.Allocated).To(Equal(int32(1)))
		names := []string{
			ipalloc.SlotAddressName(policy.Name, eipRef(eip), 0),
			ipalloc.SlotAddressName(policy.Name, eipRef(eip), 1),
		}
		Expect(names[0]).To(Equal("eip-egressip-" + eip.GetName() + "-0"))
		for i, name := range names {
			a := getAddress(name)
			owner := metav1.GetControllerOf(a)
			Expect(owner).NotTo(BeNil())
			Expect(owner.APIVersion).To(Equal("k8s.ovn.org/v1"))
			Expect(owner.Kind).To(Equal("EgressIP"))
			Expect(owner.Name).To(Equal(eip.GetName()))
			Expect(owner.UID).To(Equal(eip.GetUID()))
			Expect(owner.BlockOwnerDeletion).To(BeNil())
			Expect(a.Labels).To(HaveKeyWithValue(ipalloc.LabelTargetKind, string(ipalloc.TargetEgressIP)))
			Expect(a.Labels).To(HaveKeyWithValue(ipalloc.LabelTargetName, eip.GetName()))
			Expect(a.Labels).To(HaveKeyWithValue(ipalloc.LabelSlot, []string{"0", "1"}[i]))
			Expect(a.Labels).NotTo(HaveKey(ipalloc.LabelService))
			Expect(a.Spec.Tags).To(HaveKeyWithValue(ipalloc.TagSource, "EgressIP/"+eip.GetName()))
			Expect(a.Spec.Names).To(BeEmpty())
		}

		a0 := allocateAddress(names[0])
		reconcilePolicy(policy)
		Expect(egressIPsOf(eip)).To(BeEmpty())
		Expect(skipReasons(policy)).To(HaveKeyWithValue("EgressIP/"+eip.GetName(), string(ipalloc.SkipAddressPending)))

		a1 := allocateAddress(names[1])
		reconcilePolicy(policy)
		Expect(egressIPsOf(eip)).To(Equal([]string{a0.Status.Address, a1.Status.Address}))
		Expect(policy.Status.Assigned).To(Equal(int32(1)))
		Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionTrue))

		// Lowering the count releases the highest slot and rewrites the list first.
		annotateEgressIP(eip, map[string]string{"uddi.infoblox.com/count": "1"})
		reconcilePolicy(policy)
		Expect(egressIPsOf(eip)).To(Equal([]string{a0.Status.Address}))
		finishRelease(names[1])
		Expect(getAddress(names[0]).DeletionTimestamp).To(BeNil())

		// Opting out clears the list, since it holds only this policy's address, and releases.
		drainEvents()
		annotateEgressIP(eip, map[string]string{allocate: "false"})
		reconcilePolicy(policy)
		Expect(egressIPsOf(eip)).To(BeEmpty())
		finishRelease(names[0])
		Expect(policy.Status.Allocated).To(Equal(int32(0)))
		Expect(collectEvents()).To(ContainElement(And(ContainSubstring("Released"), ContainSubstring("EgressIP "+eip.GetName()))))
	})

	It("reports a foreign spec.egressIPs as ManuallyAssigned and allocates nothing", func() {
		space := readySpace("eip-manual-space")
		rg := readyRange("eip-manual-pool", "10.0.73.30", "10.0.73.40", space)
		policy := newPolicy("eipm", rangeRef(rg), selectOwn, func(p *uddiv1alpha1.IPAllocationPolicy) {
			p.Spec.Targets = []uddiv1alpha1.TargetKind{uddiv1alpha1.TargetKindService, uddiv1alpha1.TargetKindEgressIP}
		})
		eip := newEgressIP("egress-manual-"+ns, optedInFor(policy.Name))
		Expect(unstructured.SetNestedStringSlice(eip.Object, []string{"192.0.2.77"}, "spec", "egressIPs")).To(Succeed())
		Expect(k8sClient.Update(ctx, eip)).To(Succeed())

		reconcilePolicy(policy)
		Expect(skipReasons(policy)).To(HaveKeyWithValue("EgressIP/"+eip.GetName(), string(ipalloc.SkipManuallyAssigned)))
		Expect(policyAddresses(policy)).To(BeEmpty())
		Expect(egressIPsOf(eip)).To(Equal([]string{"192.0.2.77"}))
		Expect(collectEvents()).To(ContainElement(ContainSubstring("Skipped")))
	})

	It("keeps an allocated slot's pool and address when allocateFrom or the address annotation changes", func() {
		space := readySpace("move-space")
		poolA := readyRange("pool-a", "10.0.74.10", "10.0.74.20", space)
		poolB := readyRange("pool-b", "10.0.75.10", "10.0.75.20", space)
		svc := newLBService("web", map[string]string{allocate: "true"})
		policy := newPolicy("move", rangeRef(poolA))

		reconcilePolicy(policy)
		name := ipalloc.AddressName(policy.Name, svc.Name)
		allocated := allocateAddress(name)
		Expect(allocated.Status.Address).To(Equal("10.0.74.10"))
		reconcilePolicy(policy)
		Expect(policy.Status.Assigned).To(Equal(int32(1)))

		// A new pool only affects new slots; the existing one keeps its immutable fields,
		// which the apply would otherwise be rejected for.
		updatePolicy(policy, func(p *uddiv1alpha1.IPAllocationPolicy) { p.Spec.AllocateFrom = rangeRef(poolB) })
		reconcilePolicy(policy)
		kept := getAddress(name)
		Expect(kept.Spec.NextAvailableFrom.RangeRef.Name).To(Equal(poolA.Name))
		Expect(kept.Status.Address).To(Equal("10.0.74.10"))
		Expect(policy.Status.Assigned).To(Equal(int32(1)))

		api := newLBService("api", map[string]string{allocate: "true"})
		reconcilePolicy(policy)
		Expect(getAddress(ipalloc.AddressName(policy.Name, api.Name)).Spec.NextAvailableFrom.RangeRef.Name).To(Equal(poolB.Name))

		// An address annotation on an allocated slot does not move it either.
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(svc), svc)).To(Succeed())
		svc.Annotations["uddi.infoblox.com/address"] = "10.0.75.15"
		Expect(k8sClient.Update(ctx, svc)).To(Succeed())
		reconcilePolicy(policy)
		kept = getAddress(name)
		Expect(kept.Spec.Address).To(BeEmpty())
		Expect(kept.Spec.NextAvailableFrom.RangeRef.Name).To(Equal(poolA.Name))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(svc), svc)).To(Succeed())
		Expect(svc.Annotations).To(HaveKeyWithValue(ipalloc.AnnotationMetalLB, "10.0.74.10"))
	})

	It("mirrors its pools into MetalLB while mirrorPools is on, and deletes them when it is turned off or the policy is deleted", func() {
		mirrorNS := newNamespace()
		space := readySpace("mirror-space")
		rg := readyRange("mirror-pool", "10.0.76.10", "10.0.76.20", space)
		policy := newPolicy("mirror", rangeRef(rg), mirrorInto(mirrorNS))
		poolName := metallb.PoolName(ns, policy.Name, string(ipalloc.PoolRange), rg.Name)

		reconcilePolicy(policy)
		Expect(policy.Finalizers).To(ContainElement(MetalLBPoolsFinalizer))
		pool, err := getPool(mirrorNS, poolName)
		Expect(err).NotTo(HaveOccurred())
		Expect(poolAddresses(pool)).To(Equal([]string{"10.0.76.10-10.0.76.20"}))
		autoAssign, found, err := unstructured.NestedBool(pool.Object, "spec", "autoAssign")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(autoAssign).To(BeFalse())
		// Only Services in the policy's namespace may draw from the pool.
		Expect(serviceAllocationNamespaces(pool)).To(Equal([]string{ns}))
		Expect(pool.GetLabels()).To(HaveKeyWithValue(metallb.LabelPolicy, policy.Name))
		Expect(pool.GetLabels()).To(HaveKeyWithValue(metallb.LabelPolicyNamespace, ns))
		Expect(appliedBy(pool, MetalLBFieldManager)).To(BeTrue())
		Expect(policy.Status.Skipped).To(BeEmpty())

		// Drift is repaired on the next reconcile.
		Expect(unstructured.SetNestedStringSlice(pool.Object, []string{"10.9.9.0/24"}, "spec", "addresses")).To(Succeed())
		Expect(k8sClient.Update(ctx, pool)).To(Succeed())
		reconcilePolicy(policy)
		pool, err = getPool(mirrorNS, poolName)
		Expect(err).NotTo(HaveOccurred())
		Expect(poolAddresses(pool)).To(Equal([]string{"10.0.76.10-10.0.76.20"}))

		// Turning mirroring off deletes the pool and the finalizer.
		updatePolicy(policy, func(p *uddiv1alpha1.IPAllocationPolicy) { p.Spec.MetalLB.MirrorPools = false })
		reconcilePolicy(policy)
		_, err = getPool(mirrorNS, poolName)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		Expect(policy.Finalizers).NotTo(ContainElement(MetalLBPoolsFinalizer))

		// On again, then deleting the policy cleans up through the finalizer.
		updatePolicy(policy, func(p *uddiv1alpha1.IPAllocationPolicy) { p.Spec.MetalLB.MirrorPools = true })
		reconcilePolicy(policy)
		_, err = getPool(mirrorNS, poolName)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Delete(ctx, policy)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, requestFor(policy))
		Expect(err).NotTo(HaveOccurred())
		_, err = getPool(mirrorNS, poolName)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy))).To(BeTrue())
	})

	It("moves its mirrored pools when the operator's MetalLB namespace changes", func() {
		firstNS, secondNS := newNamespace(), newNamespace()
		space := readySpace("move-mirror-space")
		rg := readyRange("move-mirror-pool", "10.0.77.10", "10.0.77.20", space)
		policy := newPolicy("move-mirror", rangeRef(rg), mirrorInto(firstNS))
		poolName := metallb.PoolName(ns, policy.Name, string(ipalloc.PoolRange), rg.Name)

		reconcilePolicy(policy)
		_, err := getPool(firstNS, poolName)
		Expect(err).NotTo(HaveOccurred())

		reconciler.MetalLBNamespace = secondNS
		reconcilePolicy(policy)
		_, err = getPool(secondNS, poolName)
		Expect(err).NotTo(HaveOccurred())
		_, err = getPool(firstNS, poolName)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("leaves a foreign IPAddressPool with the mirrored name alone and reports it", func() {
		mirrorNS := newNamespace()
		space := readySpace("foreign-space")
		rg := readyRange("foreign-pool", "10.0.78.10", "10.0.78.20", space)
		poolName := metallb.PoolName(ns, "foreign", string(ipalloc.PoolRange), rg.Name)

		foreign := &unstructured.Unstructured{Object: map[string]interface{}{
			"spec": map[string]interface{}{"addresses": []interface{}{"192.168.50.0/24"}},
		}}
		foreign.SetGroupVersionKind(metallb.GVK)
		foreign.SetNamespace(mirrorNS)
		foreign.SetName(poolName)
		foreign.SetLabels(map[string]string{"team": "net"})
		Expect(k8sClient.Create(ctx, foreign)).To(Succeed())

		policy := newPolicy("foreign", rangeRef(rg), mirrorInto(mirrorNS))
		reconcilePolicy(policy)
		Expect(skipReasons(policy)).To(HaveKeyWithValue("Range/"+rg.Name, uddiv1alpha1.SkipReasonMetalLBPoolConflict))
		Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionTrue))
		Expect(collectEvents()).To(ContainElement(ContainSubstring(uddiv1alpha1.SkipReasonMetalLBPoolConflict)))

		Expect(k8sClient.Delete(ctx, policy)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, requestFor(policy))
		Expect(err).NotTo(HaveOccurred())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy))).To(BeTrue())

		pool, err := getPool(mirrorNS, poolName)
		Expect(err).NotTo(HaveOccurred())
		Expect(poolAddresses(pool)).To(Equal([]string{"192.168.50.0/24"}))
		Expect(pool.GetLabels()).To(Equal(map[string]string{"team": "net"}))
	})

	It("leaves the EgressIP target and pool mirroring inert when the cluster serves neither API", func() {
		mirrorNS := newNamespace()
		space := readySpace("inert-space")
		rg := readyRange("inert-pool", "10.0.79.10", "10.0.79.20", space)
		svc := newLBService("web", map[string]string{allocate: "true"})
		eip := newEgressIP("inert-"+ns, optedInFor("inert"))
		policy := newPolicy("inert", rangeRef(rg), selectOwn, mirrorInto(mirrorNS), func(p *uddiv1alpha1.IPAllocationPolicy) {
			p.Spec.Targets = []uddiv1alpha1.TargetKind{uddiv1alpha1.TargetKindService, uddiv1alpha1.TargetKindEgressIP}
		})
		eipSlot := ipalloc.SlotAddressName(policy.Name, eipRef(eip), 0)
		poolName := metallb.PoolName(ns, policy.Name, string(ipalloc.PoolRange), rg.Name)

		// Allocated while both APIs were served.
		reconcilePolicy(policy)
		getAddress(eipSlot)
		_, err := getPool(mirrorNS, poolName)
		Expect(err).NotTo(HaveOccurred())
		Expect(policy.Finalizers).To(ContainElement(MetalLBPoolsFinalizer))

		reconciler.EgressIPAvailable = false
		reconciler.MetalLBAvailable = false
		reconcilePolicy(policy)

		// The EgressIP Address stays, nothing is allocated for EgressIPs, the finalizer goes
		// so deletion cannot hang, and the Service target works as before.
		Expect(getAddress(eipSlot).DeletionTimestamp).To(BeNil())
		Expect(egressIPsOf(eip)).To(BeEmpty())
		Expect(policy.Finalizers).NotTo(ContainElement(MetalLBPoolsFinalizer))
		Expect(getAddress(ipalloc.AddressName(policy.Name, svc.Name)).DeletionTimestamp).To(BeNil())
		reasons := skipReasons(policy)
		Expect(reasons).To(HaveKeyWithValue("EgressIP", uddiv1alpha1.SkipReasonAPIUnavailable))
		Expect(reasons).To(HaveKeyWithValue("IPAddressPool", uddiv1alpha1.SkipReasonAPIUnavailable))

		// A new EgressIP gets nothing while the API is reported missing.
		other := newEgressIP("inert-other-"+ns, optedInFor(policy.Name))
		reconcilePolicy(policy)
		Expect(addressGone(ipalloc.SlotAddressName(policy.Name, eipRef(other), 0))).To(BeTrue())

		Expect(k8sClient.Delete(ctx, policy)).To(Succeed())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy))).To(BeTrue())
	})

	// The specs below cover the limits on cluster-scoped effects: the
	// --cluster-target-namespaces gate, status-only mirror sources, the serviceAllocation
	// scoping of mirrored pools and the EgressIP policy annotation.

	skipMessage := func(p *uddiv1alpha1.IPAllocationPolicy, name string) string {
		for _, sk := range p.Status.Skipped {
			if sk.Service == name {
				return sk.Message
			}
		}
		return ""
	}

	poolsIn := func(namespace string) []unstructured.Unstructured {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(metallb.ListGVK)
		Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(Succeed())
		return list.Items
	}

	bothTargets := func(p *uddiv1alpha1.IPAllocationPolicy) {
		p.Spec.Targets = []uddiv1alpha1.TargetKind{uddiv1alpha1.TargetKindService, uddiv1alpha1.TargetKindEgressIP}
	}

	It("gives a policy outside --cluster-target-namespaces no EgressIP and no mirrored pool, and keeps serving its Services", func() {
		reconciler.ClusterTargetNamespaces = NamespaceSet{"another-namespace": {}}
		mirrorNS := newNamespace()
		space := readySpace("np-space")
		rg := readyRange("np-pool", "10.0.80.10", "10.0.80.20", space)
		svc := newLBService("web", map[string]string{allocate: "true"})
		eip := newEgressIP("np-"+ns, optedInFor("np"))
		policy := newPolicy("np", rangeRef(rg), selectOwn, mirrorInto(mirrorNS), bothTargets)

		reconcilePolicy(policy)
		reasons := skipReasons(policy)
		Expect(reasons).To(HaveKeyWithValue("EgressIP", uddiv1alpha1.SkipReasonNotPermitted))
		Expect(reasons).To(HaveKeyWithValue("IPAddressPool", uddiv1alpha1.SkipReasonNotPermitted))
		Expect(skipMessage(policy, "IPAddressPool")).To(ContainSubstring("--cluster-target-namespaces"))
		Expect(skipMessage(policy, "EgressIP")).To(ContainSubstring("--cluster-target-namespaces"))
		Expect(collectEvents()).To(ContainElement(ContainSubstring(uddiv1alpha1.SkipReasonNotPermitted)))

		Expect(poolsIn(mirrorNS)).To(BeEmpty())
		Expect(policy.Finalizers).NotTo(ContainElement(MetalLBPoolsFinalizer))
		Expect(addressGone(ipalloc.SlotAddressName(policy.Name, eipRef(eip), 0))).To(BeTrue())
		Expect(egressIPsOf(eip)).To(BeEmpty())
		Expect(getAddress(ipalloc.AddressName(policy.Name, svc.Name)).DeletionTimestamp).To(BeNil())
	})

	It("releases its EgressIP assignments and mirrored pools once its namespace is no longer permitted", func() {
		mirrorNS := newNamespace()
		space := readySpace("wd-space")
		rg := readyRange("wd-pool", "10.0.81.10", "10.0.81.20", space)
		policy := newPolicy("wd", rangeRef(rg), selectOwn, mirrorInto(mirrorNS), func(p *uddiv1alpha1.IPAllocationPolicy) {
			p.Spec.Targets = []uddiv1alpha1.TargetKind{uddiv1alpha1.TargetKindEgressIP}
		})
		eip := newEgressIP("wd-"+ns, optedInFor(policy.Name))
		slot := ipalloc.SlotAddressName(policy.Name, eipRef(eip), 0)
		poolName := metallb.PoolName(ns, policy.Name, string(ipalloc.PoolRange), rg.Name)

		reconcilePolicy(policy)
		allocated := allocateAddress(slot)
		reconcilePolicy(policy)
		Expect(egressIPsOf(eip)).To(Equal([]string{allocated.Status.Address}))
		_, err := getPool(mirrorNS, poolName)
		Expect(err).NotTo(HaveOccurred())
		Expect(policy.Finalizers).To(ContainElement(MetalLBPoolsFinalizer))

		// The administrator withdraws the namespace from --cluster-target-namespaces.
		reconciler.ClusterTargetNamespaces = NamespaceSet{}
		reconcilePolicy(policy)
		Expect(egressIPsOf(eip)).To(BeEmpty())
		finishRelease(slot)
		_, err = getPool(mirrorNS, poolName)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		Expect(policy.Finalizers).NotTo(ContainElement(MetalLBPoolsFinalizer))
		reasons := skipReasons(policy)
		Expect(reasons).To(HaveKeyWithValue("EgressIP", uddiv1alpha1.SkipReasonNotPermitted))
		Expect(reasons).To(HaveKeyWithValue("IPAddressPool", uddiv1alpha1.SkipReasonNotPermitted))

		// Nothing is left that would hold up deleting the policy.
		Expect(k8sClient.Delete(ctx, policy)).To(Succeed())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy))).To(BeTrue())
	})

	It("never mirrors a Subnet from its spec, whether or not it is Ready", func() {
		mirrorNS := newNamespace()
		space := readySpace("so-space")
		sub := &uddiv1alpha1.Subnet{
			ObjectMeta: metav1.ObjectMeta{Name: "spec-only", Namespace: ns},
			Spec:       uddiv1alpha1.SubnetSpec{SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name}, Address: "10.0.82.0", CIDR: 24},
		}
		Expect(k8sClient.Create(ctx, sub)).To(Succeed())
		policy := newPolicy("so", uddiv1alpha1.AllocateFrom{SubnetRef: &uddiv1alpha1.ObjectRef{Name: sub.Name}}, mirrorInto(mirrorNS))
		entry := string(ipalloc.PoolSubnet) + "/" + sub.Name

		reconcilePolicy(policy)
		Expect(skipReasons(policy)).To(HaveKeyWithValue(entry, uddiv1alpha1.SkipReasonMetalLBPoolInvalid))
		Expect(skipMessage(policy, entry)).To(ContainSubstring("not Ready"))
		Expect(poolsIn(mirrorNS)).To(BeEmpty())

		// Ready, but with no address in its status: the spec address still is not used.
		sub.Status.ID = "ipam/subnet/so"
		Expect(k8sClient.Status().Update(ctx, sub)).To(Succeed())
		reconcilePolicy(policy)
		Expect(skipReasons(policy)).To(HaveKeyWithValue(entry, uddiv1alpha1.SkipReasonMetalLBPoolInvalid))
		Expect(skipMessage(policy, entry)).To(ContainSubstring("no address in its status"))
		Expect(poolsIn(mirrorNS)).To(BeEmpty())

		// Once Universal DDI reported the network, the mirror carries that network.
		sub.Status.Address, sub.Status.CIDR = "10.0.82.0", 24
		Expect(k8sClient.Status().Update(ctx, sub)).To(Succeed())
		reconcilePolicy(policy)
		pool, err := getPool(mirrorNS, metallb.PoolName(ns, policy.Name, string(ipalloc.PoolSubnet), sub.Name))
		Expect(err).NotTo(HaveOccurred())
		Expect(poolAddresses(pool)).To(Equal([]string{"10.0.82.0/24"}))
	})

	It("never mirrors a Range from its spec, and keeps an existing mirror while the Range is not Ready", func() {
		mirrorNS := newNamespace()
		space := readySpace("rs-space")
		specOnly := &uddiv1alpha1.Range{
			ObjectMeta: metav1.ObjectMeta{Name: "spec-range", Namespace: ns},
			Spec:       uddiv1alpha1.RangeSpec{SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name}, Start: "10.0.83.10", End: "10.0.83.20"},
		}
		Expect(k8sClient.Create(ctx, specOnly)).To(Succeed())
		policy := newPolicy("rs", rangeRef(specOnly), mirrorInto(mirrorNS))
		entry := string(ipalloc.PoolRange) + "/" + specOnly.Name

		reconcilePolicy(policy)
		Expect(skipReasons(policy)).To(HaveKeyWithValue(entry, uddiv1alpha1.SkipReasonMetalLBPoolInvalid))
		Expect(poolsIn(mirrorNS)).To(BeEmpty())

		specOnly.Status.ID = "ipam/range/rs"
		Expect(k8sClient.Status().Update(ctx, specOnly)).To(Succeed())
		reconcilePolicy(policy)
		Expect(skipMessage(policy, entry)).To(ContainSubstring("no address in its status"))
		Expect(poolsIn(mirrorNS)).To(BeEmpty())

		// A Range Universal DDI confirmed is mirrored from its status; when it stops being
		// Ready the announced range is kept rather than withdrawn or rebuilt.
		rg := readyRange("confirmed-range", "10.0.84.10", "10.0.84.20", space)
		updatePolicy(policy, func(p *uddiv1alpha1.IPAllocationPolicy) { p.Spec.AllocateFrom = rangeRef(rg) })
		reconcilePolicy(policy)
		poolName := metallb.PoolName(ns, policy.Name, string(ipalloc.PoolRange), rg.Name)
		pool, err := getPool(mirrorNS, poolName)
		Expect(err).NotTo(HaveOccurred())
		Expect(poolAddresses(pool)).To(Equal([]string{"10.0.84.10-10.0.84.20"}))

		rg.Status.ID = ""
		Expect(k8sClient.Status().Update(ctx, rg)).To(Succeed())
		reconcilePolicy(policy)
		Expect(skipReasons(policy)).To(HaveKeyWithValue(string(ipalloc.PoolRange)+"/"+rg.Name, uddiv1alpha1.SkipReasonMetalLBPoolInvalid))
		kept, err := getPool(mirrorNS, poolName)
		Expect(err).NotTo(HaveOccurred())
		Expect(poolAddresses(kept)).To(Equal([]string{"10.0.84.10-10.0.84.20"}))
		Expect(kept.GetResourceVersion()).To(Equal(pool.GetResourceVersion()))
	})

	It("manages only the EgressIPs whose policy annotation names it", func() {
		otherNS := newNamespace()
		space := readySpace("bound-space")
		rg := readyRange("bound-pool", "10.0.85.10", "10.0.85.20", space)
		policy := newPolicy("bound", rangeRef(rg), selectOwn, func(p *uddiv1alpha1.IPAllocationPolicy) {
			p.Spec.Targets = []uddiv1alpha1.TargetKind{uddiv1alpha1.TargetKindEgressIP}
		})
		foreign := newEgressIP("foreign-"+ns, map[string]string{allocate: "true", policyAnnotation: otherNS + "/" + policy.Name})
		unbound := newEgressIP("unbound-"+ns, map[string]string{allocate: "true"})
		otherName := newEgressIP("other-name-"+ns, optedInFor("not-"+policy.Name))

		reconcilePolicy(policy)
		Expect(policyAddresses(policy)).To(BeEmpty())
		Expect(policy.Status.Skipped).To(BeEmpty())
		for _, eip := range []*unstructured.Unstructured{foreign, unbound, otherName} {
			Expect(egressIPsOf(eip)).To(BeEmpty())
		}

		mine := newEgressIP("mine-"+ns, optedInFor(policy.Name))
		slot := ipalloc.SlotAddressName(policy.Name, eipRef(mine), 0)
		reconcilePolicy(policy)
		allocated := allocateAddress(slot)
		reconcilePolicy(policy)
		Expect(egressIPsOf(mine)).To(Equal([]string{allocated.Status.Address}))
		Expect(policyAddresses(policy)).To(HaveLen(1))

		// Pointed at another policy, the EgressIP is no longer this policy's: its Address is
		// released, but its spec.egressIPs is not this policy's to patch any more.
		annotateEgressIP(mine, map[string]string{policyAnnotation: otherNS + "/" + policy.Name})
		reconcilePolicy(policy)
		finishRelease(slot)
		Expect(egressIPsOf(mine)).To(Equal([]string{allocated.Status.Address}))
	})
})
