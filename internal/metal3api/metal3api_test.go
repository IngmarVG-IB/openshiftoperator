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

package metal3api

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

const (
	macA = "aa:bb:cc:00:00:01"
	macB = "aa:bb:cc:00:00:02"
	macC = "aa:bb:cc:00:00:03"
)

// nicEntry is one status.hardware.nics[] element.
func nicEntry(name, mac, ip string) interface{} {
	return map[string]interface{}{"name": name, "mac": mac, "ip": ip}
}

// bmh builds a BareMetalHost as unstructured data: bootMAC and hostname may be empty to
// leave the field out, nics is the status.hardware.nics list.
func bmh(name, bootMAC, hostname string, nics ...interface{}) unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": "metal3.io/v1alpha1",
		"kind":       "BareMetalHost",
		"metadata":   map[string]interface{}{"name": name, "namespace": "openshift-machine-api"},
		"spec":       map[string]interface{}{},
	}
	if bootMAC != "" {
		obj["spec"].(map[string]interface{})["bootMACAddress"] = bootMAC
	}
	hardware := map[string]interface{}{}
	if hostname != "" {
		hardware["hostname"] = hostname
	}
	if len(nics) > 0 {
		hardware["nics"] = nics
	}
	if len(hardware) > 0 {
		obj["status"] = map[string]interface{}{"hardware": hardware}
	}
	return unstructured.Unstructured{Object: obj}
}

func TestGVK(t *testing.T) {
	if got, want := GVK.String(), "metal3.io/v1alpha1, Kind=BareMetalHost"; got != want {
		t.Errorf("GVK = %q, want %q", got, want)
	}
	if got, want := ListGVK.Kind, "BareMetalHostList"; got != want {
		t.Errorf("ListGVK.Kind = %q, want %q", got, want)
	}
	if ListGVK.GroupVersion() != GVK.GroupVersion() {
		t.Errorf("ListGVK group version %v differs from GVK's %v", ListGVK.GroupVersion(), GVK.GroupVersion())
	}
}

// TestAvailable exercises Available's discovery-response handling against a fake
// apiserver. The envtest round trip in metal3api_envtest_test.go proves the "true" case
// against the real fixture CRD.
func TestAvailable(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "group version served with baremetalhosts resource",
			status: http.StatusOK,
			body: `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"metal3.io/v1alpha1","resources":[
				{"name":"baremetalhosts","singularName":"baremetalhost","namespaced":true,"kind":"BareMetalHost","verbs":["get","list","watch"]}
			]}`,
			want: true,
		},
		{
			name:   "group version served without a baremetalhosts resource",
			status: http.StatusOK,
			body: `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"metal3.io/v1alpha1","resources":[
				{"name":"hostfirmwaresettings","singularName":"hostfirmwaresettings","namespaced":true,"kind":"HostFirmwareSettings","verbs":["get"]}
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
			if err != nil {
				t.Fatalf("Available: unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("Available = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAvailableServerError(t *testing.T) {
	// A server that fails discovery in some other way is an error, not "no Metal3 here".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"InternalError","code":500}`))
	}))
	defer srv.Close()

	if _, err := Available(&rest.Config{Host: srv.URL}); err == nil {
		t.Fatalf("Available: want an error for a 500 from discovery, got nil")
	}
}

func TestAvailableBadConfig(t *testing.T) {
	cfg := &rest.Config{
		Host:            "https://127.0.0.1:0",
		TLSClientConfig: rest.TLSClientConfig{CertData: []byte("not-a-cert")},
	}
	if _, err := Available(cfg); err == nil {
		t.Fatalf("Available: want error for an invalid client config, got nil")
	}
}

