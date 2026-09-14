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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/tags"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

var _ = Describe("FixedAddress controller through the generic object-layer loop", func() {
	var (
		ns         string
		reconciler *FixedAddressReconciler
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		reconciler = &FixedAddressReconciler{Client: k8sClient, Runtime: testRuntime}
	})

	makeFixedAddress := func(name, address string, matchType uddiv1alpha1.FixedAddressMatchType, matchValue string, mutate func(*uddiv1alpha1.FixedAddress)) *uddiv1alpha1.FixedAddress {
		f := &uddiv1alpha1.FixedAddress{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.FixedAddressSpec{
				SpaceRef:   uddiv1alpha1.ObjectRef{ID: testIPSpaceID},
				Address:    address,
				MatchType:  matchType,
				MatchValue: matchValue,
				Name:       name,
				Hostname:   name + ".example.com",
			},
		}
		if mutate != nil {
			mutate(f)
		}
		Expect(k8sClient.Create(ctx, f)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(f), f)).To(Succeed())
		return f
	}

	reconcileFixedAddress := func(f *uddiv1alpha1.FixedAddress) error {
		_, err := reconciler.Reconcile(ctx, requestFor(f))
		if err == nil {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(f), f)).To(Succeed())
		}
		return err
	}

	readyOf := func(f *uddiv1alpha1.FixedAddress) *metav1.Condition {
		return meta.FindStatusCondition(f.Status.Conditions, uddiv1alpha1.ConditionReady)
	}

	fakeFixedAddress := func(id string) *ipam.FixedAddress {
		got, err := testFake.GetFixedAddress(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return got
	}

	Context("with a Ready Connection", func() {
		BeforeEach(func() {
			makeReadyConnection(ns, uddiv1alpha1.DefaultConnectionName)
			drainEvents()
		})

		It("creates a mac-matched fixed address, tags it and reports Ready", func() {
			f := makeFixedAddress("node-mac", "10.0.50.10", "mac", "52:54:00:aa:bb:cc", nil)

			Expect(reconcileFixedAddress(f)).To(Succeed())

			Expect(f.Status.ID).To(HavePrefix(uddi.PrefixFixedAddress + "/"))
			Expect(f.Status.Address).To(Equal("10.0.50.10"))
			Expect(readyOf(f).Status).To(Equal(metav1.ConditionTrue))

			got := fakeFixedAddress(f.Status.ID)
			Expect(got.MatchType).To(Equal("mac"))
			Expect(got.MatchValue).To(Equal("52:54:00:aa:bb:cc"))
			Expect(*got.Name).To(Equal("node-mac"))
			Expect(*got.Hostname).To(Equal("node-mac.example.com"))

			tagsOf := uddi.FromAPITags(got.Tags)
			Expect(tagsOf[tags.KeyManaged]).To(Equal(tags.ValueManaged))
			Expect(tagsOf[tags.KeyUID]).To(Equal(string(f.UID)))

			Expect(collectEvents()).To(ContainElement(ContainSubstring("Created")))
		})

		It("creates a duid-matched fixed address for an IPv6 client", func() {
			f := makeFixedAddress("node-duid", "2001:db8::50", "duid", "00:03:00:01:52:54:00:aa:bb:cc", nil)

			Expect(reconcileFixedAddress(f)).To(Succeed())

			Expect(f.Status.Address).To(Equal("2001:db8::50"))
			Expect(readyOf(f).Status).To(Equal(metav1.ConditionTrue))

			got := fakeFixedAddress(f.Status.ID)
			Expect(got.MatchType).To(Equal("duid"))
			Expect(got.MatchValue).To(Equal("00:03:00:01:52:54:00:aa:bb:cc"))
		})

		It("adopts an existing fixed address that already carries this CR's uid tag", func() {
			f := makeFixedAddress("adopt-by-uid", "10.0.50.20", "mac", "52:54:00:aa:bb:01", nil)

			existing, err := testFake.CreateFixedAddress(ctx, ipam.FixedAddress{
				Address:    "10.0.50.20",
				IpSpace:    optStr(testIPSpaceID),
				MatchType:  "mac",
				MatchValue: "52:54:00:aa:bb:01",
				Tags: uddi.ToAPITags(map[string]string{
					tags.KeyManaged: tags.ValueManaged,
					tags.KeyUID:     string(f.UID),
				}),
			})
			Expect(err).NotTo(HaveOccurred())
			createsBefore := testFake.CallCount("CreateFixedAddress")

			Expect(reconcileFixedAddress(f)).To(Succeed())

			Expect(f.Status.ID).To(Equal(*existing.Id))
			Expect(testFake.CallCount("CreateFixedAddress")).To(Equal(createsBefore))
			Expect(readyOf(f).Status).To(Equal(metav1.ConditionTrue))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Adopted")))
		})

		It("patches matchValue, hostname and dhcpOptions when the spec changes", func() {
			f := makeFixedAddress("patch-me", "10.0.50.30", "mac", "52:54:00:aa:bb:02", nil)
			Expect(reconcileFixedAddress(f)).To(Succeed())
			id := f.Status.ID
			drainEvents()

			f.Spec.MatchValue = "52:54:00:aa:bb:99"
			f.Spec.Hostname = "renamed.example.com"
			f.Spec.DHCPOptions = []uddiv1alpha1.DHCPOption{
				{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionCode: "dhcp/option_code/1", OptionValue: "300"},
			}
			Expect(k8sClient.Update(ctx, f)).To(Succeed())

			updatesBefore := testFake.CallCount("UpdateFixedAddress")
			Expect(reconcileFixedAddress(f)).To(Succeed())

			Expect(testFake.CallCount("UpdateFixedAddress")).To(Equal(updatesBefore + 1))
			Expect(f.Status.ID).To(Equal(id))
			got := fakeFixedAddress(id)
			Expect(got.MatchValue).To(Equal("52:54:00:aa:bb:99"))
			Expect(*got.Hostname).To(Equal("renamed.example.com"))
			Expect(got.DhcpOptions).To(HaveLen(1))
			Expect(*got.DhcpOptions[0].OptionValue).To(Equal("300"))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Updated")))

			// A second pass with no spec change must not patch again.
			updatesBefore = testFake.CallCount("UpdateFixedAddress")
			Expect(reconcileFixedAddress(f)).To(Succeed())
			Expect(testFake.CallCount("UpdateFixedAddress")).To(Equal(updatesBefore))
		})

		It("rejects a matchType change via CEL", func() {
			f := makeFixedAddress("immutable-match-type", "10.0.50.40", "mac", "52:54:00:aa:bb:03", nil)
			Expect(reconcileFixedAddress(f)).To(Succeed())

			f.Spec.MatchType = "client_text"
			err := k8sClient.Update(ctx, f)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("matchType is immutable"))
		})

		It("deletes the Universal DDI fixed address when the uid tag matches", func() {
			f := makeFixedAddress("delete-me", "10.0.50.50", "mac", "52:54:00:aa:bb:04", nil)
			Expect(reconcileFixedAddress(f)).To(Succeed())
			id := f.Status.ID
			drainEvents()

			Expect(k8sClient.Delete(ctx, f)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, requestFor(f))
			Expect(err).NotTo(HaveOccurred())

			_, err = testFake.GetFixedAddress(ctx, id)
			Expect(uddi.IsNotFound(err)).To(BeTrue())
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Deleted")))
		})
	})
})
