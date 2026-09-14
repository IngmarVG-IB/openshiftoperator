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
	"reflect"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/infobloxopen/universal-ddi-go-client/dnsdata"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
	"github.com/IngmarVG-IB/openshiftoperator/internal/tags"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// TestRDataOf is a unit test for the rdata builder, covering every record type. It needs
// no envtest fixture, only the pure function.
func TestRDataOf(t *testing.T) {
	cases := []struct {
		name string
		spec uddiv1alpha1.RecordSpec
		want map[string]interface{}
	}{
		{
			name: "A",
			spec: uddiv1alpha1.RecordSpec{Type: "A", RData: uddiv1alpha1.RecordRData{A: &uddiv1alpha1.ARData{Address: "192.0.2.1"}}},
			want: map[string]interface{}{"address": "192.0.2.1"},
		},
		{
			name: "AAAA",
			spec: uddiv1alpha1.RecordSpec{Type: "AAAA", RData: uddiv1alpha1.RecordRData{AAAA: &uddiv1alpha1.AAAARData{Address: "2001:db8::1"}}},
			want: map[string]interface{}{"address": "2001:db8::1"},
		},
		{
			name: "CNAME",
			spec: uddiv1alpha1.RecordSpec{Type: "CNAME", RData: uddiv1alpha1.RecordRData{CNAME: &uddiv1alpha1.CNAMERData{CNAME: "target.example.com."}}},
			want: map[string]interface{}{"cname": "target.example.com."},
		},
		{
			name: "TXT",
			spec: uddiv1alpha1.RecordSpec{Type: "TXT", RData: uddiv1alpha1.RecordRData{TXT: &uddiv1alpha1.TXTRData{Text: "v=spf1 -all"}}},
			want: map[string]interface{}{"text": "v=spf1 -all"},
		},
		{
			name: "PTR",
			spec: uddiv1alpha1.RecordSpec{Type: "PTR", RData: uddiv1alpha1.RecordRData{PTR: &uddiv1alpha1.PTRRData{DName: "host.example.com."}}},
			want: map[string]interface{}{"dname": "host.example.com."},
		},
		{
			name: "SRV",
			spec: uddiv1alpha1.RecordSpec{Type: "SRV", RData: uddiv1alpha1.RecordRData{SRV: &uddiv1alpha1.SRVRData{Priority: 10, Weight: 5, Port: 443, Target: "svc.example.com."}}},
			want: map[string]interface{}{"priority": int64(10), "weight": int64(5), "port": int64(443), "target": "svc.example.com."},
		},
		{
			name: "MX",
			spec: uddiv1alpha1.RecordSpec{Type: "MX", RData: uddiv1alpha1.RecordRData{MX: &uddiv1alpha1.MXRData{Preference: 10, Exchange: "mail.example.com."}}},
			want: map[string]interface{}{"preference": int64(10), "exchange": "mail.example.com."},
		},
		{
			name: "NS",
			spec: uddiv1alpha1.RecordSpec{Type: "NS", RData: uddiv1alpha1.RecordRData{NS: &uddiv1alpha1.NSRData{DName: "ns1.example.com."}}},
			want: map[string]interface{}{"dname": "ns1.example.com."},
		},
		{
			name: "CAA",
			spec: uddiv1alpha1.RecordSpec{Type: "CAA", RData: uddiv1alpha1.RecordRData{CAA: &uddiv1alpha1.CAARData{Flags: 0, Tag: "issue", Value: "letsencrypt.org"}}},
			want: map[string]interface{}{"flags": int64(0), "tag": "issue", "value": "letsencrypt.org"},
		},
		{
			name: "unset block",
			spec: uddiv1alpha1.RecordSpec{Type: "A"},
			want: nil,
		},
	}

	for _, c := range cases {
		got := rdataOf(&c.spec)
		// reflect.DeepEqual, not the type-tolerant rdataEqual, so a wrong numeric type
		// (say int32 instead of the int64 the builder promises) fails the test.
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("rdataOf(%s) = %#v, want %#v", c.name, got, c.want)
		}
	}
}

