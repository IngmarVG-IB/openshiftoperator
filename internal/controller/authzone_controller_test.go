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

	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"
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

// ptrTo returns a pointer to a copy of v, for building SDK model literals whose fields
// are pointers.
func ptrTo[T any](v T) *T { return &v }

var _ = Describe("AuthZone controller through the generic object-layer loop", func() {
	var (
		ns         string
		reconciler *AuthZoneReconciler
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		reconciler = &AuthZoneReconciler{Client: k8sClient, Runtime: testRuntime}
	})

	// makeReadyView creates a View CR and reconciles it so it has a status.id, the
	// dependency every AuthZone in these specs needs.
	makeReadyView := func(name string) *uddiv1alpha1.View {
		v := &uddiv1alpha1.View{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       uddiv1alpha1.ViewSpec{Name: name},
		}
		Expect(k8sClient.Create(ctx, v)).To(Succeed())
		vr := &ViewReconciler{Client: k8sClient, Runtime: testRuntime}
		_, err := vr.Reconcile(ctx, requestFor(v))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(v), v)).To(Succeed())
		return v
	}

	// makeZone creates an AuthZone CR with the given spec tweaks applied.
	makeZone := func(name string, mutate func(*uddiv1alpha1.AuthZone)) *uddiv1alpha1.AuthZone {
		z := &uddiv1alpha1.AuthZone{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.AuthZoneSpec{
				FQDN:        "apps.example.com.",
				PrimaryType: uddiv1alpha1.AuthZonePrimaryCloud,
			},
		}
		if mutate != nil {
			mutate(z)
		}
		Expect(k8sClient.Create(ctx, z)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(z), z)).To(Succeed())
		return z
	}

	reconcileZone := func(z *uddiv1alpha1.AuthZone) error {
		_, err := reconciler.Reconcile(ctx, requestFor(z))
		if err == nil {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(z), z)).To(Succeed())
		}
		return err
	}

	readyOf := func(z *uddiv1alpha1.AuthZone) *metav1.Condition {
		return meta.FindStatusCondition(z.Status.Conditions, uddiv1alpha1.ConditionReady)
	}

	fakeZone := func(id string) *dnsconfig.AuthZone {
		got, err := testFake.GetAuthZone(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return got
	}

	Context("with a Ready Connection", func() {
		BeforeEach(func() {
			makeReadyConnection(ns)
			drainEvents()
		})

		It("creates a cloud zone with nsgs, tags it and reports Ready", func() {
			view := makeReadyView("default-view")
			z := makeZone("apps-zone", func(z *uddiv1alpha1.AuthZone) {
				z.Spec.ViewRef = uddiv1alpha1.ObjectRef{Name: view.Name}
				z.Spec.NSGs = []string{"dns/auth_nsg/11111111-0000-4000-8000-000000000001"}
				z.Spec.Comment = "managed by uddi-operator"
			})

			Expect(reconcileZone(z)).To(Succeed())

			Expect(z.Status.ID).To(HavePrefix(uddi.PrefixAuthZone + "/"))
			Expect(z.Status.FQDN).To(Equal("apps.example.com."))
			Expect(z.Status.View).To(Equal(view.Status.ID))
			Expect(z.Status.ObservedGeneration).To(Equal(z.Generation))
			Expect(readyOf(z).Status).To(Equal(metav1.ConditionTrue))
			Expect(meta.IsStatusConditionTrue(z.Status.Conditions, uddiv1alpha1.ConditionSynced)).To(BeTrue())
			Expect(controllerutil.ContainsFinalizer(z, uddiv1alpha1.Finalizer)).To(BeTrue())

			got := fakeZone(z.Status.ID)
			Expect(*got.Fqdn).To(Equal("apps.example.com."))
			Expect(*got.View).To(Equal(view.Status.ID))
			Expect(*got.PrimaryType).To(Equal("cloud"))
			Expect(got.Nsgs).To(Equal(z.Spec.NSGs))
			Expect(*got.Comment).To(Equal("managed by uddi-operator"))

			t := uddi.FromAPITags(got.Tags)
			Expect(t[tags.KeyManaged]).To(Equal(tags.ValueManaged))
			Expect(t[tags.KeyUID]).To(Equal(string(z.UID)))

			Expect(collectEvents()).To(ContainElement(ContainSubstring("Created")))
		})

		It("creates an external zone with externalPrimaries", func() {
			view := makeReadyView("ext-view")
			z := makeZone("ext-zone", func(z *uddiv1alpha1.AuthZone) {
				z.Spec.ViewRef = uddiv1alpha1.ObjectRef{Name: view.Name}
				z.Spec.PrimaryType = uddiv1alpha1.AuthZonePrimaryExternal
				z.Spec.ExternalPrimaries = []uddiv1alpha1.ExternalPrimary{
					{Type: "primary", Address: "10.0.0.53", FQDN: "ns1.corp."},
				}
			})

			Expect(reconcileZone(z)).To(Succeed())
			Expect(readyOf(z).Status).To(Equal(metav1.ConditionTrue))

			got := fakeZone(z.Status.ID)
			Expect(*got.PrimaryType).To(Equal("external"))
			Expect(got.ExternalPrimaries).To(HaveLen(1))
			Expect(got.ExternalPrimaries[0].Type).To(Equal("primary"))
			Expect(*got.ExternalPrimaries[0].Address).To(Equal("10.0.0.53"))
			Expect(*got.ExternalPrimaries[0].Fqdn).To(Equal("ns1.corp."))
		})

		It("adopts a pre-existing zone with adoptExisting and leaves it in the fake under deletionPolicy Orphan", func() {
			view := makeReadyView("adopt-view")

			existing, err := testFake.CreateAuthZone(ctx, dnsconfig.AuthZone{
				Fqdn:        ptrTo("adopt.example.com."),
				View:        ptrTo(view.Status.ID),
				PrimaryType: ptrTo("cloud"),
			})
			Expect(err).NotTo(HaveOccurred())
			createsBefore := testFake.CallCount("CreateAuthZone")

			z := makeZone("adopt-zone", func(z *uddiv1alpha1.AuthZone) {
				z.Spec.FQDN = "adopt.example.com."
				z.Spec.ViewRef = uddiv1alpha1.ObjectRef{Name: view.Name}
				z.Spec.Lifecycle.AdoptExisting = true
				z.Spec.Lifecycle.DeletionPolicy = uddiv1alpha1.DeletionPolicyOrphan
			})

			Expect(reconcileZone(z)).To(Succeed())

			Expect(z.Status.ID).To(Equal(*existing.Id))
			Expect(testFake.CallCount("CreateAuthZone")).To(Equal(createsBefore))
			Expect(readyOf(z).Status).To(Equal(metav1.ConditionTrue))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Adopted")))
			drainEvents()

			Expect(k8sClient.Delete(ctx, z)).To(Succeed())
			deletesBefore := testFake.CallCount("DeleteAuthZone")
			_, err = reconciler.Reconcile(ctx, requestFor(z))
			Expect(err).NotTo(HaveOccurred())

			Expect(testFake.CallCount("DeleteAuthZone")).To(Equal(deletesBefore))
			Expect(fakeZone(*existing.Id).Fqdn).NotTo(BeNil())
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(z), z))).To(BeTrue())
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Orphaned")))
		})

		It("reports AlreadyExists for an untagged zone when adoptExisting is false", func() {
			view := makeReadyView("conflict-view")
			_, err := testFake.CreateAuthZone(ctx, dnsconfig.AuthZone{
				Fqdn:        ptrTo("conflict.example.com."),
				View:        ptrTo(view.Status.ID),
				PrimaryType: ptrTo("cloud"),
			})
			Expect(err).NotTo(HaveOccurred())

			z := makeZone("conflict-zone", func(z *uddiv1alpha1.AuthZone) {
				z.Spec.FQDN = "conflict.example.com."
				z.Spec.ViewRef = uddiv1alpha1.ObjectRef{Name: view.Name}
			})
			createsBefore := testFake.CallCount("CreateAuthZone")

			Expect(reconcileZone(z)).To(Succeed())

			Expect(z.Status.ID).To(BeEmpty())
			Expect(testFake.CallCount("CreateAuthZone")).To(Equal(createsBefore))
			cond := readyOf(z)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonAlreadyExists))
			Expect(collectEvents()).To(ContainElement(ContainSubstring(uddisync.EventConflict)))
		})

		It("patches comment and nsgs when the spec changes", func() {
			view := makeReadyView("patch-view")
			z := makeZone("patch-zone", func(z *uddiv1alpha1.AuthZone) {
				z.Spec.ViewRef = uddiv1alpha1.ObjectRef{Name: view.Name}
			})
			Expect(reconcileZone(z)).To(Succeed())
			id := z.Status.ID
			drainEvents()

			z.Spec.Comment = testUpdatedComment
			z.Spec.NSGs = []string{"dns/auth_nsg/22222222-0000-4000-8000-000000000002"}
			Expect(k8sClient.Update(ctx, z)).To(Succeed())

			updatesBefore := testFake.CallCount("UpdateAuthZone")
			Expect(reconcileZone(z)).To(Succeed())

			Expect(testFake.CallCount("UpdateAuthZone")).To(Equal(updatesBefore + 1))
			Expect(z.Status.ID).To(Equal(id))
			got := fakeZone(id)
			Expect(*got.Comment).To(Equal(testUpdatedComment))
			Expect(got.Nsgs).To(Equal(z.Spec.NSGs))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Updated")))

			// A second pass with no spec change must not patch again.
			updatesBefore = testFake.CallCount("UpdateAuthZone")
			Expect(reconcileZone(z)).To(Succeed())
			Expect(testFake.CallCount("UpdateAuthZone")).To(Equal(updatesBefore))
		})

		It("deletes the Universal DDI zone when the uid tag matches", func() {
			view := makeReadyView("delete-view")
			z := makeZone("delete-zone", func(z *uddiv1alpha1.AuthZone) {
				z.Spec.ViewRef = uddiv1alpha1.ObjectRef{Name: view.Name}
			})
			Expect(reconcileZone(z)).To(Succeed())
			id := z.Status.ID
			drainEvents()

			Expect(k8sClient.Delete(ctx, z)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, requestFor(z))
			Expect(err).NotTo(HaveOccurred())

			_, err = testFake.GetAuthZone(ctx, id)
			Expect(uddi.IsNotFound(err)).To(BeTrue())

			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(z), z))).To(BeTrue())
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Deleted")))
		})

		It("reports DependencyNotReady when the View CR has no status.id yet", func() {
			v := &uddiv1alpha1.View{
				ObjectMeta: metav1.ObjectMeta{Name: "pending-view", Namespace: ns},
				Spec:       uddiv1alpha1.ViewSpec{Name: "pending-view"},
			}
			Expect(k8sClient.Create(ctx, v)).To(Succeed())

			z := makeZone("waits-for-view", func(z *uddiv1alpha1.AuthZone) {
				z.Spec.ViewRef = uddiv1alpha1.ObjectRef{Name: v.Name}
			})

			Expect(reconcileZone(z)).To(Succeed())
			cond := readyOf(z)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonDependencyNotReady))
			Expect(z.Status.ID).To(BeEmpty())
		})
	})

	Context("API server validation (CEL and CRD schema)", func() {
		It("rejects nsgs combined with internalSecondaries", func() {
			z := &uddiv1alpha1.AuthZone{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-combo", Namespace: ns},
				Spec: uddiv1alpha1.AuthZoneSpec{
					FQDN:                "bad.example.com.",
					ViewRef:             uddiv1alpha1.ObjectRef{Name: "some-view"},
					PrimaryType:         uddiv1alpha1.AuthZonePrimaryCloud,
					NSGs:                []string{"dns/auth_nsg/1"},
					InternalSecondaries: []uddiv1alpha1.InternalSecondary{{Host: "dns/host/1"}},
				},
			}
			err := k8sClient.Create(ctx, z)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("nsgs and internalSecondaries cannot both be set"))
		})

		It("rejects a second nsg", func() {
			z := &uddiv1alpha1.AuthZone{
				ObjectMeta: metav1.ObjectMeta{Name: "two-nsgs", Namespace: ns},
				Spec: uddiv1alpha1.AuthZoneSpec{
					FQDN:        "two.example.com.",
					ViewRef:     uddiv1alpha1.ObjectRef{Name: "some-view"},
					PrimaryType: uddiv1alpha1.AuthZonePrimaryCloud,
					NSGs:        []string{"dns/auth_nsg/1", "dns/auth_nsg/2"},
				},
			}
			err := k8sClient.Create(ctx, z)
			Expect(err).To(HaveOccurred())
		})
	})
})
