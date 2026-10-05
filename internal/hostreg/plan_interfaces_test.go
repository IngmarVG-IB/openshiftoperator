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

package hostreg

import (
	"reflect"
	"strings"
	"testing"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

const regWorker1Storage = "nodes-worker-1-storage"

// ifPolicy is hrPolicy with additionalInterfaces entries.
func ifPolicy(mode uddiv1alpha1.RegisterMode, entries ...uddiv1alpha1.AdditionalInterface) *uddiv1alpha1.HostRegistrationPolicy {
	p := hrPolicy(mode)
	p.Spec.AdditionalInterfaces = entries
	return p
}

// primaryView is worker-1 with a primary address and MAC.
func primaryView(ifaces ...InterfaceView) NodeView {
	return NodeView{
		Ref:        NodeRef{Name: nodeWorker1},
		Address:    nodeAddr,
		MAC:        nodeMAC,
		Hostname:   nodeFQDN,
		Interfaces: ifaces,
	}
}

// storageView is the resolved storage interface of worker-1.
func storageView(mac string) InterfaceView {
	return InterfaceView{Name: storageName, Address: storageIP, MAC: mac, Hostname: storageFQDN}
}

func ensuredByName(t *testing.T, plan Plan, name string) DesiredRegistration {
	t.Helper()
	for _, d := range plan.Ensure {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no ensured registration named %q in %+v", name, plan.Ensure)
	return DesiredRegistration{}
}

func skipFor(plan Plan, iface string, reason SkipReason) (Skip, bool) {
	for _, s := range plan.Skips {
		if s.Interface == iface && s.Reason == reason {
			return s, true
		}
	}
	return Skip{}, false
}

func TestCompute_InterfaceRegistration(t *testing.T) {
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry(storageCIDR))
	policy.Spec.Register.DHCPOptions = []uddiv1alpha1.DHCPOption{
		{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionCode: "dhcp/option_code/26", OptionValue: "9000"},
	}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{primaryView(storageView(storageMAC))}, Space: readySpace})

	if len(plan.Skips) != 0 || len(plan.Release) != 0 {
		t.Fatalf("expected no skips or releases, got %+v / %+v", plan.Skips, plan.Release)
	}
	if len(plan.Ensure) != 2 {
		t.Fatalf("expected the primary and one interface registration, got %+v", plan.Ensure)
	}
	if plan.Ensure[0].Name != regWorker1 || plan.Ensure[1].Name != regWorker1Storage {
		t.Fatalf("Ensure order = %q, %q, want the primary first then the interface", plan.Ensure[0].Name, plan.Ensure[1].Name)
	}

	primary := plan.Ensure[0]
	if primary.Interface != "" {
		t.Errorf("primary Interface = %q, want empty", primary.Interface)
	}
	if _, has := primary.Labels[LabelInterface]; has {
		t.Errorf("primary carries the interface label: %+v", primary.Labels)
	}

	d := plan.Ensure[1]
	if d.Kind != KindFixedAddress || d.FixedAddressSpec == nil || d.AddressSpec != nil {
		t.Fatalf("interface registration = %+v, want a FixedAddress (Auto with a MAC)", d)
	}
	if d.Interface != storageName || d.Node.Name != nodeWorker1 {
		t.Errorf("Interface/Node = %q/%q", d.Interface, d.Node.Name)
	}
	wantLabels := map[string]string{LabelPolicy: "nodes", LabelNode: nodeWorker1, LabelInterface: storageName}
	if !reflect.DeepEqual(d.Labels, wantLabels) {
		t.Errorf("Labels = %+v, want %+v", d.Labels, wantLabels)
	}
	fa := d.FixedAddressSpec
	if fa.Address != storageIP || fa.MatchType != "mac" || fa.MatchValue != storageMAC {
		t.Errorf("Address/MatchType/MatchValue = %s/%s/%s", fa.Address, fa.MatchType, fa.MatchValue)
	}
	if fa.Name != storageFQDN || fa.Hostname != storageFQDN {
		t.Errorf("Name/Hostname = %s/%s, want both %s", fa.Name, fa.Hostname, storageFQDN)
	}
	if fa.SpaceRef.Name != "corp" {
		t.Errorf("SpaceRef = %+v, want the policy's corp", fa.SpaceRef)
	}
	wantComment := "Registered by HostRegistrationPolicy uddi-system/nodes for Node worker-1 interface storage"
	if fa.Comment != wantComment {
		t.Errorf("Comment = %q, want %q", fa.Comment, wantComment)
	}
	if fa.Tags["role"] != "node" || fa.Tags[TagSource] != "Node/worker-1" {
		t.Errorf("Tags = %+v", fa.Tags)
	}
	if fa.ConnectionRef != policy.Spec.ConnectionRef || fa.Lifecycle != policy.Spec.Lifecycle {
		t.Errorf("ConnectionRef/Lifecycle not copied from the policy: %+v / %+v", fa.ConnectionRef, fa.Lifecycle)
	}
	if len(fa.DHCPOptions) != 1 || fa.DHCPOptions[0].OptionValue != "9000" {
		t.Errorf("DHCPOptions = %+v, want the policy's copied over", fa.DHCPOptions)
	}
}

