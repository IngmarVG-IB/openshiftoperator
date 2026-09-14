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

var _ = Describe("AddressBlock controller through the generic object-layer loop", func() {
	var (
		ns              string
		reconciler      *AddressBlockReconciler
		spaceReconciler *IPSpaceReconciler
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		reconciler = &AddressBlockReconciler{Client: k8sClient, Runtime: testRuntime}
		spaceReconciler = &IPSpaceReconciler{Client: k8sClient, Runtime: testRuntime}
	})

	// makeReadySpace creates and reconciles an IPSpace CR, so its status.id can be used
	// as a dependency by the AddressBlock CRs under test.
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

	// makeBlock creates an AddressBlock CR with the given spec tweaks applied. Callers
	// must set spaceRef and either address or parentRef via mutate.
	makeBlock := func(name string, mutate func(*uddiv1alpha1.AddressBlock)) *uddiv1alpha1.AddressBlock {
		b := &uddiv1alpha1.AddressBlock{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       uddiv1alpha1.AddressBlockSpec{CIDR: 24},
		}
		if mutate != nil {
			mutate(b)
		}
		Expect(k8sClient.Create(ctx, b)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(b), b)).To(Succeed())
		return b
	}

	reconcileBlock := func(b *uddiv1alpha1.AddressBlock) error {
		_, err := reconciler.Reconcile(ctx, requestFor(b))
		if err == nil {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(b), b)).To(Succeed())
		}
		return err
	}

	readyOf := func(b *uddiv1alpha1.AddressBlock) *metav1.Condition {
		return meta.FindStatusCondition(b.Status.Conditions, uddiv1alpha1.ConditionReady)
	}

	fakeBlock := func(id string) *ipam.AddressBlock {
		got, err := testFake.GetAddressBlock(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return got
	}

	Context("with a Ready Connection", func() {
		BeforeEach(func() {
			makeReadyConnection(ns)
			drainEvents()
		})

		It("creates an explicit address block, tags it and reports Ready", func() {
			space := makeReadySpace("corp")
			b := makeBlock("block-10", func(b *uddiv1alpha1.AddressBlock) {
				b.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				b.Spec.Address = testRootAddress
				b.Spec.CIDR = 16
				b.Spec.Name = "corp-root"
				b.Spec.Comment = "root block"
			})

			Expect(reconcileBlock(b)).To(Succeed())

			Expect(b.Status.ID).To(HavePrefix(uddi.PrefixAddressBlock + "/"))
			Expect(b.Status.Address).To(Equal(testRootAddress))
			Expect(b.Status.CIDR).To(Equal(int32(16)))
			Expect(b.Status.Space).To(Equal(space.Status.ID))
			Expect(readyOf(b).Status).To(Equal(metav1.ConditionTrue))
			Expect(controllerutil.ContainsFinalizer(b, uddiv1alpha1.Finalizer)).To(BeTrue())

			got := fakeBlock(b.Status.ID)
			Expect(*got.Name).To(Equal("corp-root"))
			Expect(*got.Comment).To(Equal("root block"))

			gotTags := uddi.FromAPITags(got.Tags)
			Expect(gotTags[tags.KeyManaged]).To(Equal(tags.ValueManaged))
			Expect(gotTags[tags.KeyOwner]).To(Equal(tags.Owner(ns, "AddressBlock", "block-10")))
			Expect(gotTags[tags.KeyUID]).To(Equal(string(b.UID)))

			Expect(collectEvents()).To(ContainElement(ContainSubstring("Created")))
		})

		It("allocates the next available block from a parent, and a second CR gets a different block", func() {
			space := makeReadySpace("corp")
			parent := makeBlock("parent", func(b *uddiv1alpha1.AddressBlock) {
				b.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				b.Spec.Address = testRootAddress
				b.Spec.CIDR = 16
			})
			Expect(reconcileBlock(parent)).To(Succeed())

			child1 := makeBlock("child-1", func(b *uddiv1alpha1.AddressBlock) {
				b.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				b.Spec.CIDR = 24
				b.Spec.ParentRef = &uddiv1alpha1.ObjectRef{Name: parent.Name}
			})
			Expect(reconcileBlock(child1)).To(Succeed())

			Expect(child1.Status.Address).NotTo(BeEmpty())
			Expect(child1.Status.CIDR).To(Equal(int32(24)))
			Expect(readyOf(child1).Status).To(Equal(metav1.ConditionTrue))

			createsBefore := testFake.CallCount("CreateAddressBlock")
			child2 := makeBlock("child-2", func(b *uddiv1alpha1.AddressBlock) {
				b.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				b.Spec.CIDR = 24
				b.Spec.ParentRef = &uddiv1alpha1.ObjectRef{Name: parent.Name}
			})
			Expect(reconcileBlock(child2)).To(Succeed())

			Expect(testFake.CallCount("CreateAddressBlock")).To(Equal(createsBefore + 1))
			Expect(child2.Status.Address).NotTo(BeEmpty())
			Expect(child2.Status.Address).NotTo(Equal(child1.Status.Address))
		})

		It("reports DependencyNotReady when the IPSpace has no status.id yet", func() {
			space := &uddiv1alpha1.IPSpace{
				ObjectMeta: metav1.ObjectMeta{Name: "corp", Namespace: ns},
				Spec:       uddiv1alpha1.IPSpaceSpec{Name: "corp"},
			}
			Expect(k8sClient.Create(ctx, space)).To(Succeed())

			b := makeBlock("needs-space", func(b *uddiv1alpha1.AddressBlock) {
				b.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: "corp"}
				b.Spec.Address = testRootAddress
				b.Spec.CIDR = 16
			})

			Expect(reconcileBlock(b)).To(Succeed())

			cond := readyOf(b)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonDependencyNotReady))
			Expect(b.Status.ID).To(BeEmpty())

			// Once the IPSpace reports an id, the AddressBlock is created.
			space.Status.ID = uddi.PrefixIPSpace + "/abc"
			Expect(k8sClient.Status().Update(ctx, space)).To(Succeed())

			Expect(reconcileBlock(b)).To(Succeed())
			Expect(readyOf(b).Status).To(Equal(metav1.ConditionTrue))
		})

		It("patches name and dhcpOptions", func() {
			space := makeReadySpace("corp")
			b := makeBlock("patch-me", func(b *uddiv1alpha1.AddressBlock) {
				b.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				b.Spec.Address = "10.1.0.0"
				b.Spec.CIDR = 16
			})
			Expect(reconcileBlock(b)).To(Succeed())
			id := b.Status.ID
			drainEvents()

			b.Spec.Name = "renamed"
			b.Spec.DHCPOptions = []uddiv1alpha1.DHCPOption{
				{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionCode: "dhcp/option_code/aaa", OptionValue: "10.1.0.1"},
			}
			Expect(k8sClient.Update(ctx, b)).To(Succeed())

			updatesBefore := testFake.CallCount("UpdateAddressBlock")
			Expect(reconcileBlock(b)).To(Succeed())

			Expect(testFake.CallCount("UpdateAddressBlock")).To(Equal(updatesBefore + 1))
			got := fakeBlock(id)
			Expect(*got.Name).To(Equal("renamed"))
			Expect(got.DhcpOptions).To(HaveLen(1))
			Expect(*got.DhcpOptions[0].OptionCode).To(Equal("dhcp/option_code/aaa"))
			Expect(*got.DhcpOptions[0].OptionValue).To(Equal("10.1.0.1"))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Updated")))

			// A second pass with no spec change must not patch again.
			updatesBefore = testFake.CallCount("UpdateAddressBlock")
			Expect(reconcileBlock(b)).To(Succeed())
			Expect(testFake.CallCount("UpdateAddressBlock")).To(Equal(updatesBefore))
		})

		It("rejects an address change via CEL immutability", func() {
			space := makeReadySpace("corp")
			b := makeBlock("immutable-address", func(b *uddiv1alpha1.AddressBlock) {
				b.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				b.Spec.Address = "10.2.0.0"
				b.Spec.CIDR = 16
			})
			Expect(reconcileBlock(b)).To(Succeed())

			b.Spec.Address = "10.3.0.0"
			err := k8sClient.Update(ctx, b)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("address is immutable"))
		})

		// The immutability rules are spec-level so they survive the Kubernetes 1.29
		// floor, where oldSelf.orValue() does not exist. Both lanes have to hold: an
		// address that started empty for next-available allocation must stay empty, and
		// a parentRef that was set must stay exactly as it was.
		It("rejects adding or removing address and parentRef on a next-available block", func() {
			space := makeReadySpace("corp")
			parent := makeBlock("cel-parent", func(b *uddiv1alpha1.AddressBlock) {
				b.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				b.Spec.Address = "10.20.0.0"
				b.Spec.CIDR = 16
			})
			Expect(reconcileBlock(parent)).To(Succeed())

			child := makeBlock("cel-child", func(b *uddiv1alpha1.AddressBlock) {
				b.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				b.Spec.CIDR = 24
				b.Spec.ParentRef = &uddiv1alpha1.ObjectRef{Name: parent.Name}
			})
			Expect(reconcileBlock(child)).To(Succeed())
			Expect(child.Status.Address).NotTo(BeEmpty())

			// Lane 1: a block allocated from a parent must keep spec.address empty.
			child.Spec.Address = child.Status.Address
			err := k8sClient.Update(ctx, child)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("address is immutable"))

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(child), child)).To(Succeed())
			child.Spec.ParentRef = nil
			err = k8sClient.Update(ctx, child)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("parentRef is immutable"))

			// Lane 2: a block created with an explicit address must not drop it, and
			// must not gain a parentRef either.
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parent), parent)).To(Succeed())
			parent.Spec.Address = ""
			err = k8sClient.Update(ctx, parent)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("address is immutable"))

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parent), parent)).To(Succeed())
			parent.Spec.ParentRef = &uddiv1alpha1.ObjectRef{Name: "cel-child"}
			err = k8sClient.Update(ctx, parent)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("parentRef is immutable"))
		})

		It("deletes the Universal DDI address block when the uid tag matches", func() {
			space := makeReadySpace("corp")
			b := makeBlock("delete-me", func(b *uddiv1alpha1.AddressBlock) {
				b.Spec.SpaceRef = uddiv1alpha1.ObjectRef{Name: space.Name}
				b.Spec.Address = "10.4.0.0"
				b.Spec.CIDR = 16
			})
			Expect(reconcileBlock(b)).To(Succeed())
			id := b.Status.ID
			drainEvents()

			Expect(k8sClient.Delete(ctx, b)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, requestFor(b))
			Expect(err).NotTo(HaveOccurred())

			_, err = testFake.GetAddressBlock(ctx, id)
			Expect(uddi.IsNotFound(err)).To(BeTrue())

			err = k8sClient.Get(ctx, client.ObjectKeyFromObject(b), b)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Deleted")))
		})
	})
})
