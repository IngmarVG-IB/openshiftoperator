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
	"os"
	"testing"

	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// TestCredentialsValidKey answers the first half of docs/uddi-api-notes.md's credential
// check: a limit-1 View list with the configured key succeeds.
func TestCredentialsValidKey(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	if _, err := client.DNSConfig.ListViews(ctx, uddi.ListOptions{Limit: 1}); err != nil {
		t.Fatalf("VERDICT: a valid API key was rejected listing Views: %v", err)
	}
	t.Log("VERDICT: a valid API key can list Views (limit 1) successfully")
}

// TestCredentialsInvalidKey answers the second half: an invalid key is rejected with
// 401/403, recognised by uddi.IsUnauthorized.
func TestCredentialsInvalidKey(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	bad, err := uddi.NewSDKClient(uddi.Config{
		CSPURL:     os.Getenv(envCSPURL),
		APIKey:     "invalid-" + runID,
		ClientName: "uddi-operator-integration-test",
	})
	if err != nil {
		t.Fatalf("building a client with an invalid key: %v", err)
	}

	_, err = bad.DNSConfig.ListViews(ctx, uddi.ListOptions{Limit: 1})
	if err == nil {
		t.Fatal("VERDICT: an invalid API key was accepted listing Views; expected 401/403")
	}
	if !uddi.IsUnauthorized(err) {
		t.Fatalf("VERDICT: an invalid API key failed with a status uddi.IsUnauthorized does not recognise: %v", err)
	}
	t.Logf("VERDICT: an invalid API key is rejected and recognised by uddi.IsUnauthorized (%v)", err)
}
