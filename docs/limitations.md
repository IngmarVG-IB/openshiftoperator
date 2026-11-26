# Known limitations

uddi-operator is alpha (`v1alpha1`). It has been exercised only by unit tests and by envtest
against an in-memory fake of the Universal DDI API; it has never run against a real tenant
or a real OpenShift cluster. The list below covers
behaviour that is deliberate or not yet implemented, plus assumptions that real
infrastructure has yet to confirm. The plan for closing them is in [ROADMAP.md](../ROADMAP.md).

- **Tag removal assumes a PATCH of `tags` replaces the whole map.** The operator records the
  keys it wrote in `status.appliedTags` and removes keys that disappear from `spec.tags` or
  the Connection defaults, leaving foreign tags alone. The fake replaces the tag map on
  update, and the real-tenant integration test that checks whether Universal DDI replaces or
  merges it has not been run, so removal is unconfirmed against a real tenant. An empty
  `appliedTags` (an object synced before the field was recorded) removes nothing.
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
- **POST requests are never retried.** The client retries 429, 502, 503 and 504 for GET,
  PATCH and DELETE with backoff, but the API has no idempotency key, so a failed create is
  left to the next reconcile.
- **The integration and e2e coverage is thin.** The real-tenant suite in `test/integration`
  has never run, and the e2e suite in `test/e2e` is the operator-sdk scaffold smoke test: it
  checks that the manager pod runs and the metrics endpoint serves, not any Universal DDI
  behaviour.
- **The newer sources are proven on envtest only.** Real-cluster scenarios for the
  DNSRecord, Node and HTTPRoute sources, dual-stack, EgressIP, MetalLB mirroring and Metal3
  have not been run, nor has OpenShift admission behaviour that envtest does not exercise.
- **Kubernetes 1.29 CEL constraints.** The OpenShift 4.16 floor rules out
  `optionalOldSelf` / `oldSelf.orValue()`, so every "optional field is immutable" rule is
  written at the spec level instead of on the field. New validation must stay inside the
  1.29 CEL feature set, and `make test-floor` checks that.
