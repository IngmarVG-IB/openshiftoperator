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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/tags"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// testIPSpaceID is a fixed ip space id shared by the Range, Address and FixedAddress
// specs. None of the three adapters look the space up in Universal DDI (spec.spaceRef
// resolves to an id, never a lookup), so it does not need to correspond to a real fake
// IPSpace; it only has to be consistent between an Address and the fake Subnet or Range
// it allocates from, so the fake's "taken addresses in this space" filtering lines up.
const testIPSpaceID = "ipam/ip_space/corp-test"

// newSubnetWithID creates a Subnet CR backed by a real ipam.Subnet in the fake, standing
// in for the Subnet controller, which is still a wave 2 placeholder. Shared with the
// Address specs.
func newSubnetWithID(ns, name, address string, cidr int64, spaceID string) *uddiv1alpha1.Subnet {
	space := spaceID
	created, err := testFake.CreateSubnet(ctx, ipam.Subnet{
		Address: &address,
		Cidr:    &cidr,
		Space:   &space,
	})
	Expect(err).NotTo(HaveOccurred())
	sub := &uddiv1alpha1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: uddiv1alpha1.SubnetSpec{
			SpaceRef: uddiv1alpha1.ObjectRef{ID: spaceID},
			Address:  address,
			CIDR:     int32(cidr),
		},
	}
	Expect(k8sClient.Create(ctx, sub)).To(Succeed())
	sub.Status.ID = *created.Id
	Expect(k8sClient.Status().Update(ctx, sub)).To(Succeed())
	return sub
}

// newRangeWithID creates a Range CR backed by a real ipam.Range in the fake. Shared with
// the Address specs.
func newRangeWithID(ns, name, start, end, spaceID string) *uddiv1alpha1.Range {
	space := spaceID
	created, err := testFake.CreateRange(ctx, ipam.Range{
		Start: start,
		End:   end,
		Space: &space,
	})
	Expect(err).NotTo(HaveOccurred())
	rg := &uddiv1alpha1.Range{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: uddiv1alpha1.RangeSpec{
			SpaceRef: uddiv1alpha1.ObjectRef{ID: spaceID},
			Start:    start,
			End:      end,
		},
	}
	Expect(k8sClient.Create(ctx, rg)).To(Succeed())
	rg.Status.ID = *created.Id
	Expect(k8sClient.Status().Update(ctx, rg)).To(Succeed())
	return rg
}

