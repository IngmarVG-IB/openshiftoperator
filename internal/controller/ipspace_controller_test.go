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
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
	"github.com/IngmarVG-IB/openshiftoperator/internal/tags"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

var _ = Describe("IPSpace controller through the generic object-layer loop", func() {
	var (
		ns         string
		reconciler *IPSpaceReconciler
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		reconciler = &IPSpaceReconciler{Client: k8sClient, Runtime: testRuntime}
	})

	// makeIPSpace creates an IPSpace CR with the given spec tweaks applied.
	makeIPSpace := func(name string, mutate func(*uddiv1alpha1.IPSpace)) *uddiv1alpha1.IPSpace {
		s := &uddiv1alpha1.IPSpace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.IPSpaceSpec{
				Name:    name,
				Comment: "managed by uddi-operator",
			},
		}
		if mutate != nil {
			mutate(s)
		}
		Expect(k8sClient.Create(ctx, s)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(s), s)).To(Succeed())
		return s
	}

	reconcileSpace := func(s *uddiv1alpha1.IPSpace) error {
		_, err := reconciler.Reconcile(ctx, requestFor(s))
		if err == nil {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(s), s)).To(Succeed())
		}
		return err
	}

	readyOf := func(s *uddiv1alpha1.IPSpace) *metav1.Condition {
		return meta.FindStatusCondition(s.Status.Conditions, uddiv1alpha1.ConditionReady)
	}

	fakeSpace := func(id string) *ipam.IPSpace {
		got, err := testFake.GetIPSpace(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return got
	}

	Context("with a Ready Connection", func() {
		BeforeEach(func() {
			makeReadyConnection(ns)
			drainEvents()
		})

		It("creates the Universal DDI IP space, tags it and reports Ready", func() {
			s := makeIPSpace("create-me", nil)

			Expect(reconcileSpace(s)).To(Succeed())

			Expect(s.Status.ID).To(HavePrefix(uddi.PrefixIPSpace + "/"))
			Expect(s.Status.Name).To(Equal("create-me"))
			Expect(s.Status.ObservedGeneration).To(Equal(s.Generation))
			Expect(s.Status.LastSyncTime).NotTo(BeNil())
			Expect(readyOf(s).Status).To(Equal(metav1.ConditionTrue))
			Expect(meta.IsStatusConditionTrue(s.Status.Conditions, uddiv1alpha1.ConditionSynced)).To(BeTrue())
			Expect(controllerutil.ContainsFinalizer(s, uddiv1alpha1.Finalizer)).To(BeTrue())

			got := fakeSpace(s.Status.ID)
			Expect(got.Name).To(Equal("create-me"))
			Expect(*got.Comment).To(Equal("managed by uddi-operator"))

			gotTags := uddi.FromAPITags(got.Tags)
			Expect(gotTags[tags.KeyManaged]).To(Equal(tags.ValueManaged))
			Expect(gotTags[tags.KeyCluster]).To(Equal(TestClusterID))
			Expect(gotTags[tags.KeyOwner]).To(Equal(tags.Owner(ns, "IPSpace", "create-me")))
			Expect(gotTags[tags.KeyUID]).To(Equal(string(s.UID)))
			// The Connection's defaultTags land below the ownership tags.
			Expect(gotTags["env"]).To(Equal("test"))

			Expect(collectEvents()).To(ContainElement(ContainSubstring("Created")))
		})

		It("defaults spec.name to metadata.name", func() {
			s := makeIPSpace("default-name", func(s *uddiv1alpha1.IPSpace) {
				s.Spec.Name = ""
			})

			Expect(reconcileSpace(s)).To(Succeed())

			Expect(s.Status.Name).To(Equal("default-name"))
			Expect(fakeSpace(s.Status.ID).Name).To(Equal("default-name"))
		})

		It("adopts an existing IP space that already carries this CR's uid tag", func() {
			s := makeIPSpace("adopt-by-uid", nil)

			existing, err := testFake.CreateIPSpace(ctx, ipam.IPSpace{
				Name: "adopt-by-uid",
				Tags: uddi.ToAPITags(map[string]string{
					tags.KeyManaged: tags.ValueManaged,
					tags.KeyUID:     string(s.UID),
				}),
			})
			Expect(err).NotTo(HaveOccurred())
			createsBefore := testFake.CallCount("CreateIPSpace")

			Expect(reconcileSpace(s)).To(Succeed())

			Expect(s.Status.ID).To(Equal(*existing.Id))
			Expect(testFake.CallCount("CreateIPSpace")).To(Equal(createsBefore))
			Expect(readyOf(s).Status).To(Equal(metav1.ConditionTrue))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Adopted")))
		})

		It("reports AlreadyExists for an untagged IP space when adoptExisting is false", func() {
			_, err := testFake.CreateIPSpace(ctx, ipam.IPSpace{Name: "conflicting"})
			Expect(err).NotTo(HaveOccurred())

			s := makeIPSpace("conflicting", nil)
			createsBefore := testFake.CallCount("CreateIPSpace")

			Expect(reconcileSpace(s)).To(Succeed())

			Expect(s.Status.ID).To(BeEmpty())
			Expect(testFake.CallCount("CreateIPSpace")).To(Equal(createsBefore))
			cond := readyOf(s)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonAlreadyExists))
			Expect(collectEvents()).To(ContainElement(ContainSubstring(uddisync.EventConflict)))
		})

		It("patches Universal DDI when the comment changes", func() {
			s := makeIPSpace("patch-me", nil)
			Expect(reconcileSpace(s)).To(Succeed())
			id := s.Status.ID
			drainEvents()

			s.Spec.Comment = testUpdatedComment
			Expect(k8sClient.Update(ctx, s)).To(Succeed())

			updatesBefore := testFake.CallCount("UpdateIPSpace")
			Expect(reconcileSpace(s)).To(Succeed())

			Expect(testFake.CallCount("UpdateIPSpace")).To(Equal(updatesBefore + 1))
			Expect(s.Status.ID).To(Equal(id))
			got := fakeSpace(id)
			Expect(*got.Comment).To(Equal(testUpdatedComment))
			Expect(s.Status.ObservedGeneration).To(Equal(s.Generation))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Updated")))

			// A second pass with no spec change must not patch again.
			updatesBefore = testFake.CallCount("UpdateIPSpace")
			Expect(reconcileSpace(s)).To(Succeed())
			Expect(testFake.CallCount("UpdateIPSpace")).To(Equal(updatesBefore))
		})

		It("round trips dhcpOptions into the fake and patches them when the spec changes", func() {
			sp := makeIPSpace("with-options", func(s *uddiv1alpha1.IPSpace) {
				s.Spec.DHCPOptions = []uddiv1alpha1.DHCPOption{
					{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionCode: "dhcp/option_code/router", OptionValue: "10.0.0.1"},
					{Type: uddiv1alpha1.DHCPOptionTypeGroup, Group: "dhcp/option_group/base"},
				}
			})
			Expect(reconcileSpace(sp)).To(Succeed())

			got := fakeSpace(sp.Status.ID)
			Expect(got.DhcpOptions).To(HaveLen(2))
			Expect(*got.DhcpOptions[0].OptionCode).To(Equal("dhcp/option_code/router"))
			Expect(*got.DhcpOptions[0].OptionValue).To(Equal("10.0.0.1"))
			Expect(*got.DhcpOptions[1].Group).To(Equal("dhcp/option_group/base"))

			// A second pass with no spec change must not patch.
			updatesBefore := testFake.CallCount("UpdateIPSpace")
			Expect(reconcileSpace(sp)).To(Succeed())
			Expect(testFake.CallCount("UpdateIPSpace")).To(Equal(updatesBefore))

			sp.Spec.DHCPOptions = []uddiv1alpha1.DHCPOption{
				{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionCode: "dhcp/option_code/router", OptionValue: "10.0.0.254"},
			}
			Expect(k8sClient.Update(ctx, sp)).To(Succeed())
			Expect(reconcileSpace(sp)).To(Succeed())

			Expect(testFake.CallCount("UpdateIPSpace")).To(Equal(updatesBefore + 1))
			got = fakeSpace(sp.Status.ID)
			Expect(got.DhcpOptions).To(HaveLen(1))
			Expect(*got.DhcpOptions[0].OptionValue).To(Equal("10.0.0.254"))
		})

		It("deletes the Universal DDI IP space when the uid tag matches", func() {
			s := makeIPSpace("delete-me", nil)
			Expect(reconcileSpace(s)).To(Succeed())
			id := s.Status.ID
			drainEvents()

			Expect(k8sClient.Delete(ctx, s)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, requestFor(s))
			Expect(err).NotTo(HaveOccurred())

			_, err = testFake.GetIPSpace(ctx, id)
			Expect(uddi.IsNotFound(err)).To(BeTrue())

			err = k8sClient.Get(ctx, client.ObjectKeyFromObject(s), s)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Deleted")))
		})

		It("orphans rather than deletes when the uid tag no longer matches", func() {
			s := makeIPSpace("tag-mismatch", nil)
			Expect(reconcileSpace(s)).To(Succeed())
			id := s.Status.ID
			drainEvents()

			// Someone re-tagged the object in Universal DDI; it is no longer ours.
			_, err := testFake.UpdateIPSpace(ctx, id, ipam.IPSpace{
				Name: "tag-mismatch",
				Tags: uddi.ToAPITags(map[string]string{tags.KeyUID: "someone-elses-uid"}),
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Delete(ctx, s)).To(Succeed())
			deletesBefore := testFake.CallCount("DeleteIPSpace")
			_, err = reconciler.Reconcile(ctx, requestFor(s))
			Expect(err).NotTo(HaveOccurred())

			Expect(testFake.CallCount("DeleteIPSpace")).To(Equal(deletesBefore))
			Expect(fakeSpace(id).Name).To(Equal("tag-mismatch"))
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(s), s))).To(BeTrue())
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Orphaned")))
		})
	})

	Context("without a usable Connection", func() {
		It("reports ConnectionNotReady when the Connection does not exist", func() {
			s := makeIPSpace("no-connection", nil)

			Expect(reconcileSpace(s)).To(Succeed())

			cond := readyOf(s)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonConnectionNotReady))
			Expect(s.Status.ID).To(BeEmpty())
			Expect(controllerutil.ContainsFinalizer(s, uddiv1alpha1.Finalizer)).To(BeFalse())
		})
	})
})
