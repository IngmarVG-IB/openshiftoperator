# Testing

`uddi-operator` has four test lanes (DESIGN.md section 13). This document covers how to
run each one, what the real-tenant integration suite does to a Universal DDI tenant, and
how to fold its findings back into [docs/uddi-api-notes.md](uddi-api-notes.md).

| Level | Tooling | What it covers | Touches a real tenant? |
| --- | --- | --- | --- |
| Unit | `go test` | Adapters, diff logic, tag merging, natural-key lookups against `internal/uddi.Fake` | No |
| envtest (default + floor) | controller-runtime envtest, Ginkgo v2 / Gomega | Every controller end to end against the fake client: adoption, conflict, deletion, dependency ordering | No |
| Integration | `go test -tags integration` | Real Universal DDI API behaviour per kind, answering the open questions in docs/uddi-api-notes.md | **Yes** |
| e2e | kind, later OpenShift | Operator deployed from the bundle, sample CRs applied, status asserted | No (talks to the fake/a test cluster, not a real tenant) |

All `make` targets require the Linux toolchain (operator-sdk, kubebuilder's controller-gen,
setup-envtest, etc.), so on Windows run them inside WSL, same as every other `make`
workflow in this repo (DESIGN.md section 11):

```bash
wsl -e bash -lc 'export PATH=/home/ingmar/goroot/go/bin:/home/ingmar/go/bin:$PATH; cd /path/to/openshiftoperator && make <target>'
```

The rest of this document just gives the `make <target>` (or `go test ...`) command; run
it inside that wrapper.

## Unit tests

```bash
go test ./internal/... ./api/... ./cmd/...
```

Or everything envtest doesn't own, via the `test` target below, which is really "unit
plus envtest" in one invocation because they share `go test`'s package list.

## envtest: default and floor

```bash
make test        # latest envtest assets for the k8s.io version this module pins
make test-floor   # envtest 1.29.x -- the Kubernetes floor OpenShift 4.16 ships
```

Both targets download the required `kubebuilder-tools` (envtest binaries) into `bin/` on
first run via `setup-envtest`, run `go test` over every package except `test/e2e`, and
write a coverage profile (`cover.out` / `cover-floor.out`). Neither one touches a real
Universal DDI tenant: every controller test runs against `internal/uddi.Fake`.

## Integration: a real Universal DDI tenant

**Use a sandbox tenant. Never point this suite at a production or shared tenant.** It
creates and deletes real objects, and while it is careful about what it touches (see
below), a mistake in a future change to this suite, or a partially-failed cleanup, could
leave stray objects or, in the worst case, interact with objects it shouldn't. Sandbox
tenants exist for exactly this kind of testing.

### Running it

```bash
export UDDI_API_KEY=<a sandbox tenant's API key>
export UDDI_CSP_URL=https://csp.infoblox.com   # optional; this is already the SDK default
make test-integration
```

`make test-integration` runs `go test -tags integration ./test/integration/... -v
-count=1 -timeout 20m`. `-v` matters: without it, `go test` only prints "ok" for a
passing (or fully-skipped) run and swallows every `t.Log`/`t.Skip` message, which is
where this suite's findings live (see "Reading VERDICT lines" below).

Without `UDDI_API_KEY` set, every test in the package calls `t.Skip` immediately and no
Universal DDI API call is ever made:

```bash
go test -tags integration ./test/integration/...        # -> ok, 0 failures, nothing skipped is shown
go test -tags integration ./test/integration/... -v      # -> "--- SKIP: TestXxx" for every test, with the reason
```

`go vet -tags integration ./test/integration/...` (the `vet-integration` target) type-checks
the suite without running anything and needs no key.

### What it creates, and how it cleans up

Every object the suite creates gets:

- a name prefixed with `uddi-op-it-<8 hex characters>` (one prefix per test run), and
- the tags `uddi_operator_test_run=<that same run id>` and `uddi_operator_managed=true`.

The suite creates and deletes Views, AuthZones, Records, IPSpaces, AddressBlocks,
Subnets, Ranges, Addresses and FixedAddresses, always children before parents (records
before their zone, zones before their view, addresses/ranges/subnets/fixed addresses
before their IP space). Nothing without both tags is ever touched.

Cleanup happens three times, so a single crashed run cannot leak objects indefinitely:

