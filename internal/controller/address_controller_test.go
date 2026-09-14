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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/infobloxopen/universal-ddi-go-client/ipam"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/tags"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

var _ = Describe("Address controller through the generic object-layer loop", func() {
	var (
		ns         string
		reconciler *AddressReconciler
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		reconciler = &AddressReconciler{Client: k8sClient, Runtime: testRuntime}
	})

	// makeAddress creates an Address CR with spec.spaceRef already set; mutate must set
	// either spec.address or spec.nextAvailableFrom, since the CRD's CEL rule requires
	// exactly one.
	makeAddress := func(name string, mutate func(*uddiv1alpha1.Address)) *uddiv1alpha1.Address {
		a := &uddiv1alpha1.Address{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.AddressSpec{
				SpaceRef: uddiv1alpha1.ObjectRef{ID: testIPSpaceID},
			},
		}
		if mutate != nil {
			mutate(a)
		}
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(a), a)).To(Succeed())
		return a
	}

	reconcileAddress := func(a *uddiv1alpha1.Address) error {
		_, err := reconciler.Reconcile(ctx, requestFor(a))
		if err == nil {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(a), a)).To(Succeed())
		}
		return err
	}

	readyOf := func(a *uddiv1alpha1.Address) *metav1.Condition {
		return meta.FindStatusCondition(a.Status.Conditions, uddiv1alpha1.ConditionReady)
	}

	fakeAddress := func(id string) *ipam.Address {
		got, err := testFake.GetAddress(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return got
	}

	Context("with a Ready Connection", func() {
		BeforeEach(func() {
			makeReadyConnection(ns)
			drainEvents()
		})

		It("reserves an explicit address, tags it and reports Ready", func() {
			a := makeAddress("explicit", func(a *uddiv1alpha1.Address) {
				a.Spec.Address = "10.0.40.5"
				a.Spec.Comment = "managed by uddi-operator"
			})

			Expect(reconcileAddress(a)).To(Succeed())

			Expect(a.Status.ID).To(HavePrefix(uddi.PrefixAddress + "/"))
			Expect(a.Status.Address).To(Equal("10.0.40.5"))
			Expect(readyOf(a).Status).To(Equal(metav1.ConditionTrue))

			got := fakeAddress(a.Status.ID)
			Expect(got.Address).To(Equal("10.0.40.5"))
			Expect(*got.Comment).To(Equal("managed by uddi-operator"))
			tagsOf := uddi.FromAPITags(got.Tags)
			Expect(tagsOf[tags.KeyManaged]).To(Equal(tags.ValueManaged))
			Expect(tagsOf[tags.KeyUID]).To(Equal(string(a.UID)))

			Expect(collectEvents()).To(ContainElement(ContainSubstring("Created")))
		})

		It("allocates the next available address from a Subnet, and a second Address from the same Subnet gets a different one", func() {
			subnet := newSubnetWithID(ns, "sub-1", "10.0.41.0")

			a := makeAddress("from-subnet-1", func(a *uddiv1alpha1.Address) {
				a.Spec.NextAvailableFrom = &uddiv1alpha1.NextAvailableFrom{
					SubnetRef: &uddiv1alpha1.ObjectRef{Name: subnet.Name},
				}
			})
			Expect(reconcileAddress(a)).To(Succeed())

			// The allocated address is the subnet's first usable host, which is only
			// possible if Create sent the "<parent id>/nextavailableip" form: a literal,
			// unresolved address would have been rejected or stored verbatim by the fake.
			Expect(a.Status.Address).To(Equal("10.0.41.1"))
			Expect(readyOf(a).Status).To(Equal(metav1.ConditionTrue))

			b := makeAddress("from-subnet-2", func(a *uddiv1alpha1.Address) {
				a.Spec.NextAvailableFrom = &uddiv1alpha1.NextAvailableFrom{
					SubnetRef: &uddiv1alpha1.ObjectRef{Name: subnet.Name},
				}
			})
			Expect(reconcileAddress(b)).To(Succeed())

			Expect(b.Status.Address).To(Equal("10.0.41.2"))
			Expect(b.Status.Address).NotTo(Equal(a.Status.Address))
		})

		It("allocates the next available address from a Range", func() {
			rg := newRangeWithID(ns, "rng-1", "10.0.42.10", "10.0.42.20", testIPSpaceID)

			a := makeAddress("from-range", func(a *uddiv1alpha1.Address) {
				a.Spec.NextAvailableFrom = &uddiv1alpha1.NextAvailableFrom{
					RangeRef: &uddiv1alpha1.ObjectRef{Name: rg.Name},
				}
			})
			Expect(reconcileAddress(a)).To(Succeed())

			Expect(a.Status.Address).To(Equal("10.0.42.10"))
			Expect(readyOf(a).Status).To(Equal(metav1.ConditionTrue))
		})

		It("round-trips names, defaulting an empty type to \"user\"", func() {
			a := makeAddress("names", func(a *uddiv1alpha1.Address) {
				a.Spec.Address = "10.0.43.5"
				a.Spec.Names = []uddiv1alpha1.AddressName{{Name: "svc-web.team-a"}}
			})

			Expect(reconcileAddress(a)).To(Succeed())

			got := fakeAddress(a.Status.ID)
			Expect(got.Names).To(Equal([]ipam.Name{{Name: "svc-web.team-a", Type: "user"}}))
		})

		It("recovers a previously allocated address after a lost status write instead of allocating again", func() {
			subnet := newSubnetWithID(ns, "sub-recover", "10.0.44.0")

			a := makeAddress("recover-me", func(a *uddiv1alpha1.Address) {
				a.Spec.NextAvailableFrom = &uddiv1alpha1.NextAvailableFrom{
					SubnetRef: &uddiv1alpha1.ObjectRef{Name: subnet.Name},
				}
			})

			// Simulate a create that succeeded in Universal DDI whose status write was
			// lost: the address already carries this CR's uid tag, but status.id is
			// still empty on the CR.
			preCreated, err := testFake.CreateAddress(ctx, ipam.Address{
				Address: "10.0.44.1",
				Space:   optStr(testIPSpaceID),
				Tags: uddi.ToAPITags(map[string]string{
					tags.KeyManaged: tags.ValueManaged,
					tags.KeyUID:     string(a.UID),
				}),
			})
			Expect(err).NotTo(HaveOccurred())
			createsBefore := testFake.CallCount("CreateAddress")

			Expect(reconcileAddress(a)).To(Succeed())

			Expect(a.Status.ID).To(Equal(*preCreated.Id))
			Expect(a.Status.Address).To(Equal("10.0.44.1"))
			Expect(testFake.CallCount("CreateAddress")).To(Equal(createsBefore))
			Expect(readyOf(a).Status).To(Equal(metav1.ConditionTrue))
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Adopted")))
		})

		It("reports DependencyNotReady when the parent Subnet has no id yet", func() {
			emptySubnet := &uddiv1alpha1.Subnet{
				ObjectMeta: metav1.ObjectMeta{Name: "empty-subnet", Namespace: ns},
				Spec: uddiv1alpha1.SubnetSpec{
					SpaceRef: uddiv1alpha1.ObjectRef{ID: testIPSpaceID},
					Address:  "10.0.45.0",
					CIDR:     24,
				},
			}
			Expect(k8sClient.Create(ctx, emptySubnet)).To(Succeed())

			a := makeAddress("needs-subnet", func(a *uddiv1alpha1.Address) {
				a.Spec.NextAvailableFrom = &uddiv1alpha1.NextAvailableFrom{
					SubnetRef: &uddiv1alpha1.ObjectRef{Name: "empty-subnet"},
				}
			})

			Expect(reconcileAddress(a)).To(Succeed())

			cond := readyOf(a)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonDependencyNotReady))
			Expect(a.Status.ID).To(BeEmpty())
		})

		It("releases the address in the fake on delete", func() {
			a := makeAddress("delete-me", func(a *uddiv1alpha1.Address) {
				a.Spec.Address = "10.0.46.5"
			})
			Expect(reconcileAddress(a)).To(Succeed())
			id := a.Status.ID
			drainEvents()

			Expect(k8sClient.Delete(ctx, a)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, requestFor(a))
			Expect(err).NotTo(HaveOccurred())

			_, err = testFake.GetAddress(ctx, id)
			Expect(uddi.IsNotFound(err)).To(BeTrue())
			Expect(collectEvents()).To(ContainElement(ContainSubstring("Deleted")))
		})

		// The immutability rules are spec-level so they work on the Kubernetes 1.29
		// floor, where oldSelf.orValue() does not exist. Both lanes have to hold: a
		// field that started empty for next-available allocation must stay empty, and a
		// field that was set must stay equal.
		It("rejects adding, removing or changing address and nextAvailableFrom via CEL", func() {
			subnet := newSubnetWithID(ns, "sub-cel", "10.0.47.0")
			other := newSubnetWithID(ns, "sub-cel-other", "10.0.48.0")

			// Lane 1: allocated from a parent, so spec.address must stay empty.
			allocated := makeAddress("cel-next-available", func(a *uddiv1alpha1.Address) {
				a.Spec.NextAvailableFrom = &uddiv1alpha1.NextAvailableFrom{
					SubnetRef: &uddiv1alpha1.ObjectRef{Name: subnet.Name},
				}
			})
			Expect(reconcileAddress(allocated)).To(Succeed())
			Expect(allocated.Status.Address).NotTo(BeEmpty())

			allocated.Spec.Address = allocated.Status.Address
			err := k8sClient.Update(ctx, allocated)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("address is immutable"))

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(allocated), allocated)).To(Succeed())
			allocated.Spec.NextAvailableFrom = &uddiv1alpha1.NextAvailableFrom{
				SubnetRef: &uddiv1alpha1.ObjectRef{Name: other.Name},
			}
			err = k8sClient.Update(ctx, allocated)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("nextAvailableFrom is immutable"))

			// Lane 2: reserved explicitly, so spec.address must stay exactly what it was
			// and nextAvailableFrom must not appear.
			explicit := makeAddress("cel-explicit", func(a *uddiv1alpha1.Address) {
				a.Spec.Address = "10.0.49.5"
			})
			Expect(reconcileAddress(explicit)).To(Succeed())

			explicit.Spec.Address = ""
			err = k8sClient.Update(ctx, explicit)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("address is immutable"))

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(explicit), explicit)).To(Succeed())
			explicit.Spec.NextAvailableFrom = &uddiv1alpha1.NextAvailableFrom{
				SubnetRef: &uddiv1alpha1.ObjectRef{Name: subnet.Name},
			}
			err = k8sClient.Update(ctx, explicit)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("nextAvailableFrom is immutable"))
		})
	})
})

