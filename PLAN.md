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

- [x] API request metrics, and the real-tenant `test/integration` suite gated by
      `UDDI_API_KEY` with a run-scoped prefix and cleanup. The suite exists but has not
      yet run against a tenant; that run is ROADMAP.md milestone 5.
- Moved to ROADMAP.md milestone 5: a kind e2e that installs from the bundle. The e2e suite
  in `test/e2e` is still the operator-sdk scaffold and tests no Universal DDI behaviour.

### Known limitations

- **Tag removal on upgrade.** When upgrading from a version that does not record
  `status.appliedTags`, the first sync records which keys were applied but removes none,
  so it never strips tags the operator might not own. A key dropped from `spec.tags` in
  that same sync is never recorded, so it stays on the Universal DDI object until it is
  removed by hand.
- **Deleting a CR whose Connection CR is already gone orphans the Universal DDI object.**
  The finalizer is removed so the CR can go away, a `ConnectionMissing` Warning event
  says what happened, and the object stays behind with its ownership tags. Delete
  Connections after the objects that use them, or re-adopt with a new CR.
- **DHCP option names are exact and only checked through the Connection.** `optionName`
  matches case-sensitively, and a name that later stops resolving turns an existing object
  `Ready=False/DependencyNotReady` until it resolves again.
- **Unified nameservers are not modelled.** AuthZone carries the legacy `nsgs` /
  `internalSecondaries` / `externalPrimaries` fields only. The API's `nameservers` model
  exists only on the SDK's untagged master branch, so `spec.nameservers` waits for a
  tagged SDK (DESIGN.md open question 3).
- **One CR manages a Universal DDI object at a time.** `adoptExisting` refuses an object
  another CR manages. To share a zone across namespaces, adopt it once and reference it from
  other DNSPublishPolicies by `id` and `fqdn`; objects referenced by other kinds still need
  a CR in the same namespace.
- **IPAllocationPolicy pools live in the policy namespace.** `allocateFrom` and
  `allocateFromIPv6` name Subnet, Range or AddressBlock CRs in the policy's namespace, so a
  team namespace owns its own Range inside a shared subnet rather than sharing one pool CR.
- **Moving a DNSPublishPolicy zone briefly removes its records.** A Record's zone and name
  in zone are immutable, so when a zoneRef change or a longer matching zone moves a
  hostname, the Record is deleted, which removes the DNS record, and created again on the
  next pass.
- **`WATCH_NAMESPACE` must include the DNSRecord and BareMetalHost namespaces** when an
  operator-wide namespace restriction is used together with the DNSRecord source or Metal3
  MAC discovery, or those reads fail or go unnoticed.
- **Milestone 7 is proven on envtest only.** Real-cluster scenarios for the new sources,
  dual-stack, EgressIP, MetalLB mirroring and Metal3 wait for milestone 5, as does checking
  OpenShift admission behaviour envtest does not run.
- **Kubernetes 1.29 CEL constraints.** The OpenShift 4.16 floor rules out
  `optionalOldSelf` / `oldSelf.orValue()`, so every "optional field is immutable" rule is
  written at the spec level instead of on the field. New validation must stay inside the
  1.29 CEL feature set and `make test-floor` is what proves it.

## Milestone 2: DNS publishing

DNSPublishPolicy controller (DESIGN.md section 4.7): automatically publish Routes, Ingresses, and LoadBalancer Services as DNS records.

- [x] **Type and planner:** `internal/publish/model.go` (SourceKind, Endpoint, Skip, TargetMode, SkipReason constants), `internal/publish/planner.go` (pure functions: sources to endpoints, zone matching, desired Records, apply/prune logic).
- [x] **Source extractors:** `internal/publish/route.go`, `internal/publish/ingress.go`, `internal/publish/service.go`. Each extracts hostname(s) and target(s) from its source kind, respects annotation overrides, returns Endpoint or Skip.
- [x] **Route API:** Register `route.openshift.io/v1` at startup with discovery fallback (Route API missing on plain Kubernetes is logged, not an error).
- [x] **Controller:** `internal/controller/dnspublishpolicy_controller.go`. Watches policy, owned Records, Routes, Ingresses, Services; field indexes on zoneRef names and source names; server-side apply with `uddi-operator/dnspublishpolicy` field manager; status with `published`, `ready`, `skipped`, conditions `Ready`, `RecordsNotReady`, `ZonesNotReady`.
- [x] **envtest suite:** `internal/controller/dnspublishpolicy_controller_test.go` covering: Auto/CNAME/A modes, zone matching, annotation overrides, Record ownership and garbage collection, skip reasons, status conditions, pruning when sources are deleted.
- [x] **Route CRD in envtest:** Register the openshift-api Route CRD in the test environment so the controller can reconcile routes.
- [x] **Documentation:** `docs/dns-publishing.md` (task guide: what it does, prerequisites, examples, target selection per mode and source kind, annotations, Record naming, skip reasons, deletion behavior, limitations).

