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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/hostreg"
	"github.com/IngmarVG-IB/openshiftoperator/internal/nmstateapi"
)

var _ = Describe("HostRegistrationPolicy controller", func() {
	var (
		ns          string
		reconciler  *HostRegistrationPolicyReconciler
		fixedRec    *FixedAddressReconciler
		spaceRec    *IPSpaceReconciler
		nodeCounter int
		nodeNames   []string
		nnsNames    []string
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		reconciler = &HostRegistrationPolicyReconciler{Client: k8sClient, Runtime: testRuntime, NMStateAvailable: true}
		fixedRec = &FixedAddressReconciler{Client: k8sClient, Runtime: testRuntime}
		spaceRec = &IPSpaceReconciler{Client: k8sClient, Runtime: testRuntime}
		nodeCounter = 0
		nodeNames = nil
		nnsNames = nil
		makeReadyConnection(ns)
		drainEvents()
	})

	AfterEach(func() {
		// Nodes and NodeNetworkStates are cluster-scoped, so they outlive the
		// per-spec namespace and must be cleaned up by hand.
		for _, name := range nodeNames {
			_ = k8sClient.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}})
		}
		for _, name := range nnsNames {
			nns := &unstructured.Unstructured{}
			nns.SetGroupVersionKind(nmstateapi.GVK)
			nns.SetName(name)
			_ = k8sClient.Delete(ctx, nns)
		}
	})

	// selectorFor is the selector every policy in this file uses: every Node this spec
	// creates carries the "hostreg-ns" label naming the spec's own namespace, so a
	// selector on it can never match a Node left over (by ordering accident) from
	// another spec or another Describe block, without each spec having to invent its
	// own label.
	selectorFor := func(extra map[string]string) *metav1.LabelSelector {
		match := map[string]string{"hostreg-ns": ns}
		for k, v := range extra {
			match[k] = v
		}
		return &metav1.LabelSelector{MatchLabels: match}
	}

	// newNode creates a cluster-scoped Node carrying the "hostreg-ns" label (see
	// selectorFor) plus any extra labels and annotations, with a status.addresses entry
	// when addrType is non-empty. It is tracked for deletion in AfterEach.
	newNode := func(extraLabels, annotations map[string]string, addrType corev1.NodeAddressType, address string) *corev1.Node {
		nodeCounter++
		name := fmt.Sprintf("hostreg-node-%d", nodeCounter)
		nodeNames = append(nodeNames, name)

		lbls := map[string]string{"hostreg-ns": ns}
		for k, v := range extraLabels {
			lbls[k] = v
		}
		n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls, Annotations: annotations}}
		Expect(k8sClient.Create(ctx, n)).To(Succeed())
		if addrType != "" {
			n.Status.Addresses = []corev1.NodeAddress{{Type: addrType, Address: address}}
			Expect(k8sClient.Status().Update(ctx, n)).To(Succeed())
		}
		return n
	}

	// setNodeNetworkState creates (or overwrites) the NodeNetworkState named after node,
	// with the given status.currentState, exactly as internal/nmstateapi's own envtest
	// spec does. It is tracked for deletion in AfterEach.
	setNodeNetworkState := func(node string, currentState map[string]interface{}) {
		nns := &unstructured.Unstructured{}
		nns.SetGroupVersionKind(nmstateapi.GVK)
		nns.SetName(node)
		Expect(k8sClient.Create(ctx, nns)).To(Succeed())
		Expect(unstructured.SetNestedMap(nns.Object, currentState, "status", "currentState")).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, nns)).To(Succeed())
		nnsNames = append(nnsNames, node)
	}

	// readySpace creates and reconciles an IPSpace CR so its status.id can back a
	// HostRegistrationPolicy's spaceRef.
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

	// pendingSpace creates an IPSpace CR but does not reconcile it, so it starts life
	// without a status.id.
	pendingSpace := func(name string) *uddiv1alpha1.IPSpace {
		sp := &uddiv1alpha1.IPSpace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       uddiv1alpha1.IPSpaceSpec{Name: name},
		}
		Expect(k8sClient.Create(ctx, sp)).To(Succeed())
		return sp
	}

	reconcilePolicy := func(p *uddiv1alpha1.HostRegistrationPolicy) error {
		_, err := reconciler.Reconcile(ctx, requestFor(p))
		if err == nil {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
		}
		return err
	}

	readyConditionOf := func(p *uddiv1alpha1.HostRegistrationPolicy) *metav1.Condition {
		return meta.FindStatusCondition(p.Status.Conditions, uddiv1alpha1.ConditionReady)
	}

	skipReasons := func(p *uddiv1alpha1.HostRegistrationPolicy) map[string]string {
		out := map[string]string{}
		for _, sk := range p.Status.Skipped {
			out[sk.Node] = sk.Reason
		}
		return out
	}

	getFixedAddress := func(policy *uddiv1alpha1.HostRegistrationPolicy, node string) *uddiv1alpha1.FixedAddress {
		var f uddiv1alpha1.FixedAddress
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: hostreg.RegistrationName(policy.Name, node)}, &f)).To(Succeed())
		return &f
	}
	getAddress := func(policy *uddiv1alpha1.HostRegistrationPolicy, node string) *uddiv1alpha1.Address {
		var a uddiv1alpha1.Address
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: hostreg.RegistrationName(policy.Name, node)}, &a)).To(Succeed())
		return &a
	}
	fixedAddressExists := func(policy *uddiv1alpha1.HostRegistrationPolicy, node string) bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: hostreg.RegistrationName(policy.Name, node)}, &uddiv1alpha1.FixedAddress{})
		return err == nil
	}
	addressExists := func(policy *uddiv1alpha1.HostRegistrationPolicy, node string) bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: hostreg.RegistrationName(policy.Name, node)}, &uddiv1alpha1.Address{})
		return err == nil
	}

	It("registers a worker Node with a known MAC as a FixedAddress in Auto mode, then reports Ready once it syncs", func() {
		space := readySpace("space-1")
		node := newNode(
			map[string]string{"node-role.kubernetes.io/worker": ""},
			map[string]string{"uddi.infoblox.com/mac": "52:54:00:aa:bb:01"},
			corev1.NodeInternalIP, "10.1.0.10",
		)

		policy := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "nodes", Namespace: ns},
			Spec: uddiv1alpha1.HostRegistrationPolicySpec{
				Selector: selectorFor(map[string]string{"node-role.kubernetes.io/worker": ""}),
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		// hostreg.Compute only counts a registration as pending once it already
		// existed going into the reconcile that computed the plan (see
		// TestCompute_PendingMessage in internal/hostreg/plan_test.go): the very first
		// reconcile creates the FixedAddress but reports 0/0, exactly like a brand new
		// IPAllocationPolicy Address reports Allocated but not yet Assigned.
		Expect(policy.Status.Pending).To(Equal(int32(0)))
		Expect(policy.Status.Registered).To(Equal(int32(0)))

		fa := getFixedAddress(policy, node.Name)
		Expect(fa.Labels[hostreg.LabelPolicy]).To(Equal(policy.Name))
		Expect(fa.Labels[hostreg.LabelNode]).To(Equal(node.Name))

		owner := metav1.GetControllerOf(fa)
		Expect(owner).NotTo(BeNil())
		Expect(owner.APIVersion).To(Equal("v1"))
		Expect(owner.Kind).To(Equal("Node"))
		Expect(owner.Name).To(Equal(node.Name))
		Expect(owner.UID).To(Equal(node.UID))
		Expect(owner.Controller).NotTo(BeNil())
		Expect(*owner.Controller).To(BeTrue())
		Expect(owner.BlockOwnerDeletion).NotTo(BeNil())
		Expect(*owner.BlockOwnerDeletion).To(BeTrue())

		Expect(fa.Spec.MatchType).To(Equal(uddiv1alpha1.FixedAddressMatchType("mac")))
		Expect(fa.Spec.MatchValue).To(Equal("52:54:00:aa:bb:01"))
		Expect(fa.Spec.Name).To(Equal(node.Name))
		Expect(fa.Spec.Hostname).To(Equal(node.Name))
		Expect(fa.Spec.Address).To(Equal("10.1.0.10"))
		Expect(fa.Spec.Comment).To(Equal(fmt.Sprintf("Registered by HostRegistrationPolicy %s/%s for Node %s", ns, policy.Name, node.Name)))
		Expect(fa.Spec.Tags[hostreg.TagSource]).To(Equal(fmt.Sprintf("Node/%s", node.Name)))

		// Now that the FixedAddress exists, the next reconcile sees it as pending.
		Expect(reconcilePolicy(policy)).To(Succeed())
		Expect(policy.Status.Pending).To(Equal(int32(1)))
		Expect(policy.Status.Registered).To(Equal(int32(0)))
		Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionFalse))
		Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonRegistrationsPending))

		_, err := fixedRec.Reconcile(ctx, requestFor(fa))
		Expect(err).NotTo(HaveOccurred())

		Expect(reconcilePolicy(policy)).To(Succeed())
		Expect(policy.Status.Registered).To(Equal(int32(1)))
		Expect(policy.Status.Pending).To(Equal(int32(0)))
		Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionTrue))
		Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonRegistered))

		Expect(collectEvents()).To(ContainElement(ContainSubstring("Registered")))
	})

	It("registers a Node with no known MAC as an Address in Auto mode, then switches to a FixedAddress once a MAC annotation appears", func() {
		space := readySpace("space-2")
		node := newNode(nil, nil, corev1.NodeInternalIP, "10.1.0.20")

		policy := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "nodes", Namespace: ns},
			Spec: uddiv1alpha1.HostRegistrationPolicySpec{
				Selector: selectorFor(nil),
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		addr := getAddress(policy, node.Name)
		Expect(addr.Spec.Names).To(Equal([]uddiv1alpha1.AddressName{{Name: node.Name, Type: "user"}}))
		Expect(addr.Spec.HWAddr).To(BeEmpty())
		drainEvents()

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(Succeed())
		node.Annotations = map[string]string{"uddi.infoblox.com/mac": "52:54:00:aa:bb:02"}
		Expect(k8sClient.Update(ctx, node)).To(Succeed())

		Expect(reconcilePolicy(policy)).To(Succeed())

		Expect(fixedAddressExists(policy, node.Name)).To(BeTrue())
		Expect(addressExists(policy, node.Name)).To(BeFalse())
		Expect(collectEvents()).To(ContainElement(ContainSubstring("Released")))
	})

	It("reads the MAC from NodeNetworkState by matching the registered address when no annotation is set", func() {
		space := readySpace("space-3a")
		node := newNode(nil, nil, corev1.NodeInternalIP, "10.1.0.30")
		setNodeNetworkState(node.Name, map[string]interface{}{
			"interfaces": []interface{}{
				map[string]interface{}{
					"name":        "eno1",
					"type":        "ethernet",
					"state":       "up",
					"mac-address": "52:54:00:cc:dd:01",
					"ipv4": map[string]interface{}{
						"address": []interface{}{
							map[string]interface{}{"ip": "10.1.0.30", "prefix-length": int64(24)},
						},
					},
				},
			},
		})

		policy := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "nodes", Namespace: ns},
			Spec: uddiv1alpha1.HostRegistrationPolicySpec{
				Selector: selectorFor(nil),
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		fa := getFixedAddress(policy, node.Name)
		Expect(fa.Spec.MatchValue).To(Equal("52:54:00:cc:dd:01"))
	})

	It("reads the MAC from the NodeNetworkState interface named by macSource.nmstate.interface", func() {
		space := readySpace("space-3b")
		node := newNode(nil, nil, corev1.NodeInternalIP, "10.1.0.40")
		setNodeNetworkState(node.Name, map[string]interface{}{
			"interfaces": []interface{}{
				map[string]interface{}{
					"name":        "eno1",
					"type":        "ethernet",
					"state":       "up",
					"mac-address": "52:54:00:cc:dd:02",
					"ipv4": map[string]interface{}{
						"address": []interface{}{
							map[string]interface{}{"ip": "10.1.0.40", "prefix-length": int64(24)},
						},
					},
				},
				map[string]interface{}{
					"name":        "bond0",
					"type":        "bond",
					"state":       "up",
					"mac-address": "52:54:00:cc:dd:03",
				},
			},
		})

		policy := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "nodes", Namespace: ns},
			Spec: uddiv1alpha1.HostRegistrationPolicySpec{
				Selector:  selectorFor(nil),
				SpaceRef:  uddiv1alpha1.ObjectRef{Name: space.Name},
				MACSource: uddiv1alpha1.MACSource{NMState: uddiv1alpha1.NMStateSource{Interface: "bond0"}},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		fa := getFixedAddress(policy, node.Name)
		Expect(fa.Spec.MatchValue).To(Equal("52:54:00:cc:dd:03"))
	})

	It("falls back to an Address when NMStateAvailable is false, even though a NodeNetworkState exists", func() {
		space := readySpace("space-3c")
		node := newNode(nil, nil, corev1.NodeInternalIP, "10.1.0.50")
		setNodeNetworkState(node.Name, map[string]interface{}{
			"interfaces": []interface{}{
				map[string]interface{}{
					"name":        "eno1",
					"type":        "ethernet",
					"state":       "up",
					"mac-address": "52:54:00:cc:dd:04",
					"ipv4": map[string]interface{}{
						"address": []interface{}{
							map[string]interface{}{"ip": "10.1.0.50", "prefix-length": int64(24)},
						},
					},
				},
			},
		})

		policy := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "nodes", Namespace: ns},
			Spec: uddiv1alpha1.HostRegistrationPolicySpec{
				Selector: selectorFor(nil),
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		noNMState := &HostRegistrationPolicyReconciler{Client: k8sClient, Runtime: testRuntime, NMStateAvailable: false}
		_, err := noNMState.Reconcile(ctx, requestFor(policy))
		Expect(err).NotTo(HaveOccurred())

		Expect(addressExists(policy, node.Name)).To(BeTrue())
		Expect(fixedAddressExists(policy, node.Name)).To(BeFalse())
		addr := getAddress(policy, node.Name)
		Expect(addr.Spec.HWAddr).To(BeEmpty())
	})

	It("skips NoMAC in FixedAddress mode without a MAC, and sets hwaddr in Address mode when a MAC is known", func() {
		space := readySpace("space-4")
		noMAC := newNode(nil, nil, corev1.NodeInternalIP, "10.1.0.60")
		withMAC := newNode(nil, map[string]string{"uddi.infoblox.com/mac": "52:54:00:aa:bb:04"}, corev1.NodeInternalIP, "10.1.0.61")

		fixedPolicy := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "fixed-only", Namespace: ns},
			Spec: uddiv1alpha1.HostRegistrationPolicySpec{
				Selector: selectorFor(nil),
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
				Register: uddiv1alpha1.Register{Mode: uddiv1alpha1.RegisterModeFixedAddress},
			},
		}
		Expect(k8sClient.Create(ctx, fixedPolicy)).To(Succeed())
		Expect(reconcilePolicy(fixedPolicy)).To(Succeed())

		reasons := skipReasons(fixedPolicy)
		Expect(reasons).To(HaveKeyWithValue(noMAC.Name, string(hostreg.SkipNoMAC)))
		Expect(fixedAddressExists(fixedPolicy, noMAC.Name)).To(BeFalse())
		Expect(addressExists(fixedPolicy, noMAC.Name)).To(BeFalse())
		Expect(fixedAddressExists(fixedPolicy, withMAC.Name)).To(BeTrue())

		addrPolicy := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "address-only", Namespace: ns},
			Spec: uddiv1alpha1.HostRegistrationPolicySpec{
				Selector: selectorFor(nil),
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
				Register: uddiv1alpha1.Register{Mode: uddiv1alpha1.RegisterModeAddress},
			},
		}
		Expect(k8sClient.Create(ctx, addrPolicy)).To(Succeed())
		Expect(reconcilePolicy(addrPolicy)).To(Succeed())

		addr := getAddress(addrPolicy, withMAC.Name)
		Expect(addr.Spec.HWAddr).To(Equal("52:54:00:aa:bb:04"))
		Expect(addressExists(addrPolicy, noMAC.Name)).To(BeTrue())
		Expect(getAddress(addrPolicy, noMAC.Name).Spec.HWAddr).To(BeEmpty())
	})

	It("excludes an opted-out Node and releases its existing registration", func() {
		space := readySpace("space-5a")
		node := newNode(nil, nil, corev1.NodeInternalIP, "10.1.0.70")

		policy := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "nodes", Namespace: ns},
			Spec: uddiv1alpha1.HostRegistrationPolicySpec{
				Selector: selectorFor(nil),
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())
		Expect(addressExists(policy, node.Name)).To(BeTrue())
		drainEvents()

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(Succeed())
		node.Annotations = map[string]string{"uddi.infoblox.com/register": "false"}
		Expect(k8sClient.Update(ctx, node)).To(Succeed())

		Expect(reconcilePolicy(policy)).To(Succeed())

		Expect(skipReasons(policy)).To(HaveKeyWithValue(node.Name, string(hostreg.SkipExcluded)))
		Expect(addressExists(policy, node.Name)).To(BeFalse())
		Expect(collectEvents()).To(ContainElement(ContainSubstring("Released")))
	})

	It("releases a registration when the Node no longer matches the selector", func() {
		space := readySpace("space-5b")
		node := newNode(map[string]string{"worker": "true"}, nil, corev1.NodeInternalIP, "10.1.0.71")

		policy := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "nodes", Namespace: ns},
			Spec: uddiv1alpha1.HostRegistrationPolicySpec{
				Selector: selectorFor(map[string]string{"worker": "true"}),
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())
		Expect(addressExists(policy, node.Name)).To(BeTrue())
		drainEvents()

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(Succeed())
		delete(node.Labels, "worker")
		Expect(k8sClient.Update(ctx, node)).To(Succeed())

		Expect(reconcilePolicy(policy)).To(Succeed())

		Expect(addressExists(policy, node.Name)).To(BeFalse())
		Expect(collectEvents()).To(ContainElement(ContainSubstring("Released")))
	})

	It("honours the address and hostname annotations", func() {
		space := readySpace("space-6a")
		node := newNode(nil, map[string]string{
			"uddi.infoblox.com/address":  "10.1.0.80",
			"uddi.infoblox.com/hostname": "custom.example.com",
		}, corev1.NodeInternalIP, "10.1.0.81")

		policy := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "nodes", Namespace: ns},
			Spec: uddiv1alpha1.HostRegistrationPolicySpec{
				Selector: selectorFor(nil),
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		addr := getAddress(policy, node.Name)
		Expect(addr.Spec.Address).To(Equal("10.1.0.80"))
		Expect(addr.Spec.Names).To(Equal([]uddiv1alpha1.AddressName{{Name: "custom.example.com", Type: "user"}}))
	})

	It("picks the ExternalIP address when addressType is ExternalIP, and reports NoAddress when the Node has none", func() {
		space := readySpace("space-6b")
		withExternal := newNode(nil, nil, "", "")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(withExternal), withExternal)).To(Succeed())
		withExternal.Status.Addresses = []corev1.NodeAddress{
			{Type: corev1.NodeInternalIP, Address: "10.1.0.90"},
			{Type: corev1.NodeExternalIP, Address: "203.0.113.9"},
		}
		Expect(k8sClient.Status().Update(ctx, withExternal)).To(Succeed())

		noExternal := newNode(nil, nil, corev1.NodeInternalIP, "10.1.0.91")

		policy := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "nodes", Namespace: ns},
			Spec: uddiv1alpha1.HostRegistrationPolicySpec{
				Selector:    selectorFor(nil),
				SpaceRef:    uddiv1alpha1.ObjectRef{Name: space.Name},
				AddressType: uddiv1alpha1.AddressTypeExternalIP,
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		addr := getAddress(policy, withExternal.Name)
		Expect(addr.Spec.Address).To(Equal("203.0.113.9"))

		Expect(skipReasons(policy)).To(HaveKeyWithValue(noExternal.Name, string(hostreg.SkipNoAddress)))
		Expect(addressExists(policy, noExternal.Name)).To(BeFalse())
	})

	It("reports SpaceNotReady until the IPSpace becomes Ready, then registers the Node", func() {
		space := pendingSpace("space-7")
		node := newNode(nil, nil, corev1.NodeInternalIP, "10.1.0.100")

		policy := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "nodes", Namespace: ns},
			Spec: uddiv1alpha1.HostRegistrationPolicySpec{
				Selector: selectorFor(nil),
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(reconcilePolicy(policy)).To(Succeed())

		Expect(readyConditionOf(policy).Status).To(Equal(metav1.ConditionFalse))
		Expect(readyConditionOf(policy).Reason).To(Equal(uddiv1alpha1.ReasonSpaceNotReady))
		Expect(skipReasons(policy)).To(HaveKeyWithValue(node.Name, string(hostreg.SkipSpaceNotReady)))
		Expect(addressExists(policy, node.Name)).To(BeFalse())

		_, err := spaceRec.Reconcile(ctx, requestFor(space))
		Expect(err).NotTo(HaveOccurred())

		Expect(reconcilePolicy(policy)).To(Succeed())
		Expect(addressExists(policy, node.Name)).To(BeTrue())
		Expect(readyConditionOf(policy).Reason).NotTo(Equal(uddiv1alpha1.ReasonSpaceNotReady))
	})
})
