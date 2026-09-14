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

	"github.com/infobloxopen/universal-ddi-go-client/ipam"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/tags"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

var _ = Describe("Subnet controller through the generic object-layer loop", func() {
	var (
		ns              string
		reconciler      *SubnetReconciler
		spaceReconciler *IPSpaceReconciler
		blockReconciler *AddressBlockReconciler
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		reconciler = &SubnetReconciler{Client: k8sClient, Runtime: testRuntime}
		spaceReconciler = &IPSpaceReconciler{Client: k8sClient, Runtime: testRuntime}
		blockReconciler = &AddressBlockReconciler{Client: k8sClient, Runtime: testRuntime}
	})

	// makeReadySpace creates and reconciles an IPSpace CR, so its status.id can be used
	// as a dependency by the Subnet CRs under test.
	makeReadySpace := func(name string) *uddiv1alpha1.IPSpace {
		sp := &uddiv1alpha1.IPSpace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       uddiv1alpha1.IPSpaceSpec{Name: name},
		}
		Expect(k8sClient.Create(ctx, sp)).To(Succeed())
		_, err := spaceReconciler.Reconcile(ctx, requestFor(sp))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
		Expect(sp.Status.ID).NotTo(BeEmpty())
		return sp
	}

	// makeReadyBlock creates and reconciles an explicit AddressBlock CR, so its
	// status.id can be used as the parentRef of a next-available Subnet under test.
	makeReadyBlock := func(name, address string, cidr int32, space *uddiv1alpha1.IPSpace) *uddiv1alpha1.AddressBlock {
		b := &uddiv1alpha1.AddressBlock{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.AddressBlockSpec{
				SpaceRef: uddiv1alpha1.ObjectRef{Name: space.Name},
				Address:  address,
				CIDR:     cidr,
			},
		}
		Expect(k8sClient.Create(ctx, b)).To(Succeed())
		_, err := blockReconciler.Reconcile(ctx, requestFor(b))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(b), b)).To(Succeed())
		Expect(b.Status.ID).NotTo(BeEmpty())
		return b
	}

	// makeSubnet creates a Subnet CR with the given spec tweaks applied. Callers must
	// set spaceRef and either address or parentRef via mutate.
	makeSubnet := func(name string, mutate func(*uddiv1alpha1.Subnet)) *uddiv1alpha1.Subnet {
		s := &uddiv1alpha1.Subnet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       uddiv1alpha1.SubnetSpec{CIDR: 24},
		}
		if mutate != nil {
			mutate(s)
		}
		Expect(k8sClient.Create(ctx, s)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(s), s)).To(Succeed())
		return s
	}

	reconcileSubnet := func(s *uddiv1alpha1.Subnet) error {
		_, err := reconciler.Reconcile(ctx, requestFor(s))
		if err == nil {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(s), s)).To(Succeed())
		}
		return err
	}

	readyOf := func(s *uddiv1alpha1.Subnet) *metav1.Condition {
		return meta.FindStatusCondition(s.Status.Conditions, uddiv1alpha1.ConditionReady)
	}

	fakeSubnet := func(id string) *ipam.Subnet {
		got, err := testFake.GetSubnet(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return got
	}

	Context("with a Ready Connection", func() {
		BeforeEach(func() {
			makeReadyConnection(ns)
			drainEvents()
		})

		It("creates an explicit subnet, tags it and reports Ready", func() {
			space := makeReadySpace("corp")
			s := makeSubnet("openshift-lb", func(s *uddiv1alpha1.Subnet) {
				s.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				s.Spec.Address = "10.0.10.0"
				s.Spec.CIDR = 24
				s.Spec.Name = "openshift-lb"
			})

			Expect(reconcileSubnet(s)).To(Succeed())

			Expect(s.Status.ID).To(HavePrefix(uddi.PrefixSubnet + "/"))
			Expect(s.Status.Address).To(Equal("10.0.10.0"))
			Expect(s.Status.CIDR).To(Equal(int32(24)))
			Expect(s.Status.Space).To(Equal(space.Status.ID))
			Expect(readyOf(s).Status).To(Equal(metav1.ConditionTrue))
			Expect(controllerutil.ContainsFinalizer(s, uddiv1alpha1.Finalizer)).To(BeTrue())

			got := fakeSubnet(s.Status.ID)
			Expect(*got.Name).To(Equal("openshift-lb"))

			gotTags := uddi.FromAPITags(got.Tags)
			Expect(gotTags[tags.KeyManaged]).To(Equal(tags.ValueManaged))
			Expect(gotTags[tags.KeyOwner]).To(Equal(tags.Owner(ns, "Subnet", "openshift-lb")))
			Expect(gotTags[tags.KeyUID]).To(Equal(string(s.UID)))

			Expect(collectEvents()).To(ContainElement(ContainSubstring("Created")))
		})

		It("allocates the next available subnet from an address block, and a second CR gets a different subnet", func() {
			space := makeReadySpace("corp")
			block := makeReadyBlock("block-10", testRootAddress, 16, space)

			s := makeSubnet("next-lb", func(s *uddiv1alpha1.Subnet) {
				s.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				s.Spec.CIDR = 24
				s.Spec.ParentRef = &uddiv1alpha1.ObjectRef{Name: block.Name}
			})

			Expect(reconcileSubnet(s)).To(Succeed())

			Expect(s.Status.Address).NotTo(BeEmpty())
			Expect(s.Status.CIDR).To(Equal(int32(24)))
			Expect(s.Status.Space).To(Equal(space.Status.ID))
			Expect(readyOf(s).Status).To(Equal(metav1.ConditionTrue))

			s2 := makeSubnet("next-lb-2", func(s *uddiv1alpha1.Subnet) {
				s.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				s.Spec.CIDR = 24
				s.Spec.ParentRef = &uddiv1alpha1.ObjectRef{Name: block.Name}
			})
			Expect(reconcileSubnet(s2)).To(Succeed())

			Expect(s2.Status.Address).NotTo(BeEmpty())
			Expect(s2.Status.Address).NotTo(Equal(s.Status.Address))
		})

		It("round trips dhcpOptions into the fake", func() {
			space := makeReadySpace("corp")
			s := makeSubnet("dhcp-subnet", func(s *uddiv1alpha1.Subnet) {
				s.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				s.Spec.Address = "10.0.20.0"
				s.Spec.CIDR = 24
				s.Spec.DHCPOptions = []uddiv1alpha1.DHCPOption{
					{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionCode: "dhcp/option_code/aaa", OptionValue: "10.0.20.1"},
					{Type: uddiv1alpha1.DHCPOptionTypeGroup, Group: "dhcp/option_group/bbb"},
				}
			})

			Expect(reconcileSubnet(s)).To(Succeed())

			got := fakeSubnet(s.Status.ID)
			Expect(got.DhcpOptions).To(HaveLen(2))
			Expect(*got.DhcpOptions[0].Type).To(Equal("option"))
			Expect(*got.DhcpOptions[0].OptionCode).To(Equal("dhcp/option_code/aaa"))
			Expect(*got.DhcpOptions[0].OptionValue).To(Equal("10.0.20.1"))
			Expect(*got.DhcpOptions[1].Type).To(Equal("group"))
			Expect(*got.DhcpOptions[1].Group).To(Equal("dhcp/option_group/bbb"))
		})

		It("patches dhcpHost", func() {
			space := makeReadySpace("corp")
			s := makeSubnet("host-subnet", func(s *uddiv1alpha1.Subnet) {
				s.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				s.Spec.Address = "10.0.30.0"
				s.Spec.CIDR = 24
			})
			Expect(reconcileSubnet(s)).To(Succeed())
			id := s.Status.ID
			drainEvents()

			s.Spec.DHCPHost = "dhcp/host/abc"
			Expect(k8sClient.Update(ctx, s)).To(Succeed())

			updatesBefore := testFake.CallCount("UpdateSubnet")
			Expect(reconcileSubnet(s)).To(Succeed())

			Expect(testFake.CallCount("UpdateSubnet")).To(Equal(updatesBefore + 1))
			got := fakeSubnet(id)
			Expect(*got.DhcpHost).To(Equal("dhcp/host/abc"))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Updated")))

			// A second pass with no spec change must not patch again.
			updatesBefore = testFake.CallCount("UpdateSubnet")
			Expect(reconcileSubnet(s)).To(Succeed())
			Expect(testFake.CallCount("UpdateSubnet")).To(Equal(updatesBefore))
		})

		It("recovers a next-available subnet whose status write was lost, without allocating twice", func() {
			space := makeReadySpace("corp")
			block := makeReadyBlock("block-10", testRootAddress, 16, space)

			s := makeSubnet("recovered", func(s *uddiv1alpha1.Subnet) {
				s.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				s.Spec.CIDR = 24
				s.Spec.ParentRef = &uddiv1alpha1.ObjectRef{Name: block.Name}
			})

			// Simulate a previous pass that allocated the subnet in Universal DDI but
			// crashed before the status write landed: the fake already holds the
			// subnet, tagged with this CR's uid, while the CR's status is still empty.
			cidr := int64(24)
			pre, err := testFake.CreateSubnet(ctx, ipam.Subnet{
				Address: strp(block.Status.ID + uddi.NextAvailableSubnetSuffix),
				Cidr:    &cidr,
				Space:   strp(space.Status.ID),
				Tags: uddi.ToAPITags(map[string]string{
					tags.KeyManaged: tags.ValueManaged,
					tags.KeyUID:     string(s.UID),
				}),
			})
			Expect(err).NotTo(HaveOccurred())
			createsBefore := testFake.CallCount("CreateSubnet")

			Expect(reconcileSubnet(s)).To(Succeed())

			Expect(testFake.CallCount("CreateSubnet")).To(Equal(createsBefore))
			Expect(s.Status.ID).To(Equal(*pre.Id))
			Expect(s.Status.Address).To(Equal(*pre.Address))
			Expect(readyOf(s).Status).To(Equal(metav1.ConditionTrue))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Adopted")))
		})

		It("round trips disableDhcp into the fake and patches it when the spec changes", func() {
			sub := makeSubnet("dhcp-off", func(s *uddiv1alpha1.Subnet) {
				s.Spec.SpaceRef = uddiv1alpha1.ObjectRef{ID: testIPSpaceID}
				s.Spec.Address = "10.30.7.0"
				s.Spec.DisableDHCP = true
			})
			Expect(reconcileSubnet(sub)).To(Succeed())

			Expect(fakeSubnet(sub.Status.ID).DisableDhcp).NotTo(BeNil())
			Expect(*fakeSubnet(sub.Status.ID).DisableDhcp).To(BeTrue())

			// A second pass with no spec change must not patch.
			updatesBefore := testFake.CallCount("UpdateSubnet")
			Expect(reconcileSubnet(sub)).To(Succeed())
			Expect(testFake.CallCount("UpdateSubnet")).To(Equal(updatesBefore))

			sub.Spec.DisableDHCP = false
			Expect(k8sClient.Update(ctx, sub)).To(Succeed())
			Expect(reconcileSubnet(sub)).To(Succeed())

			Expect(testFake.CallCount("UpdateSubnet")).To(Equal(updatesBefore + 1))
			Expect(*fakeSubnet(sub.Status.ID).DisableDhcp).To(BeFalse())
		})

		It("deletes the Universal DDI subnet when the uid tag matches", func() {
			space := makeReadySpace("corp")
			s := makeSubnet("delete-me", func(s *uddiv1alpha1.Subnet) {
				s.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				s.Spec.Address = "10.0.40.0"
				s.Spec.CIDR = 24
			})
			Expect(reconcileSubnet(s)).To(Succeed())
			id := s.Status.ID
			drainEvents()

			Expect(k8sClient.Delete(ctx, s)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, requestFor(s))
			Expect(err).NotTo(HaveOccurred())

			_, err = testFake.GetSubnet(ctx, id)
			Expect(uddi.IsNotFound(err)).To(BeTrue())

			err = k8sClient.Get(ctx, client.ObjectKeyFromObject(s), s)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Deleted")))
		})
	})
})
