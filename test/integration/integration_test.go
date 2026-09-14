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

// Package integration exercises internal/uddi.NewSDKClient directly against a real
// Universal DDI tenant. It never touches Kubernetes: there is no envtest, no manager, no
// CRDs, just the Universal DDI client layer under test/integration. Its purpose is to
// answer the questions in docs/uddi-api-notes.md marked **verify** or "undocumented"
// that only a real tenant can settle (DESIGN.md section 14, "Open questions").
//
// The whole suite is gated on UDDI_API_KEY: every test calls requireAPIKey first, so
// `go test -tags integration ./test/integration/...` reports SKIP, not a failure, when
// the key is not set. UDDI_CSP_URL is optional and defaults to the SDK's own default
// (https://csp.infoblox.com).
//
// Every object this suite creates gets a run-scoped name prefix ("uddi-op-it-<8 hex>")
// and carries the tags uddi_operator_test_run=<that id> and uddi_operator_managed=true.
// TestMain deletes leftovers from earlier runs before the tests run, and this run's own
// objects afterwards; every test also registers a t.Cleanup as a per-object backstop.
// Nothing lacking the run tag is ever touched. See docs/testing.md for how to run this
// suite, what it creates in a tenant, and how to read its "VERDICT:" log lines.
package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

const (
	// envAPIKey names the environment variable that gates the whole suite.
	envAPIKey = "UDDI_API_KEY"
	// envCSPURL optionally overrides the Cloud Services Portal URL; empty means the SDK
	// default.
	envCSPURL = "UDDI_CSP_URL"

	// tagRunKey and tagManagedKey are the tags every object this suite creates carries.
	// They deliberately mirror internal/tags.KeyUID / KeyManaged in spirit, but this
	// package does not import internal/tags: it is exercising the Universal DDI client
	// layer in isolation from the rest of the operator.
	tagRunKey     = "uddi_operator_test_run"
	tagManagedKey = "uddi_operator_managed"
	tagManagedVal = "true"

	// namePrefix is the fixed part of every object name this suite creates.
	namePrefix = "uddi-op-it-"

	// requestTimeout bounds every individual API call this suite makes.
	requestTimeout = 30 * time.Second

	// skipMessage explains why a test skipped when UDDI_API_KEY is not set.
	skipMessage = "set UDDI_API_KEY (and optionally UDDI_CSP_URL) to run the Universal DDI integration suite against a sandbox tenant; see docs/testing.md"
)

// runID is this run's 8 hex character identifier, generated once in TestMain. prefix is
// namePrefix+runID. client is shared read-only across every test after TestMain builds
// it; the Universal DDI client has no per-request state of its own to race on.
var (
	runID  string
	prefix string
	client *uddi.Client
)

// TestMain gates the whole suite on UDDI_API_KEY, cleans up leftovers from earlier runs,
// runs the tests, then cleans up after this run.
//
// When UDDI_API_KEY is unset, TestMain still calls m.Run(): every test's first line is
// requireAPIKey(t), so each one reports "--- SKIP" individually with its reason. `go
// test` only prints those lines (and any t.Log output) with -v or on failure -- without
// -v a fully-skipped run just prints "ok" -- which is why `make test-integration` always
// passes -v (see the Makefile and docs/testing.md).
func TestMain(m *testing.M) {
	if os.Getenv(envAPIKey) == "" {
		os.Exit(m.Run())
	}

	runID = newRunID()
	prefix = namePrefix + runID

	c, err := uddi.NewSDKClient(uddi.Config{
		CSPURL:          os.Getenv(envCSPURL),
		APIKey:          os.Getenv(envAPIKey),
		ClientName:      "uddi-operator-integration-test",
		ConnectionLabel: "integration-test",
		RateLimitRPS:    5,
		RateLimitBurst:  5,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration: building the Universal DDI client:", err)
		os.Exit(1)
	}
	client = c

	fmt.Printf("integration: run id %s, object name prefix %q\n", runID, prefix)

	func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		// A previous run's id is not known in advance, so leftovers are found by the
		// managed tag alone; requireRunTag=true then requires uddi_operator_test_run to
		// actually be present client-side before anything is deleted, which is what
		// keeps this pass from ever touching a hand-created object that merely happens
		// to carry uddi_operator_managed=true. See cleanupMatching's doc comment.
		if err := cleanupMatching(ctx, client, uddi.TagFilter(map[string]string{tagManagedKey: tagManagedVal}), true); err != nil {
			fmt.Fprintln(os.Stderr, "integration: cleaning up leftovers from earlier runs:", err)
		}
	}()

	code := m.Run()

	func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := cleanupMatching(ctx, client, uddi.TagFilter(map[string]string{tagRunKey: runID}), false); err != nil {
			fmt.Fprintln(os.Stderr, "integration: cleaning up this run:", err)
		}
	}()

	os.Exit(code)
}