func TestCompute_InterfaceKindFollowsModeAndInterfaceMAC(t *testing.T) {
	cases := []struct {
		name     string
		mode     uddiv1alpha1.RegisterMode
		mac      string
		wantKind RegistrationKind
		wantSkip SkipReason
	}{
		{"auto with the interface MAC", uddiv1alpha1.RegisterModeAuto, storageMAC, KindFixedAddress, ""},
		{"auto without a MAC", uddiv1alpha1.RegisterModeAuto, "", KindAddress, ""},
		{"address mode with a MAC sets hwaddr", uddiv1alpha1.RegisterModeAddress, storageMAC, KindAddress, ""},
		{"fixedaddress mode with a MAC", uddiv1alpha1.RegisterModeFixedAddress, storageMAC, KindFixedAddress, ""},
		{"fixedaddress mode without a MAC", uddiv1alpha1.RegisterModeFixedAddress, "", "", SkipNoMAC},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			policy := ifPolicy(c.mode, storageEntry(storageCIDR))
			// The primary MAC is deliberately different from the interface's: the interface
			// registration must use its own.
			v := primaryView(storageView(c.mac))

			plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace})

			if c.wantSkip != "" {
				if _, ok := skipFor(plan, storageName, c.wantSkip); !ok {
					t.Fatalf("expected a %s skip for the interface, got %+v", c.wantSkip, plan.Skips)
				}
				if len(plan.Ensure) != 1 || plan.Ensure[0].Name != regWorker1 {
					t.Fatalf("expected only the primary to be ensured, got %+v", plan.Ensure)
				}
				return
			}
			d := ensuredByName(t, plan, regWorker1Storage)
			if d.Kind != c.wantKind {
				t.Fatalf("Kind = %s, want %s", d.Kind, c.wantKind)
			}
			if d.Kind == KindAddress {
				if d.AddressSpec == nil || d.AddressSpec.HWAddr != c.mac || d.AddressSpec.Address != storageIP {
					t.Errorf("AddressSpec = %+v, want hwaddr %q and address %s", d.AddressSpec, c.mac, storageIP)
				}
				if len(d.AddressSpec.Names) != 1 || d.AddressSpec.Names[0].Name != storageFQDN || d.AddressSpec.Names[0].Type != "user" {
					t.Errorf("Names = %+v", d.AddressSpec.Names)
				}
			}
		})
	}
}

func TestCompute_FixedAddressNoMACMessageNamesInterface(t *testing.T) {
	policy := ifPolicy(uddiv1alpha1.RegisterModeFixedAddress, storageEntry(storageCIDR))

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{primaryView(storageView(""))}, Space: readySpace})

	s, ok := skipFor(plan, storageName, SkipNoMAC)
	if !ok {
		t.Fatalf("no NoMAC skip in %+v", plan.Skips)
	}
	want := "interface storage: mode FixedAddress needs a MAC; no NodeNetworkState interface or Metal3 NIC carries 10.20.0.7"
	if s.Message != want || s.Node.Name != nodeWorker1 {
		t.Errorf("skip = %+v, want message %q", s, want)
	}
}

