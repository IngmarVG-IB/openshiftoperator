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

var _ = Describe("View controller through the generic object-layer loop", func() {
	var (
		ns         string
		reconciler *ViewReconciler
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		reconciler = &ViewReconciler{Client: k8sClient, Runtime: testRuntime}
	})

	// makeView creates a View CR with the given spec tweaks applied.
	makeView := func(name string, mutate func(*uddiv1alpha1.View)) *uddiv1alpha1.View {
		v := &uddiv1alpha1.View{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.ViewSpec{
				Name:    name,
				Comment: "managed by uddi-operator",
			},
		}
		if mutate != nil {
			mutate(v)
		}
		Expect(k8sClient.Create(ctx, v)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(v), v)).To(Succeed())
		return v
	}

	reconcileView := func(v *uddiv1alpha1.View) error {
		_, err := reconciler.Reconcile(ctx, requestFor(v))
		if err == nil {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(v), v)).To(Succeed())
		}
		return err
	}

	readyOf := func(v *uddiv1alpha1.View) *metav1.Condition {
		return meta.FindStatusCondition(v.Status.Conditions, uddiv1alpha1.ConditionReady)
	}

	fakeView := func(id string) *dnsconfig.View {
		got, err := testFake.GetView(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return got
	}

	Context("with a Ready Connection", func() {
		BeforeEach(func() {
			makeReadyConnection(ns)
			drainEvents()
		})

		It("creates the Universal DDI view, tags it and reports Ready", func() {
			v := makeView("create-me", nil)

			Expect(reconcileView(v)).To(Succeed())

			Expect(v.Status.ID).To(HavePrefix(uddi.PrefixView + "/"))
			Expect(v.Status.Name).To(Equal("create-me"))
			Expect(v.Status.ObservedGeneration).To(Equal(v.Generation))
			Expect(v.Status.LastSyncTime).NotTo(BeNil())
			Expect(readyOf(v).Status).To(Equal(metav1.ConditionTrue))
			Expect(meta.IsStatusConditionTrue(v.Status.Conditions, uddiv1alpha1.ConditionSynced)).To(BeTrue())
			Expect(controllerutil.ContainsFinalizer(v, uddiv1alpha1.Finalizer)).To(BeTrue())

			got := fakeView(v.Status.ID)
			Expect(got.Name).To(Equal("create-me"))
			Expect(*got.Comment).To(Equal("managed by uddi-operator"))

			t := uddi.FromAPITags(got.Tags)
			Expect(t[tags.KeyManaged]).To(Equal(tags.ValueManaged))
			Expect(t[tags.KeyCluster]).To(Equal(TestClusterID))
			Expect(t[tags.KeyOwner]).To(Equal(tags.Owner(ns, "View", "create-me")))
			Expect(t[tags.KeyUID]).To(Equal(string(v.UID)))
			// The Connection's defaultTags land below the ownership tags.
			Expect(t["env"]).To(Equal("test"))

			Expect(collectEvents()).To(ContainElement(ContainSubstring("Created")))
		})

		It("adopts an existing view that already carries this CR's uid tag", func() {
			v := makeView("adopt-by-uid", nil)

			existing, err := testFake.CreateView(ctx, dnsconfig.View{
				Name: "adopt-by-uid",
				Tags: uddi.ToAPITags(map[string]string{
					tags.KeyManaged: tags.ValueManaged,
					tags.KeyUID:     string(v.UID),
				}),
			})
			Expect(err).NotTo(HaveOccurred())
			createsBefore := testFake.CallCount("CreateView")

			Expect(reconcileView(v)).To(Succeed())

			Expect(v.Status.ID).To(Equal(*existing.Id))
			Expect(testFake.CallCount("CreateView")).To(Equal(createsBefore))
			Expect(readyOf(v).Status).To(Equal(metav1.ConditionTrue))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Adopted")))
		})

		It("never adopts a view another CR manages, even with lifecycle.adoptExisting", func() {
			existing, err := testFake.CreateView(ctx, dnsconfig.View{
				Name: "shared-view",
				Tags: uddi.ToAPITags(map[string]string{
					tags.KeyManaged: tags.ValueManaged,
					tags.KeyCluster: TestClusterID,
					tags.KeyOwner:   tags.Owner("other-namespace", "View", "shared"),
					tags.KeyUID:     "uid-of-the-other-cr",
				}),
			})
			Expect(err).NotTo(HaveOccurred())

			v := makeView("shared-view", func(v *uddiv1alpha1.View) {
				v.Spec.Lifecycle.AdoptExisting = true
			})
			patchesBefore := testFake.CallCount("UpdateView")

			Expect(reconcileView(v)).To(Succeed())

			Expect(v.Status.ID).To(BeEmpty())
			Expect(readyOf(v).Status).To(Equal(metav1.ConditionFalse))
			Expect(readyOf(v).Reason).To(Equal(uddiv1alpha1.ReasonAlreadyExists))
			Expect(readyOf(v).Message).To(ContainSubstring("other-namespace/View/shared"))
			Expect(testFake.CallCount("UpdateView")).To(Equal(patchesBefore))
			Expect(uddi.FromAPITags(fakeView(*existing.Id).Tags)[tags.KeyUID]).To(Equal("uid-of-the-other-cr"))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Conflict")))
		})

		It("re-adopts its own orphaned view after the CR is recreated with the same name", func() {
			first := makeView("recreated", func(v *uddiv1alpha1.View) {
				v.Spec.Lifecycle.DeletionPolicy = uddiv1alpha1.DeletionPolicyOrphan
			})
			Expect(reconcileView(first)).To(Succeed())
			id := first.Status.ID
			Expect(k8sClient.Delete(ctx, first)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, requestFor(first))
			Expect(err).NotTo(HaveOccurred())
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(first), first))).To(BeTrue())

			second := makeView("recreated", func(v *uddiv1alpha1.View) {
				v.Spec.Lifecycle.AdoptExisting = true
			})
			Expect(second.UID).NotTo(Equal(first.UID))
			Expect(reconcileView(second)).To(Succeed())

			Expect(second.Status.ID).To(Equal(id))
			Expect(readyOf(second).Status).To(Equal(metav1.ConditionTrue))
			Expect(uddi.FromAPITags(fakeView(id).Tags)[tags.KeyUID]).To(Equal(string(second.UID)))
		})

		It("adopts an untagged view when lifecycle.adoptExisting is set", func() {
			existing, err := testFake.CreateView(ctx, dnsconfig.View{Name: "adopt-flag"})
			Expect(err).NotTo(HaveOccurred())

			v := makeView("adopt-flag", func(v *uddiv1alpha1.View) {
				v.Spec.Lifecycle.AdoptExisting = true
			})
			createsBefore := testFake.CallCount("CreateView")

			Expect(reconcileView(v)).To(Succeed())

			Expect(v.Status.ID).To(Equal(*existing.Id))
			Expect(testFake.CallCount("CreateView")).To(Equal(createsBefore))
			Expect(readyOf(v).Status).To(Equal(metav1.ConditionTrue))

			// Adoption stamps the ownership tags on the next diff.
			Expect(uddi.FromAPITags(fakeView(v.Status.ID).Tags)[tags.KeyUID]).To(Equal(string(v.UID)))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Adopted")))
		})

		It("reports AlreadyExists for an untagged view when adoptExisting is false", func() {
			_, err := testFake.CreateView(ctx, dnsconfig.View{Name: "conflicting"})
			Expect(err).NotTo(HaveOccurred())

			v := makeView("conflicting", nil)
			createsBefore := testFake.CallCount("CreateView")

			Expect(reconcileView(v)).To(Succeed())

			Expect(v.Status.ID).To(BeEmpty())
			Expect(testFake.CallCount("CreateView")).To(Equal(createsBefore))
			cond := readyOf(v)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonAlreadyExists))
			Expect(collectEvents()).To(ContainElement(ContainSubstring(uddisync.EventConflict)))
		})

		It("patches Universal DDI when the spec changes", func() {
			v := makeView("patch-me", nil)
			Expect(reconcileView(v)).To(Succeed())
			id := v.Status.ID
			drainEvents()

			v.Spec.Comment = testUpdatedComment
			v.Spec.Disabled = true
			Expect(k8sClient.Update(ctx, v)).To(Succeed())

			updatesBefore := testFake.CallCount("UpdateView")
			Expect(reconcileView(v)).To(Succeed())

			Expect(testFake.CallCount("UpdateView")).To(Equal(updatesBefore + 1))
			Expect(v.Status.ID).To(Equal(id))
			got := fakeView(id)
			Expect(*got.Comment).To(Equal(testUpdatedComment))
			Expect(*got.Disabled).To(BeTrue())
			Expect(v.Status.ObservedGeneration).To(Equal(v.Generation))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Updated")))

			// A second pass with no spec change must not patch again.
			updatesBefore = testFake.CallCount("UpdateView")
			Expect(reconcileView(v)).To(Succeed())
			Expect(testFake.CallCount("UpdateView")).To(Equal(updatesBefore))
		})

		It("recreates the view when it disappears out of band", func() {
			v := makeView("recreate-me", nil)
			Expect(reconcileView(v)).To(Succeed())
			oldID := v.Status.ID
			drainEvents()

			// Someone deletes the object straight in Universal DDI.
			Expect(testFake.DeleteView(ctx, oldID)).To(Succeed())

			Expect(reconcileView(v)).To(Succeed())

			Expect(v.Status.ID).NotTo(BeEmpty())
			Expect(v.Status.ID).NotTo(Equal(oldID))
			Expect(readyOf(v).Status).To(Equal(metav1.ConditionTrue))
			Expect(fakeView(v.Status.ID).Name).To(Equal("recreate-me"))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Recreated")))
		})

		It("deletes the Universal DDI view when the uid tag matches", func() {
			v := makeView("delete-me", nil)
			Expect(reconcileView(v)).To(Succeed())
			id := v.Status.ID
			drainEvents()

			Expect(k8sClient.Delete(ctx, v)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, requestFor(v))
			Expect(err).NotTo(HaveOccurred())

			_, err = testFake.GetView(ctx, id)
			Expect(uddi.IsNotFound(err)).To(BeTrue())

			err = k8sClient.Get(ctx, client.ObjectKeyFromObject(v), v)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Deleted")))
		})

		It("orphans rather than deletes when the uid tag no longer matches", func() {
			v := makeView("tag-mismatch", nil)
			Expect(reconcileView(v)).To(Succeed())
			id := v.Status.ID
			drainEvents()

			// Someone re-tagged the object in Universal DDI; it is no longer ours.
			_, err := testFake.UpdateView(ctx, id, dnsconfig.View{
				Name: "tag-mismatch",
				Tags: uddi.ToAPITags(map[string]string{tags.KeyUID: "someone-elses-uid"}),
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Delete(ctx, v)).To(Succeed())
			deletesBefore := testFake.CallCount("DeleteView")
			_, err = reconciler.Reconcile(ctx, requestFor(v))
			Expect(err).NotTo(HaveOccurred())

			Expect(testFake.CallCount("DeleteView")).To(Equal(deletesBefore))
			Expect(fakeView(id).Name).To(Equal("tag-mismatch"))
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(v), v))).To(BeTrue())
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Orphaned")))
		})

		It("leaves Universal DDI alone under deletionPolicy Orphan", func() {
			v := makeView("orphan-me", func(v *uddiv1alpha1.View) {
				v.Spec.Lifecycle.DeletionPolicy = uddiv1alpha1.DeletionPolicyOrphan
			})
			Expect(reconcileView(v)).To(Succeed())
			id := v.Status.ID
			drainEvents()

			Expect(k8sClient.Delete(ctx, v)).To(Succeed())
			deletesBefore := testFake.CallCount("DeleteView")
			_, err := reconciler.Reconcile(ctx, requestFor(v))
			Expect(err).NotTo(HaveOccurred())

			Expect(testFake.CallCount("DeleteView")).To(Equal(deletesBefore))
			Expect(fakeView(id).Name).To(Equal("orphan-me"))
			// The ownership tags stay behind so a later CR can re-adopt the object.
			Expect(uddi.FromAPITags(fakeView(id).Tags)[tags.KeyUID]).To(Equal(string(v.UID)))
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(v), v))).To(BeTrue())
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Orphaned")))
		})

		// A natural key is user-controlled text that ends up inside a _filter literal,
		// so it has to be quoted rather than concatenated.
		It("finds a view whose name contains a double quote", func() {
			const awkward = `we"ird "view"`
			existing, err := testFake.CreateView(ctx, dnsconfig.View{Name: awkward})
			Expect(err).NotTo(HaveOccurred())

			v := makeView("awkward-name", func(v *uddiv1alpha1.View) {
				v.Spec.Name = awkward
				v.Spec.Lifecycle.AdoptExisting = true
			})

			Expect(reconcileView(v)).To(Succeed())

			// The lookup matched the existing object instead of erroring or creating a
			// second one.
			Expect(v.Status.ID).To(Equal(*existing.Id))
			Expect(readyOf(v).Status).To(Equal(metav1.ConditionTrue))
			Expect(fakeView(v.Status.ID).Name).To(Equal(awkward))
			Expect(collectEvents()).To(ContainElement(ContainSubstring(uddisync.EventAdopted)))
		})

		It("waits for a referenced IPSpace to gain a status.id", func() {
			space := &uddiv1alpha1.IPSpace{
				ObjectMeta: metav1.ObjectMeta{Name: "corp", Namespace: ns},
				Spec:       uddiv1alpha1.IPSpaceSpec{Name: "corp"},
			}
			Expect(k8sClient.Create(ctx, space)).To(Succeed())

			v := makeView("needs-space", func(v *uddiv1alpha1.View) {
				v.Spec.IPSpaces = []uddiv1alpha1.ObjectRef{{Name: "corp"}}
			})

			Expect(reconcileView(v)).To(Succeed())
			cond := readyOf(v)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonDependencyNotReady))
			Expect(v.Status.ID).To(BeEmpty())

			// Once the IPSpace reports an id, the View binds it.
			space.Status.ID = uddi.PrefixIPSpace + "/abc"
			Expect(k8sClient.Status().Update(ctx, space)).To(Succeed())

			Expect(reconcileView(v)).To(Succeed())
			Expect(readyOf(v).Status).To(Equal(metav1.ConditionTrue))
			Expect(fakeView(v.Status.ID).IpSpaces).To(Equal([]string{uddi.PrefixIPSpace + "/abc"}))
		})
	})

	Context("without a usable Connection", func() {
		It("reports ConnectionNotReady when the Connection does not exist", func() {
			v := makeView("no-connection", nil)

			Expect(reconcileView(v)).To(Succeed())

			cond := readyOf(v)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonConnectionNotReady))
			Expect(v.Status.ID).To(BeEmpty())
			Expect(controllerutil.ContainsFinalizer(v, uddiv1alpha1.Finalizer)).To(BeFalse())
		})
	})

	// DESIGN.md section 7: a CR whose Connection CR is gone can never reach Universal
	// DDI again, so it is orphaned rather than wedged on Ready=False/Deleting forever.
	// A Connection that merely stopped verifying is transient and keeps blocking.
	Context("deleting without a usable Connection", func() {
		It("orphans the Universal DDI object when the Connection CR no longer exists", func() {
			conn := makeReadyConnection(ns)
			v := makeView("connection-deleted", nil)
			Expect(reconcileView(v)).To(Succeed())
			id := v.Status.ID
			drainEvents()

			Expect(k8sClient.Delete(ctx, conn)).To(Succeed())
			Expect(k8sClient.Delete(ctx, v)).To(Succeed())

			deletesBefore := testFake.CallCount("DeleteView")
			_, err := reconciler.Reconcile(ctx, requestFor(v))
			Expect(err).NotTo(HaveOccurred())

			// The CR is gone, the Universal DDI object is not, and it kept its tags so
			// a later CR with the same natural key can re-adopt it.
			Expect(testFake.CallCount("DeleteView")).To(Equal(deletesBefore))
			Expect(fakeView(id).Name).To(Equal("connection-deleted"))
			Expect(uddi.FromAPITags(fakeView(id).Tags)[tags.KeyUID]).To(Equal(string(v.UID)))
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(v), v))).To(BeTrue())
			Expect(collectEvents()).To(ContainElement(And(
				ContainSubstring(uddisync.EventConnectionMissing),
				ContainSubstring("left in place with its ownership tags"),
			)))
		})

		It("keeps blocking while the Connection exists but is not Ready", func() {
			conn := makeReadyConnection(ns)
			v := makeView("connection-unready", nil)
			Expect(reconcileView(v)).To(Succeed())
			id := v.Status.ID
			drainEvents()

			setConnectionReady(conn, metav1.ConditionFalse, uddiv1alpha1.ReasonAPIError, "the tenant is unreachable")
			Expect(k8sClient.Delete(ctx, v)).To(Succeed())

			deletesBefore := testFake.CallCount("DeleteView")
			_, err := reconciler.Reconcile(ctx, requestFor(v))
			Expect(err).NotTo(HaveOccurred())

			Expect(testFake.CallCount("DeleteView")).To(Equal(deletesBefore))
			Expect(fakeView(id).Name).To(Equal("connection-unready"))
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(v), v)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(v, uddiv1alpha1.Finalizer)).To(BeTrue())
			Expect(readyOf(v).Reason).To(Equal(uddiv1alpha1.ReasonDeleting))
			Expect(collectEvents()).NotTo(ContainElement(ContainSubstring(uddisync.EventConnectionMissing)))

			// Once the Connection verifies again the delete goes through normally.
			setConnectionReady(conn, metav1.ConditionTrue, uddiv1alpha1.ReasonVerified, "verified")
			_, err = reconciler.Reconcile(ctx, requestFor(v))
			Expect(err).NotTo(HaveOccurred())

			_, gerr := testFake.GetView(ctx, id)
			Expect(uddi.IsNotFound(gerr)).To(BeTrue())
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(v), v))).To(BeTrue())
		})
	})

	Context("without a usable Connection", func() {
		It("reports ConnectionNotReady when the Connection exists but is unverified", func() {
			makeSecret(ns, "creds", uddiv1alpha1.DefaultSecretKey, "key")
			conn := &uddiv1alpha1.Connection{
				ObjectMeta: metav1.ObjectMeta{Name: uddiv1alpha1.DefaultConnectionName, Namespace: ns},
				Spec:       uddiv1alpha1.ConnectionSpec{SecretRef: uddiv1alpha1.SecretKeyRef{Name: "creds"}},
			}
			Expect(k8sClient.Create(ctx, conn)).To(Succeed())

			v := makeView("unverified-connection", nil)
			Expect(reconcileView(v)).To(Succeed())

			cond := readyOf(v)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonConnectionNotReady))
		})
	})
})