var _ = Describe("Range controller through the generic object-layer loop", func() {
	var (
		ns         string
		reconciler *RangeReconciler
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		reconciler = &RangeReconciler{Client: k8sClient, Runtime: testRuntime}
	})

	makeRange := func(name string, mutate func(*uddiv1alpha1.Range)) *uddiv1alpha1.Range {
		rg := &uddiv1alpha1.Range{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.RangeSpec{
				SpaceRef: uddiv1alpha1.ObjectRef{ID: testIPSpaceID},
				Start:    "10.0.30.10",
				End:      "10.0.30.20",
				Name:     name,
				Comment:  "managed by uddi-operator",
			},
		}
		if mutate != nil {
			mutate(rg)
		}
		Expect(k8sClient.Create(ctx, rg)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rg), rg)).To(Succeed())
		return rg
	}

	reconcileRange := func(rg *uddiv1alpha1.Range) error {
		_, err := reconciler.Reconcile(ctx, requestFor(rg))
		if err == nil {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rg), rg)).To(Succeed())
		}
		return err
	}

	readyOf := func(rg *uddiv1alpha1.Range) *metav1.Condition {
		return meta.FindStatusCondition(rg.Status.Conditions, uddiv1alpha1.ConditionReady)
	}

	fakeRange := func(id string) *ipam.Range {
		got, err := testFake.GetRange(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return got
	}

	Context("with a Ready Connection", func() {
		BeforeEach(func() {
			makeReadyConnection(ns, uddiv1alpha1.DefaultConnectionName)
			drainEvents()
		})

		It("creates the Universal DDI range with exclusion ranges and dhcp options, tags it and reports Ready", func() {
			rg := makeRange("create-me", func(rg *uddiv1alpha1.Range) {
				rg.Spec.ExclusionRanges = []uddiv1alpha1.ExclusionRange{
					{Start: "10.0.30.15", End: "10.0.30.16", Comment: "reserved"},
				}
				rg.Spec.DHCPOptions = []uddiv1alpha1.DHCPOption{
					{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionCode: "dhcp/option_code/1", OptionValue: "10.0.30.1"},
				}
			})

			Expect(reconcileRange(rg)).To(Succeed())

			Expect(rg.Status.ID).To(HavePrefix(uddi.PrefixRange + "/"))
			Expect(rg.Status.Start).To(Equal("10.0.30.10"))
			Expect(rg.Status.End).To(Equal("10.0.30.20"))
			Expect(readyOf(rg).Status).To(Equal(metav1.ConditionTrue))
			Expect(meta.IsStatusConditionTrue(rg.Status.Conditions, uddiv1alpha1.ConditionSynced)).To(BeTrue())
			Expect(controllerutil.ContainsFinalizer(rg, uddiv1alpha1.Finalizer)).To(BeTrue())

			got := fakeRange(rg.Status.ID)
			Expect(*got.Name).To(Equal("create-me"))
			Expect(got.ExclusionRanges).To(HaveLen(1))
			Expect(got.ExclusionRanges[0].Start).To(Equal("10.0.30.15"))
			Expect(*got.ExclusionRanges[0].Comment).To(Equal("reserved"))
			Expect(got.DhcpOptions).To(HaveLen(1))
			Expect(*got.DhcpOptions[0].OptionCode).To(Equal("dhcp/option_code/1"))

			t := uddi.FromAPITags(got.Tags)
			Expect(t[tags.KeyManaged]).To(Equal(tags.ValueManaged))
			Expect(t[tags.KeyUID]).To(Equal(string(rg.UID)))

			Expect(collectEvents()).To(ContainElement(ContainSubstring("Created")))
		})

		It("adopts an existing range that already carries this CR's uid tag", func() {
			rg := makeRange("adopt-by-uid", nil)

			existing, err := testFake.CreateRange(ctx, ipam.Range{
				Start: "10.0.30.10",
				End:   "10.0.30.20",
				Space: optStr(testIPSpaceID),
				Tags: uddi.ToAPITags(map[string]string{
					tags.KeyManaged: tags.ValueManaged,
					tags.KeyUID:     string(rg.UID),
				}),
			})
			Expect(err).NotTo(HaveOccurred())
			createsBefore := testFake.CallCount("CreateRange")

			Expect(reconcileRange(rg)).To(Succeed())

			Expect(rg.Status.ID).To(Equal(*existing.Id))
			Expect(testFake.CallCount("CreateRange")).To(Equal(createsBefore))
			Expect(readyOf(rg).Status).To(Equal(metav1.ConditionTrue))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Adopted")))
		})

		It("patches exclusionRanges when the spec changes", func() {
			rg := makeRange("patch-me", nil)
			Expect(reconcileRange(rg)).To(Succeed())
			id := rg.Status.ID
			drainEvents()

			rg.Spec.ExclusionRanges = []uddiv1alpha1.ExclusionRange{
				{Start: "10.0.30.18", End: "10.0.30.19"},
			}
			Expect(k8sClient.Update(ctx, rg)).To(Succeed())

			updatesBefore := testFake.CallCount("UpdateRange")
			Expect(reconcileRange(rg)).To(Succeed())

			Expect(testFake.CallCount("UpdateRange")).To(Equal(updatesBefore + 1))
			Expect(rg.Status.ID).To(Equal(id))
			got := fakeRange(id)
			Expect(got.ExclusionRanges).To(HaveLen(1))
			Expect(got.ExclusionRanges[0].Start).To(Equal("10.0.30.18"))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Updated")))

			// A second pass with no spec change must not patch again.
			updatesBefore = testFake.CallCount("UpdateRange")
			Expect(reconcileRange(rg)).To(Succeed())
			Expect(testFake.CallCount("UpdateRange")).To(Equal(updatesBefore))
		})

		It("deletes the Universal DDI range when the uid tag matches", func() {
			rg := makeRange("delete-me", nil)
			Expect(reconcileRange(rg)).To(Succeed())
			id := rg.Status.ID
			drainEvents()

			Expect(k8sClient.Delete(ctx, rg)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, requestFor(rg))
			Expect(err).NotTo(HaveOccurred())

			_, err = testFake.GetRange(ctx, id)
			Expect(uddi.IsNotFound(err)).To(BeTrue())
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Deleted")))
		})
	})
})