func TestCompute_InterfaceNoAddress(t *testing.T) {
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry(storageCIDR))
	// No InterfaceView resolved an address.
	v := primaryView(InterfaceView{Name: storageName, Hostname: storageFQDN})
	existing := []RegistrationState{{Name: regWorker1Storage, Kind: KindFixedAddress, Node: nodeWorker1, Interface: storageName, Ready: true}}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace, Existing: existing})

	s, ok := skipFor(plan, storageName, SkipNoAddress)
	if !ok {
		t.Fatalf("no NoAddress skip for the interface in %+v", plan.Skips)
	}
	if s.Message != "interface storage: no Node or NodeNetworkState address inside 10.20.0.0/16" {
		t.Errorf("Message = %q, want it to name the interface and the CIDR", s.Message)
	}
	if len(plan.Ensure) != 1 || plan.Ensure[0].Name != regWorker1 {
		t.Errorf("the primary must still be registered, got %+v", plan.Ensure)
	}
	if len(plan.Release) != 0 {
		t.Errorf("a NoAddress interface keeps its existing registration (frozen), got releases %+v", plan.Release)
	}
	if plan.Registered != 0 {
		t.Errorf("Registered = %d, want 0: the frozen interface object is not counted while it is skipped", plan.Registered)
	}
}

func TestCompute_InterfaceWithoutViewIsNoAddress(t *testing.T) {
	// A controller that has not resolved interfaces yet leaves NodeView.Interfaces empty.
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry(storageCIDR))

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{primaryView()}, Space: readySpace})

	if _, ok := skipFor(plan, storageName, SkipNoAddress); !ok {
		t.Fatalf("expected a NoAddress skip for the unresolved interface, got %+v", plan.Skips)
	}
	if len(plan.Ensure) != 1 {
		t.Fatalf("expected only the primary, got %+v", plan.Ensure)
	}
}

func TestCompute_InterfaceIndependentOfPrimary(t *testing.T) {
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry(storageCIDR))
	v := primaryView(storageView(storageMAC))
	v.Address = "" // no InternalIP

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace})

	if s, ok := skipFor(plan, "", SkipNoAddress); !ok || s.Message != "node has no InternalIP address" {
		t.Errorf("expected the primary NoAddress skip, got %+v", plan.Skips)
	}
	if len(plan.Ensure) != 1 || plan.Ensure[0].Name != regWorker1Storage {
		t.Errorf("the interface must still be registered, got %+v", plan.Ensure)
	}
}

func TestCompute_InterfaceInvalidIsFrozen(t *testing.T) {
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry("nonsense"))
	v := primaryView(InterfaceView{Name: storageName, Invalid: `cidr "nonsense" is not a valid IPv4 or IPv6 CIDR`})
	existing := []RegistrationState{{Name: regWorker1Storage, Kind: KindAddress, Node: nodeWorker1, Interface: storageName, Ready: true}}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace, Existing: existing})

	s, ok := skipFor(plan, storageName, SkipInvalidInterface)
	if !ok || !strings.HasPrefix(s.Message, "interface storage: cidr ") {
		t.Fatalf("expected an InvalidInterface skip naming the interface, got %+v", plan.Skips)
	}
	if len(plan.Release) != 0 {
		t.Errorf("expected the existing interface registration frozen, got releases %+v", plan.Release)
	}
	if len(plan.Ensure) != 1 || plan.Ensure[0].Name != regWorker1 {
		t.Errorf("the primary must still be registered, got %+v", plan.Ensure)
	}
}

