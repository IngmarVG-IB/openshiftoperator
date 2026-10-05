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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/labelvalue"
)

const (
	// nodeStorageNamed is a Node whose name makes its primary registration name equal the
	// storage interface registration of worker-1: "<policy>-worker-1-storage".
	nodeStorageNamed = "worker-1-storage"
	storageOtherAddr = "10.0.0.6"
)

// longNodeName builds a dotted Node name of exactly n characters, longer than a label value
// may be but a valid Kubernetes object name.
func longNodeName(n int) string {
	const suffix = ".example.com"
	prefix := "long-"
	return prefix + strings.Repeat("x", n-len(prefix)-len(suffix)) + suffix
}

func TestCompute_LongNodeNameLabels(t *testing.T) {
	name := longNodeName(100)
	if len(name) != 100 {
		t.Fatalf("test setup: node name has %d characters, want 100", len(name))
	}
	label := labelvalue.For(name)
	if label == name || len(label) > labelvalue.MaxLength {
		t.Fatalf("test setup: labelvalue.For(%q) = %q, want a shortened value", name, label)
	}

	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry(storageCIDR))
	view := NodeView{
		Ref:        NodeRef{Name: name},
		Address:    nodeAddr,
		MAC:        nodeMAC,
		Hostname:   name,
		Interfaces: []InterfaceView{storageView(storageMAC)},
	}

	t.Run("the labels carry the shortened Node name and are valid label values", func(t *testing.T) {
		plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{view}, Space: readySpace})

		if len(plan.Ensure) != 2 || len(plan.Skips) != 0 {
			t.Fatalf("expected the primary and the storage registration, got %+v / %+v", plan.Ensure, plan.Skips)
		}
		for _, d := range plan.Ensure {
			if got := d.Labels[LabelNode]; got != label {
				t.Errorf("%s: %s label = %q, want %q", d.Name, LabelNode, got, label)
			}
			if errs := validation.IsValidLabelValue(d.Labels[LabelNode]); len(errs) != 0 {
				t.Errorf("%s: %s label %q is not a valid label value: %v", d.Name, LabelNode, d.Labels[LabelNode], errs)
			}
			if d.Node.Name != name {
				t.Errorf("%s: Node.Name = %q, want the full name", d.Name, d.Node.Name)
			}
			if d.Name != RegistrationName("nodes", name) && d.Name != InterfaceRegistrationName("nodes", name, storageName) {
				t.Errorf("unexpected registration name %q", d.Name)
			}
		}
	})

	t.Run("existing objects labelled with the shortened name are matched, not released", func(t *testing.T) {
		existing := []RegistrationState{
			{Name: RegistrationName("nodes", name), Kind: KindFixedAddress, Node: label, Ready: true},
			{Name: InterfaceRegistrationName("nodes", name, storageName), Kind: KindFixedAddress, Node: label, Interface: storageName, Ready: true},
		}

		plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{view}, Space: readySpace, Existing: existing})

		if len(plan.Release) != 0 {
			t.Fatalf("expected no release, got %+v", plan.Release)
		}
		if plan.Registered != 2 || plan.Pending != 0 {
			t.Errorf("Registered/Pending = %d/%d, want 2/0: the existing objects belong to the Node", plan.Registered, plan.Pending)
		}
	})

	t.Run("an opted-out Node releases its objects, named by the full Node name", func(t *testing.T) {
		optedOut := view
		optedOut.OptOut = true
		existing := []RegistrationState{
			{Name: RegistrationName("nodes", name), Kind: KindFixedAddress, Node: label, Ready: true},
			{Name: InterfaceRegistrationName("nodes", name, storageName), Kind: KindFixedAddress, Node: label, Interface: storageName, Ready: true},
		}

		plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{optedOut}, Space: readySpace, Existing: existing})

		if len(plan.Release) != 2 {
			t.Fatalf("expected both registrations released, got %+v", plan.Release)
		}
		for _, r := range plan.Release {
			if r.Node.Name != name {
				t.Errorf("Release.Node = %q, want the full Node name %q", r.Node.Name, name)
			}
		}
	})

	t.Run("a Node that left the input releases its objects", func(t *testing.T) {
		existing := []RegistrationState{
			{Name: RegistrationName("nodes", name), Kind: KindAddress, Node: label, Ready: true},
		}

		plan := Compute(PlanInput{Policy: policy, Nodes: nil, Space: readySpace, Existing: existing})

		// The planner only knows the label value of a Node it no longer sees.
		if len(plan.Release) != 1 || plan.Release[0].Name != RegistrationName("nodes", name) || plan.Release[0].Node.Name != label {
			t.Fatalf("Release = %+v, want the Address released under the label value", plan.Release)
		}
	})
}

