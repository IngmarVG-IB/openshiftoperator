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

package metallb

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
)

func TestGVK(t *testing.T) {
	if got, want := GVK.Group, "metallb.io"; got != want {
		t.Errorf("GVK.Group = %q, want %q", got, want)
	}
	if got, want := GVK.Version, "v1beta1"; got != want {
		t.Errorf("GVK.Version = %q, want %q", got, want)
	}
	if got, want := GVK.Kind, "IPAddressPool"; got != want {
		t.Errorf("GVK.Kind = %q, want %q", got, want)
	}
	if got, want := ListGVK.Kind, "IPAddressPoolList"; got != want {
		t.Errorf("ListGVK.Kind = %q, want %q", got, want)
	}
	if got, want := ListGVK.GroupVersion(), GVK.GroupVersion(); got != want {
		t.Errorf("ListGVK group version = %v, want %v", got, want)
	}
}

// TestAvailable exercises Available's discovery-response handling against a fake
// apiserver, without needing a real cluster. The envtest-backed round trip in
// metallb_envtest_test.go additionally proves both cases against a real apiserver.
func TestAvailable(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    bool
		wantErr bool
	}{
		{
			name:   "group version served with ipaddresspools resource",
			status: http.StatusOK,
			body: `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"metallb.io/v1beta1","resources":[
				{"name":"ipaddresspools","singularName":"ipaddresspool","namespaced":true,"kind":"IPAddressPool","verbs":["get","list","create","update","delete","watch"]}
			]}`,
			want: true,
		},
		{
			name:   "group version served without an ipaddresspools resource",
			status: http.StatusOK,
			body: `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"metallb.io/v1beta1","resources":[
				{"name":"bgppeers","singularName":"bgppeer","namespaced":true,"kind":"BGPPeer","verbs":["get"]}
			]}`,
			want: false,
		},
		{
			name:   "group version not found",
			status: http.StatusNotFound,
			body: `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure",
				"message":"the server could not find the requested resource",
				"reason":"NotFound","details":{},"code":404}`,
			want: false,
		},
		{
			name:   "discovery fails",
			status: http.StatusInternalServerError,
			body: `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure",
				"message":"boom","reason":"InternalError","details":{},"code":500}`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got, err := Available(&rest.Config{Host: srv.URL})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Available: want error, got nil")
				}
				if got {
					t.Errorf("Available = true alongside an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Available: unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("Available = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAvailableBadConfig(t *testing.T) {
	// A config that fails to build a client (a bad TLS setup) surfaces the error rather than
	// reporting MetalLB as merely absent.
	cfg := &rest.Config{
		Host: "https://127.0.0.1:0",
		TLSClientConfig: rest.TLSClientConfig{
			CertData: []byte("not-a-cert"),
		},
	}
	if _, err := Available(cfg); err == nil {
		t.Fatalf("Available: want error for an invalid client config, got nil")
	}
}

// -----------------------------------------------------------------------------------
// PoolName
// -----------------------------------------------------------------------------------

func TestPoolName(t *testing.T) {
	cases := []struct {
		name                                        string
		policyNamespace, policy, poolKind, poolName string
		want                                        string
	}{
		{"subnet", "apps", "lb", "Subnet", "lb-net", "uddi-apps-lb-subnet-lb-net"},
		{"range", "apps", "lb", "Range", "lb-range", "uddi-apps-lb-range-lb-range"},
		{"address block", "apps", "lb", "AddressBlock", "block", "uddi-apps-lb-addressblock-block"},
		{"kind already lower case", "apps", "lb", "subnet", "net", "uddi-apps-lb-subnet-net"},
		{"dots in names", "apps", "lb.prod", "Subnet", "net.a", "uddi-apps-lb.prod-subnet-net.a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PoolName(tc.policyNamespace, tc.policy, tc.poolKind, tc.poolName)
			if got != tc.want {
				t.Errorf("PoolName() = %q, want %q", got, tc.want)
			}
			if errs := validation.IsDNS1123Subdomain(got); len(errs) > 0 {
				t.Errorf("PoolName() = %q is not a DNS-1123 subdomain: %v", got, errs)
			}
		})
	}
}

func TestPoolNameKindsDiffer(t *testing.T) {
	// The same pool name under different kinds, policies or namespaces never shares a name.
	seen := map[string]string{}
	for _, kind := range []string{"Subnet", "Range", "AddressBlock"} {
		for _, ns := range []string{"a", "b"} {
			for _, policy := range []string{"p", "q"} {
				name := PoolName(ns, policy, kind, "pool")
				key := kind + "/" + ns + "/" + policy
				if prev, dup := seen[name]; dup {
					t.Errorf("%s and %s both map to %q", prev, key, name)
				}
				seen[name] = key
			}
		}
	}
}

// poolNameHead is the part of the mirrored name of a Subnet of policy ns/pol that precedes
// the pool name.
const poolNameHead = "uddi-ns-pol-subnet-" // 19 characters

func TestPoolNameLength(t *testing.T) {
	const limit = validation.DNS1123SubdomainMaxLength

	atLimit := PoolName("ns", "pol", "Subnet", strings.Repeat("a", limit-len(poolNameHead)))
	if len(atLimit) != limit {
		t.Fatalf("name at the limit has length %d, want %d", len(atLimit), limit)
	}
	if want := poolNameHead + strings.Repeat("a", limit-len(poolNameHead)); atLimit != want {
		t.Errorf("a name of exactly %d characters was changed: %q", limit, atLimit)
	}

	over := PoolName("ns", "pol", "Subnet", strings.Repeat("a", limit+1-len(poolNameHead)))
	if len(over) != limit {
		t.Errorf("name one over the limit has length %d, want %d", len(over), limit)
	}
	if over == atLimit {
		t.Errorf("a truncated name equals the untruncated one: %q", over)
	}
	if errs := validation.IsDNS1123Subdomain(over); len(errs) > 0 {
		t.Errorf("truncated name %q is not a DNS-1123 subdomain: %v", over, errs)
	}
}

func TestPoolNameHashTruncation(t *testing.T) {
	const limit = validation.DNS1123SubdomainMaxLength

	// Two long names that differ only in their last character share the whole kept prefix
	// and must be told apart by the hash suffix alone.
	a := PoolName("ns", "pol", "Subnet", strings.Repeat("a", 300)+"x")
	b := PoolName("ns", "pol", "Subnet", strings.Repeat("a", 300)+"y")
	if a == b {
		t.Fatalf("different long names truncated to the same name %q", a)
	}
	for _, name := range []string{a, b} {
		if len(name) != limit {
			t.Errorf("truncated name has length %d, want %d", len(name), limit)
		}
		if !strings.HasPrefix(name, poolNameHead+"aaaa") {
			t.Errorf("truncated name %q lost its readable prefix", name)
		}
		if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
			t.Errorf("truncated name %q is not a DNS-1123 subdomain: %v", name, errs)
		}
	}
	// Deterministic.
	if again := PoolName("ns", "pol", "Subnet", strings.Repeat("a", 300)+"x"); again != a {
		t.Errorf("PoolName is not deterministic: %q then %q", a, again)
	}
	// The suffix is "-" and the first 10 hex digits of the sha256 of the full, untruncated name.
	full := poolNameHead + strings.Repeat("a", 300) + "x"
	if want := "-" + hashPrefix(full); !strings.HasSuffix(a, want) {
		t.Errorf("truncated name %q does not end in %q", a, want)
	}
}

func TestPoolNameTruncationTrimsSeparators(t *testing.T) {
	// When the cut falls right after a "-" or a ".", the separator is dropped so the hash
	// suffix does not follow a non-alphanumeric character.
	const keep = validation.DNS1123SubdomainMaxLength - nameHashLength - 1
	for _, sep := range []string{"-", "."} {
		poolName := strings.Repeat("a", keep-len(poolNameHead)-1) + sep + strings.Repeat("b", 60)
		full := poolNameHead + poolName
		if full[keep-1] != sep[0] {
			t.Fatalf("test setup: character %d of the full name is %q, want %q", keep-1, full[keep-1], sep)
		}
		got := PoolName("ns", "pol", "Subnet", poolName)
		if errs := validation.IsDNS1123Subdomain(got); len(errs) > 0 {
			t.Errorf("separator %q: %q is not a DNS-1123 subdomain: %v", sep, got, errs)
		}
		if strings.Contains(got, sep+"-") {
			t.Errorf("separator %q left before the hash in %q", sep, got)
		}
		if !strings.HasSuffix(got, "-"+hashPrefix(full)) {
			t.Errorf("separator %q: %q does not end in -%s", sep, got, hashPrefix(full))
		}
	}
}

// -----------------------------------------------------------------------------------
// Desired
// -----------------------------------------------------------------------------------

func TestDesired(t *testing.T) {
	cases := []struct {
		name          string
		src           Source
		wantName      string
		wantAddresses []string
	}{
		{
			name:          "ipv4 subnet",
			src:           Source{PoolKind: "Subnet", PoolName: "lb-net", CIDR: "10.20.0.0/24"},
			wantName:      "uddi-apps-lb-subnet-lb-net",
			wantAddresses: []string{"10.20.0.0/24"},
		},
		{
			name:          "ipv6 address block",
			src:           Source{PoolKind: "AddressBlock", PoolName: "v6", CIDR: "2001:db8:0:1::/64"},
			wantName:      "uddi-apps-lb-addressblock-v6",
			wantAddresses: []string{"2001:db8:0:1::/64"},
		},
		{
			name:          "ipv6 cidr is written in canonical form",
			src:           Source{PoolKind: "Subnet", PoolName: "v6", CIDR: "2001:0DB8:0000:0001::/64"},
			wantName:      "uddi-apps-lb-subnet-v6",
			wantAddresses: []string{"2001:db8:0:1::/64"},
		},
		{
			name:          "cidr with host bits becomes its network",
			src:           Source{PoolKind: "Subnet", PoolName: "net", CIDR: "10.20.0.7/24"},
			wantName:      "uddi-apps-lb-subnet-net",
			wantAddresses: []string{"10.20.0.0/24"},
		},
		{
			name:          "ipv4 range",
			src:           Source{PoolKind: "Range", PoolName: "r", Start: "10.20.0.10", End: "10.20.0.50"},
			wantName:      "uddi-apps-lb-range-r",
			wantAddresses: []string{"10.20.0.10-10.20.0.50"},
		},
		{
			name:          "ipv6 range",
			src:           Source{PoolKind: "Range", PoolName: "r6", Start: "2001:db8::10", End: "2001:DB8::50"},
			wantName:      "uddi-apps-lb-range-r6",
			wantAddresses: []string{"2001:db8::10-2001:db8::50"},
		},
		{
			name:          "single address range",
			src:           Source{PoolKind: "Range", PoolName: "one", Start: "10.20.0.10", End: "10.20.0.10"},
			wantName:      "uddi-apps-lb-range-one",
			wantAddresses: []string{"10.20.0.10-10.20.0.10"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Desired("apps", "lb", "metallb-system", tc.src)
			if err != nil {
				t.Fatalf("Desired: %v", err)
			}
			if gvk := got.GroupVersionKind(); gvk != GVK {
				t.Errorf("GVK = %v, want %v", gvk, GVK)
			}
			if got.GetName() != tc.wantName {
				t.Errorf("name = %q, want %q", got.GetName(), tc.wantName)
			}
			if got.GetNamespace() != "metallb-system" {
				t.Errorf("namespace = %q, want metallb-system", got.GetNamespace())
			}
			wantLabels := map[string]string{
				"uddi.infoblox.com/policy":           "lb",
				"uddi.infoblox.com/policy-namespace": "apps",
				"app.kubernetes.io/managed-by":       "uddi-operator",
			}
			if !reflect.DeepEqual(got.GetLabels(), wantLabels) {
				t.Errorf("labels = %v, want %v", got.GetLabels(), wantLabels)
			}
			addresses, found, err := unstructured.NestedStringSlice(got.Object, "spec", "addresses")
			if err != nil || !found {
				t.Fatalf("spec.addresses: found=%v err=%v", found, err)
			}
			if !reflect.DeepEqual(addresses, tc.wantAddresses) {
				t.Errorf("spec.addresses = %v, want %v", addresses, tc.wantAddresses)
			}
			autoAssign, found, err := unstructured.NestedBool(got.Object, "spec", "autoAssign")
			if err != nil || !found || autoAssign {
				t.Errorf("spec.autoAssign = %v (found=%v, err=%v), want false", autoAssign, found, err)
			}
			// Only Services in the policy's own namespace may be allocated from the pool.
			namespaces, found, err := unstructured.NestedStringSlice(got.Object, "spec", "serviceAllocation", "namespaces")
			if err != nil || !found || !reflect.DeepEqual(namespaces, []string{"apps"}) {
				t.Errorf("spec.serviceAllocation.namespaces = %v (found=%v, err=%v), want [apps]", namespaces, found, err)
			}
			if !OwnedBy(got, "apps", "lb") || !Selector("apps", "lb").Matches(labels.Set(got.GetLabels())) {
				t.Errorf("Desired's pool is not matched by Selector/OwnedBy of its own policy")
			}
			// A client deep-copies objects it sends; unstructured panics on non-JSON types
			// such as []string, so this guards the []interface{} the builder has to use.
			if cp := got.DeepCopy(); !Equal(got, cp) {
				t.Errorf("DeepCopy differs from the original")
			}
		})
	}
}

func TestDesiredLongName(t *testing.T) {
	src := Source{PoolKind: "Subnet", PoolName: strings.Repeat("a", 250), CIDR: "10.0.0.0/8"}
	got, err := Desired("apps", "lb", "metallb-system", src)
	if err != nil {
		t.Fatalf("Desired: %v", err)
	}
	if len(got.GetName()) != validation.DNS1123SubdomainMaxLength {
		t.Errorf("name length = %d, want %d", len(got.GetName()), validation.DNS1123SubdomainMaxLength)
	}
	if errs := validation.IsDNS1123Subdomain(got.GetName()); len(errs) > 0 {
		t.Errorf("name %q is not a DNS-1123 subdomain: %v", got.GetName(), errs)
	}
}

func TestDesiredInvalid(t *testing.T) {
	validCIDR := Source{PoolKind: "Subnet", PoolName: "net", CIDR: "10.20.0.0/24"}
	cases := []struct {
		name            string
		policyNamespace string
		policy          string
		mirrorNamespace string
		src             Source
		wantErr         string
	}{
		{"empty policy namespace", "", "lb", "metallb-system", validCIDR, "policy namespace must not be empty"},
		{"empty policy", "apps", "", "metallb-system", validCIDR, "policy must not be empty"},
		{"empty mirror namespace", "apps", "lb", "", validCIDR, "mirror namespace must not be empty"},
		{"empty pool kind", "apps", "lb", "metallb-system", Source{PoolName: "net", CIDR: "10.0.0.0/8"}, "pool kind must not be empty"},
		{"empty pool name", "apps", "lb", "metallb-system", Source{PoolKind: "Subnet", CIDR: "10.0.0.0/8"}, "pool name must not be empty"},
		{"invalid mirror namespace", "apps", "lb", "Not_A_Namespace", validCIDR, "mirror namespace"},
		{"policy too long for a label value", "apps", strings.Repeat("p", 64), "metallb-system", validCIDR, "label value"},
		{"pool name makes an invalid object name", "apps", "lb", "metallb-system",
			Source{PoolKind: "Subnet", PoolName: "Not_Valid", CIDR: "10.0.0.0/8"}, "not a valid name"},
		{"no addresses at all", "apps", "lb", "metallb-system", Source{PoolKind: "Subnet", PoolName: "net"}, "no CIDR or start and end"},
		{"both cidr and range", "apps", "lb", "metallb-system",
			Source{PoolKind: "Subnet", PoolName: "net", CIDR: "10.0.0.0/8", Start: "10.0.0.1", End: "10.0.0.2"}, "not both"},
		{"cidr and a lone start", "apps", "lb", "metallb-system",
			Source{PoolKind: "Subnet", PoolName: "net", CIDR: "10.0.0.0/8", Start: "10.0.0.1"}, "not both"},
		{"unparsable cidr", "apps", "lb", "metallb-system", Source{PoolKind: "Subnet", PoolName: "net", CIDR: "not-a-cidr"}, "invalid CIDR"},
		{"cidr without a prefix length", "apps", "lb", "metallb-system", Source{PoolKind: "Subnet", PoolName: "net", CIDR: "10.0.0.0"}, "invalid CIDR"},
		{"cidr prefix too long", "apps", "lb", "metallb-system", Source{PoolKind: "Subnet", PoolName: "net", CIDR: "10.0.0.0/33"}, "invalid CIDR"},
		{"cidr with a space", "apps", "lb", "metallb-system", Source{PoolKind: "Subnet", PoolName: "net", CIDR: " 10.0.0.0/8"}, "invalid CIDR"},
		{"ipv4-mapped cidr", "apps", "lb", "metallb-system", Source{PoolKind: "Subnet", PoolName: "net", CIDR: "::ffff:10.0.0.0/104"}, "IPv4-mapped"},
		{"range without an end", "apps", "lb", "metallb-system", Source{PoolKind: "Range", PoolName: "r", Start: "10.0.0.1"}, "needs both"},
		{"range without a start", "apps", "lb", "metallb-system", Source{PoolKind: "Range", PoolName: "r", End: "10.0.0.1"}, "needs both"},
		{"range start unparsable", "apps", "lb", "metallb-system", Source{PoolKind: "Range", PoolName: "r", Start: "10.0.0.x", End: "10.0.0.9"}, "range start"},
		{"range end unparsable", "apps", "lb", "metallb-system", Source{PoolKind: "Range", PoolName: "r", Start: "10.0.0.1", End: "nope"}, "range end"},
		{"range start after end", "apps", "lb", "metallb-system", Source{PoolKind: "Range", PoolName: "r", Start: "10.0.0.9", End: "10.0.0.1"}, "after its end"},
		{"ipv6 range start after end", "apps", "lb", "metallb-system", Source{PoolKind: "Range", PoolName: "r", Start: "2001:db8::9", End: "2001:db8::1"}, "after its end"},
		{"range mixes families", "apps", "lb", "metallb-system", Source{PoolKind: "Range", PoolName: "r", Start: "10.0.0.1", End: "2001:db8::1"}, "mixes IPv4 and IPv6"},
		{"range with an ipv6 zone", "apps", "lb", "metallb-system", Source{PoolKind: "Range", PoolName: "r", Start: "fe80::1%eth0", End: "fe80::9"}, "zones"},
		{"range with ipv4-mapped addresses", "apps", "lb", "metallb-system", Source{PoolKind: "Range", PoolName: "r", Start: "::ffff:10.0.0.1", End: "::ffff:10.0.0.9"}, "IPv4-mapped"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Desired(tc.policyNamespace, tc.policy, tc.mirrorNamespace, tc.src)
			if err == nil {
				t.Fatalf("Desired = %v, want an error containing %q", got.Object, tc.wantErr)
			}
			if got != nil {
				t.Errorf("Desired returned an object alongside the error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// -----------------------------------------------------------------------------------
// Selector, OwnedBy, Stale
// -----------------------------------------------------------------------------------

// testPool returns a mirrored-looking pool with exactly the given labels.
func testPool(name string, labelValues map[string]string) unstructured.Unstructured {
	pool := unstructured.Unstructured{Object: map[string]interface{}{}}
	pool.SetGroupVersionKind(GVK)
	pool.SetNamespace("metallb-system")
	pool.SetName(name)
	if labelValues != nil {
		pool.SetLabels(labelValues)
	}
	return pool
}

func mirrorLabels(policyNamespace, policy string) map[string]string {
	return map[string]string{
		LabelPolicy:          policy,
		LabelPolicyNamespace: policyNamespace,
		LabelManagedBy:       ManagedBy,
	}
}

func TestSelector(t *testing.T) {
	sel := Selector("apps", "lb")
	if got, want := sel.String(), "app.kubernetes.io/managed-by=uddi-operator,uddi.infoblox.com/policy=lb,uddi.infoblox.com/policy-namespace=apps"; got != want {
		t.Errorf("Selector string = %q, want %q", got, want)
	}

	cases := []struct {
		name        string
		labelValues map[string]string
		want        bool
	}{
		{"this policy's pool", mirrorLabels("apps", "lb"), true},
		{"this policy's pool with foreign labels", func() map[string]string {
			l := mirrorLabels("apps", "lb")
			l["team"] = "a"
			return l
		}(), true},
		{"same name, other namespace", mirrorLabels("infra", "lb"), false},
		{"same namespace, other policy", mirrorLabels("apps", "other"), false},
		{"policy labels without managed-by", map[string]string{LabelPolicy: "lb", LabelPolicyNamespace: "apps"}, false},
		{"managed-by of someone else", map[string]string{LabelPolicy: "lb", LabelPolicyNamespace: "apps", LabelManagedBy: "metallb-operator"}, false},
		{"no labels", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sel.Matches(labels.Set(tc.labelValues)); got != tc.want {
				t.Errorf("Matches = %v, want %v", got, tc.want)
			}
			pool := testPool("p", tc.labelValues)
			if got := OwnedBy(&pool, "apps", "lb"); got != tc.want {
				t.Errorf("OwnedBy = %v, want %v", got, tc.want)
			}
		})
	}

	if OwnedBy(nil, "apps", "lb") {
		t.Errorf("OwnedBy(nil) = true")
	}
}

func TestStale(t *testing.T) {
	existing := []unstructured.Unstructured{
		testPool("uddi-apps-lb-subnet-keep", mirrorLabels("apps", "lb")),
		testPool("uddi-apps-lb-subnet-b-gone", mirrorLabels("apps", "lb")),
		testPool("uddi-apps-lb-range-a-gone", mirrorLabels("apps", "lb")),
		// Another policy's pools: same names are not stale for this policy, whatever the
		// desired set says.
		testPool("uddi-apps-other-subnet-gone", mirrorLabels("apps", "other")),
		testPool("uddi-infra-lb-subnet-gone", mirrorLabels("infra", "lb")),
		// Pools a user created, or that lost their labels.
		testPool("user-pool", nil),
		testPool("user-pool-with-labels", map[string]string{"team": "a"}),
		testPool("half-labelled", map[string]string{LabelPolicy: "lb", LabelPolicyNamespace: "apps"}),
	}
	desired := map[string]bool{"uddi-apps-lb-subnet-keep": true, "not-existing-yet": true}

	got := Stale("apps", "lb", existing, desired)
	want := []string{"uddi-apps-lb-range-a-gone", "uddi-apps-lb-subnet-b-gone"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Stale = %v, want %v (sorted)", got, want)
	}

	t.Run("everything desired", func(t *testing.T) {
		all := map[string]bool{}
		for i := range existing {
			all[existing[i].GetName()] = true
		}
		if got := Stale("apps", "lb", existing, all); len(got) != 0 {
			t.Errorf("Stale = %v, want none", got)
		}
	})
	t.Run("a false entry is not desired", func(t *testing.T) {
		got := Stale("apps", "lb", existing, map[string]bool{"uddi-apps-lb-subnet-keep": false})
		if len(got) != 3 {
			t.Errorf("Stale = %v, want all three of the policy's pools", got)
		}
	})
	t.Run("nil desired set prunes all of the policy's pools", func(t *testing.T) {
		got := Stale("apps", "lb", existing, nil)
		if len(got) != 3 {
			t.Errorf("Stale = %v, want all three of the policy's pools", got)
		}
	})
	t.Run("no existing pools", func(t *testing.T) {
		if got := Stale("apps", "lb", nil, desired); len(got) != 0 {
			t.Errorf("Stale = %v, want none", got)
		}
	})
	t.Run("the other policy sees only its own", func(t *testing.T) {
		got := Stale("apps", "other", existing, nil)
		if want := []string{"uddi-apps-other-subnet-gone"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Stale = %v, want %v", got, want)
		}
	})
}

// -----------------------------------------------------------------------------------
// Equal
// -----------------------------------------------------------------------------------

func TestEqual(t *testing.T) {
	newDesired := func(t *testing.T) *unstructured.Unstructured {
		t.Helper()
		d, err := Desired("apps", "lb", "metallb-system", Source{PoolKind: "Subnet", PoolName: "net", CIDR: "10.20.0.0/24"})
		if err != nil {
			t.Fatalf("Desired: %v", err)
		}
		return d
	}
	mustSet := func(t *testing.T, o *unstructured.Unstructured, value interface{}, fields ...string) {
		t.Helper()
		if err := unstructured.SetNestedField(o.Object, value, fields...); err != nil {
			t.Fatalf("SetNestedField %v: %v", fields, err)
		}
	}

	cases := []struct {
		name   string
		mutate func(t *testing.T, live *unstructured.Unstructured)
		want   bool
	}{
		{"identical", func(*testing.T, *unstructured.Unstructured) {}, true},
		{"foreign labels are ignored", func(_ *testing.T, live *unstructured.Unstructured) {
			l := live.GetLabels()
			l["team"] = "a"
			live.SetLabels(l)
		}, true},
		{"status is ignored", func(t *testing.T, live *unstructured.Unstructured) {
			mustSet(t, live, map[string]interface{}{"observed": "yes"}, "status")
		}, true},
		{"server metadata and defaulted spec fields are ignored", func(t *testing.T, live *unstructured.Unstructured) {
			live.SetResourceVersion("42")
			live.SetUID("abc")
			live.SetAnnotations(map[string]string{"x": "y"})
			mustSet(t, live, false, "spec", "avoidBuggyIPs")
			mustSet(t, live, int64(1), "spec", "serviceAllocation", "priority")
			mustSet(t, live, []interface{}{map[string]interface{}{}}, "spec", "serviceAllocation", "serviceSelectors")
		}, true},
		{"serviceAllocation.namespaces widened", func(t *testing.T, live *unstructured.Unstructured) {
			mustSet(t, live, []interface{}{"apps", "other"}, "spec", "serviceAllocation", "namespaces")
		}, false},
		{"serviceAllocation.namespaces of another namespace", func(t *testing.T, live *unstructured.Unstructured) {
			mustSet(t, live, []interface{}{"other"}, "spec", "serviceAllocation", "namespaces")
		}, false},
		{"serviceAllocation absent", func(t *testing.T, live *unstructured.Unstructured) {
			unstructured.RemoveNestedField(live.Object, "spec", "serviceAllocation")
		}, false},
		{"different cidr", func(t *testing.T, live *unstructured.Unstructured) {
			mustSet(t, live, []interface{}{"10.30.0.0/24"}, "spec", "addresses")
		}, false},
		{"extra address", func(t *testing.T, live *unstructured.Unstructured) {
			mustSet(t, live, []interface{}{"10.20.0.0/24", "10.30.0.0/24"}, "spec", "addresses")
		}, false},
		{"no addresses", func(t *testing.T, live *unstructured.Unstructured) {
			unstructured.RemoveNestedField(live.Object, "spec", "addresses")
		}, false},
		{"addresses of the wrong type", func(t *testing.T, live *unstructured.Unstructured) {
			mustSet(t, live, "10.20.0.0/24", "spec", "addresses")
		}, false},
		{"autoAssign true", func(t *testing.T, live *unstructured.Unstructured) {
			mustSet(t, live, true, "spec", "autoAssign")
		}, false},
		{"autoAssign absent", func(t *testing.T, live *unstructured.Unstructured) {
			unstructured.RemoveNestedField(live.Object, "spec", "autoAssign")
		}, false},
		{"autoAssign of the wrong type", func(t *testing.T, live *unstructured.Unstructured) {
			mustSet(t, live, "false", "spec", "autoAssign")
		}, false},
		{"policy label changed", func(_ *testing.T, live *unstructured.Unstructured) {
			l := live.GetLabels()
			l[LabelPolicy] = "other"
			live.SetLabels(l)
		}, false},
		{"policy namespace label changed", func(_ *testing.T, live *unstructured.Unstructured) {
			l := live.GetLabels()
			l[LabelPolicyNamespace] = "other"
			live.SetLabels(l)
		}, false},
		{"managed-by label missing", func(_ *testing.T, live *unstructured.Unstructured) {
			l := live.GetLabels()
			delete(l, LabelManagedBy)
			live.SetLabels(l)
		}, false},
		{"no labels at all", func(_ *testing.T, live *unstructured.Unstructured) {
			live.SetLabels(nil)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desired := newDesired(t)
			live := newDesired(t)
			tc.mutate(t, live)
			if got := Equal(desired, live); got != tc.want {
				t.Errorf("Equal(desired, live) = %v, want %v", got, tc.want)
			}
			if got := Equal(live, desired); got != tc.want {
				t.Errorf("Equal(live, desired) = %v, want %v (not symmetric)", got, tc.want)
			}
		})
	}

	t.Run("nil", func(t *testing.T) {
		d := newDesired(t)
		if !Equal(nil, nil) {
			t.Errorf("Equal(nil, nil) = false")
		}
		if Equal(d, nil) || Equal(nil, d) {
			t.Errorf("Equal with one nil side = true")
		}
	})
}

// hashPrefix is the independent reference for the truncation suffix: the first 10 hex
// digits of the sha256 of the full name.
func hashPrefix(full string) string {
	sum := sha256.Sum256([]byte(full))
	return fmt.Sprintf("%x", sum)[:10]
}
