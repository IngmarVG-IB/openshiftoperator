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
	"errors"
	"testing"

	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"
	"github.com/infobloxopen/universal-ddi-go-client/dnsdata"
	"github.com/infobloxopen/universal-ddi-go-client/ipam"

	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// recordFor builds an identical A record body for the given zone, used twice by
// TestDuplicateRecord to attempt a duplicate create.
func recordFor(zoneID string) dnsdata.Record {
	return dnsdata.Record{
		Type:       ptr("A"),
		Zone:       &zoneID,
		NameInZone: ptr("dup"),
		Rdata:      map[string]interface{}{"address": "10.65.0.10"},
		Tags:       testTags(nil),
	}
}

// logDuplicateOutcome records the status code and error body shape of a duplicate
// create attempt, without asserting a specific code: docs/uddi-api-notes.md notes that a
// 409 is documented only for Threat Defense objects, and IPAM/DNS may return a generic
// 400 or something else entirely.
func logDuplicateOutcome(t *testing.T, kind string, err error) {
	t.Helper()
	if err == nil {
		t.Logf("VERDICT: creating a duplicate %s succeeded (no error at all); the API allows more than one object with the same natural key", kind)
		return
	}
	var apiErr *uddi.APIError
	if errors.As(err, &apiErr) {
		t.Logf("VERDICT: creating a duplicate %s failed with HTTP %d, body: %s", kind, apiErr.StatusCode, apiErr.Body)
		return
	}
	t.Logf("VERDICT: creating a duplicate %s failed with a non-APIError error: %v", kind, err)
}

// TestDuplicateIPSpace answers docs/uddi-api-notes.md open question 7 for IPSpace:
// what happens, and what status/body shape results, when a second IPSpace is created
// with the same name.
func TestDuplicateIPSpace(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	spaceName := objName("dup-ipspace")
	first, err := client.IPAM.CreateIPSpace(ctx, ipam.IPSpace{Name: spaceName, Tags: testTags(nil)})
	if err != nil {
		t.Fatalf("CreateIPSpace (first): %v", err)
	}
	registerDelete(t, "ip space", strv(first.Id), client.IPAM.DeleteIPSpace)

	second, err := client.IPAM.CreateIPSpace(ctx, ipam.IPSpace{Name: spaceName, Tags: testTags(nil)})
	if err == nil {
		// The API allowed it: clean up the second object too so it doesn't leak.
		registerDelete(t, "ip space", strv(second.Id), client.IPAM.DeleteIPSpace)
	}
	logDuplicateOutcome(t, "IPSpace", err)
}

// TestDuplicateRecord answers the same question for an identical A record created
// twice in the same zone.
func TestDuplicateRecord(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	view, err := client.DNSConfig.CreateView(ctx, dnsconfig.View{Name: objName("dup-record-view"), Tags: testTags(nil)})
	if err != nil {
		t.Fatalf("CreateView (dependency): %v", err)
	}
	viewID := strv(view.Id)
	registerDelete(t, "view", viewID, client.DNSConfig.DeleteView)

	fqdn := canonicalFQDN(objName("dup-record-zone") + ".example.com.")
	zone, err := client.DNSConfig.CreateAuthZone(ctx, dnsconfig.AuthZone{
		Fqdn:        &fqdn,
		PrimaryType: ptr("cloud"),
		View:        &viewID,
		Tags:        testTags(nil),
	})
	if err != nil {
		t.Fatalf("CreateAuthZone (dependency): %v", err)
	}
	zoneID := strv(zone.Id)
	registerDelete(t, "auth zone", zoneID, client.DNSConfig.DeleteAuthZone)

	first, err := client.DNSData.CreateRecord(ctx, recordFor(zoneID))
	if err != nil {
		t.Fatalf("CreateRecord (first): %v", err)
	}
	registerDelete(t, "record", strv(first.Id), client.DNSData.DeleteRecord)

	second, err := client.DNSData.CreateRecord(ctx, recordFor(zoneID))
	if err == nil {
		registerDelete(t, "record", strv(second.Id), client.DNSData.DeleteRecord)
	}
	logDuplicateOutcome(t, "A record", err)
}