// TestSDKCreateAddressSerializesPerParent is a plain (non-Ginkgo) unit test proving that
// internal/uddi/sdk.go's CreateAddress honours the per-parent KeyedMutex for
// next-available creates, exercised through the Address adapter's own allocation path
// (Create sets Address to "<parent id>/nextavailableip"). It runs entirely against a
// local httptest.Server, so it needs no real Universal DDI tenant and no other network
// access: two concurrent CreateAddress calls for the *same* parent must never be
// in-flight against the transport at the same time.
func TestSDKCreateAddressSerializesPerParent(t *testing.T) {
	var (
		mu        sync.Mutex
		active    int
		maxActive int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()

		// Hold the "critical section" long enough that a second concurrent call for the
		// same parent would overlap here if the keyed mutex were not serializing creates.
		time.Sleep(50 * time.Millisecond)

		mu.Lock()
		active--
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{
				"id":      "ipam/address/00000000-0000-4000-8000-000000000001",
				"address": "10.0.0.1",
				"space":   "ipam/ip_space/test",
			},
		})
	}))
	defer srv.Close()

	c, err := uddi.NewSDKClient(uddi.Config{APIKey: "test-key", CSPURL: srv.URL})
	if err != nil {
		t.Fatalf("NewSDKClient: %v", err)
	}

	const parent = "ipam/subnet/00000000-0000-4000-8000-000000000099"
	space := "ipam/ip_space/test"

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, _ = c.IPAM.CreateAddress(context.Background(), ipam.Address{
				Address: parent + uddi.NextAvailableIPSuffix,
				Space:   &space,
			})
		}()
	}
	close(start)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if maxActive > 1 {
		t.Fatalf("CreateAddress for the same parent overlapped at the transport: the keyed mutex did not serialize the calls (max concurrent = %d)", maxActive)
	}
}
