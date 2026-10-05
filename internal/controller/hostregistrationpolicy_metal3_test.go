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
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/hostreg"
	"github.com/IngmarVG-IB/openshiftoperator/internal/labelvalue"
	"github.com/IngmarVG-IB/openshiftoperator/internal/metal3api"
	"github.com/IngmarVG-IB/openshiftoperator/internal/nmstateapi"
)

// Milestone 7 specs for HostRegistrationPolicy: Metal3 BareMetalHosts as a MAC source,
// additional interfaces, and the planner fixes for long Node names and name collisions. They
// drive Reconcile by hand like the rest of the controller suite; the real-manager watch case
// lives in manager_test.go.
var _ = Describe("HostRegistrationPolicy with Metal3 and additional interfaces", func() {
	const (
		storageCIDR  = "10.40.0.0/16"
		primaryMAC   = "52:54:00:aa:00:01"
		bmhNICMAC    = "52:55:00:D1:55:C1"
		bmhNICMACLow = "52:55:00:d1:55:c1"
	)

	var (
		ns         string
		reconciler *HostRegistrationPolicyReconciler
		fixedRec   *FixedAddressReconciler
		spaceRec   *IPSpaceReconciler

		nodeCounter int
		hostCounter int
		nodeNames   []string
		nnsNames    []string
		hosts       []*unstructured.Unstructured
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		reconciler = &HostRegistrationPolicyReconciler{Client: k8sClient, Runtime: testRuntime, NMStateAvailable: true, Metal3Available: true}
		fixedRec = &FixedAddressReconciler{Client: k8sClient, Runtime: testRuntime}
		spaceRec = &IPSpaceReconciler{Client: k8sClient, Runtime: testRuntime}
		nodeCounter, hostCounter = 0, 0
		nodeNames, nnsNames, hosts = nil, nil, nil
		makeReadyConnection(ns)
		drainEvents()

		// BareMetalHosts live in openshift-machine-api on a real cluster. The namespace
		// outlives the spec (envtest runs no namespace controller), so it is created once and
		// the hosts are removed by hand.
		err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: metal3api.DefaultNamespace}})
		if !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}
	})

	AfterEach(func() {
		for _, name := range nodeNames {
			_ = k8sClient.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}})
		}
		for _, name := range nnsNames {
			nns := &unstructured.Unstructured{}
			nns.SetGroupVersionKind(nmstateapi.GVK)
			nns.SetName(name)
			_ = k8sClient.Delete(ctx, nns)
		}
		for _, h := range hosts {
			_ = k8sClient.Delete(ctx, h)
		}
	})

	// selectorFor selects only the Nodes this spec created, which carry the "hostreg3-ns"
	// label naming the spec's namespace.
	selectorFor := func() *metav1.LabelSelector {
		return &metav1.LabelSelector{MatchLabels: map[string]string{"hostreg3-ns": ns}}
	}

	// newNodeNamed creates a cluster-scoped Node with the given name, the selector label and
	// the given annotations and status addresses.
	newNodeNamed := func(name string, annotations map[string]string, addresses ...corev1.NodeAddress) *corev1.Node {
		nodeNames = append(nodeNames, name)
		n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      map[string]string{"hostreg3-ns": ns},
			Annotations: annotations,
		}}
		Expect(k8sClient.Create(ctx, n)).To(Succeed())
		if len(addresses) > 0 {
			n.Status.Addresses = addresses
			Expect(k8sClient.Status().Update(ctx, n)).To(Succeed())
		}
		return n
	}
	newNode := func(annotations map[string]string, addresses ...corev1.NodeAddress) *corev1.Node {
		nodeCounter++
		return newNodeNamed(fmt.Sprintf("hostreg3-node-%d", nodeCounter), annotations, addresses...)
	}
	internalIP := func(ip string) corev1.NodeAddress {
		return corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: ip}
	}

	// bareMetalHost creates a BareMetalHost in namespace and writes its status.hardware
	// through the status subresource, as the baremetal-operator does after inspection.
	bareMetalHost := func(namespace, bootMAC, hostname string, nics ...map[string]interface{}) *unstructured.Unstructured {
		hostCounter++
		host := &unstructured.Unstructured{}
		host.SetGroupVersionKind(metal3api.GVK)
		host.SetNamespace(namespace)
		host.SetName(fmt.Sprintf("bmh-%s-%d", ns, hostCounter))
		if bootMAC != "" {
			Expect(unstructured.SetNestedField(host.Object, bootMAC, "spec", "bootMACAddress")).To(Succeed())
		}
		Expect(k8sClient.Create(ctx, host)).To(Succeed())
		hosts = append(hosts, host)

		nicList := make([]interface{}, 0, len(nics))
		for _, n := range nics {
			nicList = append(nicList, n)
		}
		hardware := map[string]interface{}{"nics": nicList}
		if hostname != "" {
			hardware["hostname"] = hostname
		}
		Expect(unstructured.SetNestedMap(host.Object, hardware, "status", "hardware")).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, host)).To(Succeed())
		return host
	}
	nic := func(name, mac, ip string) map[string]interface{} {
		return map[string]interface{}{"name": name, "mac": mac, "ip": ip}
	}

	// nodeNetworkState creates the NodeNetworkState named after node.
	nodeNetworkState := func(node string, ifaces ...map[string]interface{}) {
		list := make([]interface{}, 0, len(ifaces))
		for _, i := range ifaces {
			list = append(list, i)
		}
		nns := &unstructured.Unstructured{}
		nns.SetGroupVersionKind(nmstateapi.GVK)
		nns.SetName(node)
		Expect(k8sClient.Create(ctx, nns)).To(Succeed())
		Expect(unstructured.SetNestedMap(nns.Object, map[string]interface{}{"interfaces": list}, "status", "currentState")).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, nns)).To(Succeed())
		nnsNames = append(nnsNames, node)
	}
	nnsEthernet := func(name, mac, ip string) map[string]interface{} {
		return map[string]interface{}{
			"name": name, "type": "ethernet", "state": "up", "mac-address": mac,
			"ipv4": map[string]interface{}{
				"address": []interface{}{map[string]interface{}{"ip": ip, "prefix-length": int64(24)}},
			},
		}
	}

	ipSpace := func(name string, ready bool) *uddiv1alpha1.IPSpace {
		sp := &uddiv1alpha1.IPSpace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       uddiv1alpha1.IPSpaceSpec{Name: name},
		}
		Expect(k8sClient.Create(ctx, sp)).To(Succeed())
		if ready {
			_, err := spaceRec.Reconcile(ctx, requestFor(sp))
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
			Expect(sp.Status.ID).NotTo(BeEmpty())
		}
		return sp
	}

	// policyIn creates a HostRegistrationPolicy in the spec's namespace, after letting mutate
	// adjust the spec.
	policyIn := func(name, space string, mutate func(*uddiv1alpha1.HostRegistrationPolicySpec)) *uddiv1alpha1.HostRegistrationPolicy {
		p := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.HostRegistrationPolicySpec{
				Selector: selectorFor(),
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space},
			},
		}
		if mutate != nil {
			mutate(&p.Spec)
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		return p
	}

	reconcileWith := func(r *HostRegistrationPolicyReconciler, p *uddiv1alpha1.HostRegistrationPolicy) {
		_, err := r.Reconcile(ctx, requestFor(p))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
	}
	reconcilePolicy := func(p *uddiv1alpha1.HostRegistrationPolicy) { reconcileWith(reconciler, p) }

	getFixed := func(name string) *uddiv1alpha1.FixedAddress {
		var f uddiv1alpha1.FixedAddress
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &f)).To(Succeed())
		return &f
	}
	getAddress := func(name string) *uddiv1alpha1.Address {
		var a uddiv1alpha1.Address
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &a)).To(Succeed())
		return &a
	}
	exists := func(obj client.Object, name string) bool {
		return k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, obj) == nil
	}
	fixedExists := func(name string) bool { return exists(&uddiv1alpha1.FixedAddress{}, name) }
	addressExists := func(name string) bool { return exists(&uddiv1alpha1.Address{}, name) }

	skipOf := func(p *uddiv1alpha1.HostRegistrationPolicy, node string, reason hostreg.SkipReason) *uddiv1alpha1.SkippedNode {
		for i := range p.Status.Skipped {
			if p.Status.Skipped[i].Node == node && p.Status.Skipped[i].Reason == string(reason) {
				return &p.Status.Skipped[i]
			}
		}
		return nil
	}

	Describe("Metal3 as a MAC source", func() {
		It("takes the MAC of the BareMetalHost NIC carrying the InternalIP, and Auto mode registers a FixedAddress", func() {
			space := ipSpace("space-nic", true)
			node := newNode(nil, internalIP("10.30.0.10"))
			bareMetalHost(metal3api.DefaultNamespace, "", "other-host.example.com",
				nic("eno1", "52:55:00:D1:55:AA", "10.30.0.10"),
				nic("eno2", "52:55:00:D1:55:AB", "10.99.9.9"))
			policy := policyIn("nodes", space.Name, nil)

			reconcilePolicy(policy)

			name := hostreg.RegistrationName(policy.Name, node.Name)
			Expect(addressExists(name)).To(BeFalse())
			fa := getFixed(name)
			Expect(fa.Spec.MatchType).To(Equal(uddiv1alpha1.FixedAddressMatchType("mac")))
			Expect(fa.Spec.MatchValue).To(Equal("52:55:00:d1:55:aa"))
			Expect(fa.Spec.Address).To(Equal("10.30.0.10"))
			Expect(skipOf(policy, node.Name, hostreg.SkipNoMAC)).To(BeNil())
		})

		It("falls back to the bootMACAddress of the host whose inspected hostname is the Node name", func() {
			space := ipSpace("space-boot", true)
			node := newNode(nil, internalIP("10.30.0.20"))
			// No NIC carries the Node's address, so only the hostname rule can match.
			bareMetalHost(metal3api.DefaultNamespace, "52:55:00:D1:55:BB", strings.ToUpper(node.Name),
				nic("eno1", "52:55:00:D1:55:BC", "10.99.9.20"))
			policy := policyIn("nodes", space.Name, nil)

			reconcilePolicy(policy)

			fa := getFixed(hostreg.RegistrationName(policy.Name, node.Name))
			Expect(fa.Spec.MatchValue).To(Equal("52:55:00:d1:55:bb"))
		})

		It("falls back to an Address when metal3.enabled is false and when the API is not available", func() {
			space := ipSpace("space-off", true)
			node := newNode(nil, internalIP("10.30.0.30"))
			bareMetalHost(metal3api.DefaultNamespace, "", "", nic("eno1", "52:55:00:D1:55:CC", "10.30.0.30"))

			// Proof the host would be used: the defaults register a FixedAddress.
			enabled := policyIn("enabled", space.Name, nil)
			reconcilePolicy(enabled)
			Expect(getFixed(hostreg.RegistrationName(enabled.Name, node.Name)).Spec.MatchValue).To(Equal("52:55:00:d1:55:cc"))

			off := false
			disabled := policyIn("disabled", space.Name, func(s *uddiv1alpha1.HostRegistrationPolicySpec) {
				s.MACSource.Metal3.Enabled = &off
			})
			reconcilePolicy(disabled)
			name := hostreg.RegistrationName(disabled.Name, node.Name)
			Expect(fixedExists(name)).To(BeFalse())
			Expect(getAddress(name).Spec.HWAddr).To(BeEmpty())

			unavailable := policyIn("unavailable", space.Name, nil)
			noMetal3 := &HostRegistrationPolicyReconciler{Client: k8sClient, Runtime: testRuntime, NMStateAvailable: true, Metal3Available: false}
			reconcileWith(noMetal3, unavailable)
			name = hostreg.RegistrationName(unavailable.Name, node.Name)
			Expect(fixedExists(name)).To(BeFalse())
			Expect(getAddress(name).Spec.HWAddr).To(BeEmpty())
		})

		It("reads BareMetalHosts from macSource.metal3.namespace only", func() {
			space := ipSpace("space-ns", true)
			inCustom := newNode(nil, internalIP("10.30.0.40"))
			inDefault := newNode(nil, internalIP("10.30.0.41"))
			custom := "hostreg3-bmh-" + ns
			Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: custom}})).To(Succeed())
			bareMetalHost(custom, "", "", nic("eno1", "52:55:00:D1:55:DD", "10.30.0.40"))
			bareMetalHost(metal3api.DefaultNamespace, "", "", nic("eno1", "52:55:00:D1:55:DE", "10.30.0.41"))
			policy := policyIn("nodes", space.Name, func(s *uddiv1alpha1.HostRegistrationPolicySpec) {
				s.MACSource.Metal3.Namespace = custom
			})

			reconcilePolicy(policy)

			Expect(getFixed(hostreg.RegistrationName(policy.Name, inCustom.Name)).Spec.MatchValue).To(Equal("52:55:00:d1:55:dd"))
			Expect(fixedExists(hostreg.RegistrationName(policy.Name, inDefault.Name))).To(BeFalse())
			Expect(addressExists(hostreg.RegistrationName(policy.Name, inDefault.Name))).To(BeTrue())
		})
	})

	Describe("additional interfaces", func() {
		storageInterface := func(space string) uddiv1alpha1.AdditionalInterface {
			e := uddiv1alpha1.AdditionalInterface{Name: "storage", CIDR: storageCIDR, HostnameSuffix: "-storage"}
			if space != "" {
				e.SpaceRef = &uddiv1alpha1.ObjectRef{Name: space}
			}
			return e
		}

		It("registers an interface found among the Node addresses and one found only in NodeNetworkState, each in the interface's IPSpace", func() {
			primary := ipSpace("space-primary", true)
			storage := ipSpace("space-storage", true)

			// alpha lists its storage address in status.addresses; Metal3 knows the NIC.
			alpha := newNode(map[string]string{
				"uddi.infoblox.com/hostname": "alpha.example.com",
				"uddi.infoblox.com/mac":      primaryMAC,
			}, internalIP("10.30.1.10"), internalIP("10.40.0.10"))
			bareMetalHost(metal3api.DefaultNamespace, "", "", nic("eno2", bmhNICMAC, "10.40.0.10"))

			// bravo does not: NodeNetworkState is the only place that has the storage address.
			bravo := newNode(map[string]string{"uddi.infoblox.com/hostname": "bravo.example.com"}, internalIP("10.30.1.20"))
			nodeNetworkState(bravo.Name,
				nnsEthernet("eno1", "52:54:00:bb:00:01", "10.30.1.20"),
				nnsEthernet("eno2", "52:54:00:bb:00:02", "10.40.0.20"))

			policy := policyIn("nodes", primary.Name, func(s *uddiv1alpha1.HostRegistrationPolicySpec) {
				s.AdditionalInterfaces = []uddiv1alpha1.AdditionalInterface{storageInterface(storage.Name)}
			})
			reconcilePolicy(policy)

			Expect(policy.Status.Pending).To(Equal(int32(4)), "two primary and two storage registrations")
			Expect(policy.Status.Skipped).To(BeEmpty())

			// Primary registrations keep their own address, MAC and IPSpace.
			alphaPrimary := getFixed(hostreg.RegistrationName(policy.Name, alpha.Name))
			Expect(alphaPrimary.Spec.Address).To(Equal("10.30.1.10"))
			Expect(alphaPrimary.Spec.MatchValue).To(Equal(primaryMAC))
			Expect(alphaPrimary.Spec.SpaceRef.Name).To(Equal(primary.Name))
			Expect(alphaPrimary.Labels).NotTo(HaveKey(hostreg.LabelInterface))

			alphaStorage := getFixed(hostreg.InterfaceRegistrationName(policy.Name, alpha.Name, "storage"))
			Expect(alphaStorage.Labels).To(HaveKeyWithValue(hostreg.LabelPolicy, policy.Name))
			Expect(alphaStorage.Labels).To(HaveKeyWithValue(hostreg.LabelNode, alpha.Name))
			Expect(alphaStorage.Labels).To(HaveKeyWithValue(hostreg.LabelInterface, "storage"))
			Expect(alphaStorage.Spec.Address).To(Equal("10.40.0.10"))
			Expect(alphaStorage.Spec.MatchValue).To(Equal(bmhNICMACLow))
			Expect(alphaStorage.Spec.Name).To(Equal("alpha-storage.example.com"))
			Expect(alphaStorage.Spec.Hostname).To(Equal("alpha-storage.example.com"))
			Expect(alphaStorage.Spec.SpaceRef.Name).To(Equal(storage.Name))
			Expect(alphaStorage.Spec.Comment).To(Equal(fmt.Sprintf(
				"Registered by HostRegistrationPolicy %s/%s for Node %s interface storage", ns, policy.Name, alpha.Name)))
			owner := metav1.GetControllerOf(alphaStorage)
			Expect(owner).NotTo(BeNil())
			Expect(owner.Kind).To(Equal("Node"))
			Expect(owner.Name).To(Equal(alpha.Name))

			bravoStorage := getFixed(hostreg.InterfaceRegistrationName(policy.Name, bravo.Name, "storage"))
			Expect(bravoStorage.Labels).To(HaveKeyWithValue(hostreg.LabelInterface, "storage"))
			Expect(bravoStorage.Spec.Address).To(Equal("10.40.0.20"))
			Expect(bravoStorage.Spec.MatchValue).To(Equal("52:54:00:bb:00:02"))
			Expect(bravoStorage.Spec.Name).To(Equal("bravo-storage.example.com"))
			Expect(bravoStorage.Spec.SpaceRef.Name).To(Equal(storage.Name))
			// The primary registration of bravo comes from the NodeNetworkState interface
			// that carries its registered address, not from the storage interface.
			Expect(getFixed(hostreg.RegistrationName(policy.Name, bravo.Name)).Spec.MatchValue).To(Equal("52:54:00:bb:00:01"))

			Expect(collectEvents()).To(ContainElement(ContainSubstring(fmt.Sprintf("for Node %s interface storage", alpha.Name))))
		})

		It("releases the registration of an interface that is removed from the spec", func() {
			space := ipSpace("space-release", true)
			node := newNode(map[string]string{"uddi.infoblox.com/mac": primaryMAC}, internalIP("10.30.1.30"), internalIP("10.40.0.30"))
			policy := policyIn("nodes", space.Name, func(s *uddiv1alpha1.HostRegistrationPolicySpec) {
				s.AdditionalInterfaces = []uddiv1alpha1.AdditionalInterface{storageInterface("")}
			})

			reconcilePolicy(policy)

			storageName := hostreg.InterfaceRegistrationName(policy.Name, node.Name, "storage")
			primaryName := hostreg.RegistrationName(policy.Name, node.Name)
			// No MAC for the storage address anywhere, so Auto mode registers an Address, in
			// the policy's own IPSpace because the entry has no spaceRef.
			Expect(getAddress(storageName).Spec.SpaceRef.Name).To(Equal(space.Name))
			Expect(fixedExists(primaryName)).To(BeTrue())
			drainEvents()

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(policy), policy)).To(Succeed())
			policy.Spec.AdditionalInterfaces = nil
			Expect(k8sClient.Update(ctx, policy)).To(Succeed())
			reconcilePolicy(policy)

			Expect(addressExists(storageName)).To(BeFalse())
			Expect(fixedExists(primaryName)).To(BeTrue(), "the primary registration stays")
			Expect(collectEvents()).To(ContainElement(ContainSubstring("released Address " + storageName + " for Node " + node.Name + " interface storage")))
		})

		It("reports SpaceNotReady until the IPSpace of an additional interface has a status.id", func() {
			primary := ipSpace("space-ready", true)
			storage := ipSpace("space-pending", false)
			node := newNode(map[string]string{"uddi.infoblox.com/mac": primaryMAC}, internalIP("10.30.1.40"), internalIP("10.40.0.40"))
			policy := policyIn("nodes", primary.Name, func(s *uddiv1alpha1.HostRegistrationPolicySpec) {
				s.AdditionalInterfaces = []uddiv1alpha1.AdditionalInterface{storageInterface(storage.Name)}
			})

			reconcilePolicy(policy)

			cond := readyOf(policy)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonSpaceNotReady))
			Expect(cond.Message).To(ContainSubstring(storage.Name))
			skip := skipOf(policy, node.Name, hostreg.SkipSpaceNotReady)
			Expect(skip).NotTo(BeNil())
			Expect(skip.Message).To(HavePrefix("interface storage:"))
			Expect(fixedExists(hostreg.RegistrationName(policy.Name, node.Name))).To(BeTrue())
			Expect(addressExists(hostreg.InterfaceRegistrationName(policy.Name, node.Name, "storage"))).To(BeFalse())

			_, err := spaceRec.Reconcile(ctx, requestFor(storage))
			Expect(err).NotTo(HaveOccurred())
			reconcilePolicy(policy)

			Expect(readyOf(policy).Reason).NotTo(Equal(uddiv1alpha1.ReasonSpaceNotReady))
			Expect(addressExists(hostreg.InterfaceRegistrationName(policy.Name, node.Name, "storage"))).To(BeTrue())
		})
	})

	Describe("planner fixes", func() {
		It("keeps the primary registration when a Node named <node>-<interface> collides with an interface registration", func() {
			space := ipSpace("space-conflict", true)
			nodeA := newNode(map[string]string{"uddi.infoblox.com/mac": primaryMAC}, internalIP("10.30.2.10"), internalIP("10.40.0.50"))
			nodeB := newNodeNamed(nodeA.Name+"-storage", map[string]string{"uddi.infoblox.com/mac": "52:54:00:bb:00:09"}, internalIP("10.30.2.20"))
			policy := policyIn("nodes", space.Name, func(s *uddiv1alpha1.HostRegistrationPolicySpec) {
				s.AdditionalInterfaces = []uddiv1alpha1.AdditionalInterface{{Name: "storage", CIDR: storageCIDR}}
			})

			reconcilePolicy(policy)

			// "<policy>-<A>-storage" is both A's storage registration and B's primary one.
			contested := hostreg.RegistrationName(policy.Name, nodeB.Name)
			Expect(contested).To(Equal(hostreg.InterfaceRegistrationName(policy.Name, nodeA.Name, "storage")))
			fa := getFixed(contested)
			Expect(fa.Labels).To(HaveKeyWithValue(hostreg.LabelNode, nodeB.Name))
			Expect(fa.Labels).NotTo(HaveKey(hostreg.LabelInterface))
			Expect(fa.Spec.Address).To(Equal("10.30.2.20"))
			owner := metav1.GetControllerOf(fa)
			Expect(owner).NotTo(BeNil())
			Expect(owner.Name).To(Equal(nodeB.Name))

			skip := skipOf(policy, nodeA.Name, hostreg.SkipNameConflict)
			Expect(skip).NotTo(BeNil())
			Expect(skip.Message).To(HavePrefix("interface storage:"))
			Expect(skip.Message).To(ContainSubstring(nodeB.Name))
			Expect(fixedExists(hostreg.RegistrationName(policy.Name, nodeA.Name))).To(BeTrue())

			var all uddiv1alpha1.FixedAddressList
			Expect(k8sClient.List(ctx, &all, client.InNamespace(ns))).To(Succeed())
			Expect(all.Items).To(HaveLen(2), "A's primary and B's primary; the colliding interface registration is skipped")

			// Reconciling again changes nothing.
			reconcilePolicy(policy)
			Expect(getFixed(contested).Labels).To(HaveKeyWithValue(hostreg.LabelNode, nodeB.Name))
			Expect(skipOf(policy, nodeA.Name, hostreg.SkipNameConflict)).NotTo(BeNil())
		})

		It("registers a Node with a 100 character name, labels it with the shortened name and still recognises its object", func() {
			space := ipSpace("space-long", true)
			const suffix = ".example.com"
			prefix := "hostreg3-long-"
			name := prefix + strings.Repeat("x", 100-len(prefix)-len(suffix)) + suffix
			Expect(name).To(HaveLen(100))
			node := newNodeNamed(name, map[string]string{"uddi.infoblox.com/mac": primaryMAC}, internalIP("10.30.3.10"))
			policy := policyIn("nodes", space.Name, nil)

			reconcilePolicy(policy)

			objName := hostreg.RegistrationName(policy.Name, node.Name)
			fa := getFixed(objName)
			label := fa.Labels[hostreg.LabelNode]
			Expect(label).To(Equal(labelvalue.For(node.Name)))
			Expect(label).NotTo(Equal(node.Name))
			Expect(validation.IsValidLabelValue(label)).To(BeEmpty())
			Expect(fa.Spec.Hostname).To(Equal(node.Name))
			uid := fa.UID

			// Once the FixedAddress is Ready, the planner must match it to the Node through
			// the shortened label: Registered, nothing released, same object.
			_, err := fixedRec.Reconcile(ctx, requestFor(fa))
			Expect(err).NotTo(HaveOccurred())
			drainEvents()
			reconcilePolicy(policy)
			Expect(policy.Status.Registered).To(Equal(int32(1)))
			Expect(policy.Status.Pending).To(Equal(int32(0)))
			Expect(getFixed(objName).UID).To(Equal(uid))
			Expect(collectEvents()).NotTo(ContainElement(ContainSubstring("Released")))

			// Opting the Node out releases the object through the same label.
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(Succeed())
			node.Annotations["uddi.infoblox.com/register"] = "false"
			Expect(k8sClient.Update(ctx, node)).To(Succeed())
			reconcilePolicy(policy)

			var current uddiv1alpha1.FixedAddress
			err = k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: objName}, &current)
			Expect(apierrors.IsNotFound(err) || (err == nil && !current.DeletionTimestamp.IsZero())).To(BeTrue(),
				"the FixedAddress is deleted (or terminating on its finalizer)")
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Released")))
		})
	})
})