func TestCompute_InterfaceSpaceRef(t *testing.T) {
	own := uddiv1alpha1.AdditionalInterface{Name: storageName, CIDR: storageCIDR, HostnameSuffix: storageSufx, SpaceRef: &uddiv1alpha1.ObjectRef{Name: "storage-space"}}
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, own)
	v := primaryView(storageView(storageMAC))

	t.Run("own space ready", func(t *testing.T) {
		plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace,
			Spaces: map[string]SpaceState{"storage-space": {Name: "storage-space", Ready: true}}})

		d := ensuredByName(t, plan, regWorker1Storage)
		if d.FixedAddressSpec.SpaceRef.Name != "storage-space" {
			t.Errorf("SpaceRef = %+v, want the entry's storage-space", d.FixedAddressSpec.SpaceRef)
		}
		if ensuredByName(t, plan, regWorker1).FixedAddressSpec.SpaceRef.Name != "corp" {
			t.Errorf("the primary must keep the policy's space")
		}
	})

	t.Run("own space not ready", func(t *testing.T) {
		plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace,
			Spaces: map[string]SpaceState{"storage-space": {Name: "storage-space"}}})

		s, ok := skipFor(plan, storageName, SkipSpaceNotReady)
		if !ok || s.Message != "interface storage: space storage-space has no status.id yet" {
			t.Fatalf("expected a SpaceNotReady skip for the interface, got %+v", plan.Skips)
		}
		if len(plan.Ensure) != 1 || plan.Ensure[0].Name != regWorker1 {
			t.Errorf("the primary must still be registered, got %+v", plan.Ensure)
		}
	})

	t.Run("own space unknown to the planner counts as not ready", func(t *testing.T) {
		plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace})

		if _, ok := skipFor(plan, storageName, SkipSpaceNotReady); !ok {
			t.Fatalf("expected a SpaceNotReady skip, got %+v", plan.Skips)
		}
	})

	t.Run("entry naming the policy's own space uses Space", func(t *testing.T) {
		same := own
		same.SpaceRef = &uddiv1alpha1.ObjectRef{Name: "corp"}
		plan := Compute(PlanInput{Policy: ifPolicy(uddiv1alpha1.RegisterModeAuto, same), Nodes: []NodeView{v}, Space: readySpace})

		if d := ensuredByName(t, plan, regWorker1Storage); d.FixedAddressSpec.SpaceRef.Name != "corp" {
			t.Errorf("SpaceRef = %+v", d.FixedAddressSpec.SpaceRef)
		}
	})

	t.Run("policy space not ready blocks entries that default to it", func(t *testing.T) {
		def := own
		def.SpaceRef = nil
		plan := Compute(PlanInput{Policy: ifPolicy(uddiv1alpha1.RegisterModeAuto, def), Nodes: []NodeView{v}, Space: SpaceState{Name: "corp"}})

		if len(plan.Ensure) != 0 {
			t.Fatalf("expected nothing ensured, got %+v", plan.Ensure)
		}
		if _, ok := skipFor(plan, storageName, SkipSpaceNotReady); !ok {
			t.Errorf("expected an interface SpaceNotReady skip, got %+v", plan.Skips)
		}
	})
}