func TestMACForNode(t *testing.T) {
	hosts := []unstructured.Unstructured{
		bmh("worker-0", macA, "worker-0.example.com",
			nicEntry("eno1", macB, "10.0.0.10"),
			nicEntry("eno2", macC, "10.20.0.10"),
		),
		bmh("worker-1", macC, "worker-1.example.com",
			nicEntry("eno1", "AA-BB-CC-00-00-0D", "10.0.0.11"),
		),
		bmh("worker-2", "", "worker-2.example.com"), // no bootMACAddress, no NICs
	}

	cases := []struct {
		name     string
		address  string
		node     string
		wantMAC  string
		wantHost string
		wantOK   bool
	}{
		{name: "NIC IP wins over hostname", address: "10.0.0.10", node: "worker-1.example.com", wantMAC: macB, wantHost: "worker-0", wantOK: true},
		{name: "NIC IP, MAC normalised", address: "10.0.0.11", node: "unknown", wantMAC: "aa:bb:cc:00:00:0d", wantHost: "worker-1", wantOK: true},
		{name: "no NIC IP match falls back to hostname bootMAC", address: "10.9.9.9", node: "worker-1.example.com", wantMAC: macC, wantHost: "worker-1", wantOK: true},
		{name: "empty address uses hostname", address: "", node: "worker-0.example.com", wantMAC: macA, wantHost: "worker-0", wantOK: true},
		{name: "hostname match is case and trailing dot insensitive", address: "", node: "Worker-0.Example.com.", wantMAC: macA, wantHost: "worker-0", wantOK: true},
		{name: "unparsable address uses hostname", address: "not-an-ip", node: "worker-0.example.com", wantMAC: macA, wantHost: "worker-0", wantOK: true},
		{name: "hostname match without a bootMAC is no answer", address: "10.9.9.9", node: "worker-2.example.com", wantOK: false},
		{name: "neither matches", address: "10.9.9.9", node: "nope", wantOK: false},
		{name: "nothing to look up", address: "", node: "", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mac, host, ok := MACForNode(hosts, tc.address, tc.node)
			if ok != tc.wantOK || mac != tc.wantMAC || host != tc.wantHost {
				t.Errorf("MACForNode(%q, %q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.address, tc.node, mac, host, ok, tc.wantMAC, tc.wantHost, tc.wantOK)
			}
		})
	}
}

func TestMACForNode_NoHosts(t *testing.T) {
	if mac, host, ok := MACForNode(nil, "10.0.0.10", "worker-0"); ok || mac != "" || host != "" {
		t.Errorf("MACForNode(nil) = (%q, %q, %v), want no answer", mac, host, ok)
	}
	var zero Index
	if _, _, ok := zero.MACForIP(netip.MustParseAddr("10.0.0.10")); ok {
		t.Errorf("the zero Index answered a lookup")
	}
}

func TestMACForNode_IPv6FormsMatch(t *testing.T) {
	hosts := []unstructured.Unstructured{
		bmh("worker-0", macA, "worker-0",
			nicEntry("eno1", macB, "2001:DB8:0:0::10"),
			nicEntry("eno2", macC, "fd00::1"),
		),
	}

	// The NIC reports 2001:DB8:0:0::10; the Node reports the compressed lower-case form.
	mac, host, ok := MACForNode(hosts, "2001:db8::10", "other")
	if !ok || mac != macB || host != "worker-0" {
		t.Errorf("MACForNode(2001:db8::10) = (%q, %q, %v), want (%q, worker-0, true)", mac, host, ok, macB)
	}
	mac, _, ok = MACForNode(hosts, "FD00:0:0:0:0:0:0:1", "other")
	if !ok || mac != macC {
		t.Errorf("MACForNode(FD00:0:0:0:0:0:0:1) = (%q, %v), want %q", mac, ok, macC)
	}
}

