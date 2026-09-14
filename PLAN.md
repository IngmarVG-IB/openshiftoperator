# uddi-operator implementation plan

Companion to [DESIGN.md](DESIGN.md). Milestones are ordered by dependency; each one ends
with `make manifests generate lint test` green and a tagged commit.

## Milestone 0: bootstrap (this session)

- [x] git init, private repo `IngmarVG-IB/openshiftoperator`, LICENSE, README, .gitignore
- [x] DESIGN.md, PLAN.md
- [x] Toolchain in WSL (Ubuntu 20.04, Go 1.26.5 at `/home/ingmar/goroot/go/bin/go`, not on the
      login-shell PATH). Install into `~/go/bin` (no sudo needed):
      - operator-sdk v1.42.3 from GitHub releases (`operator-sdk_linux_amd64`)
      - opm v1.74.0 from operator-registry releases (`linux-amd64-opm`)
      - kind via `go install sigs.k8s.io/kind@latest`
      - controller-gen, kustomize, setup-envtest are fetched by the generated Makefile into `./bin`
      - Docker Desktop WSL integration must be enabled for image builds and kind (not needed for
        `make build test`)
- [x] `docs/uddi-api-notes.md` from the SDK research (client, auth, packages, per-kind
      fields, next-available flows, tags, filtering, pagination, errors)

## Milestone 1: object layer

Delivered by subagents in two waves.

### Wave 1 (one agent, sequential): scaffold and framework

- [x] 1. `operator-sdk init --domain infoblox.com --repo github.com/IngmarVG-IB/openshiftoperator --project-name uddi-operator --plugins go/v4`
- [x] 2. `operator-sdk create api --group uddi --version v1alpha1 --kind <K> --resource --controller`
      for Connection, View, AuthZone, Record, IPSpace, AddressBlock, Subnet, Range, Address,
      FixedAddress. (`Range` collides with the Go keyword in the scaffolded controller-test
      template, so its controller, `PROJECT` entry and `cmd/main.go` registration were written
      by hand; everything else the scaffolder produced for it is intact.)
- [x] 3. `api/v1alpha1/common_types.go`: ConnectionRef, ObjectRef, Lifecycle, DeletionPolicy,
      ObjectStatus, DHCPOption, condition helpers. Every kind's Spec / Status as in DESIGN.md
      section 4, with kubebuilder validation markers and CEL immutability rules.
- [x] 4. `internal/uddi`: per-domain interfaces, in-memory fake with natural-key uniqueness,
      real adapter over the Infoblox Go client (construction, auth, tag filter, error
      wrapping, keyed mutex for next-available creates).
- [x] 5. `internal/connection`: Secret resolution, client cache keyed by Secret
      resourceVersion plus a spec hash, Connection controller with verification call and
      `Ready` condition.
- [x] 6. `internal/sync`: the generic reconcile loop from DESIGN.md section 5 with the
      Adapter interface, finalizer handling, ownership tags, adoption, deletion policies.
- [x] 7. `internal/clusterid`, `internal/tags`.
- [x] 8. envtest suite for Connection and for the generic loop, driven through the real View
      adapter rather than a dummy one.
- [x] 9. Makefile works under WSL; `make manifests generate fmt vet build test` green.

Wave 1 also lands the View kind end to end (adapter, controller, field indexes, sample) as
the reference the wave 2 kinds copy. The other eight controllers are registered and
reconcile to a no-op; each carries a `WAVE 2 PLACEHOLDER` comment naming what is missing.

### Wave 2 (four agents in parallel): kinds, CI, docs

All four work packages are delivered: every kind has an adapter, a controller with its
Connection and dependency field indexes, samples and an envtest suite against the fake.

| Agent | Work package |
| --- | --- |
| DNS | Adapters and controllers for View, AuthZone, Record; envtest suites; samples in `config/samples`. |
| IPAM | Adapters and controllers for IPSpace, AddressBlock, Subnet, Range, Address including next-available flows; envtest suites; samples. |
| DHCP | Adapter and controller for FixedAddress; shared dhcpOptions mapping used by Subnet and Range; envtest suite; samples. |
| Delivery | GitHub Actions `ci.yml` and `release.yml`, Dockerfile on UBI 9, `make bundle` metadata (CSV description, icon, keywords, install modes, minKubeVersion, OpenShift versions annotation), FBC catalog template, README install and usage docs. |

