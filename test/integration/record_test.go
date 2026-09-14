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
	"testing"

	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"
	"github.com/infobloxopen/universal-ddi-go-client/dnsdata"

	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// TestRecord exercises the Record lifecycle for a static A record: create under a fresh
// View and AuthZone, read back, the natural-key filter recordAdapter.Find builds (zone
// plus name_in_zone plus type, then a client-side rdata match), the run's tfilter, a
// comment patch and delete.
func TestRecord(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	view, err := client.DNSConfig.CreateView(ctx, dnsconfig.View{Name: objName("record-view"), Tags: testTags(nil)})
	if err != nil {
		t.Fatalf("CreateView (dependency): %v", err)
	}
	viewID := strv(view.Id)
	registerDelete(t, "view", viewID, client.DNSConfig.DeleteView)

	fqdn := canonicalFQDN(objName("record-zone") + ".example.com.")
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

	const nameInZone = "www"
	const recordType = "A"
	const address = "10.255.0.10"

	created, err := client.DNSData.CreateRecord(ctx, dnsdata.Record{
		Type:       ptr(recordType),
		Zone:       &zoneID,
		NameInZone: ptr(nameInZone),
		Rdata:      map[string]interface{}{"address": address},
		Tags:       testTags(nil),
	})
	if err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}
	id := strv(created.Id)
	registerDelete(t, "record", id, client.DNSData.DeleteRecord)

	got, err := client.DNSData.GetRecord(ctx, id)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if got.Rdata["address"] != address {
		t.Fatalf("GetRecord: rdata[address] = %v, want %q", got.Rdata["address"], address)
	}

	natFilter := `zone=="` + zoneID + `" and name_in_zone=="` + nameInZone + `" and type=="` + recordType + `"`
	byNat, err := client.DNSData.ListRecords(ctx, uddi.ListOptions{Filter: natFilter})
	if err != nil {
		t.Fatalf("ListRecords by natural key: %v", err)
	}
	if !containsID(byNat, id, func(r dnsdata.Record) string { return strv(r.Id) }) {
		t.Fatalf("ListRecords by natural key (%s): created record %s not among %d results", natFilter, id, len(byNat))
	}

	byTag, err := client.DNSData.ListRecords(ctx, uddi.ListOptions{TFilter: uddi.TagFilter(map[string]string{tagRunKey: runID}), Limit: 100})
	if err != nil {
		t.Fatalf("ListRecords by tfilter: %v", err)
	}
	if !containsID(byTag, id, func(r dnsdata.Record) string { return strv(r.Id) }) {
		t.Fatalf("ListRecords by tfilter: created record %s not among %d results", id, len(byTag))
	}

	// recordAdapter.Diff (internal/controller/record_adapter.go) only sets patch.Rdata
	// when the desired rdata actually differs from current, so a comment-only change
	// patches with Rdata left at its zero value (nil). Unlike every other kind's
	// identity field (View/IPSpace's Name, Address/FixedAddress's Address,
	// FixedAddress's MatchType/MatchValue, Range's Start/End -- all of which the
	// respective adapters carry forward on every patch, and all of which this suite's
	// own patch tests do the same for below), dnsdata.Record.Rdata has no omitempty
	// guard in the generated MarshalJSON (unlike every *string field on Record), so this
	// patch body genuinely sends "rdata": null. Send the same shape recordAdapter would
	// and see what a real tenant does with it.
	const comment = "patched by uddi-operator integration test"
	updated, err := client.DNSData.UpdateRecord(ctx, id, dnsdata.Record{Comment: ptr(comment)})
	if err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	if strv(updated.Comment) != comment {
		t.Fatalf("UpdateRecord: comment = %q, want %q", strv(updated.Comment), comment)
	}

	afterPatch, err := client.DNSData.GetRecord(ctx, id)
	if err != nil {
		t.Fatalf("GetRecord after patch: %v", err)
	}
	if afterPatch.Rdata["address"] == address {
		t.Log("VERDICT: a Record PATCH body with rdata omitted (sent as \"rdata\": null by " +
			"the generated client, since dnsdata.Record.Rdata has no omitempty guard) leaves " +
			"the existing rdata untouched")
	} else {
		t.Logf("VERDICT: a Record PATCH body with rdata omitted (sent as \"rdata\": null) "+
			"changed rdata from %v to %v -- record_adapter.go's Diff must always carry rdata "+
			"forward like it does zone/name_in_zone/type, or every comment-only patch will "+
			"corrupt the record", address, afterPatch.Rdata["address"])
	}

	if err := client.DNSData.DeleteRecord(ctx, id); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}
}