## Milestone 3: IP allocation

IPAllocationPolicy controller: Address CR per selected Service, assignment via a
configurable annotation (MetalLB by default), release on Service deletion.

- [x] **Type and planner:** `internal/ipalloc/model.go` (ServiceRef, PoolRef, AssignMode, SkipReason constants, ServiceView, PoolState, AddressState, DesiredAddress, Assignment, Release, Plan), `internal/ipalloc/planner.go` (pure functions: Service selection, annotation parsing, pool resolution, desired Addresses, apply/assign/release logic).
- [x] **Service view and assignment helpers:** `internal/ipalloc/service.go`. Extract hostname(s) and allocation settings from a Service, validate annotations, return ServiceView or InvalidAnnotation error.
- [x] **Controller:** `internal/controller/ipallocationpolicy_controller.go`. Watches policy, selected Services, generated Addresses; field indexes on pool names; server-side apply with `uddi-operator/ipallocationpolicy` field manager; status with `allocated`, `assigned`, `skipped`, conditions `Ready`, `AddressesPending`, `PoolNotReady`.
- [x] **envtest suite:** `internal/controller/ipallocationpolicy_controller_test.go` covering: pool selection (subnet/range/addressblock), annotation overrides (`allocate-from`, `address`, `hostname`), assignment via annotation and spec.loadBalancerIP, ManuallyAssigned skip, AddressPending handling, Service deletion garbage collection, release on opt-out, skip reasons, status conditions.
- [x] **Documentation:** `docs/ip-allocation.md` (task guide: what it does, prerequisites, load balancer support, complete example, Service annotations, Address naming and labeling, how assignment works, status fields, skip reasons, release and deletion behaviour, combining with DNSPublishPolicy, limitations).

## Milestone 4: host and DHCP registration

HostRegistrationPolicy: Nodes become DHCP FixedAddress reservations when a MAC is known, or named IPAM Addresses otherwise. MAC discovery via Node annotation or NMState operator.

- [x] **Type and planner:** `internal/hostreg/model.go` (NodeRef, AddressType, RegisterMode, RegistrationKind, Node annotations, MACSource, SkipReason constants, NodeView, SpaceState, RegistrationState, DesiredRegistration, Release, Plan), `internal/hostreg/planner.go` (pure functions: Node selection, MAC discovery chain, address/hostname resolution, registration kind decision, desired objects, apply/release/status logic).
- [x] **Node view and NMState reader:** `internal/hostreg/node.go`. Extract address, MAC, hostname from a Node and its annotations; read NodeNetworkState when the API is present; validate annotation values and return NodeView or InvalidAnnotation skip.
- [x] **Controller:** `internal/controller/hostregistrationpolicy_controller.go`. Watches policy, selected Nodes, generated Addresses and FixedAddresses, NodeNetworkStates (when available); field indexes on IPSpace names; server-side apply with `uddi-operator/hostregistrationpolicy` field manager; status with `registered`, `pending`, `skipped`, conditions `Ready`, `RegistrationsPending`, `SpaceNotReady`.
- [x] **envtest suite:** `internal/controller/hostregistrationpolicy_controller_test.go` covering: Auto/Address/FixedAddress modes, MAC discovery from annotation and mock NodeNetworkState, kind changes (Address to FixedAddress when MAC appears), Node deletion garbage collection, opt-out and policy deletion behavior, skip reasons, status conditions.
- [x] **NodeNetworkState fixture:** Mock `nmstate.io/v1beta1 NodeNetworkState` in envtest so the controller can test MAC reading.
- [x] **Documentation:** `docs/host-registration.md` (task guide: what it does, prerequisites, complete example, Node annotations, modes, MAC discovery, generated object naming, status fields, skip reasons, release and deletion behavior, limitations).

## Milestone 6: harden the object layer

Started 2026-10-04. Scope: the ROADMAP.md milestone 6 items that need neither a Universal
DDI tenant nor a cluster. Unified nameservers stay blocked until the SDK tags them, and
checking the `restricted-v2` SCC on a real cluster belongs to milestone 5. Each work package
below is the contract its agent builds against; DESIGN.md carries the same semantics.

### Wave 1 (parallel)