// readyOf returns the Ready condition of a policy.
func readyOf(p *uddiv1alpha1.HostRegistrationPolicy) *metav1.Condition {
	for i := range p.Status.Conditions {
		if p.Status.Conditions[i].Type == uddiv1alpha1.ConditionReady {
			return &p.Status.Conditions[i]
		}
	}
	return nil
}

func TestBareMetalHostMACInputsChanged(t *testing.T) {
	host := func(boot string, hardware map[string]interface{}, phase string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]interface{}{
			"spec":   map[string]interface{}{"bootMACAddress": boot},
			"status": map[string]interface{}{"hardware": hardware, "provisioning": map[string]interface{}{"state": phase}},
		}}
		return u
	}
	hw := func(mac string) map[string]interface{} {
		return map[string]interface{}{"nics": []interface{}{map[string]interface{}{"mac": mac, "ip": "10.0.0.1"}}}
	}
	pred := bareMetalHostMACInputsChanged()

	base := host("aa:aa:aa:aa:aa:01", hw("bb:bb:bb:bb:bb:01"), "inspecting")
	cases := []struct {
		name          string
		before, after *unstructured.Unstructured
		want          bool
	}{
		{"an unrelated status field changes", base, host("aa:aa:aa:aa:aa:01", hw("bb:bb:bb:bb:bb:01"), "provisioned"), false},
		{"status.hardware changes", base, host("aa:aa:aa:aa:aa:01", hw("bb:bb:bb:bb:bb:02"), "inspecting"), true},
		{"spec.bootMACAddress changes", base, host("aa:aa:aa:aa:aa:02", hw("bb:bb:bb:bb:bb:01"), "inspecting"), true},
		{"status.hardware appears", host("aa:aa:aa:aa:aa:01", nil, "inspecting"), base, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pred.Update(event.UpdateEvent{ObjectOld: c.before, ObjectNew: c.after}); got != c.want {
				t.Errorf("Update = %v, want %v", got, c.want)
			}
		})
	}
	if !pred.Create(event.CreateEvent{Object: base}) || !pred.Delete(event.DeleteEvent{Object: base}) {
		t.Errorf("creates and deletes must always pass")
	}
}