func TestCompute_ReleaseOnInterfaceRemoval(t *testing.T) {
	// The spec now lists only storage; mgmt was removed.
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry(storageCIDR))
	existing := []RegistrationState{
		{Name: regWorker1, Kind: KindFixedAddress, Node: nodeWorker1, Ready: true},
		{Name: regWorker1Storage, Kind: KindFixedAddress, Node: nodeWorker1, Interface: storageName, Ready: true},
		{Name: "nodes-worker-1-mgmt", Kind: KindAddress, Node: nodeWorker1, Interface: "mgmt", Ready: true},
		{Name: "nodes-worker-1-mgmt-2", Kind: KindFixedAddress, Node: nodeWorker1, Interface: "mgmt", Ready: true},
	}

	t.Run("node healthy", func(t *testing.T) {
		plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{primaryView(storageView(storageMAC))}, Space: readySpace, Existing: existing})

		want := []Release{
			{Name: "nodes-worker-1-mgmt", Kind: KindAddress, Node: NodeRef{Name: nodeWorker1}, Interface: "mgmt"},
			{Name: "nodes-worker-1-mgmt-2", Kind: KindFixedAddress, Node: NodeRef{Name: nodeWorker1}, Interface: "mgmt"},
		}
		if !reflect.DeepEqual(plan.Release, want) {
			t.Errorf("Release = %+v, want %+v", plan.Release, want)
		}
		if plan.Registered != 2 || plan.Pending != 0 {
			t.Errorf("Registered/Pending = %d/%d, want 2/0 (primary and storage)", plan.Registered, plan.Pending)
		}
	})

	t.Run("node frozen on an invalid annotation still releases the removed interface", func(t *testing.T) {
		v := primaryView()
		v.Invalid = "uddi.infoblox.com/mac: bad"

		plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace, Existing: existing})

		if len(plan.Ensure) != 0 {
			t.Fatalf("expected nothing ensured while frozen, got %+v", plan.Ensure)
		}
		if len(plan.Release) != 2 || plan.Release[0].Interface != "mgmt" || plan.Release[1].Interface != "mgmt" {
			t.Errorf("expected only the removed mgmt registrations released, got %+v", plan.Release)
		}
	})

	t.Run("every interface removed from the spec", func(t *testing.T) {
		plan := Compute(PlanInput{Policy: hrPolicy(uddiv1alpha1.RegisterModeAuto), Nodes: []NodeView{primaryView()}, Space: readySpace, Existing: existing})

		if len(plan.Release) != 3 {
			t.Fatalf("expected the storage and both mgmt registrations released, got %+v", plan.Release)
		}
		for _, r := range plan.Release {
			if r.Interface == "" {
				t.Errorf("the primary registration was released: %+v", r)
			}
		}
	})
}

func TestCompute_OptOutReleasesInterfaceRegistrations(t *testing.T) {
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry(storageCIDR))
	v := primaryView(storageView(storageMAC))
	v.OptOut = true
	existing := []RegistrationState{
		{Name: regWorker1, Kind: KindFixedAddress, Node: nodeWorker1, Ready: true},
		{Name: regWorker1Storage, Kind: KindFixedAddress, Node: nodeWorker1, Interface: storageName, Ready: true},
	}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v}, Space: readySpace, Existing: existing})

	if len(plan.Ensure) != 0 {
		t.Fatalf("expected nothing ensured for an opted-out Node, got %+v", plan.Ensure)
	}
	if len(plan.Skips) != 1 || plan.Skips[0].Reason != SkipExcluded || plan.Skips[0].Interface != "" {
		t.Errorf("expected exactly one Excluded skip for the Node, got %+v", plan.Skips)
	}
	if len(plan.Release) != 2 || plan.Release[0].Name != regWorker1 || plan.Release[1].Name != regWorker1Storage || plan.Release[1].Interface != storageName {
		t.Errorf("expected both registrations released, got %+v", plan.Release)
	}
}

func TestCompute_NodeDisappearanceReleasesInterfaceRegistrations(t *testing.T) {
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry(storageCIDR))
	existing := []RegistrationState{
		{Name: regWorker1, Kind: KindFixedAddress, Node: nodeWorker1, Ready: true},
		{Name: regWorker1Storage, Kind: KindAddress, Node: nodeWorker1, Interface: storageName, Ready: true},
	}

	plan := Compute(PlanInput{Policy: policy, Nodes: nil, Space: readySpace, Existing: existing})

	want := []Release{
		{Name: regWorker1, Kind: KindFixedAddress, Node: NodeRef{Name: nodeWorker1}},
		{Name: regWorker1Storage, Kind: KindAddress, Node: NodeRef{Name: nodeWorker1}, Interface: storageName},
	}
	if !reflect.DeepEqual(plan.Release, want) {
		t.Errorf("Release = %+v, want %+v", plan.Release, want)
	}
}

