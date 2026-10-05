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

package metrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

const (
	objectsHeader = "# HELP uddi_operator_objects Number of objects per kind, namespace and " +
		"Ready condition value (true, false or unknown).\n# TYPE uddi_operator_objects gauge\n"
	policyObjectsHeader = "# HELP uddi_operator_policy_objects Counts from a policy's status: published and " +
		"ready, allocated and assigned, or registered and pending.\n# TYPE uddi_operator_policy_objects gauge\n"
	policySkippedHeader = "# HELP uddi_operator_policy_skipped Number of status.skipped entries of a policy " +
		"per reason.\n# TYPE uddi_operator_policy_skipped gauge\n"
	errorsHeader = "# HELP uddi_operator_collector_errors_total Number of scrapes in which listing a kind " +
		"from the cache failed.\n# TYPE uddi_operator_collector_errors_total counter\n"
)

// allKinds is every kind the collector lists, spelled out here so the test does not depend
// on the tables in collector.go.
var allKinds = []string{
	"Connection", "View", "AuthZone", "Record", "IPSpace", "AddressBlock", "Subnet", "Range",
	"Address", "FixedAddress", "DNSPublishPolicy", "IPAllocationPolicy", "HostRegistrationPolicy",
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := uddiv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("registering the API types: %v", err)
	}
	return s
}

func fakeClient(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objs...).
		WithInterceptorFuncs(funcs).
		Build()
}

// conditions returns a Ready condition with the given status, or nothing for "".
func conditions(status metav1.ConditionStatus) []metav1.Condition {
	if status == "" {
		return nil
	}
	return []metav1.Condition{{
		Type:               uddiv1alpha1.ConditionReady,
		Status:             status,
		Reason:             "Test",
		LastTransitionTime: metav1.Now(),
	}}
}

func objMeta(namespace, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: namespace, Name: name}
}

func objectStatus(status metav1.ConditionStatus) uddiv1alpha1.ObjectStatus {
	return uddiv1alpha1.ObjectStatus{Conditions: conditions(status)}
}

func view(namespace, name string, status metav1.ConditionStatus) *uddiv1alpha1.View {
	return &uddiv1alpha1.View{
		ObjectMeta: objMeta(namespace, name),
		Status:     uddiv1alpha1.ViewStatus{ObjectStatus: objectStatus(status)},
	}
}

func subnet(namespace, name string, status metav1.ConditionStatus) *uddiv1alpha1.Subnet {
	return &uddiv1alpha1.Subnet{
		ObjectMeta: objMeta(namespace, name),
		Status:     uddiv1alpha1.SubnetStatus{ObjectStatus: objectStatus(status)},
	}
}

func connection(namespace, name string, status metav1.ConditionStatus) *uddiv1alpha1.Connection {
	return &uddiv1alpha1.Connection{
		ObjectMeta: objMeta(namespace, name),
		Status:     uddiv1alpha1.ConnectionStatus{Conditions: conditions(status)},
	}
}

// errorsText renders uddi_operator_collector_errors_total for every kind, with the given
// number of failures for the kinds in failed and zero for the rest.
func errorsText(failed map[string]int) string {
	var b strings.Builder
	b.WriteString(errorsHeader)
	for _, kind := range allKinds {
		fmt.Fprintf(&b, "uddi_operator_collector_errors_total{kind=%q} %d\n", kind, failed[kind])
	}
	return b.String()
}