func TestMACForIP(t *testing.T) {
	hosts := []unstructured.Unstructured{
		// A dual-stack NIC is reported as two entries sharing a MAC.
		bmh("worker-0", macA, "worker-0",
			nicEntry("eno1", macB, "10.0.0.10"),
			nicEntry("eno1", macB, "2001:db8::10"),
			nicEntry("eno2", macC, "10.20.0.10"),
			nicEntry("eno3", "not-a-mac", "10.30.0.10"), // dropped
			nicEntry("eno4", macA, ""),                  // no IP: dropped
			nicEntry("eno5", macA, "garbage"),           // dropped
		),
		bmh("worker-1", macA, "worker-1", nicEntry("eno1", macA, "10.20.0.11")),
	}

	cases := []struct {
		ip       string
		wantMAC  string
		wantHost string
		wantOK   bool
	}{
		{"10.0.0.10", macB, "worker-0", true},
		{"2001:db8:0:0:0:0:0:10", macB, "worker-0", true},
		{"10.20.0.10", macC, "worker-0", true},
		{"10.20.0.11", macA, "worker-1", true},
		{"::ffff:10.20.0.11", macA, "worker-1", true}, // IPv4-mapped form of the same address
		{"10.30.0.10", "", "", false},                 // the NIC's MAC is invalid
		{"10.99.0.1", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			mac, host, ok := MACForIP(hosts, netip.MustParseAddr(tc.ip))
			if ok != tc.wantOK || mac != tc.wantMAC || host != tc.wantHost {
				t.Errorf("MACForIP(%s) = (%q, %q, %v), want (%q, %q, %v)", tc.ip, mac, host, ok, tc.wantMAC, tc.wantHost, tc.wantOK)
			}
		})
	}

	if _, _, ok := MACForIP(hosts, netip.Addr{}); ok {
		t.Errorf("MACForIP(zero Addr) answered, want no answer")
	}
}

func TestIndexOrderIsDeterministic(t *testing.T) {
	// Two hosts claim the same IP; the one sorting first by name wins regardless of the
	// order the list came back in.
	a := bmh("a-host", macA, "a", nicEntry("eno1", macA, "10.0.0.5"))
	b := bmh("b-host", macB, "b", nicEntry("eno1", macB, "10.0.0.5"))

	for _, hosts := range [][]unstructured.Unstructured{{a, b}, {b, a}} {
		mac, host, ok := MACForNode(hosts, "10.0.0.5", "")
		if !ok || mac != macA || host != "a-host" {
			t.Errorf("MACForNode = (%q, %q, %v), want (%q, a-host, true)", mac, host, ok, macA)
		}
	}
}

func TestReadHostToleratesMalformedObjects(t *testing.T) {
	hosts := []unstructured.Unstructured{
		{Object: map[string]interface{}{ // no spec, no status
			"apiVersion": "metal3.io/v1alpha1", "kind": "BareMetalHost",
			"metadata": map[string]interface{}{"name": "bare"},
		}},
		{Object: map[string]interface{}{ // wrong types all over
			"apiVersion": "metal3.io/v1alpha1", "kind": "BareMetalHost",
			"metadata": map[string]interface{}{"name": "weird"},
			"spec":     map[string]interface{}{"bootMACAddress": int64(5)},
			"status": map[string]interface{}{"hardware": map[string]interface{}{
				"hostname": []interface{}{"x"},
				"nics":     []interface{}{"not-a-map", map[string]interface{}{"mac": macA, "ip": int64(3)}},
			}},
		}},
		{Object: map[string]interface{}{ // an invalid bootMACAddress is ignored
			"apiVersion": "metal3.io/v1alpha1", "kind": "BareMetalHost",
			"metadata": map[string]interface{}{"name": "badboot"},
			"spec":     map[string]interface{}{"bootMACAddress": "zz:zz"},
			"status":   map[string]interface{}{"hardware": map[string]interface{}{"hostname": "badboot"}},
		}},
	}
	if mac, host, ok := MACForNode(hosts, "10.0.0.1", "badboot"); ok {
		t.Errorf("MACForNode = (%q, %q, true), want no answer from malformed hosts", mac, host)
	}
}