func TestCompute_InterfaceKindChangeOnlyTouchesItsOwnSlot(t *testing.T) {
	// The interface lost its MAC: its FixedAddress becomes an Address. The primary keeps its
	// FixedAddress untouched.
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry(storageCIDR))
	existing := []RegistrationState{
		{Name: regWorker1, Kind: KindFixedAddress, Node: nodeWorker1, Ready: true},
		{Name: regWorker1Storage, Kind: KindFixedAddress, Node: nodeWorker1, Interface: storageName, Ready: true},
	}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{primaryView(storageView(""))}, Space: readySpace, Existing: existing})

	if d := ensuredByName(t, plan, regWorker1Storage); d.Kind != KindAddress {
		t.Errorf("interface Kind = %s, want Address", d.Kind)
	}
	if d := ensuredByName(t, plan, regWorker1); d.Kind != KindFixedAddress {
		t.Errorf("primary Kind = %s, want FixedAddress", d.Kind)
	}
	want := []Release{{Name: regWorker1Storage, Kind: KindFixedAddress, Node: NodeRef{Name: nodeWorker1}, Interface: storageName}}
	if !reflect.DeepEqual(plan.Release, want) {
		t.Errorf("Release = %+v, want %+v", plan.Release, want)
	}
	if plan.Registered != 1 {
		t.Errorf("Registered = %d, want 1 (only the primary; the interface object is the wrong kind)", plan.Registered)
	}
}

func TestCompute_InterfaceRegisteredAndPending(t *testing.T) {
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry(storageCIDR))
	existing := []RegistrationState{
		{Name: regWorker1, Kind: KindFixedAddress, Node: nodeWorker1, Ready: true},
		{Name: regWorker1Storage, Kind: KindFixedAddress, Node: nodeWorker1, Interface: storageName, Reason: "APIError", Message: "address outside space"},
	}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{primaryView(storageView(storageMAC))}, Space: readySpace, Existing: existing})

	if plan.Registered != 1 || plan.Pending != 1 {
		t.Fatalf("Registered/Pending = %d/%d, want 1/1", plan.Registered, plan.Pending)
	}
	s, ok := skipFor(plan, storageName, SkipRegistrationPending)
	if !ok || s.Message != "interface storage: APIError: address outside space" {
		t.Errorf("expected a RegistrationPending skip for the interface, got %+v", plan.Skips)
	}
	if _, ok := skipFor(plan, "", SkipRegistrationPending); ok {
		t.Errorf("the ready primary must not be reported pending: %+v", plan.Skips)
	}
}

func TestCompute_PrimaryUnchangedByAdditionalInterfaces(t *testing.T) {
	v := primaryView(storageView(storageMAC))
	existing := []RegistrationState{{Name: regWorker1, Kind: KindFixedAddress, Node: nodeWorker1, Ready: true}}

	without := Compute(PlanInput{Policy: hrPolicy(uddiv1alpha1.RegisterModeAuto), Nodes: []NodeView{v}, Space: readySpace, Existing: existing})
	with := Compute(PlanInput{Policy: ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry(storageCIDR)), Nodes: []NodeView{v}, Space: readySpace, Existing: existing})

	if len(without.Ensure) != 1 {
		t.Fatalf("baseline Ensure = %+v", without.Ensure)
	}
	got := ensuredByName(t, with, regWorker1)
	if !reflect.DeepEqual(got, without.Ensure[0]) {
		t.Errorf("primary registration changed by additionalInterfaces:\n got %+v\nwant %+v", got, without.Ensure[0])
	}
	if with.Registered != without.Registered || with.Pending != without.Pending {
		t.Errorf("Registered/Pending = %d/%d, want %d/%d", with.Registered, with.Pending, without.Registered, without.Pending)
	}
}