func TestObjectsCountReadyTrueFalseAndUnknown(t *testing.T) {
	c := newCollector(fakeClient(t, interceptor.Funcs{},
		connection("team-a", "default", metav1.ConditionTrue),
		view("team-a", "v1", metav1.ConditionTrue),
		view("team-a", "v2", metav1.ConditionTrue),
		view("team-a", "v3", metav1.ConditionFalse),
		// No Ready condition yet, and an explicit Unknown, both count as unknown.
		view("team-b", "v4", ""),
		view("team-b", "v5", metav1.ConditionUnknown),
		subnet("team-b", "s1", metav1.ConditionFalse),
	), time.Second)

	expected := objectsHeader + `
uddi_operator_objects{kind="Connection",namespace="team-a",ready="true"} 1
uddi_operator_objects{kind="Connection",namespace="team-a",ready="false"} 0
uddi_operator_objects{kind="Connection",namespace="team-a",ready="unknown"} 0
uddi_operator_objects{kind="View",namespace="team-a",ready="true"} 2
uddi_operator_objects{kind="View",namespace="team-a",ready="false"} 1
uddi_operator_objects{kind="View",namespace="team-a",ready="unknown"} 0
uddi_operator_objects{kind="View",namespace="team-b",ready="true"} 0
uddi_operator_objects{kind="View",namespace="team-b",ready="false"} 0
uddi_operator_objects{kind="View",namespace="team-b",ready="unknown"} 2
uddi_operator_objects{kind="Subnet",namespace="team-b",ready="true"} 0
uddi_operator_objects{kind="Subnet",namespace="team-b",ready="false"} 1
uddi_operator_objects{kind="Subnet",namespace="team-b",ready="unknown"} 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected), "uddi_operator_objects"); err != nil {
		t.Fatal(err)
	}
}

func TestObjectsCoverEveryKind(t *testing.T) {
	// One Ready object of each of the ten kinds with a Ready condition.
	c := newCollector(fakeClient(t, interceptor.Funcs{},
		connection("ns", "o", metav1.ConditionTrue),
		view("ns", "o", metav1.ConditionTrue),
		&uddiv1alpha1.AuthZone{ObjectMeta: objMeta("ns", "o"),
			Status: uddiv1alpha1.AuthZoneStatus{ObjectStatus: objectStatus(metav1.ConditionTrue)}},
		&uddiv1alpha1.Record{ObjectMeta: objMeta("ns", "o"),
			Status: uddiv1alpha1.RecordStatus{ObjectStatus: objectStatus(metav1.ConditionTrue)}},
		&uddiv1alpha1.IPSpace{ObjectMeta: objMeta("ns", "o"),
			Status: uddiv1alpha1.IPSpaceStatus{ObjectStatus: objectStatus(metav1.ConditionTrue)}},
		&uddiv1alpha1.AddressBlock{ObjectMeta: objMeta("ns", "o"),
			Status: uddiv1alpha1.AddressBlockStatus{ObjectStatus: objectStatus(metav1.ConditionTrue)}},
		subnet("ns", "o", metav1.ConditionTrue),
		&uddiv1alpha1.Range{ObjectMeta: objMeta("ns", "o"),
			Status: uddiv1alpha1.RangeStatus{ObjectStatus: objectStatus(metav1.ConditionTrue)}},
		&uddiv1alpha1.Address{ObjectMeta: objMeta("ns", "o"),
			Status: uddiv1alpha1.AddressStatus{ObjectStatus: objectStatus(metav1.ConditionTrue)}},
		&uddiv1alpha1.FixedAddress{ObjectMeta: objMeta("ns", "o"),
			Status: uddiv1alpha1.FixedAddressStatus{ObjectStatus: objectStatus(metav1.ConditionTrue)}},
	), time.Second)

	var b strings.Builder
	b.WriteString(objectsHeader)
	for _, kind := range allKinds[:10] {
		fmt.Fprintf(&b, "uddi_operator_objects{kind=%q,namespace=\"ns\",ready=\"true\"} 1\n", kind)
		fmt.Fprintf(&b, "uddi_operator_objects{kind=%q,namespace=\"ns\",ready=\"false\"} 0\n", kind)
		fmt.Fprintf(&b, "uddi_operator_objects{kind=%q,namespace=\"ns\",ready=\"unknown\"} 0\n", kind)
	}
	if err := testutil.CollectAndCompare(c, strings.NewReader(b.String()), "uddi_operator_objects"); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyStatesAndSkippedReasons(t *testing.T) {
	c := newCollector(fakeClient(t, interceptor.Funcs{},
		&uddiv1alpha1.DNSPublishPolicy{
			ObjectMeta: objMeta("team-a", "publish"),
			Status: uddiv1alpha1.DNSPublishPolicyStatus{
				Published: 5,
				Ready:     4,
				Skipped: []uddiv1alpha1.SkippedSource{
					{Source: "Route/a", Hostname: "a.example.com", Reason: "NoMatchingZone"},
					{Source: "Route/b", Hostname: "b.example.com", Reason: "NoMatchingZone"},
					{Source: "Ingress/c", Reason: "InvalidHostname"},
				},
			},
		},
		&uddiv1alpha1.IPAllocationPolicy{
			ObjectMeta: objMeta("team-a", "alloc"),
			Status: uddiv1alpha1.IPAllocationPolicyStatus{
				Allocated: 3,
				Assigned:  2,
				Skipped: []uddiv1alpha1.SkippedService{
					{Service: "web", Reason: "PoolNotReady"},
					// A skipped entry without a reason is counted under Unknown.
					{Service: "db"},
				},
			},
		},
		&uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: objMeta("team-b", "nodes"),
			Status: uddiv1alpha1.HostRegistrationPolicyStatus{
				Registered: 6,
				Pending:    1,
				Skipped:    []uddiv1alpha1.SkippedNode{{Node: "worker-9", Reason: "NoMAC"}},
			},
		},
		// A policy with an empty status still reports its states, at zero.
		&uddiv1alpha1.HostRegistrationPolicy{ObjectMeta: objMeta("team-b", "fresh")},
	), time.Second)

	states := policyObjectsHeader + `
uddi_operator_policy_objects{kind="DNSPublishPolicy",namespace="team-a",policy="publish",state="published"} 5
uddi_operator_policy_objects{kind="DNSPublishPolicy",namespace="team-a",policy="publish",state="ready"} 4
uddi_operator_policy_objects{kind="IPAllocationPolicy",namespace="team-a",policy="alloc",state="allocated"} 3
uddi_operator_policy_objects{kind="IPAllocationPolicy",namespace="team-a",policy="alloc",state="assigned"} 2
uddi_operator_policy_objects{kind="HostRegistrationPolicy",namespace="team-b",policy="nodes",state="registered"} 6
uddi_operator_policy_objects{kind="HostRegistrationPolicy",namespace="team-b",policy="nodes",state="pending"} 1
uddi_operator_policy_objects{kind="HostRegistrationPolicy",namespace="team-b",policy="fresh",state="registered"} 0
uddi_operator_policy_objects{kind="HostRegistrationPolicy",namespace="team-b",policy="fresh",state="pending"} 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(states), "uddi_operator_policy_objects"); err != nil {
		t.Fatal(err)
	}

	skipped := policySkippedHeader + `
uddi_operator_policy_skipped{kind="DNSPublishPolicy",namespace="team-a",policy="publish",reason="NoMatchingZone"} 2
uddi_operator_policy_skipped{kind="DNSPublishPolicy",namespace="team-a",policy="publish",reason="InvalidHostname"} 1
uddi_operator_policy_skipped{kind="IPAllocationPolicy",namespace="team-a",policy="alloc",reason="PoolNotReady"} 1
uddi_operator_policy_skipped{kind="IPAllocationPolicy",namespace="team-a",policy="alloc",reason="Unknown"} 1
uddi_operator_policy_skipped{kind="HostRegistrationPolicy",namespace="team-b",policy="nodes",reason="NoMAC"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(skipped), "uddi_operator_policy_skipped"); err != nil {
		t.Fatal(err)
	}
}

func TestListFailureOfOneKindKeepsTheOthers(t *testing.T) {
	failSubnets := interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*uddiv1alpha1.SubnetList); ok {
				return errors.New("subnet list failed")
			}
			return cl.List(ctx, list, opts...)
		},
	}
	c := newCollector(fakeClient(t, failSubnets,
		view("team-a", "v1", metav1.ConditionTrue),
		subnet("team-a", "s1", metav1.ConditionTrue),
		&uddiv1alpha1.IPAllocationPolicy{
			ObjectMeta: objMeta("team-a", "alloc"),
			Status:     uddiv1alpha1.IPAllocationPolicyStatus{Allocated: 1, Assigned: 1},
		},
	), time.Second)

	// Every CollectAndCompare below is one scrape. The first one fails the Subnet list once,
	// and every other kind stays at zero.
	expectedErrors := errorsText(map[string]int{"Subnet": 1})
	if err := testutil.CollectAndCompare(c, strings.NewReader(expectedErrors),
		"uddi_operator_collector_errors_total"); err != nil {
		t.Fatal(err)
	}

	// The Subnet series is gone, the View and policy series are not.
	expectedObjects := objectsHeader + `