func TestHostRegistrationSpaceNamesIncludesInterfaceSpaces(t *testing.T) {
	p := &uddiv1alpha1.HostRegistrationPolicy{Spec: uddiv1alpha1.HostRegistrationPolicySpec{
		SpaceRef: uddiv1alpha1.ObjectRef{Name: "corp"},
		AdditionalInterfaces: []uddiv1alpha1.AdditionalInterface{
			{Name: "storage", SpaceRef: &uddiv1alpha1.ObjectRef{Name: "storage-space"}},
			{Name: "mgmt"},
		},
	}}
	got := hostRegistrationSpaceNames(p)
	if len(got) != 2 || got[0] != "corp" || got[1] != "storage-space" {
		t.Errorf("hostRegistrationSpaceNames = %v, want [corp storage-space]", got)
	}
}

func TestToSkippedNodesCapsMessages(t *testing.T) {
	skips := []hostreg.Skip{{
		Node:    hostreg.NodeRef{Name: "n"},
		Reason:  hostreg.SkipNameConflict,
		Message: strings.Repeat("m", 2*maxSkipMessage),
	}}
	got := toSkippedNodes(skips)
	if len(got) != 1 || len(got[0].Message) != maxSkipMessage || !strings.HasSuffix(got[0].Message, "...") {
		t.Errorf("message = %d bytes, want it cut to %d with an ellipsis", len(got[0].Message), maxSkipMessage)
	}
}