- [x] **A. Tag removal and correctness items.**
  - `ObjectStatus.appliedTags` (sorted list of keys): the tag keys the operator wrote at the
    last successful sync, meaning ownership tags, Connection `defaultTags` and `spec.tags`.
    It is written only together with `Synced=True` for the current generation.
  - `tags.Desired{Want map[string]string; Remove []string}`. `Remove` is `appliedTags`
    minus the keys of `Want`, and never contains an ownership key. `tags.ForPatch(current,
    desired)` returns `current` minus `Remove` plus `Want`, and whether that differs from
    `current`. Keys the operator never wrote are left alone. An empty `appliedTags`, as on
    the first sync after an upgrade, removes nothing.
  - The adapter interface changes so `Create` receives `Want` and `Diff` receives the whole
    `tags.Desired`; all nine adapters follow.
  - The fake's updates replace the tag map wholesale, which is what the operator assumes a
    PATCH of `tags` does. `test/integration` gains a test that removes one key through a
    PATCH and logs a `VERDICT:` line saying whether Universal DDI replaced or merged the map.
  - Record apex: the spec-level immutability rule treats an omitted `name` and `@` as the
    same value.
  - DNSPublishPolicy: a second condition type `ZonesResolved`, True when every zoneRef names
    an existing AuthZone CR, otherwise False with reason `ZoneNotFound` and the missing
    names in the message, plus a Warning event `ZoneNotFound`. Ready logic is unchanged.
- [x] **B. API client behaviour**, confined to `internal/uddi`.
  - Pagination: every SDK List helper pages through all results when `ListOptions.Limit`
    is 0, following the page token when the response carries one and otherwise using
    offset and limit pages of 1000. More than 100 pages is an error, never an endless loop.
    The fake supports offset and limit so the tests exercise paging.
  - Retries: a RoundTripper retries 429, 502, 503 and 504 for GET, PATCH and DELETE. POST is
    never retried because the API has no idempotency key. It honours `Retry-After` in
    seconds or as an HTTP date, otherwise backs off exponentially with full jitter from
    500 ms, retries at most 3 times within 30 s of total waiting, stops when the context is
    cancelled, and rewinds request bodies with `GetBody`.
  - Metrics: `uddi_operator_api_retries_total{connection,domain,method,code}` and the
    histogram `uddi_operator_api_ratelimit_wait_seconds{connection}` for time spent waiting
    on the client-side limiter.
- [x] **C. Observability.**
  - `internal/metrics`: a Prometheus collector that reads the manager's cache at scrape time,
    so the reconcile loop does not change. It exports
    `uddi_operator_objects{kind,namespace,ready}` for Connection and the nine object-layer
    kinds, with `ready` as true, false or unknown from the Ready condition;
    `uddi_operator_policy_objects{kind,namespace,policy,state}` from policy status
    (DNSPublishPolicy published and ready, IPAllocationPolicy allocated and assigned,
    HostRegistrationPolicy registered and pending); and
    `uddi_operator_policy_skipped{kind,namespace,policy,reason}`.
  - `config/observability`, not part of `config/default`: a ServiceMonitor for OpenShift
    user-workload monitoring, a PrometheusRule and dashboards. Alerts: UDDIConnectionNotReady
    (10 min), UDDIObjectsNotReady (15 min), UDDIAPIServerErrors (5xx rate),
    UDDIAPIThrottled (429 rate), UDDIAddressesPending (15 min), UDDIRegistrationsPending
    (15 min) and UDDIReconcileErrors (controller-runtime error rate). Each has a summary, a
    description and a runbook link into `docs/observability.md`. Dashboards: a Grafana JSON
    and an OpenShift console dashboard ConfigMap.
  - Rules pass `promtool check rules` locally and in a new CI step.
  - `docs/observability.md`: metric reference and one runbook section per alert.
- [x] **F. Deployment hardening.**
  - Manager pod: `runAsNonRoot`, seccomp `RuntimeDefault`, no privilege escalation,
    read-only root filesystem, all capabilities dropped, explicit requests and limits.
  - `config/ha` overlay: two replicas, a PodDisruptionBudget with `maxUnavailable: 1` and
    soft pod anti-affinity. The default stays one replica without a PDB, because a PDB on a
    single leader-elected replica only blocks node drains.
  - `config/network-policy`: egress to cluster DNS, the API server and TCP 443 for
    `csp.infoblox.com`, which a NetworkPolicy cannot name by host; ingress to the metrics
    port from `openshift-monitoring` and `openshift-user-workload-monitoring`.
  - `docs/install.md`: a hardening section.

### Wave 2 (after wave 1 merges)

- [x] **D. controller-runtime modernisation.** Events go through `events.EventRecorder` from
  `mgr.GetEventRecorder`, and the policies' generated Records, Addresses and FixedAddresses
  are applied with generated apply configurations through the client's `Apply` method. The
  nolint suppressions and the matching known limitations go away.