1. **Before the tests run** (`TestMain`), leftovers from any *earlier* run are deleted.
   Since an earlier run's id isn't known in advance, this pass lists by
   `uddi_operator_managed=='true'` and then additionally requires the
   `uddi_operator_test_run` tag to actually be present (checked client-side) before
   deleting anything -- so a hand-created object that happens to carry
   `uddi_operator_managed=true` for unrelated reasons is never touched.
2. **After the tests run** (`TestMain` again), this run's own objects are deleted by
   `uddi_operator_test_run=='<this run's id>'`.
3. **Per test**, every object also gets a `t.Cleanup` that deletes it, so a single test
   failure doesn't depend on the whole binary reaching the end to clean up after itself.

If a run is killed hard enough that none of this fires (a machine losing power mid-run,
say), the next run's leftover-cleanup pass (step 1) picks up the pieces.

### What it answers

Each test logs one or more `VERDICT:`-prefixed lines stating what it found. The suite
covers:

1. Credential check: a valid key lists Views; an invalid key fails with 401/403.
2. Per kind (View, AuthZone, Record, IPSpace, AddressBlock, Subnet, Range, Address,
   FixedAddress): create with tags, read back, the natural-key filter the matching
   controller adapter's `Find` builds, the run's `_tfilter`, a comment patch, delete.
3. Next-available allocation: two sequential `.../nextavailableip` and
   `.../nextavailablesubnet` creates succeed, return distinct results, and carry the tags
   sent in the same request body.
4. Duplicate create: a second IPSpace with the same name, and a second identical A
   record -- what status code and error body shape result (docs/uddi-api-notes.md notes
   this is undocumented outside Threat Defense objects).
5. Recycle bin: after deleting an IPSpace or a View, does a natural-key `List` still
   return it, and does re-creating the same name succeed immediately?
6. Tag filter grammar: the single-quoted `'key'=='value'` `_tfilter` form, and whether a
   double-quoted form also works.
7. `Get` on a deleted id returns 404.

It also incidentally exercises a handful of client-layer behaviours worth knowing about
regardless of their outcome: `dnsconfig.View.Name` and `ipam.IPSpace.Name` (and
`ipam.Address.Address`, `ipam.FixedAddress.Address`/`MatchType`/`MatchValue`,
`ipam.Range.Start`/`End`) are required, non-pointer fields with no `omitempty` guard in
the generated client, so every patch must carry the current value forward or risk
renaming/corrupting the object on an unrelated field's update -- every production adapter
already does this, and `test/integration`'s own patch tests mirror it. `record_test.go`
specifically checks whether `dnsdata.Record`'s equivalent gap around `Rdata` (which
`recordAdapter.Diff` does *not* carry forward on a comment-only patch) actually corrupts
a record's `rdata` against a real tenant.

### Reading `VERDICT:` lines and updating docs/uddi-api-notes.md

```bash
make test-integration 2>&1 | tee integration-test.log
grep 'VERDICT:' integration-test.log
```

Each line is self-contained: which behaviour it checked and what the tenant actually did.
Cross-reference it against the item in
[docs/uddi-api-notes.md](uddi-api-notes.md) marked **verify** or "undocumented" (mostly
under "List, filter, pagination", "Errors, rate limiting, retries", and DESIGN.md section
14's numbered open questions), and:

- if the verdict confirms the documented assumption, remove the **verify** marker and
  note "confirmed against a real tenant on `<date>`";
- if it contradicts the documented assumption, update the assumption itself and flag
  anywhere in the codebase that relied on the old one (in particular
  `internal/controller`, which is out of this suite's scope to fix);
- if it answers one of DESIGN.md section 14's open questions, move it from "Open
  questions" to "Resolved" with a one-line summary, the same way the existing "Resolved"
  paragraph there reads.

The `.github/workflows/integration.yml` workflow (manual `workflow_dispatch` only --
this suite must never run on every push or PR) uploads its full log as a build artifact,
so the same `grep 'VERDICT:'` works there without re-running anything locally.

## e2e

```bash
make test-e2e
```

Spins up a local `kind` cluster, builds and loads the manager image, applies sample CRs
and asserts on status. Talks to a real Kubernetes API server but not to a real Universal
DDI tenant.