func TestCompute_TwoInterfacesDeterministicOrder(t *testing.T) {
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto,
		uddiv1alpha1.AdditionalInterface{Name: "storage", CIDR: storageCIDR, HostnameSuffix: "-storage"},
		uddiv1alpha1.AdditionalInterface{Name: "mgmt", CIDR: "192.168.0.0/24", HostnameSuffix: "-mgmt"},
		uddiv1alpha1.AdditionalInterface{Name: "backup", CIDR: "172.16.0.0/24", HostnameSuffix: "-backup"},
	)
	v := primaryView(
		storageView(storageMAC),
		InterfaceView{Name: "mgmt", Hostname: "worker-1-mgmt.example.com"},     // no address
		InterfaceView{Name: "backup", Hostname: "worker-1-backup.example.com"}, // no address
	)
	other := NodeView{Ref: NodeRef{Name: "worker-0"}, Address: "10.0.0.4", Hostname: "worker-0"}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{v, other}, Space: readySpace})

	names := make([]string, 0, len(plan.Ensure))
	for _, d := range plan.Ensure {
		names = append(names, d.Name)
	}
	if want := []string{"nodes-worker-0", regWorker1, regWorker1Storage}; !reflect.DeepEqual(names, want) {
		t.Errorf("Ensure order = %v, want %v", names, want)
	}

	skips := make([]string, 0, len(plan.Skips))
	for _, s := range plan.Skips {
		skips = append(skips, s.Node.Name+"/"+s.Interface+"/"+string(s.Reason))
	}
	want := []string{
		"worker-0/backup/NoAddress",
		"worker-0/mgmt/NoAddress",
		"worker-0/storage/NoAddress",
		"worker-1/backup/NoAddress",
		"worker-1/mgmt/NoAddress",
	}
	if !reflect.DeepEqual(skips, want) {
		t.Errorf("Skips order = %v, want %v", skips, want)
	}
}

func TestCompute_InterfaceIPv6EndToEnd(t *testing.T) {
	entry := uddiv1alpha1.AdditionalInterface{Name: storageName, CIDR: storageCIDR6, HostnameSuffix: storageSufx}
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, entry)
	node := nodeWith(internalIP(nodeAddr), internalIP("FD00:20:0:0:0:0:0:7"))
	nns := nnsIfaces(nnsIface("eth1", strings.ToUpper(storageMAC), nil, []string{"fd00:20::7"}))

	views := ResolveInterfaces(policy.Spec.AdditionalInterfaces, node, nodeFQDN, nns, nil)
	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{primaryView(views...)}, Space: readySpace})

	d := ensuredByName(t, plan, regWorker1Storage)
	if d.Kind != KindFixedAddress || d.FixedAddressSpec.Address != storageIPv6 || d.FixedAddressSpec.MatchValue != storageMAC {
		t.Errorf("registration = %+v / %+v, want a FixedAddress for %s with MAC %s", d, d.FixedAddressSpec, storageIPv6, storageMAC)
	}
	if d.FixedAddressSpec.Hostname != storageFQDN {
		t.Errorf("Hostname = %q, want %q", d.FixedAddressSpec.Hostname, storageFQDN)
	}
}

func TestCompute_InterfaceSkipsDoNotFreezeOtherNodes(t *testing.T) {
	// An interface with no address on one Node must not affect another Node's registration.
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry(storageCIDR))
	nodeA := NodeView{Ref: NodeRef{Name: "a"}, Address: nodeAddr, Hostname: "a.example.com",
		Interfaces: []InterfaceView{{Name: storageName, Hostname: "a-storage.example.com"}}}
	nodeB := NodeView{Ref: NodeRef{Name: "b"}, Address: nodeAddr, Hostname: "b.example.com",
		Interfaces: []InterfaceView{{Name: storageName, Address: storageIP, Hostname: "b-storage.example.com"}}}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{nodeA, nodeB}, Space: readySpace})

	got := map[string]bool{}
	for _, d := range plan.Ensure {
		got[d.Name] = true
	}
	for _, want := range []string{"nodes-a", "nodes-b", "nodes-b-storage"} {
		if !got[want] {
			t.Errorf("missing ensured registration %q in %+v", want, plan.Ensure)
		}
	}
	if got["nodes-a-storage"] {
		t.Errorf("registered an interface that had no address")
	}
}