// ensuredNames returns the names of plan.Ensure and fails when two entries share one.
func ensuredNames(t *testing.T, plan Plan) map[string]DesiredRegistration {
	t.Helper()
	out := make(map[string]DesiredRegistration, len(plan.Ensure))
	for _, d := range plan.Ensure {
		if prev, dup := out[d.Name]; dup {
			t.Fatalf("two desired registrations share the name %q: %+v and %+v", d.Name, prev, d)
		}
		out[d.Name] = d
	}
	return out
}

// collisionNodes are worker-1, which has a storage interface, and a Node named
// "worker-1-storage" whose primary registration gets the same object name.
func collisionNodes() (worker1, storageNamed NodeView) {
	worker1 = primaryView(storageView(storageMAC))
	storageNamed = NodeView{
		Ref:      NodeRef{Name: nodeStorageNamed},
		Address:  storageOtherAddr,
		MAC:      "aa:bb:cc:dd:ee:01",
		Hostname: nodeStorageNamed,
	}
	return worker1, storageNamed
}

func TestCompute_NameConflictKeepsPrimaryRegistration(t *testing.T) {
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry(storageCIDR))
	worker1, storageNamed := collisionNodes()

	orders := map[string][]NodeView{
		"the colliding Node last":  {worker1, storageNamed},
		"the colliding Node first": {storageNamed, worker1},
	}
	for label, nodes := range orders {
		t.Run(label, func(t *testing.T) {
			plan := Compute(PlanInput{Policy: policy, Nodes: nodes, Space: readySpace})

			byName := ensuredNames(t, plan)
			if len(byName) != 2 {
				t.Fatalf("expected the two primary registrations only, got %+v", plan.Ensure)
			}
			kept, ok := byName[regWorker1Storage]
			if !ok {
				t.Fatalf("no registration named %q in %+v", regWorker1Storage, plan.Ensure)
			}
			if kept.Interface != "" || kept.Node.Name != nodeStorageNamed {
				t.Errorf("the registration named %q = %+v, want the primary registration of Node %s", regWorker1Storage, kept, nodeStorageNamed)
			}
			if _, has := kept.Labels[LabelInterface]; has {
				t.Errorf("the kept primary registration carries an interface label: %+v", kept.Labels)
			}
			if _, ok := byName[regWorker1]; !ok {
				t.Errorf("worker-1's own primary registration is missing from %+v", plan.Ensure)
			}

			s, ok := skipFor(plan, storageName, SkipNameConflict)
			if !ok {
				t.Fatalf("no NameConflict skip for the interface in %+v", plan.Skips)
			}
			if s.Node.Name != nodeWorker1 {
				t.Errorf("skip Node = %q, want %q", s.Node.Name, nodeWorker1)
			}
			want := "interface storage: object name nodes-worker-1-storage is already used by Node worker-1-storage"
			if s.Message != want {
				t.Errorf("Message = %q, want %q", s.Message, want)
			}
			if len(plan.Release) != 0 {
				t.Errorf("expected no release, got %+v", plan.Release)
			}
		})
	}
}

func TestCompute_NameConflictFollowsTheInterfaceOnceTheCollidingNodeGoes(t *testing.T) {
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry(storageCIDR))
	worker1, storageNamed := collisionNodes()

	t.Run("a Node that opted out stops holding the name", func(t *testing.T) {
		storageNamed.OptOut = true
		existing := []RegistrationState{
			{Name: regWorker1Storage, Kind: KindFixedAddress, Node: nodeStorageNamed, Ready: true},
		}

		plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{worker1, storageNamed}, Space: readySpace, Existing: existing})

		d := ensuredByName(t, plan, regWorker1Storage)
		if d.Interface != storageName {
			t.Fatalf("the registration named %q = %+v, want worker-1's storage interface", regWorker1Storage, d)
		}
		if _, ok := skipFor(plan, storageName, SkipNameConflict); ok {
			t.Errorf("unexpected NameConflict skip in %+v", plan.Skips)
		}
		// The old object has the same kind and name as the new registration: it is applied
		// over, never deleted right after.
		for _, r := range plan.Release {
			if r.Name == regWorker1Storage && r.Kind == KindFixedAddress {
				t.Errorf("released %+v although the same object is ensured again", r)
			}
		}
	})
}

