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
	"github.com/infobloxopen/universal-ddi-go-client/ipam"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/tags"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// taggedObject is what the tag removal specs need from an object-layer CR.
type taggedObject interface {
	client.Object
	StatusOf() *uddiv1alpha1.ObjectStatus
}

// tagRemovalSubject adapts one object-layer kind to the shared tag removal specs, so
// View and Subnet are held to exactly the same behaviour (DESIGN.md section 6, "Tag
// removal").
type tagRemovalSubject struct {
	kind string
	// updateCall is the fake method name counted for a patch, for example "UpdateView".
	updateCall string
	// create makes a new CR with the given spec.tags in ns, not reconciled yet.
	create func(ns, name string, specTags map[string]string) taggedObject
	// setSpecTags replaces spec.tags on the in-memory CR.
	setSpecTags func(obj taggedObject, specTags map[string]string)
	// reconcile runs one pass of the kind's reconciler.
	reconcile func(req ctrl.Request) error
	// fakeTags reads the tags of the fake's object.
	fakeTags func(id string) map[string]string
	// writeFakeTags replaces the tags of the fake's object, as an edit made straight in
	// Universal DDI would.
	writeFakeTags func(id string, t map[string]string)
}

var viewTagSubject = tagRemovalSubject{
	kind:       "View",
	updateCall: "UpdateView",
	create: func(ns, name string, specTags map[string]string) taggedObject {
		v := &uddiv1alpha1.View{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       uddiv1alpha1.ViewSpec{Name: name},
		}
		v.Spec.Tags = specTags
		Expect(k8sClient.Create(ctx, v)).To(Succeed())
		return v
	},
	setSpecTags: func(obj taggedObject, specTags map[string]string) {
		obj.(*uddiv1alpha1.View).Spec.Tags = specTags
	},
	reconcile: func(req ctrl.Request) error {
		_, err := (&ViewReconciler{Client: k8sClient, Runtime: testRuntime}).Reconcile(ctx, req)
		return err
	},
	fakeTags: func(id string) map[string]string {
		got, err := testFake.GetView(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return uddi.FromAPITags(got.Tags)
	},
	writeFakeTags: func(id string, t map[string]string) {
		got, err := testFake.GetView(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		_, err = testFake.UpdateView(ctx, id, dnsconfig.View{Name: got.Name, Tags: uddi.ToAPITags(t)})
		Expect(err).NotTo(HaveOccurred())
	},
}

var subnetTagSubject = tagRemovalSubject{
	kind:       "Subnet",
	updateCall: "UpdateSubnet",
	create: func(ns, name string, specTags map[string]string) taggedObject {
		s := &uddiv1alpha1.Subnet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.SubnetSpec{
				SpaceRef: uddiv1alpha1.ObjectRef{ID: testIPSpaceID},
				Address:  "10.60.0.0",
				CIDR:     24,
			},
		}
		s.Spec.Tags = specTags
		Expect(k8sClient.Create(ctx, s)).To(Succeed())
		return s
	},
	setSpecTags: func(obj taggedObject, specTags map[string]string) {
		obj.(*uddiv1alpha1.Subnet).Spec.Tags = specTags
	},
	reconcile: func(req ctrl.Request) error {
		_, err := (&SubnetReconciler{Client: k8sClient, Runtime: testRuntime}).Reconcile(ctx, req)
		return err
	},
	fakeTags: func(id string) map[string]string {
		got, err := testFake.GetSubnet(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return uddi.FromAPITags(got.Tags)
	},
	writeFakeTags: func(id string, t map[string]string) {
		_, err := testFake.UpdateSubnet(ctx, id, ipam.Subnet{Tags: uddi.ToAPITags(t)})
		Expect(err).NotTo(HaveOccurred())
	},
}

var _ = Describe("Tag removal through status.appliedTags", func() {
	for _, subject := range []tagRemovalSubject{viewTagSubject, subnetTagSubject} {
		s := subject

		Context("for a "+s.kind, func() {
			var (
				ns   string
				conn *uddiv1alpha1.Connection
			)

			BeforeEach(func() {
				testFake.Reset()
				drainEvents()
				ns = newNamespace()
				// The Connection carries defaultTags {env: test}.
				conn = makeReadyConnection(ns)
			})

			// reconcileFresh runs one pass and re-reads the CR on success.
			reconcileFresh := func(obj taggedObject) error {
				err := s.reconcile(requestFor(obj))
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())
				return err
			}

			// syncedWith creates the CR, reconciles it once and returns it re-read.
			syncedWith := func(name string, specTags map[string]string) taggedObject {
				obj := s.create(ns, name, specTags)
				Expect(reconcileFresh(obj)).To(Succeed())
				Expect(obj.StatusOf().ID).NotTo(BeEmpty())
				return obj
			}

			// changeSpecTags writes new spec.tags on a fresh copy of the CR.
			changeSpecTags := func(obj taggedObject, specTags map[string]string) {
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())
				s.setSpecTags(obj, specTags)
				Expect(k8sClient.Update(ctx, obj)).To(Succeed())
			}

			expectOwnershipTags := func(obj taggedObject, got map[string]string) {
				Expect(got).To(HaveKeyWithValue(tags.KeyManaged, tags.ValueManaged))
				Expect(got).To(HaveKeyWithValue(tags.KeyCluster, TestClusterID))
				Expect(got).To(HaveKeyWithValue(tags.KeyOwner, tags.Owner(ns, s.kind, obj.GetName())))
				Expect(got).To(HaveKeyWithValue(tags.KeyUID, string(obj.GetUID())))
			}

			It("records the applied tag keys, sorted, after the first sync", func() {
				obj := syncedWith("applied", map[string]string{"team": "a", "app": "web"})

				st := obj.StatusOf()
				Expect(st.AppliedTags).To(Equal([]string{
					"app", "env", "team",
					tags.KeyCluster, tags.KeyManaged, tags.KeyOwner, tags.KeyUID,
				}))
				Expect(st.ObservedGeneration).To(Equal(obj.GetGeneration()))
				Expect(meta.IsStatusConditionTrue(st.Conditions, uddiv1alpha1.ConditionSynced)).To(BeTrue())
			})

			It("removes a key dropped from spec.tags and keeps out-of-band and ownership tags", func() {
				obj := syncedWith("drop-key", map[string]string{"team": "a", "app": "web"})
				id := obj.StatusOf().ID

				// Someone adds a tag straight in Universal DDI.
				withForeign := s.fakeTags(id)
				withForeign["set-by-hand"] = "keep"
				s.writeFakeTags(id, withForeign)

				changeSpecTags(obj, map[string]string{"app": "web"})
				updatesBefore := testFake.CallCount(s.updateCall)
				Expect(reconcileFresh(obj)).To(Succeed())
				Expect(testFake.CallCount(s.updateCall)).To(Equal(updatesBefore + 1))

				got := s.fakeTags(id)
				Expect(got).NotTo(HaveKey("team"))
				Expect(got).To(HaveKeyWithValue("app", "web"))
				Expect(got).To(HaveKeyWithValue("env", "test"))
				Expect(got).To(HaveKeyWithValue("set-by-hand", "keep"))
				expectOwnershipTags(obj, got)

				Expect(obj.StatusOf().AppliedTags).NotTo(ContainElement("team"))
				Expect(obj.StatusOf().AppliedTags).NotTo(ContainElement("set-by-hand"))
				Expect(obj.StatusOf().AppliedTags).To(ContainElement("app"))

				// The next pass is a no-op: nothing left to remove, nothing to add.
				updatesBefore = testFake.CallCount(s.updateCall)
				Expect(reconcileFresh(obj)).To(Succeed())
				Expect(testFake.CallCount(s.updateCall)).To(Equal(updatesBefore))
			})

			It("removes a Connection defaultTags key once the Connection drops it", func() {
				obj := syncedWith("conn-default", map[string]string{"app": "web"})
				id := obj.StatusOf().ID
				Expect(s.fakeTags(id)).To(HaveKeyWithValue("env", "test"))

				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(conn), conn)).To(Succeed())
				conn.Spec.DefaultTags = map[string]string{"tier": "gold"}
				Expect(k8sClient.Update(ctx, conn)).To(Succeed())
				// The edited Connection is not Ready until it is verified at its new generation.
				_, err := (&ConnectionReconciler{Client: k8sClient, Connections: testResolver, Recorder: testRecorder}).
					Reconcile(ctx, requestFor(conn))
				Expect(err).NotTo(HaveOccurred())

				Expect(reconcileFresh(obj)).To(Succeed())

				got := s.fakeTags(id)
				Expect(got).NotTo(HaveKey("env"))
				Expect(got).To(HaveKeyWithValue("tier", "gold"))
				Expect(got).To(HaveKeyWithValue("app", "web"))
				expectOwnershipTags(obj, got)
				Expect(obj.StatusOf().AppliedTags).To(ContainElement("tier"))
				Expect(obj.StatusOf().AppliedTags).NotTo(ContainElement("env"))
			})

			It("removes nothing when status carries no appliedTags, as after an upgrade", func() {
				obj := syncedWith("upgraded", map[string]string{"team": "a", "app": "web"})
				id := obj.StatusOf().ID

				// An operator version that did not record appliedTags wrote this status.
				obj.StatusOf().AppliedTags = nil
				Expect(k8sClient.Status().Update(ctx, obj)).To(Succeed())

				changeSpecTags(obj, map[string]string{"app": "web"})
				Expect(reconcileFresh(obj)).To(Succeed())

				// "team" stays: nothing says the operator wrote it.
				got := s.fakeTags(id)
				Expect(got).To(HaveKeyWithValue("team", "a"))
				Expect(got).To(HaveKeyWithValue("app", "web"))
				expectOwnershipTags(obj, got)

				// From here on the keys are recorded, so the next removal goes through.
				Expect(obj.StatusOf().AppliedTags).To(ContainElement("app"))
				Expect(obj.StatusOf().AppliedTags).NotTo(ContainElement("team"))

				changeSpecTags(obj, nil)
				Expect(reconcileFresh(obj)).To(Succeed())
				got = s.fakeTags(id)
				Expect(got).NotTo(HaveKey("app"))
				Expect(got).To(HaveKeyWithValue("team", "a"))
				expectOwnershipTags(obj, got)
			})

			It("leaves appliedTags alone when the update fails, and removes the key on the retry", func() {
				obj := syncedWith("update-fails", map[string]string{"team": "a", "app": "web"})
				id := obj.StatusOf().ID
				appliedBefore := append([]string(nil), obj.StatusOf().AppliedTags...)

				changeSpecTags(obj, map[string]string{"app": "web"})
				testFake.PushStatus(s.updateCall, 500, "boom")
				Expect(reconcileFresh(obj)).NotTo(Succeed())

				Expect(obj.StatusOf().AppliedTags).To(Equal(appliedBefore))
				Expect(meta.IsStatusConditionFalse(obj.StatusOf().Conditions, uddiv1alpha1.ConditionSynced)).To(BeTrue())
				Expect(s.fakeTags(id)).To(HaveKeyWithValue("team", "a"))

				Expect(reconcileFresh(obj)).To(Succeed())
				Expect(s.fakeTags(id)).NotTo(HaveKey("team"))
				Expect(obj.StatusOf().AppliedTags).NotTo(ContainElement("team"))
			})
		})
	}
})
