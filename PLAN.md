# uddi-operator implementation plan

Companion to [DESIGN.md](DESIGN.md). Milestones are ordered by dependency; each one ends
with `make manifests generate lint test` green and a tagged commit.

## Milestone 0: bootstrap (this session)

- [x] git init, private repo `IngmarVG-IB/openshiftoperator`, LICENSE, README, .gitignore
- [x] DESIGN.md, PLAN.md
- [ ] Toolchain in WSL (Ubuntu 20.04, Go 1.26.5 at `/home/ingmar/goroot/go/bin/go`, not on the
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

1. `operator-sdk init --domain infoblox.com --repo github.com/IngmarVG-IB/openshiftoperator --project-name uddi-operator --plugins go/v4`
2. `operator-sdk create api --group uddi --version v1alpha1 --kind <K> --resource --controller`
   for Connection, View, AuthZone, Record, IPSpace, AddressBlock, Subnet, Range, Address,
   FixedAddress.
3. `api/v1alpha1/common_types.go`: ConnectionRef, ObjectRef, Lifecycle, DeletionPolicy,
   ObjectStatus, DHCPOption, condition helpers. Fill in every kind's Spec / Status exactly as
   in DESIGN.md section 4, with kubebuilder validation markers and CEL immutability rules.
4. `internal/uddi`: per-domain interfaces, in-memory fake with natural-key uniqueness, real
   adapter over the Infoblox Go client (construction, auth, tag filter, pagination, error
   wrapping).
5. `internal/connection`: Secret resolution, client cache keyed by Secret resourceVersion,
   Connection controller with verification call and `Ready` condition.
6. `internal/sync`: the generic reconcile loop from DESIGN.md section 5 with the Adapter
   interface, finalizer handling, ownership tags, adoption, deletion policies.
7. `internal/clusterid`, `internal/tags`.
8. envtest suite for Connection and for the generic loop using a dummy adapter.
9. Makefile works under WSL; `make manifests generate build test` green; commit.

### Wave 2 (four agents in parallel): kinds, CI, docs

| Agent | Work package |
| --- | --- |
| DNS | Adapters and controllers for View, AuthZone, Record; envtest suites; samples in `config/samples`. |
| IPAM | Adapters and controllers for IPSpace, AddressBlock, Subnet, Range, Address including next-available flows; envtest suites; samples. |
| DHCP | Adapter and controller for FixedAddress; shared dhcpOptions mapping used by Subnet and Range; envtest suite; samples. |
| Delivery | GitHub Actions `ci.yml` and `release.yml`, Dockerfile on UBI 9, `make bundle` metadata (CSV description, icon, keywords, install modes, minKubeVersion, OpenShift versions annotation), FBC catalog template, README install and usage docs. |

Each agent works on its own files, adds tests against the fake client, and leaves
`make test` green.

### Wave 3 (one agent): integration and review

- Merge review across the wave 2 work: consistent conditions, events, naming, RBAC markers.
- `test/integration` against the real tenant, gated by `UDDI_API_KEY`, with a run-scoped
  prefix and cleanup, covering create / adopt / update / delete for every kind.
- `make bundle && operator-sdk bundle validate` clean; kind e2e installing from the bundle.

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