// requireAPIKey skips t when the suite is not configured against a real tenant. Every
// test calls this before doing anything else.
func requireAPIKey(t *testing.T) {
	t.Helper()
	if os.Getenv(envAPIKey) == "" {
		t.Skip(skipMessage)
	}
}

// newCtx returns a context bounded by requestTimeout for the body of a test, and
// arranges for it to be canceled when t finishes. Cleanup callbacks (registerDelete)
// use their own independent context (cleanupCtx) rather than this one, so a delete still
// runs with a fresh timeout even if the test body ran long.
func newCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	t.Cleanup(cancel)
	return ctx
}

// cleanupCtx returns a fresh, independent context for use inside a t.Cleanup callback
// that must not depend on the test's own (possibly already-expired) context.
func cleanupCtx() (context.Context, func()) {
	return context.WithTimeout(context.Background(), requestTimeout)
}

// newRunID returns 8 random hex characters, used both as the object name suffix and as
// the tagRunKey value.
func newRunID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read failing on any real OS is essentially unheard of; a
		// timestamp-based fallback still gives a run id unique enough for this purpose.
		return fmt.Sprintf("%08x", uint32(time.Now().UnixNano()))
	}
	return hex.EncodeToString(b)
}

// objName builds a run-scoped object name from parts, for example objName("view", "1").
func objName(parts ...string) string {
	out := prefix
	for _, p := range parts {
		out += "-" + p
	}
	return out
}

// testTags merges the run's ownership tags with any test-specific extras and converts
// the result to the map[string]interface{} shape the SDK models expect.
func testTags(extra map[string]string) map[string]interface{} {
	out := map[string]string{
		tagRunKey:     runID,
		tagManagedKey: tagManagedVal,
	}
	for k, v := range extra {
		out[k] = v
	}
	return uddi.ToAPITags(out)
}

// strv dereferences a *string, returning "" for nil.
func strv(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// hasTag reports whether tags carries key with a non-empty string value.
func hasTag(tags map[string]interface{}, key string) bool {
	v, ok := tags[key]
	if !ok {
		return false
	}
	s, ok := v.(string)
	return ok && s != ""
}

// containsID reports whether id appears among items, using idOf to extract each item's
// id.
func containsID[T any](items []T, id string, idOf func(T) string) bool {
	for _, it := range items {
		if idOf(it) == id {
			return true
		}
	}
	return false
}

// ptr returns a pointer to a copy of v, for building the *T fields the SDK models use.
func ptr[T any](v T) *T { return &v }

// registerDelete arranges for del to run when t finishes, logging (but not failing the
// test on) anything other than a 404: an object already gone is not a cleanup failure.
func registerDelete(t *testing.T, kind, id string, del func(ctx context.Context, id string) error) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := cleanupCtx()
		defer cancel()
		if err := del(ctx, id); err != nil && !uddi.IsNotFound(err) {
			t.Errorf("cleanup: delete %s %s: %v", kind, id, err)
		}
	})
}