func TestCompute_NameConflictAgainstAFrozenRegistration(t *testing.T) {
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto, storageEntry(storageCIDR))
	worker1, storageNamed := collisionNodes()
	// The colliding Node has no address right now, so it plans nothing and keeps the object
	// it has: applying the interface registration over it would hand it to worker-1.
	storageNamed.Address = ""
	existing := []RegistrationState{
		{Name: regWorker1Storage, Kind: KindAddress, Node: nodeStorageNamed, Ready: true},
	}

	plan := Compute(PlanInput{Policy: policy, Nodes: []NodeView{worker1, storageNamed}, Space: readySpace, Existing: existing})

	if primary := ensuredByName(t, plan, regWorker1); primary.Interface != "" {
		t.Fatalf("unexpected ensure %+v", primary)
	}
	for _, d := range plan.Ensure {
		if d.Name == regWorker1Storage {
			t.Errorf("the interface registration %+v was ensured over a frozen object of another Node", d)
		}
	}
	s, ok := skipFor(plan, storageName, SkipNameConflict)
	if !ok || !strings.Contains(s.Message, "Node worker-1-storage") {
		t.Fatalf("expected a NameConflict skip naming Node worker-1-storage, got %+v", plan.Skips)
	}
	if len(plan.Release) != 0 {
		t.Errorf("the frozen object must not be released, got %+v", plan.Release)
	}
}

func TestCompute_NameConflictBetweenInterfacesOfDifferentNodes(t *testing.T) {
	// Interface names may contain dashes, so interface "b-c" of Node "a" and interface "c" of
	// Node "a-b" are both "<policy>-a-b-c".
	policy := ifPolicy(uddiv1alpha1.RegisterModeAuto,
		uddiv1alpha1.AdditionalInterface{Name: "b-c", CIDR: storageCIDR},
		uddiv1alpha1.AdditionalInterface{Name: "c", CIDR: storageCIDR},
	)
	resolved := func(name string) InterfaceView {
		return InterfaceView{Name: name, Address: storageIP, MAC: storageMAC, Hostname: "h-" + name + ".example.com"}
	}
	nodeA := NodeView{Ref: NodeRef{Name: "a"}, Address: nodeAddr, MAC: nodeMAC, Hostname: "a", Interfaces: []InterfaceView{resolved("b-c")}}
	nodeAB := NodeView{Ref: NodeRef{Name: "a-b"}, Address: storageOtherAddr, MAC: nodeMAC, Hostname: "a-b", Interfaces: []InterfaceView{resolved("c")}}

	for label, nodes := range map[string][]NodeView{
		"a first":   {nodeA, nodeAB},
		"a-b first": {nodeAB, nodeA},
	} {
		t.Run(label, func(t *testing.T) {
			plan := Compute(PlanInput{Policy: policy, Nodes: nodes, Space: readySpace})

			byName := ensuredNames(t, plan)
			winner, ok := byName["nodes-a-b-c"]
			if !ok {
				t.Fatalf("no registration named nodes-a-b-c in %+v", plan.Ensure)
			}
			if winner.Node.Name != "a" || winner.Interface != "b-c" {
				t.Errorf("winner = Node %s interface %s, want the first Node in name order (a, b-c)", winner.Node.Name, winner.Interface)
			}
			s, ok := skipFor(plan, "c", SkipNameConflict)
			if !ok || s.Node.Name != "a-b" {
				t.Fatalf("expected a NameConflict skip for interface c of Node a-b, got %+v", plan.Skips)
			}
			if !strings.Contains(s.Message, "interface b-c of Node a") {
				t.Errorf("Message = %q, want it to name interface b-c of Node a", s.Message)
			}
		})
	}
}