var _ = Describe("Record controller through the generic object-layer loop", func() {
	var (
		ns         string
		reconciler *RecordReconciler
		viewRec    *ViewReconciler
		zoneRec    *AuthZoneReconciler
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		reconciler = &RecordReconciler{Client: k8sClient, Runtime: testRuntime}
		viewRec = &ViewReconciler{Client: k8sClient, Runtime: testRuntime}
		zoneRec = &AuthZoneReconciler{Client: k8sClient, Runtime: testRuntime}
	})

	// makeReadyZone creates a View and an AuthZone, reconciling both, so the zone has a
	// status.id, the dependency every Record in these specs needs.
	makeReadyZone := func(name string) *uddiv1alpha1.AuthZone {
		v := &uddiv1alpha1.View{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-view", Namespace: ns},
			Spec:       uddiv1alpha1.ViewSpec{Name: name + "-view"},
		}
		Expect(k8sClient.Create(ctx, v)).To(Succeed())
		_, err := viewRec.Reconcile(ctx, requestFor(v))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(v), v)).To(Succeed())

		z := &uddiv1alpha1.AuthZone{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.AuthZoneSpec{
				FQDN:        name + ".example.com.",
				ViewRef:     uddiv1alpha1.ObjectRef{Name: v.Name},
				PrimaryType: uddiv1alpha1.AuthZonePrimaryCloud,
			},
		}
		Expect(k8sClient.Create(ctx, z)).To(Succeed())
		_, err = zoneRec.Reconcile(ctx, requestFor(z))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(z), z)).To(Succeed())
		return z
	}

	makeRecord := func(name string, mutate func(*uddiv1alpha1.Record)) *uddiv1alpha1.Record {
		r := &uddiv1alpha1.Record{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		}
		if mutate != nil {
			mutate(r)
		}
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(r), r)).To(Succeed())
		return r
	}

	reconcileRecord := func(r *uddiv1alpha1.Record) error {
		_, err := reconciler.Reconcile(ctx, requestFor(r))
		if err == nil {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(r), r)).To(Succeed())
		}
		return err
	}

	readyOf := func(r *uddiv1alpha1.Record) *metav1.Condition {
		return meta.FindStatusCondition(r.Status.Conditions, uddiv1alpha1.ConditionReady)
	}

	fakeRecord := func(id string) *dnsdata.Record {
		got, err := testFake.GetRecord(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return got
	}

	Context("with a Ready Connection and zone", func() {
		var zone *uddiv1alpha1.AuthZone

		BeforeEach(func() {
			makeReadyConnection(ns)
			zone = makeReadyZone("apps-zone")
			drainEvents()
		})

		It("creates an A record and reports Ready", func() {
			ttl := int32(300)
			r := makeRecord("www-a", func(r *uddiv1alpha1.Record) {
				r.Spec.ZoneRef = uddiv1alpha1.ObjectRef{Name: zone.Name}
				r.Spec.Name = "www"
				r.Spec.Type = "A"
				r.Spec.RData = uddiv1alpha1.RecordRData{A: &uddiv1alpha1.ARData{Address: "192.0.2.10"}}
				r.Spec.TTL = &ttl
			})

			Expect(reconcileRecord(r)).To(Succeed())

			Expect(r.Status.ID).To(HavePrefix(uddi.PrefixRecord + "/"))
			Expect(r.Status.ObservedGeneration).To(Equal(r.Generation))
			Expect(r.Status.AbsoluteName).To(Equal("www.apps-zone.example.com."))
			Expect(r.Status.Source).To(ConsistOf("STATIC"))
			Expect(r.Status.Zone).To(Equal(zone.Status.ID))
			Expect(readyOf(r).Status).To(Equal(metav1.ConditionTrue))
			Expect(controllerutil.ContainsFinalizer(r, uddiv1alpha1.Finalizer)).To(BeTrue())

			got := fakeRecord(r.Status.ID)
			Expect(got.Rdata).To(Equal(map[string]interface{}{"address": "192.0.2.10"}))
			Expect(*got.Ttl).To(Equal(int64(300)))

			t := uddi.FromAPITags(got.Tags)
			Expect(t[tags.KeyUID]).To(Equal(string(r.UID)))

			Expect(collectEvents()).To(ContainElement(ContainSubstring("Created")))
		})

		It("creates a CNAME record", func() {
			r := makeRecord("router-cname", func(r *uddiv1alpha1.Record) {
				r.Spec.ZoneRef = uddiv1alpha1.ObjectRef{Name: zone.Name}
				r.Spec.Name = "router"
				r.Spec.Type = "CNAME"
				r.Spec.RData = uddiv1alpha1.RecordRData{CNAME: &uddiv1alpha1.CNAMERData{CNAME: "router-default.apps.example.com."}}
			})

			Expect(reconcileRecord(r)).To(Succeed())
			Expect(readyOf(r).Status).To(Equal(metav1.ConditionTrue))

			got := fakeRecord(r.Status.ID)
			Expect(got.Rdata).To(Equal(map[string]interface{}{"cname": "router-default.apps.example.com."}))
		})

		It("creates a TXT record", func() {
			r := makeRecord("spf-txt", func(r *uddiv1alpha1.Record) {
				r.Spec.ZoneRef = uddiv1alpha1.ObjectRef{Name: zone.Name}
				r.Spec.Name = "spf"
				r.Spec.Type = "TXT"
				r.Spec.RData = uddiv1alpha1.RecordRData{TXT: &uddiv1alpha1.TXTRData{Text: "v=spf1 -all"}}
			})

			Expect(reconcileRecord(r)).To(Succeed())

			got := fakeRecord(r.Status.ID)
			Expect(got.Rdata).To(Equal(map[string]interface{}{"text": "v=spf1 -all"}))
		})

		It("creates an SRV record", func() {
			r := makeRecord("sip-srv", func(r *uddiv1alpha1.Record) {
				r.Spec.ZoneRef = uddiv1alpha1.ObjectRef{Name: zone.Name}
				r.Spec.Name = "_sip._tcp"
				r.Spec.Type = "SRV"
				r.Spec.RData = uddiv1alpha1.RecordRData{SRV: &uddiv1alpha1.SRVRData{Priority: 10, Weight: 5, Port: 443, Target: "www.example.com."}}
			})

			Expect(reconcileRecord(r)).To(Succeed())

			got := fakeRecord(r.Status.ID)
			Expect(got.Rdata).To(Equal(map[string]interface{}{
				"priority": int64(10), "weight": int64(5), "port": int64(443), "target": "www.example.com.",
			}))
		})

		It("creates an apex record for name @", func() {
			r := makeRecord("apex-a", func(r *uddiv1alpha1.Record) {
				r.Spec.ZoneRef = uddiv1alpha1.ObjectRef{Name: zone.Name}
				r.Spec.Name = "@"
				r.Spec.Type = "A"
				r.Spec.RData = uddiv1alpha1.RecordRData{A: &uddiv1alpha1.ARData{Address: "192.0.2.1"}}
			})

			Expect(reconcileRecord(r)).To(Succeed())

			got := fakeRecord(r.Status.ID)
			Expect(*got.NameInZone).To(Equal(""))
			Expect(r.Status.AbsoluteName).To(Equal(zone.Spec.FQDN))
		})

		It("sends the create_ptr option when createPtr is set on an A record", func() {
			r := makeRecord("ptr-a", func(r *uddiv1alpha1.Record) {
				r.Spec.ZoneRef = uddiv1alpha1.ObjectRef{Name: zone.Name}
				r.Spec.Name = "ptr-host"
				r.Spec.Type = "A"
				r.Spec.RData = uddiv1alpha1.RecordRData{A: &uddiv1alpha1.ARData{Address: "192.0.2.20"}}
				r.Spec.CreatePtr = ptrTo(true)
			})

			Expect(reconcileRecord(r)).To(Succeed())

			got := fakeRecord(r.Status.ID)
			Expect(got.Options).To(Equal(map[string]interface{}{"create_ptr": true}))
		})

		It("adopts an existing STATIC record by natural key", func() {
			existing, err := testFake.CreateRecord(ctx, dnsdata.Record{
				Type:       ptrTo("A"),
				Zone:       ptrTo(zone.Status.ID),
				NameInZone: ptrTo("adopt-me"),
				Rdata:      map[string]interface{}{"address": "192.0.2.30"},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(existing.Source).To(ConsistOf("STATIC"))
			createsBefore := testFake.CallCount("CreateRecord")

			r := makeRecord("adopt-a", func(r *uddiv1alpha1.Record) {
				r.Spec.ZoneRef = uddiv1alpha1.ObjectRef{Name: zone.Name}
				r.Spec.Name = "adopt-me"
				r.Spec.Type = "A"
				r.Spec.RData = uddiv1alpha1.RecordRData{A: &uddiv1alpha1.ARData{Address: "192.0.2.30"}}
				r.Spec.Lifecycle.AdoptExisting = true
			})

			Expect(reconcileRecord(r)).To(Succeed())

			Expect(r.Status.ID).To(Equal(*existing.Id))
			Expect(testFake.CallCount("CreateRecord")).To(Equal(createsBefore))
			Expect(readyOf(r).Status).To(Equal(metav1.ConditionTrue))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Adopted")))
		})

		It("reports AlreadyExists for a non-STATIC record and never patches or deletes it", func() {
			existing, err := testFake.CreateRecord(ctx, dnsdata.Record{
				Type:       ptrTo("A"),
				Zone:       ptrTo(zone.Status.ID),
				NameInZone: ptrTo("system-a"),
				Rdata:      map[string]interface{}{"address": "192.0.2.40"},
				Source:     []string{"SYSTEM"},
			})
			Expect(err).NotTo(HaveOccurred())

			r := makeRecord("system-a-cr", func(r *uddiv1alpha1.Record) {
				r.Spec.ZoneRef = uddiv1alpha1.ObjectRef{Name: zone.Name}
				r.Spec.Name = "system-a"
				r.Spec.Type = "A"
				r.Spec.RData = uddiv1alpha1.RecordRData{A: &uddiv1alpha1.ARData{Address: "192.0.2.40"}}
				// Even with adoptExisting set, a non-STATIC record must never be adopted.
				r.Spec.Lifecycle.AdoptExisting = true
			})

			Expect(reconcileRecord(r)).To(Succeed())

			Expect(r.Status.ID).To(BeEmpty())
			cond := readyOf(r)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonAlreadyExists))
			Expect(cond.Message).To(ContainSubstring("SYSTEM"))
			Expect(collectEvents()).To(ContainElement(ContainSubstring(uddisync.EventConflict)))

			Expect(testFake.CallCount("UpdateRecord")).To(Equal(0))
			Expect(testFake.CallCount("DeleteRecord")).To(Equal(0))
			still := fakeRecord(*existing.Id)
			Expect(still.Source).To(ConsistOf("SYSTEM"))

			// Deleting the CR must not delete the Universal DDI record either.
			Expect(k8sClient.Delete(ctx, r)).To(Succeed())
			_, err = reconciler.Reconcile(ctx, requestFor(r))
			Expect(err).NotTo(HaveOccurred())
			Expect(testFake.CallCount("DeleteRecord")).To(Equal(0))
			_, err = testFake.GetRecord(ctx, *existing.Id)
			Expect(err).NotTo(HaveOccurred())
		})

		It("lets two A records with the same name coexist", func() {
			r1 := makeRecord("multi-a-1", func(r *uddiv1alpha1.Record) {
				r.Spec.ZoneRef = uddiv1alpha1.ObjectRef{Name: zone.Name}
				r.Spec.Name = "multi"
				r.Spec.Type = "A"
				r.Spec.RData = uddiv1alpha1.RecordRData{A: &uddiv1alpha1.ARData{Address: "192.0.2.50"}}
			})
			r2 := makeRecord("multi-a-2", func(r *uddiv1alpha1.Record) {
				r.Spec.ZoneRef = uddiv1alpha1.ObjectRef{Name: zone.Name}
				r.Spec.Name = "multi"
				r.Spec.Type = "A"
				r.Spec.RData = uddiv1alpha1.RecordRData{A: &uddiv1alpha1.ARData{Address: "192.0.2.51"}}
			})

			Expect(reconcileRecord(r1)).To(Succeed())
			Expect(reconcileRecord(r2)).To(Succeed())

			Expect(r1.Status.ID).NotTo(Equal(r2.Status.ID))
			Expect(readyOf(r1).Status).To(Equal(metav1.ConditionTrue))
			Expect(readyOf(r2).Status).To(Equal(metav1.ConditionTrue))
			Expect(fakeRecord(r1.Status.ID).Rdata["address"]).To(Equal("192.0.2.50"))
			Expect(fakeRecord(r2.Status.ID).Rdata["address"]).To(Equal("192.0.2.51"))
		})

		It("patches ttl and rdata", func() {
			ttl := int32(60)
			r := makeRecord("patch-a", func(r *uddiv1alpha1.Record) {
				r.Spec.ZoneRef = uddiv1alpha1.ObjectRef{Name: zone.Name}
				r.Spec.Name = "patch-me"
				r.Spec.Type = "A"
				r.Spec.RData = uddiv1alpha1.RecordRData{A: &uddiv1alpha1.ARData{Address: "192.0.2.60"}}
				r.Spec.TTL = &ttl
			})
			Expect(reconcileRecord(r)).To(Succeed())
			id := r.Status.ID
			drainEvents()

			r.Spec.RData.A.Address = "192.0.2.61"
			newTTL := int32(120)
			r.Spec.TTL = &newTTL
			Expect(k8sClient.Update(ctx, r)).To(Succeed())

			updatesBefore := testFake.CallCount("UpdateRecord")
			Expect(reconcileRecord(r)).To(Succeed())

			Expect(testFake.CallCount("UpdateRecord")).To(Equal(updatesBefore + 1))
			Expect(r.Status.ID).To(Equal(id))
			got := fakeRecord(id)
			Expect(got.Rdata["address"]).To(Equal("192.0.2.61"))
			Expect(*got.Ttl).To(Equal(int64(120)))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Updated")))

			// A second pass with no spec change must not patch again.
			updatesBefore = testFake.CallCount("UpdateRecord")
			Expect(reconcileRecord(r)).To(Succeed())
			Expect(testFake.CallCount("UpdateRecord")).To(Equal(updatesBefore))
		})

		It("deletes the record when the uid tag matches", func() {
			r := makeRecord("delete-a", func(r *uddiv1alpha1.Record) {
				r.Spec.ZoneRef = uddiv1alpha1.ObjectRef{Name: zone.Name}
				r.Spec.Name = "delete-me"
				r.Spec.Type = "A"
				r.Spec.RData = uddiv1alpha1.RecordRData{A: &uddiv1alpha1.ARData{Address: "192.0.2.70"}}
			})
			Expect(reconcileRecord(r)).To(Succeed())
			id := r.Status.ID
			drainEvents()

			Expect(k8sClient.Delete(ctx, r)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, requestFor(r))
			Expect(err).NotTo(HaveOccurred())

			_, err = testFake.GetRecord(ctx, id)
			Expect(uddi.IsNotFound(err)).To(BeTrue())
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Deleted")))
		})
	})

	Context("dependency handling", func() {
		It("reports DependencyNotReady when the zone has no status.id yet", func() {
			makeReadyConnection(ns)
			z := &uddiv1alpha1.AuthZone{
				ObjectMeta: metav1.ObjectMeta{Name: "pending-zone", Namespace: ns},
				Spec: uddiv1alpha1.AuthZoneSpec{
					FQDN:        "pending.example.com.",
					ViewRef:     uddiv1alpha1.ObjectRef{Name: "some-view"},
					PrimaryType: uddiv1alpha1.AuthZonePrimaryCloud,
				},
			}
			Expect(k8sClient.Create(ctx, z)).To(Succeed())

			r := makeRecord("waits-for-zone", func(r *uddiv1alpha1.Record) {
				r.Spec.ZoneRef = uddiv1alpha1.ObjectRef{Name: z.Name}
				r.Spec.Name = "www"
				r.Spec.Type = "A"
				r.Spec.RData = uddiv1alpha1.RecordRData{A: &uddiv1alpha1.ARData{Address: "192.0.2.80"}}
			})

			Expect(reconcileRecord(r)).To(Succeed())
			cond := readyOf(r)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonDependencyNotReady))
			Expect(r.Status.ID).To(BeEmpty())
		})
	})
})
