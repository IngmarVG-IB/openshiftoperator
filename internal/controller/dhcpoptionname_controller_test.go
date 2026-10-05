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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// toUnstructured converts a typed Subnet into the raw form the API server receives, so a
// spec can send a field the typed client would leave out because of omitempty.
func toUnstructured(s *uddiv1alpha1.Subnet) (*unstructured.Unstructured, error) {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(s)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: raw}
	u.SetGroupVersionKind(uddiv1alpha1.GroupVersion.WithKind("Subnet"))
	return u, nil
}

// These specs cover DHCP option codes named by optionName: the name is looked up through
// the Connection's cached Universal DDI client in Adapter.Resolve, and everything after
// that sees option code ids exactly as for optionCode.
var _ = Describe("DHCP options named by optionName", func() {
	var (
		ns        string
		subnets   *SubnetReconciler
		fixeds    *FixedAddressReconciler
		spaces    *IPSpaceReconciler
		blocks    *AddressBlockReconciler
		rangesRec *RangeReconciler
	)

	BeforeEach(func() {
		testFake.Reset()
		drainEvents()
		ns = newNamespace()
		subnets = &SubnetReconciler{Client: k8sClient, Runtime: testRuntime}
		fixeds = &FixedAddressReconciler{Client: k8sClient, Runtime: testRuntime}
		spaces = &IPSpaceReconciler{Client: k8sClient, Runtime: testRuntime}
		blocks = &AddressBlockReconciler{Client: k8sClient, Runtime: testRuntime}
		rangesRec = &RangeReconciler{Client: k8sClient, Runtime: testRuntime}
		makeReadyConnection(ns)
		drainEvents()
	})

	// optionByName is a dhcpOptions entry naming its option code. The type is always set
	// because the CRD's default only applies to a field that is absent from the JSON.
	optionByName := func(name, value string) uddiv1alpha1.DHCPOption {
		return uddiv1alpha1.DHCPOption{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionName: name, OptionValue: value}
	}
	optionByID := func(id, value string) uddiv1alpha1.DHCPOption {
		return uddiv1alpha1.DHCPOption{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionCode: id, OptionValue: value}
	}

	readyOf := func(conds []metav1.Condition) *metav1.Condition {
		return meta.FindStatusCondition(conds, uddiv1alpha1.ConditionReady)
	}

	createSubnet := func(name, address string, opts ...uddiv1alpha1.DHCPOption) *uddiv1alpha1.Subnet {
		s := &uddiv1alpha1.Subnet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.SubnetSpec{
				SpaceRef:    uddiv1alpha1.ObjectRef{ID: testIPSpaceID},
				Address:     address,
				CIDR:        24,
				DHCPOptions: opts,
			},
		}
		Expect(k8sClient.Create(ctx, s)).To(Succeed())
		return s
	}
	reconcileSubnet := func(s *uddiv1alpha1.Subnet) {
		GinkgoHelper()
		_, err := subnets.Reconcile(ctx, requestFor(s))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(s), s)).To(Succeed())
	}

	createFixedAddress := func(name, address string, opts ...uddiv1alpha1.DHCPOption) *uddiv1alpha1.FixedAddress {
		f := &uddiv1alpha1.FixedAddress{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: uddiv1alpha1.FixedAddressSpec{
				SpaceRef:    uddiv1alpha1.ObjectRef{ID: testIPSpaceID},
				Address:     address,
				MatchType:   "mac",
				MatchValue:  "52:54:00:aa:bb:01",
				DHCPOptions: opts,
			},
		}
		Expect(k8sClient.Create(ctx, f)).To(Succeed())
		return f
	}
	reconcileFixedAddress := func(f *uddiv1alpha1.FixedAddress) {
		GinkgoHelper()
		_, err := fixeds.Reconcile(ctx, requestFor(f))
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(f), f)).To(Succeed())
	}

	// addWpad makes the option code "wpad" appear in the tenant and returns its id. The
	// resolver lists again on a miss only once its list is a minute old, which unit tests
	// cover with a controllable clock; here dropping the Connection's cached client gives
	// the next reconcile a cold cache, the same as a minute passing would.
	addWpad := func() string {
		GinkgoHelper()
		added, err := testFake.CreateOptionCode(ctx, ipam.OptionCode{
			Code: 252, Name: "wpad", OptionSpace: uddi.FakeOptionSpaceDHCPv4, Type: "text",
		})
		Expect(err).NotTo(HaveOccurred())
		testResolver.Invalidate(ns, uddiv1alpha1.DefaultConnectionName)
		return *added.Id
	}

	Context("on a Subnet", func() {
		It("sends the option code id for the name, and does not patch it again", func() {
			s := createSubnet("named", "10.40.1.0",
				optionByName("routers", "10.40.1.1"),
				optionByName("domain-name-servers", "10.40.1.2"),
				optionByID("dhcp/option_code/given", "kept"),
			)

			reconcileSubnet(s)

			Expect(readyOf(s.Status.Conditions).Status).To(Equal(metav1.ConditionTrue))
			got, err := testFake.GetSubnet(ctx, s.Status.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.DhcpOptions).To(HaveLen(3))
			Expect(*got.DhcpOptions[0].OptionCode).To(Equal(uddi.FakeOptionCodeID(3)))
			Expect(*got.DhcpOptions[0].OptionValue).To(Equal("10.40.1.1"))
			Expect(*got.DhcpOptions[1].OptionCode).To(Equal(uddi.FakeOptionCodeID(6)))
			Expect(*got.DhcpOptions[2].OptionCode).To(Equal("dhcp/option_code/given"))

			// The same cached client answers from its cache and Diff sees no drift.
			updatesBefore := testFake.CallCount("UpdateSubnet")
			reconcileSubnet(s)
			Expect(testFake.CallCount("UpdateSubnet")).To(Equal(updatesBefore))
			Expect(testFake.CallCount("ListOptionCodes")).To(Equal(1))
		})

		It("patches a named option once when only its value changes", func() {
			s := createSubnet("named-patch", "10.40.5.0", optionByName("routers", "10.40.5.1"))
			reconcileSubnet(s)
			drainEvents()

			s.Spec.DHCPOptions[0].OptionValue = "10.40.5.254"
			Expect(k8sClient.Update(ctx, s)).To(Succeed())
			updatesBefore := testFake.CallCount("UpdateSubnet")
			reconcileSubnet(s)

			Expect(testFake.CallCount("UpdateSubnet")).To(Equal(updatesBefore + 1))
			got, err := testFake.GetSubnet(ctx, s.Status.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(*got.DhcpOptions[0].OptionCode).To(Equal(uddi.FakeOptionCodeID(3)))
			Expect(*got.DhcpOptions[0].OptionValue).To(Equal("10.40.5.254"))

			reconcileSubnet(s)
			Expect(testFake.CallCount("UpdateSubnet")).To(Equal(updatesBefore + 1))
		})

		It("resolves a name that several option spaces define once optionSpace picks one", func() {
			s := createSubnet("named-space", "10.40.3.0", uddiv1alpha1.DHCPOption{
				Type: uddiv1alpha1.DHCPOptionTypeOption, OptionName: "ntp-servers",
				OptionSpace: uddi.FakeOptionSpaceVendor, OptionValue: "pool.example.com",
			})

			reconcileSubnet(s)

			Expect(readyOf(s.Status.Conditions).Status).To(Equal(metav1.ConditionTrue))
			got, err := testFake.GetSubnet(ctx, s.Status.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(*got.DhcpOptions[0].OptionCode).To(Equal(uddi.FakeOptionCodeID(200)))
		})

		It("reports DependencyNotReady, naming both spaces, for an ambiguous name", func() {
			s := createSubnet("ambiguous", "10.40.4.0", optionByName("ntp-servers", "pool.example.com"))

			reconcileSubnet(s)

			cond := readyOf(s.Status.Conditions)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonDependencyNotReady))
			Expect(cond.Message).To(ContainSubstring(`"ntp-servers"`))
			Expect(cond.Message).To(ContainSubstring(uddi.FakeOptionSpaceDHCPv4))
			Expect(cond.Message).To(ContainSubstring(uddi.FakeOptionSpaceVendor))
			Expect(testFake.CallCount("CreateSubnet")).To(BeZero())
		})

		It("reports DependencyNotReady for an unknown name and succeeds once the code exists", func() {
			s := createSubnet("unknown", "10.40.2.0", optionByName("wpad", "http://wpad.example.com/wpad.dat"))

			reconcileSubnet(s)

			cond := readyOf(s.Status.Conditions)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonDependencyNotReady))
			Expect(cond.Message).To(ContainSubstring(`"wpad"`))
			synced := meta.FindStatusCondition(s.Status.Conditions, uddiv1alpha1.ConditionSynced)
			Expect(synced.Status).To(Equal(metav1.ConditionFalse))
			Expect(s.Status.ID).To(BeEmpty())
			Expect(testFake.CallCount("CreateSubnet")).To(BeZero())

			wpad := addWpad()
			reconcileSubnet(s)

			Expect(readyOf(s.Status.Conditions).Status).To(Equal(metav1.ConditionTrue))
			got, err := testFake.GetSubnet(ctx, s.Status.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(*got.DhcpOptions[0].OptionCode).To(Equal(wpad))
			Expect(*got.DhcpOptions[0].OptionValue).To(Equal("http://wpad.example.com/wpad.dat"))
		})

		It("names the option space in the message when the name is missing from it", func() {
			s := createSubnet("wrong-space", "10.40.6.0", uddiv1alpha1.DHCPOption{
				Type: uddiv1alpha1.DHCPOptionTypeOption, OptionName: "routers",
				OptionSpace: uddi.FakeOptionSpaceVendor, OptionValue: "10.40.6.1",
			})

			reconcileSubnet(s)

			cond := readyOf(s.Status.Conditions)
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonDependencyNotReady))
			Expect(cond.Message).To(ContainSubstring(`"routers"`))
			Expect(cond.Message).To(ContainSubstring(uddi.FakeOptionSpaceVendor))
		})

		It("returns a plain error, not DependencyNotReady, when the option codes cannot be listed", func() {
			s := createSubnet("list-fails", "10.40.7.0", optionByName("routers", "10.40.7.1"))
			testFake.PushStatus("ListOptionCodes", 500, "boom")

			_, err := subnets.Reconcile(ctx, requestFor(s))

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("listing DHCP option codes"))
			Expect(testFake.CallCount("CreateSubnet")).To(BeZero())

			// The failed list was not cached, so the retry succeeds.
			reconcileSubnet(s)
			Expect(readyOf(s.Status.Conditions).Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("on a FixedAddress", func() {
		It("sends the option code id for the name, and does not patch it again", func() {
			f := createFixedAddress("named", "10.60.0.10",
				optionByName("routers", "10.60.0.1"),
				optionByName("domain-name", "example.com"),
			)

			reconcileFixedAddress(f)

			Expect(readyOf(f.Status.Conditions).Status).To(Equal(metav1.ConditionTrue))
			got, err := testFake.GetFixedAddress(ctx, f.Status.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.DhcpOptions).To(HaveLen(2))
			Expect(*got.DhcpOptions[0].OptionCode).To(Equal(uddi.FakeOptionCodeID(3)))
			Expect(*got.DhcpOptions[0].OptionValue).To(Equal("10.60.0.1"))
			Expect(*got.DhcpOptions[1].OptionCode).To(Equal(uddi.FakeOptionCodeID(15)))

			updatesBefore := testFake.CallCount("UpdateFixedAddress")
			reconcileFixedAddress(f)
			Expect(testFake.CallCount("UpdateFixedAddress")).To(Equal(updatesBefore))
		})

		It("reports DependencyNotReady for an unknown name and succeeds once the code exists", func() {
			f := createFixedAddress("unknown", "10.60.0.20", optionByName("wpad", "http://wpad.example.com/wpad.dat"))

			reconcileFixedAddress(f)

			cond := readyOf(f.Status.Conditions)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonDependencyNotReady))
			Expect(cond.Message).To(ContainSubstring(`"wpad"`))
			Expect(f.Status.ID).To(BeEmpty())
			Expect(testFake.CallCount("CreateFixedAddress")).To(BeZero())

			wpad := addWpad()
			reconcileFixedAddress(f)

			Expect(readyOf(f.Status.Conditions).Status).To(Equal(metav1.ConditionTrue))
			got, err := testFake.GetFixedAddress(ctx, f.Status.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(*got.DhcpOptions[0].OptionCode).To(Equal(wpad))
		})

		It("names the option space of an ambiguous name", func() {
			f := createFixedAddress("ambiguous", "10.60.0.30", optionByName("ntp-servers", "pool.example.com"))

			reconcileFixedAddress(f)

			cond := readyOf(f.Status.Conditions)
			Expect(cond.Reason).To(Equal(uddiv1alpha1.ReasonDependencyNotReady))
			Expect(cond.Message).To(ContainSubstring(uddi.FakeOptionSpaceDHCPv4))
			Expect(cond.Message).To(ContainSubstring(uddi.FakeOptionSpaceVendor))
		})
	})

	Context("on the other kinds that carry dhcpOptions", func() {
		It("resolves the name for an IPSpace, an AddressBlock and a Range", func() {
			space := &uddiv1alpha1.IPSpace{
				ObjectMeta: metav1.ObjectMeta{Name: "named-space", Namespace: ns},
				Spec:       uddiv1alpha1.IPSpaceSpec{Name: "named-space", DHCPOptions: []uddiv1alpha1.DHCPOption{optionByName("routers", "10.0.0.1")}},
			}
			Expect(k8sClient.Create(ctx, space)).To(Succeed())
			_, err := spaces.Reconcile(ctx, requestFor(space))
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(space), space)).To(Succeed())
			Expect(readyOf(space.Status.Conditions).Status).To(Equal(metav1.ConditionTrue))
			gotSpace, err := testFake.GetIPSpace(ctx, space.Status.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(*gotSpace.DhcpOptions[0].OptionCode).To(Equal(uddi.FakeOptionCodeID(3)))

			block := &uddiv1alpha1.AddressBlock{
				ObjectMeta: metav1.ObjectMeta{Name: "named-block", Namespace: ns},
				Spec: uddiv1alpha1.AddressBlockSpec{
					SpaceRef:    uddiv1alpha1.ObjectRef{ID: testIPSpaceID},
					Address:     "10.50.0.0",
					CIDR:        16,
					DHCPOptions: []uddiv1alpha1.DHCPOption{optionByName("domain-name-servers", "10.50.0.2")},
				},
			}
			Expect(k8sClient.Create(ctx, block)).To(Succeed())
			_, err = blocks.Reconcile(ctx, requestFor(block))
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(block), block)).To(Succeed())
			Expect(readyOf(block.Status.Conditions).Status).To(Equal(metav1.ConditionTrue))
			gotBlock, err := testFake.GetAddressBlock(ctx, block.Status.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(*gotBlock.DhcpOptions[0].OptionCode).To(Equal(uddi.FakeOptionCodeID(6)))

			rg := &uddiv1alpha1.Range{
				ObjectMeta: metav1.ObjectMeta{Name: "named-range", Namespace: ns},
				Spec: uddiv1alpha1.RangeSpec{
					SpaceRef:    uddiv1alpha1.ObjectRef{ID: testIPSpaceID},
					Start:       "10.50.1.10",
					End:         "10.50.1.20",
					DHCPOptions: []uddiv1alpha1.DHCPOption{optionByName("domain-search", "example.com")},
				},
			}
			Expect(k8sClient.Create(ctx, rg)).To(Succeed())
			_, err = rangesRec.Reconcile(ctx, requestFor(rg))
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rg), rg)).To(Succeed())
			Expect(readyOf(rg.Status.Conditions).Status).To(Equal(metav1.ConditionTrue))
			gotRange, err := testFake.GetRange(ctx, rg.Status.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(*gotRange.DhcpOptions[0].OptionCode).To(Equal(uddi.FakeOptionCodeID(119)))
		})
	})

	// The CRD's CEL rules, which run on every Kubernetes version in the support matrix:
	// the same specs run in make test and make test-floor.
	Context("validation in the CRD", func() {
		subnetWith := func(name string, opt uddiv1alpha1.DHCPOption) *uddiv1alpha1.Subnet {
			return &uddiv1alpha1.Subnet{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: uddiv1alpha1.SubnetSpec{
					SpaceRef:    uddiv1alpha1.ObjectRef{ID: testIPSpaceID},
					Address:     "10.70.0.0",
					CIDR:        24,
					DHCPOptions: []uddiv1alpha1.DHCPOption{opt},
				},
			}
		}

		DescribeTable("rejects",
			func(opt uddiv1alpha1.DHCPOption, message string) {
				err := k8sClient.Create(ctx, subnetWith("rejected", opt))
				Expect(err).To(HaveOccurred())
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
				Expect(err.Error()).To(ContainSubstring(message))
			},
			Entry("both optionCode and optionName",
				uddiv1alpha1.DHCPOption{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionCode: "dhcp/option_code/aaa", OptionName: "routers", OptionValue: "1"},
				"exactly one of optionCode and optionName"),
			Entry("an option with neither optionCode nor optionName",
				uddiv1alpha1.DHCPOption{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionValue: "1"},
				"exactly one of optionCode and optionName"),
			Entry("optionSpace without optionName",
				uddiv1alpha1.DHCPOption{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionCode: "dhcp/option_code/aaa", OptionSpace: uddi.FakeOptionSpaceDHCPv4, OptionValue: "1"},
				"optionSpace may only be set together with optionName"),
			Entry("optionSpace on its own",
				uddiv1alpha1.DHCPOption{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionSpace: uddi.FakeOptionSpaceDHCPv4, OptionValue: "1"},
				"optionSpace may only be set together with optionName"),
			Entry("a group that also names an option",
				uddiv1alpha1.DHCPOption{Type: uddiv1alpha1.DHCPOptionTypeGroup, Group: "dhcp/option_group/bbb", OptionName: "routers"},
				"type group takes neither optionCode nor optionName"),
			Entry("a group that also sets optionCode",
				uddiv1alpha1.DHCPOption{Type: uddiv1alpha1.DHCPOptionTypeGroup, Group: "dhcp/option_group/bbb", OptionCode: "dhcp/option_code/aaa"},
				"type group takes neither optionCode nor optionName"),
			Entry("a group without a group id",
				uddiv1alpha1.DHCPOption{Type: uddiv1alpha1.DHCPOptionTypeGroup},
				"type group requires group"),
		)

		DescribeTable("accepts",
			func(opt uddiv1alpha1.DHCPOption) {
				Expect(k8sClient.Create(ctx, subnetWith("accepted", opt))).To(Succeed())
			},
			Entry("optionCode alone, as before",
				uddiv1alpha1.DHCPOption{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionCode: "dhcp/option_code/aaa", OptionValue: "1"}),
			Entry("optionName alone",
				uddiv1alpha1.DHCPOption{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionName: "routers", OptionValue: "10.70.0.1"}),
			Entry("optionName with optionSpace",
				uddiv1alpha1.DHCPOption{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionName: "routers", OptionSpace: "dhcp/option_space/abc", OptionValue: "10.70.0.1"}),
			Entry("a group, as before",
				uddiv1alpha1.DHCPOption{Type: uddiv1alpha1.DHCPOptionTypeGroup, Group: "dhcp/option_group/bbb"}),
		)

		It("rejects an empty optionName or optionSpace sent as an explicit empty string", func() {
			for _, field := range []string{"optionName", "optionSpace"} {
				s := subnetWith("empty-"+field, uddiv1alpha1.DHCPOption{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionName: "routers"})
				u, err := toUnstructured(s)
				Expect(err).NotTo(HaveOccurred())
				opts := u.Object["spec"].(map[string]any)["dhcpOptions"].([]any)
				opts[0].(map[string]any)[field] = ""
				err = k8sClient.Create(ctx, u)
				Expect(err).To(HaveOccurred(), field)
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
			}
		})
	})
})