- [x] **E. DHCP option codes by name.** `DHCPOption` gains `optionName` and an optional
  `optionSpace` as an alternative to `optionCode`; exactly one of the two is set for type
  `option`. A new `ListOptionCodes` on the DHCP client interface, a per-Connection cache
  with a 10-minute TTL, and resolution in `Adapter.Resolve`, which therefore receives the
  Universal DDI client. A name that does not resolve reports `DependencyNotReady` with the
  name and space in the message, and is retried.
- [x] **G. Documentation pass**: API reference and usage guide for `appliedTags`,
  `optionName` and `ZonesResolved`; API notes for retries and pagination; known
  limitations updated.

Milestone 6 ends with both envtest lanes, lint, govulncheck and the bundle validators green
and the bundle regenerated.

## Milestone 7: complete the integration layer

Started 2026-10-04. Each work package is the contract its agent builds against; DESIGN.md
4.7 carries the semantics under "Milestone 7 extensions" for each policy. The real-cluster
smoke scenarios from the roadmap's exit criteria wait for milestone 5, because no cluster is
available yet; milestone 7 is proven on envtest.

### Wave 1: types and pure logic (parallel)

- [x] **P1. DNS publishing core.** DNSPublishPolicy spec gains `createPtr`,
  `compatibility.externalDNS`, `nodeAddressType`, `dnsRecordNamespace` and the source
  kinds Node, DNSRecord and HTTPRoute. `internal/publish`: the Node and DNSRecord
  extractors, external-dns annotation fallbacks in the shared annotation parsing (no
  signature change, driven by `SourceOptions.ExternalDNS`), wildcard hostnames in planning,
  `createPtr` on generated A and AAAA Records, and tests proving dual-stack load balancers
  and Nodes yield A and AAAA. A scheme helper for openshift/api `operatoringress/v1` and the
  DNSRecord CRD fixture for envtest.
- [x] **P2. HTTPRoute source.** `internal/gatewayapi`: discovery for
  `gateway.networking.k8s.io/v1`, minimal Gateway and HTTPRoute CRD fixtures, unstructured
  accessors. `internal/publish`: the HTTPRoute extractor in its own file, using the shared
  annotation helpers.
- [x] **P3. IP allocation claims.** `internal/ipalloc` generalised to claims and slots:
  dual-stack Services and EgressIP claims with the naming and label compatibility rules,
  pool family detection, comma-joined assignments, `NoPoolForFamily` and
  `DualStackUnsupported`. IPAllocationPolicy spec gains `targets`, `allocateFromIPv6` and
  `metallb`. `internal/egressip`: discovery, a minimal EgressIP CRD fixture and an
  unstructured view.
- [x] **P4. MetalLB pool mirroring.** `internal/metallb`: discovery, a minimal IPAddressPool
  CRD fixture, and pure builders for the desired pool objects, names, labels and the stale
  set to prune.
- [x] **P5. Host registration.** `internal/metal3api`: discovery, a minimal BareMetalHost CRD
  fixture and the MAC lookup. `internal/hostreg`: Metal3 in the MAC chain, additional
  interfaces in the view and the planner. HostRegistrationPolicy spec gains
  `macSource.metal3` and `additionalInterfaces`.
- [x] **P6. Patterns guide.** `docs/patterns.md`: a bare-metal cluster using all three
  policies together.

### Wave 2: controllers and docs (after wave 1 merges)

- [x] **C1. DNSPublishPolicy controller** wiring the three new sources, their discovery
  flags, watches and RBAC, with envtest; plus `zoneRefs` by `id` and `fqdn` (type, planner
  and controller), so several namespaces can publish into one zone adopted once.
- [x] **C2. IPAllocationPolicy controller** wiring claims for dual-stack Services and
  EgressIPs and MetalLB mirroring with its finalizer, with envtest.
- [x] **C3. HostRegistrationPolicy controller** wiring Metal3 and additional interfaces, with
  envtest.
- [x] **D1. Documentation** for the three policy guides and the API reference.

Milestone 7 ends with both envtest lanes, lint, govulncheck, the rule checks and the bundle
validators green and the bundle regenerated.

## Milestones 5 and beyond

Planned in [ROADMAP.md](ROADMAP.md): proving the operator on a real tenant and cluster
(5), hardening the object layer (6, complete above), completing the integration layer (7, in progress above), API maturity
with `v1beta1` (8), distribution through OperatorHub and certification (9), and operating
at scale (10). Each milestone moves here as a checklist when it starts.