uddi_operator_objects{kind="View",namespace="team-a",ready="true"} 1
uddi_operator_objects{kind="View",namespace="team-a",ready="false"} 0
uddi_operator_objects{kind="View",namespace="team-a",ready="unknown"} 0
`
	expectedPolicies := policyObjectsHeader + `
uddi_operator_policy_objects{kind="IPAllocationPolicy",namespace="team-a",policy="alloc",state="allocated"} 1
uddi_operator_policy_objects{kind="IPAllocationPolicy",namespace="team-a",policy="alloc",state="assigned"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expectedObjects), "uddi_operator_objects"); err != nil {
		t.Fatal(err)
	}
	if err := testutil.CollectAndCompare(c, strings.NewReader(expectedPolicies), "uddi_operator_policy_objects"); err != nil {
		t.Fatal(err)
	}
}

func TestSlowListIsCutOffByTheTimeout(t *testing.T) {
	blockViews := interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*uddiv1alpha1.ViewList); ok {
				<-ctx.Done()
				return ctx.Err()
			}
			return cl.List(ctx, list, opts...)
		},
	}
	c := newCollector(fakeClient(t, blockViews,
		view("team-a", "v1", metav1.ConditionTrue),
		subnet("team-a", "s1", metav1.ConditionFalse),
	), 50*time.Millisecond)

	// Every CollectAndCompare below is one scrape, and the first one times the View list out.
	start := time.Now()
	expectedErrors := errorsText(map[string]int{"View": 1})
	if err := testutil.CollectAndCompare(c, strings.NewReader(expectedErrors),
		"uddi_operator_collector_errors_total"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the scrape took %s, the timeout should have cut the blocked list off", elapsed)
	}

	// The blocked View list costs only the View series.
	expectedObjects := objectsHeader + `
uddi_operator_objects{kind="Subnet",namespace="team-a",ready="true"} 0
uddi_operator_objects{kind="Subnet",namespace="team-a",ready="false"} 1
uddi_operator_objects{kind="Subnet",namespace="team-a",ready="unknown"} 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expectedObjects), "uddi_operator_objects"); err != nil {
		t.Fatal(err)
	}
}

func TestSeriesDisappearWhenObjectsAreDeleted(t *testing.T) {
	v := view("team-a", "v1", metav1.ConditionTrue)
	policy := &uddiv1alpha1.HostRegistrationPolicy{
		ObjectMeta: objMeta("team-a", "nodes"),
		Status: uddiv1alpha1.HostRegistrationPolicyStatus{
			Registered: 1,
			Skipped:    []uddiv1alpha1.SkippedNode{{Node: "n", Reason: "NoMAC"}},
		},
	}
	cl := fakeClient(t, interceptor.Funcs{}, v, policy)
	c := newCollector(cl, time.Second)

	for name, want := range map[string]int{
		"uddi_operator_objects":        3,
		"uddi_operator_policy_objects": 2,
		"uddi_operator_policy_skipped": 1,
	} {
		if got := testutil.CollectAndCount(c, name); got != want {
			t.Fatalf("%s: got %d series, want %d", name, got, want)
		}
	}

	if err := cl.Delete(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if err := cl.Delete(context.Background(), policy); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		"uddi_operator_objects", "uddi_operator_policy_objects", "uddi_operator_policy_skipped",
	} {
		if got := testutil.CollectAndCount(c, name); got != 0 {
			t.Fatalf("%s after deleting the objects: got %d series, want 0", name, got)
		}
	}
}

func TestCollectorIsConsistentAndLintClean(t *testing.T) {
	c := NewCollector(fakeClient(t, interceptor.Funcs{},
		connection("team-a", "default", metav1.ConditionTrue),
		view("team-a", "v1", metav1.ConditionFalse),
		&uddiv1alpha1.DNSPublishPolicy{
			ObjectMeta: objMeta("team-a", "publish"),
			Status: uddiv1alpha1.DNSPublishPolicyStatus{
				Skipped: []uddiv1alpha1.SkippedSource{{Reason: "NoMatchingZone"}},
			},
		},
	))

	// A pedantic registry checks every collected metric against its descriptor.
	reg := prometheus.NewPedanticRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Gather(); err != nil {
		t.Fatal(err)
	}

	problems, err := testutil.CollectAndLint(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Errorf("metric %s: %s", p.Metric, p.Text)
	}
}
