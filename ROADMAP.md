# uddi-operator roadmap

Written 2026-09-15, after milestones 1 to 3 shipped and milestone 4 went into
implementation. [PLAN.md](PLAN.md) tracks the milestones that are built; this document
plans everything from here to a supported 1.0 and names what stays out of scope.
[DESIGN.md](DESIGN.md) remains the architecture reference; every item below that changes
behaviour gets a design section there before it is built.

## Where we are

| Milestone | State | What it gave us |
| --- | --- | --- |
| 1 Object layer | shipped | ten kinds, generic reconcile loop, ownership tags, adoption, deletion policies, OLM bundle, CI |
| 2 DNS publishing | shipped | DNSPublishPolicy for Routes, Ingresses, LoadBalancer Services |
| 3 IP allocation | shipped | IPAllocationPolicy with MetalLB, Cilium and kube-vip assignment |
| 4 Host registration | building | HostRegistrationPolicy with annotation and NMState MAC discovery |

Everything so far is proven against an in-memory fake of Universal DDI and against envtest
on Kubernetes 1.29 and 1.36. Nothing has yet touched a real tenant or a real cluster. That
gap is the first thing the rest of the plan closes, because every later decision (API
stability, distribution, scale) depends on what the real API does.

## What "done" means

Version 1.0 is reached when all of the following hold:

1. Installed from an OperatorHub catalog on a supported OpenShift release, upgraded in
   place from the previous minor through OLM, and removed cleanly.
2. Every kind and every policy exercised against a real Universal DDI tenant by an
   automated suite that runs on demand in CI.
3. A `v1beta1` API that the project commits to keep backward compatible, with a written
   deprecation policy.
4. Observable in production: metrics, alerts, a dashboard, a must-gather script and a
   troubleshooting guide that support engineers can follow without reading the code.
5. A documented support matrix, a signed multi-arch image with an SBOM, and a release
   process that a maintainer can run in under an hour.

Versions along the way: `v0.1.0` at the end of milestone 4, `v0.2.0` after milestones 5
and 6, `v0.3.0` after milestone 7, `v0.9.0` when `v1beta1` lands (milestone 8), `v1.0.0`
when milestone 9 completes.

## Milestone 5: prove it on real infrastructure

Goal: replace assumptions with evidence before hardening or distributing anything.

1. **Real-tenant integration run.** Run `make test-integration` against a sandbox tenant
   with a run-scoped prefix (the suite exists and cleans up by tag). Feed every `VERDICT`
   line back into `docs/uddi-api-notes.md`. This closes open questions 1, 6, 7 and 8 of
   DESIGN.md section 14: tag lookup behaviour, recycle-bin visibility and name reuse after
   delete, duplicate-create status codes, option code id prefix.
2. **Fake fidelity.** Where the tenant disagrees with the fake (status codes, list
   semantics after delete, `_tfilter` edge cases, next-available concurrency), change the
   fake first, watch which envtest specs go red, then fix the adapters. The fake is the
   contract every controller test relies on, so it has to tell the truth.
3. **Image and kind e2e.** Enable Docker Desktop's WSL integration, build the image,
   run the scaffolded e2e on kind from the bundle, and add the operator-sdk scorecard
   basic suite. Make the `test-e2e` workflow green on pull requests.
4. **First OpenShift install.** Push image, bundle and catalog to `quay.io/ingmarvg-ib`,
   install through a CatalogSource on the available 4.16+ cluster, and run a scripted
   smoke test: adopt a zone, publish a Route, allocate an IP for a MetalLB Service,
   register a worker node. Record the exact steps as `docs/smoke-test.md` so the same run
   repeats on every release.
5. **Fix list.** Everything the four steps above surface, plus the CI floor lane pinned
   to the Kubernetes minor of the oldest supported OpenShift.

Exit criteria: integration and e2e workflows green on demand, smoke test passed on a real
cluster, open questions 1, 6, 7 and 8 closed in DESIGN.md, tag `v0.1.0`.

## Milestone 6: harden the object layer

Goal: remove the known limitations and the technical debt listed in PLAN.md before the
API is frozen.

- **Tag removal.** Record the tag keys the operator applied in `status.appliedTags` and
  delete keys that vanish from `spec.tags` or the Connection defaults, while still leaving
  foreign tags alone.
- **Modern controller-runtime APIs.** Move events to `events.EventRecorder` and generated
  objects to apply configurations, then drop the three nolint suppressions.