Each agent works on its own files, adds tests against the fake client, and leaves
`make test` green.

### Wave 3 (two agents in parallel): integration and review

Review and correctness pass:

- [x] CEL on the Kubernetes 1.29 floor: the seven `optionalOldSelf` rules compiled to
      `oldSelf.orValue(self)`, which needs Kubernetes 1.30, so the CRDs would not install
      on OpenShift 4.16. They are now spec-level rules that also reject adding or removing
      the field, and `make test-floor` runs the whole suite on envtest 1.29.5.
- [x] Unmanaged spec fields wired into Create and Diff: `IPSpace.dhcpOptions`,
      `Subnet.disableDhcp`, `Range.disableDhcp`, each with envtest coverage.
- [x] Deletion no longer wedges when the referenced Connection CR is gone: the object is
      orphaned with a `ConnectionMissing` Warning event and the finalizer is removed. A
      Connection that exists but is not Ready still blocks, because that is transient.
- [x] Merge review across the wave 2 work: consistent conditions, events, naming, RBAC
      markers, nil-safe `Observe`, and natural-key `_filter` values quoted through one
      escaping helper so a name containing a double quote cannot break the expression.
- [x] Manager-backed envtest: every controller's `SetupWithManager` against a real
      controller-runtime manager, proving the watches and dependency indexes requeue.
- [x] `make lint` clean (it was red before this pass) without weakening `.golangci.yml`.
- [x] `make bundle && operator-sdk bundle validate` clean, including
      `--select-optional suite=operatorframework` and `--select-optional name=operatorhub`.

Owned by the second wave 3 agent:

- API metrics, real-tenant `test/integration` gated by `UDDI_API_KEY` with a run-scoped
  prefix and cleanup, covering create / adopt / update / delete for every kind.
- kind e2e installing from the bundle.

### Known limitations

- **Tag keys removed from `spec.tags` are not removed in Universal DDI.** The tag merge
  is additive: the operator sets the ownership tags, the Connection's `defaultTags` and
  `spec.tags`, but a key that disappears from `spec.tags` is left on the object rather
  than deleted, so a stale tag has to be removed by hand.
- **Deleting a CR whose Connection CR is already gone orphans the Universal DDI object.**
  The finalizer is removed so the CR can go away, a `ConnectionMissing` Warning event
  says what happened, and the object stays behind with its ownership tags. Delete
  Connections after the objects that use them, or re-adopt with a new CR.
- **Unified nameservers are not modelled.** AuthZone carries the legacy `nsgs` /
  `internalSecondaries` / `externalPrimaries` fields only. The API's `nameservers` model
  exists only on the SDK's untagged master branch, so `spec.nameservers` waits for a
  tagged SDK (DESIGN.md open question 3).
- **Kubernetes 1.29 CEL constraints.** The OpenShift 4.16 floor rules out
  `optionalOldSelf` / `oldSelf.orValue()`, so every "optional field is immutable" rule is
  written at the spec level instead of on the field. New validation must stay inside the
  1.29 CEL feature set and `make test-floor` is what proves it.

## Milestone 2: DNS publishing

DNSPublishPolicy controller (DESIGN.md 4.7): source watches for Routes, Ingresses,
LoadBalancer Services; zone matching by longest suffix; Record CR generation with
ownerReferences and pruning; annotation overrides; status with skipped sources. Decide the
wildcard router record approach (open question 7).

## Milestone 3: IP allocation

IPAllocationPolicy controller: Address CR per selected Service, assignment via a
configurable annotation (MetalLB by default), release on Service deletion. Resolve the
MetalLB annotation key (open question 5).

## Milestone 4: host and DHCP registration

HostRegistrationPolicy: Node to FixedAddress / named Address, MAC discovery decision (open
question 4).

## Milestone 5: distribution

Publish to quay.io, community OperatorHub submission checklist, Red Hat certification
prerequisites (image labels, licenses directory, scorecard), docs site.
