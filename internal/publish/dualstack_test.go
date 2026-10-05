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

package publish

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

// The dual-stack tests prove DESIGN.md 4.7's claim that load balancers and Nodes with an IPv4
// and an IPv6 address already yield one A and one AAAA Record each: the extractor gives one
// endpoint carrying an A and an AAAA target, and the planner turns that into two Records.

// planDualStack runs the planner for one endpoint in a single ready zone and returns the
// Records keyed by type, failing the test unless there is exactly one A and one AAAA.
func planDualStack(t *testing.T, ep Endpoint, createPtr bool) map[string]DesiredRecord {
	t.Helper()
	policy := testPolicy(int32Ptr(300), nil)
	policy.Spec.CreatePtr = createPtr
	zones := []Zone{{Name: "apps-zone", FQDN: "example.com.", Ready: true}}

	records, skips := Plan(PlanInput{Policy: policy, Endpoints: []Endpoint{ep}, Zones: zones})
	if len(skips) != 0 {
		t.Fatalf("expected no skips, got %+v", skips)
	}
	if len(records) != 2 {
		t.Fatalf("expected two Records (one A, one AAAA), got %d: %+v", len(records), records)
	}
	byType := map[string]DesiredRecord{}
	for _, r := range records {
		byType[string(r.Spec.Type)] = r
	}
	a, aaaa := byType["A"], byType["AAAA"]
	if a.Spec.RData.A == nil || aaaa.Spec.RData.AAAA == nil {
		t.Fatalf("expected one A and one AAAA Record, got types %v", reflect.ValueOf(byType).MapKeys())
	}
	if a.Name == aaaa.Name {
		t.Errorf("the A and AAAA Records share the Record CR name %q", a.Name)
	}
	if a.Spec.Name != aaaa.Spec.Name {
		t.Errorf("A name %q and AAAA name %q differ, want the same name in zone", a.Spec.Name, aaaa.Spec.Name)
	}
	for typ, r := range byType {
		if createPtr != (r.Spec.CreatePtr != nil && *r.Spec.CreatePtr) {
			t.Errorf("%s CreatePtr = %v, want %v", typ, r.Spec.CreatePtr, createPtr)
		}
	}
	return byType
}

func dualStackTargets() []Target {
	return []Target{
		{Type: TargetA, Value: "192.0.2.10"},
		{Type: TargetAAAA, Value: "2001:db8::10"},
	}
}

func TestDualStack_Ingress(t *testing.T) {
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "web"},
		Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "app.example.com"}}},
		Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{
			Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: "192.0.2.10"}, {IP: "2001:db8::10"}},
		}},
	}
	eps, skips := EndpointsFromIngress(ing, SourceOptions{})
	if len(skips) != 0 || len(eps) != 1 {
		t.Fatalf("got endpoints %#v skips %#v, want one endpoint", eps, skips)
	}
	if !reflect.DeepEqual(eps[0].Targets, dualStackTargets()) {
		t.Fatalf("targets = %#v, want one A and one AAAA", eps[0].Targets)
	}

	for _, createPtr := range []bool{false, true} {
		got := planDualStack(t, eps[0], createPtr)
		if got["A"].Spec.RData.A.Address != "192.0.2.10" || got["AAAA"].Spec.RData.AAAA.Address != "2001:db8::10" {
			t.Errorf("Records carry the wrong addresses: A %+v AAAA %+v", got["A"].Spec.RData, got["AAAA"].Spec.RData)
		}
		if got["A"].Spec.Name != "app" {
			t.Errorf("name in zone = %q, want app", got["A"].Spec.Name)
		}
	}
}

func TestDualStack_LoadBalancerService(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "team-a", Name: "web",
			Annotations: map[string]string{"uddi.infoblox.com/hostname": "svc.example.com"},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
			Ingress: []corev1.LoadBalancerIngress{{IP: "192.0.2.10"}, {IP: "2001:db8::10"}},
		}},
	}
	eps, skips := EndpointsFromService(svc, SourceOptions{})
	if len(skips) != 0 || len(eps) != 1 {
		t.Fatalf("got endpoints %#v skips %#v, want one endpoint", eps, skips)
	}
	if !reflect.DeepEqual(eps[0].Targets, dualStackTargets()) {
		t.Fatalf("targets = %#v, want one A and one AAAA", eps[0].Targets)
	}

	for _, createPtr := range []bool{false, true} {
		got := planDualStack(t, eps[0], createPtr)
		if got["A"].Spec.Name != "svc" {
			t.Errorf("name in zone = %q, want svc", got["A"].Spec.Name)
		}
	}
}

func TestDualStack_Node(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-0.example.com"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeInternalIP, Address: "192.0.2.10"},
			{Type: corev1.NodeInternalIP, Address: "2001:db8::10"},
			{Type: corev1.NodeHostName, Address: "worker-0"},
		}},
	}
	eps, skips := EndpointsFromNode(node, SourceOptions{})
	if len(skips) != 0 || len(eps) != 1 {
		t.Fatalf("got endpoints %#v skips %#v, want one endpoint", eps, skips)
	}
	if !reflect.DeepEqual(eps[0].Targets, dualStackTargets()) {
		t.Fatalf("targets = %#v, want one A and one AAAA", eps[0].Targets)
	}

	for _, createPtr := range []bool{false, true} {
		got := planDualStack(t, eps[0], createPtr)
		if got["A"].Spec.Name != "worker-0" {
			t.Errorf("name in zone = %q, want worker-0", got["A"].Spec.Name)
		}
		if got["A"].Spec.Type != uddiv1alpha1.RecordType("A") || got["AAAA"].Spec.Type != uddiv1alpha1.RecordType("AAAA") {
			t.Errorf("unexpected Record types %q and %q", got["A"].Spec.Type, got["AAAA"].Spec.Type)
		}
	}
}