- **Object metrics and alerts.** `uddi_operator_objects{kind,namespace,ready}` from the
  reconcile loop, reconcile error counters, a PrometheusRule (Connection not Ready,
  objects not Ready for 15 minutes, API 5xx rate, allocation failures) and a Grafana
  dashboard under `config/observability`.
- **API client behaviour.** Pagination in every List helper, 429 backoff on top of the
  client-side limiter, and per-Connection request budgets visible in metrics.
- **Unified nameservers** on AuthZone as soon as a tagged SDK ships them (DESIGN.md open
  question 3), including adoption of `version: v2` zones.
- **DHCP option codes by name.** A per-Connection lookup cache from
  `/dhcp/option_code` so `dhcpOptions` accept `optionName: routers` next to the raw id.
- **Small correctness items.** Normalise the Record apex spelling in CEL, surface a
  missing AuthZone in `zoneRefs` as a condition, and put `status.appliedTags` behind the
  same `observedGeneration` discipline as the rest of status.
- **Runtime security.** Verify `restricted-v2` on a real cluster, set seccomp
  `RuntimeDefault`, read-only root filesystem, resource requests and limits, a
  PodDisruptionBudget, and a sample NetworkPolicy that allows only the API server, the
  metrics scraper and `csp.infoblox.com`.

Exit criteria: PLAN.md "Known limitations" reduced to the CEL 1.29 constraint, metrics and
alerts documented, tag `v0.2.0`.

## Milestone 7: complete the integration layer

Goal: cover the cases real clusters hit that the first cut of each policy left out.

- **DNS publishing.** The default router wildcard via the ingress operator's `DNSRecord`
  CRs (open question 5); dual-stack sources producing A and AAAA together; per-policy
  `createPtr`; `external-dns.alpha.kubernetes.io/hostname` read as a compatibility
  annotation so existing external-dns users migrate without relabelling; Gateway API
  `HTTPRoute` as a fourth source once the target OpenShift releases ship it GA; a `Node`
  source so node A records come from the same policy that HostRegistrationPolicy already
  names them for.
- **IP allocation.** Dual-stack (two Addresses, comma-joined annotation for MetalLB and
  Cilium); OVN-Kubernetes `EgressIP` allocation from a pool; optional mirroring of a
  Range into a MetalLB `IPAddressPool` so the pool definition lives in Universal DDI only;
  real-cluster verification of the Cilium and kube-vip keys.
- **Host registration.** Metal3 `BareMetalHost` as a third MAC source on bare-metal IPI
  clusters (open question 2); several interfaces per node (management plus workload
  networks) registered as separate objects; automatic PTR through the DNS policy.
- **Cross-policy conventions.** One `docs/patterns.md` describing how the shared
  `uddi.infoblox.com/hostname` annotation ties DNS, IPAM and host registration together,
  with a worked example of a bare-metal cluster using all three.

Exit criteria: each policy has a real-cluster scenario in `docs/smoke-test.md`, tag
`v0.3.0`.

## Milestone 8: API maturity

Goal: a `v1beta1` API the project can promise not to break.

- **API review.** Field names, defaults, enum casing, status conventions and printcolumns
  reviewed across all thirteen kinds against the Kubernetes API conventions; anything that
  needs renaming is renamed now, once.
- **Versioning.** Add `v1beta1` as the storage version with `v1alpha1` still served.
  Prefer no conversion webhook: keep the schemas structurally identical and let
  `v1alpha1` be a served alias; if a rename forces a conversion, use OpenShift's
  service-ca for the webhook certificate rather than requiring cert-manager.
- **Upgrade tests.** An OLM upgrade from the previous minor on kind, driven by the
  catalog, asserting existing CRs keep their status and ownership tags. `olm.skipRange`
  and channel policy (`alpha` now, `stable` from `v1beta1`).
- **Bounds.** Status list caps, label and annotation lengths, managedFields growth from
  server-side apply, and CRD size all measured at scale (milestone 10 feeds back here).
- **Compatibility policy.** A short written promise: what changes are allowed in
  `v1beta1`, how deprecations are announced, how long `v1alpha1` stays served.

Exit criteria: `v1beta1` storage version, upgrade test in CI, tag `v0.9.0`.

## Milestone 9: distribution

Goal: an operator anyone can install from OperatorHub with confidence in its provenance.

