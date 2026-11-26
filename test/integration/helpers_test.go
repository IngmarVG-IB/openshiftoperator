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
	"context"
	"strings"
	"testing"
	"time"

	"github.com/infobloxopen/universal-ddi-go-client/dnsdata"
	"github.com/infobloxopen/universal-ddi-go-client/ipam"
)

// zoneNotReadyMessage is what a real tenant answers (HTTP 400) when a Record is created
// in an AuthZone whose own create call has only just returned.
const zoneNotReadyMessage = "does not refer to a valid 'Zone' object"

// zoneReadyTimeout bounds how long createRecordWhenZoneReady waits for a new zone.
const zoneReadyTimeout = 90 * time.Second

// mustParentSubnet creates a /24 Subnet at address in the given IPSpace. A real tenant
// refuses an Address, Range or FixedAddress that no Subnet covers ("No parent subnet
// found for address ..."), so those tests need one first. Cleanup runs in reverse
// registration order, so the subnet outlives whatever the caller creates inside it.
func mustParentSubnet(ctx context.Context, t *testing.T, spaceID, address string) {
	t.Helper()
	subnet, err := client.IPAM.CreateSubnet(ctx, ipam.Subnet{
		Address: ptr(address),
		Cidr:    ptr(int64(24)),
		Space:   &spaceID,
		Tags:    testTags(nil),
	})
	if err != nil {
		t.Fatalf("CreateSubnet (dependency): %v", err)
	}
	registerDelete(t, "subnet", strv(subnet.Id), client.IPAM.DeleteSubnet)
}

// createRecordWhenZoneReady creates rec, retrying while the tenant still reports the
// record's freshly created zone as invalid, and logs how long the zone took to become
// usable. Any other error, or the timeout, is returned to the caller.
func createRecordWhenZoneReady(ctx context.Context, t *testing.T, rec dnsdata.Record) (*dnsdata.Record, error) {
	t.Helper()
	start := time.Now()
	for attempt := 1; ; attempt++ {
		created, err := client.DNSData.CreateRecord(ctx, rec)
		if err == nil {
			if attempt > 1 {
				t.Logf("VERDICT: a new AuthZone accepted its first Record only after %s (%d attempts); "+
					"until then CreateRecord fails with HTTP 400 %q",
					time.Since(start).Round(time.Second), attempt, zoneNotReadyMessage)
			} else {
				t.Logf("VERDICT: a new AuthZone accepted a Record on the first attempt")
			}
			return created, nil
		}
		if !strings.Contains(err.Error(), zoneNotReadyMessage) || time.Since(start) > zoneReadyTimeout {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}
