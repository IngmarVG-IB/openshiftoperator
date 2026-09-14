//go:build integration

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

package integration

import (
	"strings"
	"testing"

	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"

	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// canonicalFQDN mirrors internal/controller/authzone_adapter.go's canonicalFQDN: lower
// case, exactly one trailing dot. This package does not import internal/controller (it
// is unexported there, and out of scope besides), so the tiny helper is duplicated here
// to build the same natural-key value the adapter would send.
func canonicalFQDN(fqdn string) string {
	f := strings.ToLower(strings.TrimSuffix(fqdn, "."))
	return f + "."
}

// TestAuthZone exercises the AuthZone lifecycle for a cloud-type zone with no
// nameserver configuration set (DESIGN.md section 14 open question 3 flags the
// nameserver fields as mid-migration; this test deliberately stays off that path):
// create under a fresh View, read back, the natural-key filter authzoneAdapter.Find
// builds (fqdn plus view), the run's tfilter, a comment patch and delete.
func TestAuthZone(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	view, err := client.DNSConfig.CreateView(ctx, dnsconfig.View{
		Name: objName("authzone-view"),
		Tags: testTags(nil),
	})
	if err != nil {
		t.Fatalf("CreateView (dependency): %v", err)
	}
	viewID := strv(view.Id)
	registerDelete(t, "view", viewID, client.DNSConfig.DeleteView)

	fqdn := canonicalFQDN(objName("zone", "1") + ".example.com.")
	created, err := client.DNSConfig.CreateAuthZone(ctx, dnsconfig.AuthZone{
		Fqdn:        &fqdn,
		PrimaryType: ptr("cloud"),
		View:        &viewID,
		Tags:        testTags(nil),
	})
	if err != nil {
		t.Fatalf("CreateAuthZone: %v", err)
	}
	id := strv(created.Id)
	registerDelete(t, "auth zone", id, client.DNSConfig.DeleteAuthZone)

	if strv(created.Fqdn) != fqdn {
		t.Fatalf("CreateAuthZone: fqdn = %q, want %q", strv(created.Fqdn), fqdn)
	}

	got, err := client.DNSConfig.GetAuthZone(ctx, id)
	if err != nil {
		t.Fatalf("GetAuthZone: %v", err)
	}
	if strv(got.PrimaryType) != "cloud" {
		t.Fatalf("GetAuthZone: primary_type = %q, want %q", strv(got.PrimaryType), "cloud")
	}

	natFilter := `fqdn=="` + fqdn + `" and view=="` + viewID + `"`
	byNat, err := client.DNSConfig.ListAuthZones(ctx, uddi.ListOptions{Filter: natFilter, Limit: 1})
	if err != nil {
		t.Fatalf("ListAuthZones by natural key: %v", err)
	}
	if !containsID(byNat, id, func(z dnsconfig.AuthZone) string { return strv(z.Id) }) {
		t.Fatalf("ListAuthZones by natural key (%s): created zone %s not among %d results", natFilter, id, len(byNat))
	}

	byTag, err := client.DNSConfig.ListAuthZones(ctx, uddi.ListOptions{TFilter: uddi.TagFilter(map[string]string{tagRunKey: runID}), Limit: 100})
	if err != nil {
		t.Fatalf("ListAuthZones by tfilter: %v", err)
	}
	if !containsID(byTag, id, func(z dnsconfig.AuthZone) string { return strv(z.Id) }) {
		t.Fatalf("ListAuthZones by tfilter: created zone %s not among %d results", id, len(byTag))
	}

	const comment = "patched by uddi-operator integration test"
	updated, err := client.DNSConfig.UpdateAuthZone(ctx, id, dnsconfig.AuthZone{Comment: ptr(comment)})
	if err != nil {
		t.Fatalf("UpdateAuthZone: %v", err)
	}
	if strv(updated.Comment) != comment {
		t.Fatalf("UpdateAuthZone: comment = %q, want %q", strv(updated.Comment), comment)
	}

	if err := client.DNSConfig.DeleteAuthZone(ctx, id); err != nil {
		t.Fatalf("DeleteAuthZone: %v", err)
	}
}