- **Release engineering.** `release.yml` proven end to end on a tag; semantic versions
  with a generated changelog; multi-arch image signed with cosign and shipped with an
  SBOM; vulnerability scanning of the image and the Go modules in CI with a documented
  CVE response policy; Renovate for dependency updates, including the pinned
  openshift/api pseudo-version and the Universal DDI SDK.
- **Community OperatorHub.** Submission to `community-operators-prod` with the bundle
  validators already green; keep the submission scripted so every release repeats it.
- **Red Hat certification.** A Connect project, `preflight` checks on the image and the
  operator bundle, the certification pipeline; the Dockerfile already carries the labels
  and licenses directory. Budget several weeks of lead time and start it as soon as
  `v0.9.0` exists.
- **Documentation site.** The `docs/` tree published as a site with versioned pages, a
  support matrix (OpenShift and Kubernetes versions, Universal DDI API version,
  load balancer implementations), and the smoke-test runbook.

Exit criteria: listed on OperatorHub, certification submitted or granted, tag `v1.0.0`.

## Milestone 10: operate at scale

Goal: know how it behaves with thousands of objects and after a disaster.

- **Scale.** Reconcile 1,000 Records, 500 LoadBalancer Services and 200 Nodes in one
  tenant; tune `MaxConcurrentReconciles` per controller, the per-Connection rate limit and
  the resync interval; add lookup caching where the API budget demands it; publish the
  numbers.
- **Multi-cluster.** Two clusters against one tenant, proving the cluster id tag keeps
  ownership separate and documenting the conflict behaviour when they claim the same
  natural key.
- **Cluster rebuild and restore.** After a rebuild every CR gets a new UID, so the uid tag
  no longer matches. Design and implement a re-adoption path (match on owner and cluster
  tags when the uid differs, rewrite the uid tag) and document Velero backup and restore
  of the CRDs with finalizers.
- **Supportability.** `hack/must-gather.sh` collecting CRs, conditions, events, operator
  logs and redacted Connection state; a troubleshooting guide organised by condition
  reason; SLOs for time-to-Ready per kind and alert thresholds derived from them.

Exit criteria: scale numbers in the docs, restore procedure tested, must-gather shipped.

## Deferred and out of scope

Deferred until there is a concrete request:

- **ExternalDNS webhook provider mode.** Package the Universal DDI adapter as an
  external-dns webhook so existing external-dns installations can use it without the
  operator. Cheap to build on the client layer; only worth it if users ask.
- **ClusterConnection.** A cluster-scoped credential for single-tenant clusters, which
  DESIGN.md deliberately avoided; the per-namespace model covers the known cases.
- **OpenShift console plugin** showing policies, their generated objects and skip reasons
  in the web console.
- **Universal DDI federation realms, DTC and LBDN objects** as CRDs.

Out of scope, as DESIGN.md section 15 states: NIOS WAPI, DNS view ACLs, DNSSEC, DHCP HA
groups, anycast, a standalone UI.

## Sequencing, effort and risk

| Milestone | Needs | Effort in agent waves | Main risk |
| --- | --- | --- | --- |
| 5 prove it | tenant key, cluster, Docker in WSL | 2 waves plus manual cluster work | undocumented API behaviour forcing adapter changes |
| 6 harden | 5 | 3 waves | SDK master churn before a tag ships unified nameservers |
| 7 complete | 5 | 3 waves | Gateway API and Metal3 availability on the target releases |
| 8 API maturity | 6, 7 | 2 waves plus review | a rename that forces a conversion webhook |
| 9 distribution | 8 | 2 waves plus certification lead time | certification turnaround measured in weeks |
| 10 scale | 5 | 2 waves plus a large test tenant | API rate limits at scale |

Milestones 6 and 7 can run in parallel after 5; milestone 10's scale runs can start once 5
has a working cluster. The delivery pattern stays the one that worked for milestones 2 to
4: a design section and a shared contract file first, Sonnet agents on disjoint packages
in manually created worktrees, a Haiku pass for docs, then one controller agent and a
review. Every milestone ends with both envtest lanes, lint and the bundle validators green
on main and a signed tag.

## Immediate next steps

1. Finish milestone 4 (controller wave in progress), tag `v0.1.0`.
2. Enable Docker Desktop's WSL integration and run the real-tenant integration suite;
   both need the user, not an agent.
3. Start milestone 5 step 4 on the real cluster the same week, because its findings
   decide the size of milestone 6.
