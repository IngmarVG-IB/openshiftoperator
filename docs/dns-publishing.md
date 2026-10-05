# DNS Publishing Guide

This guide covers DNSPublishPolicy: how to automatically publish OpenShift Routes, Ingresses, and LoadBalancer Services as DNS records in Infoblox Universal DDI.

## What DNSPublishPolicy Does

A DNSPublishPolicy watches Routes, Ingresses, and LoadBalancer Services in a namespace and automatically generates DNS Record CRs that point to them. When you create a Route or Ingress with a hostname, the operator:

1. Matches the hostname to an AuthZone you specify
2. Extracts the target address or canonical hostname from the source object
3. Creates a Record CR owned by the policy
4. Updates Universal DDI with the DNS record
5. Cleans up the record when the policy or source object is deleted

Records are garbage-collected by the policy's ownerReference, so deleting the policy removes all its generated records (which then remove themselves from Universal DDI via their finalizers).

The default sources are `Route`, `Ingress` and `Service`. Three more kinds are opt-in through `spec.sources`: cluster `Node`s, the OpenShift ingress operator's `DNSRecord`s (the router wildcard), and Gateway API `HTTPRoute`s. They are described under [How Targets Are Chosen](#how-targets-are-chosen). A policy can also publish into zones that live in another namespace, see [Zones by ID](#zones-by-id).

## Prerequisites

Before creating a DNSPublishPolicy, you need:

1. **A ready Connection CR** in the namespace with valid credentials.
2. **An AuthZone CR** with `status.id` set (the zone must be adopted or created, not just pending).

A typical setup:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: AuthZone
metadata:
  name: apps-zone
spec:
  fqdn: apps.example.com.
  viewRef:
    name: default
  primaryType: cloud
  lifecycle:
    adoptExisting: true
    deletionPolicy: Orphan
```

Check that the zone is ready:

```bash
kubectl get authzone apps-zone -o jsonpath='{.status.id}'
# dns/auth_zone/... (non-empty means ready)
```

An AuthZone CR is not the only way to name a zone. A `zoneRefs` entry can also carry a Universal DDI zone `id` together with its `fqdn`, which needs no AuthZone CR in the policy's namespace. That is how several namespaces publish into one zone; see [Zones by ID](#zones-by-id).

## Complete Example

Create an AuthZone for the applications namespace:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: AuthZone
metadata:
  name: apps-zone
  namespace: my-team
spec:
  fqdn: apps.example.com.
  viewRef:
    name: default
  primaryType: cloud
  lifecycle:
    adoptExisting: true
    deletionPolicy: Orphan
```

Create the DNSPublishPolicy:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: DNSPublishPolicy
metadata:
  name: public
  namespace: my-team
spec:
  connectionRef:
    name: default
  sources:
    - Route
    - Ingress
    - Service
  zoneRefs:
    - name: apps-zone
  target:
    mode: Auto
    addresses:
      - 192.0.2.10
  ttl: 300
  recordLifecycle:
    adoptExisting: false
    deletionPolicy: Delete
  tags:
    team: my-team
    environment: production
```

Now create a Route:

```yaml
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: web
  namespace: my-team
spec:
  host: web.apps.example.com
  to:
    kind: Service
    name: web
  tls:
    termination: edge
    insecureEdgeTerminationPolicy: Redirect
```

When the Route is admitted (a router accepts it), the policy automatically creates a Record CR and publishes it to Universal DDI. Check the policy status:

```bash
kubectl get dnspublishpolicy public -n my-team
# NAME     READY   PUBLISHED   READY-COUNT   AGE
# public   True    1           1             2m

# Or using the short name:
oc get uddipublish public -n my-team
# NAME     READY   PUBLISHED   READY-COUNT   AGE
# public   True    1           1             2m

kubectl describe dnspublishpolicy public -n my-team
# Status:
#   Published:          1
#   Ready:              1
#   Observed Generation:  1
#   Conditions:
#     Last Transition Time:  2026-09-14T12:00:00Z
#     Message:               All records published and ready
#     Observed Generation:   1
#     Reason:                Published
#     Status:                True
#     Type:                  Ready
```

List the generated records:

```bash
kubectl get records -l uddi.infoblox.com/policy=public -n my-team
# NAME                          ZONE      NAME   TYPE   READY
# public-a1b2c3d4e5f6g7h8      apps-zone  web    A      True
```

## How Targets Are Chosen

Target selection depends on the source kind, target mode, and policy settings.

### Route Sources (OpenShift only)

A Route contributes one endpoint per admitted ingress (the first ingress where a router accepted it). The hostname is `spec.host`; if the Route uses `spec.subdomain` without `spec.host`, it is marked `Unsupported`.

**Auto mode (default):**
- If `target.addresses` is set, create A/AAAA records to those addresses
- Otherwise, create a CNAME to the admitted ingress's `routerCanonicalHostname`, or to `target.canonicalHostname` if set

**CNAME mode:**
- Always create a CNAME to `target.canonicalHostname` (if set) or the ingress's `routerCanonicalHostname`
- If no CNAME target exists, skip with `NoTarget`

**A mode:**
- Create A/AAAA records to `target.addresses` if set
- If no addresses, skip with `NoTarget`

Example: a Route with a fixed router IP on a bare-metal cluster:

```yaml
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: app
spec:
  host: app.apps.example.com
  to:
    kind: Service
    name: app
```

With policy mode `Auto` and `spec.target.addresses: [192.0.2.10]`, the operator creates an A record to `192.0.2.10`.

### Ingress Sources

An Ingress contributes one hostname per `spec.rules[].host`. For each hostname, targets come from `status.loadBalancer.ingress[]`:
- If any ingress entry has an IP, create one A or AAAA record per IP
- If any ingress entry has a hostname, create one CNAME record
- If the ingress is pending (no status.loadBalancer), skip with `NoTarget`

**CNAME mode** keeps only CNAME targets; **A mode** keeps only address targets.

### LoadBalancer Service Sources

A LoadBalancer Service is only published if:
- Its `type` is `LoadBalancer`, or
- It carries the `uddi.infoblox.com/hostname` annotation (relaxes the type check)

Hostnames come only from the `uddi.infoblox.com/hostname` annotation (comma-separated; required unless the policy has a target annotation override on the source object).

Targets come from `status.loadBalancer.ingress[]` just like Ingress.

Example:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: external-svc
  namespace: my-team
  annotations:
    uddi.infoblox.com/hostname: "api.apps.example.com"
spec:
  type: LoadBalancer
  selector:
    app: external
  ports:
    - port: 443
      targetPort: 8443
```

### Node Sources

The `Node` source publishes cluster Nodes. It is opt-in: list `Node` in `spec.sources`.

Nodes are cluster-scoped, so the policy reads every Node in the cluster that matches `spec.selector` (a label selector on the Node's labels), whichever namespace the policy lives in. If two policies in different namespaces select the same Nodes, both generate Records for them and the second set reports `Ready=False/AlreadyExists`; keep the selectors apart.

- **Hostnames:** the Node's name when it contains a dot (a short name such as `worker-0` is not usable DNS), plus any names in the `uddi.infoblox.com/hostname` annotation. A Node with neither is skipped with `NoHostname`.
- **Targets:** every Node address of the type in `spec.nodeAddressType`, `InternalIP` (the default) or `ExternalIP`. IPv4 addresses become A Records and IPv6 addresses AAAA Records, so a dual-stack Node gets both. `spec.target.addresses` is not used for Nodes. A `target` annotation on the Node replaces the computed targets.
- **Mode:** `target.mode: A` or `Auto` keeps the address targets. `CNAME` filters every address out, so the Node is skipped with `NoTarget` unless its `target` annotation names a CNAME.

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: DNSPublishPolicy
metadata:
  name: nodes
  namespace: uddi-system
spec:
  sources:
    - Node
  selector:
    matchLabels:
      node-role.kubernetes.io/worker: ""
  zoneRefs:
    - name: cluster-zone      # AuthZone for ocp1.example.com.
  nodeAddressType: InternalIP
  createPtr: true
  ttl: 300
```

A Node named `worker-0.ocp1.example.com` gets an A Record `worker-0` in `ocp1.example.com.`, and an AAAA Record too when it also has an IPv6 InternalIP. A Node whose name is not inside any `zoneRefs` zone is skipped with `NoMatchingZone`. For a Node with a short name, set the hostname by annotation:

```bash
kubectl annotate node worker-0 uddi.infoblox.com/hostname=worker-0.ocp1.example.com
```

### DNSRecord Sources (the router wildcard)

The OpenShift ingress operator keeps a `DNSRecord` (`ingress.operator.openshift.io/v1`) for the default router wildcard, named `default-wildcard`, in the `openshift-ingress-operator` namespace. With `DNSRecord` in `spec.sources`, the policy turns those objects into Records, so `*.apps.<cluster>.` is published without a hand-written Record.

- **Where it reads:** the namespace in `spec.dnsRecordNamespace` (default `openshift-ingress-operator`), not the policy's own namespace. `spec.selector` matches the DNSRecord's labels; leave it unset to take every DNSRecord there, or select one ingress controller's record.
- **Hostname:** `spec.dnsName`, kept with its leading `*.` label (see [Wildcard Hostnames](#wildcard-hostnames)), plus any names in the hostname annotation. A DNSRecord without a `dnsName` is skipped with `NoHostname`, and one whose name is not a valid DNS name with `Unsupported`.
- **Targets:** `spec.targets`, typed by `spec.recordType`. `A` takes IPv4 addresses, `AAAA` IPv6 addresses and `CNAME` names (a trailing dot is added). Any other record type, or a target that does not fit the type, is `Unsupported`. The CRD shipped by openshift/api currently allows only `A` and `CNAME`. A `target` annotation replaces the computed targets, and `target.mode` filters them as for the other sources.
- **TTL:** the `uddi.infoblox.com/ttl` annotation on the DNSRecord first, then `spec.recordTTL` when it is positive, then the policy's `ttl`. The ingress operator fills `recordTTL` itself (its CRD describes a default of 30 seconds), so a published wildcard normally carries that short TTL rather than the policy's.
- **Management policy:** `spec.dnsManagementPolicy` is not consulted. It tells the ingress operator whether it manages the cloud provider's DNS; it does not stop this policy from publishing.

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: DNSPublishPolicy
metadata:
  name: router
  namespace: uddi-system
spec:
  sources:
    - DNSRecord
  zoneRefs:
    - name: apps-zone         # AuthZone for apps.ocp1.example.com.
  # dnsRecordNamespace defaults to openshift-ingress-operator
```

With the default wildcard record (`dnsName: "*.apps.ocp1.example.com."`, one A target) and the zone `apps.ocp1.example.com.`, the policy generates one Record named `*` in that zone. With only the zone `ocp1.example.com.`, the name would be `*.apps`. The skip is reported as `DNSRecord/default-wildcard`.

If the cluster does not serve the DNSRecord API (kind, or a cluster without the ingress operator), the manager logs that once at startup and the source stays inert: no error, no skips, no Records.

**Running with `WATCH_NAMESPACE`.** When the operator is installed in `OwnNamespace`, `SingleNamespace` or `MultiNamespace` mode, its cache covers only the watched namespaces. The DNSRecord namespace must be one of them, or listing the DNSRecords fails and the policy keeps retrying with an error. Add `openshift-ingress-operator` (or the namespace in `dnsRecordNamespace`) to the OperatorGroup's `targetNamespaces`, or install in `AllNamespaces` mode.

### HTTPRoute Sources (Gateway API)

The `HTTPRoute` source publishes Gateway API routes (`gateway.networking.k8s.io/v1`). It is opt-in. The operator reads HTTPRoutes and Gateways as unstructured data, so it carries no Gateway API module dependency, and it checks once at startup whether the API is served.

- **Where it reads:** HTTPRoutes in the policy's namespace that match `spec.selector`. The Gateways they point at may live in any namespace.
- **Hostnames:** `spec.hostnames` (wildcards such as `*.example.com` are kept) plus any names in the hostname annotation. A route with neither is skipped with `NoHostname`: a route without hostnames matches every host, so there is nothing to publish. An entry that is neither a DNS name nor a `*.` wildcard is `Unsupported`.
- **Accepted by a Gateway:** the route needs a `status.parents` entry with `Accepted=True` whose parent is a Gateway (group `gateway.networking.k8s.io` or unset, kind `Gateway` or unset). Without one, every hostname is skipped with `NotAdmitted`.
- **Targets:** the union of `status.addresses` of every accepted parent Gateway, deduplicated. An `IPAddress` becomes an A or AAAA target and a `Hostname` becomes a CNAME; other address types are ignored. The Gateway is looked up in the parentRef's namespace (the route's namespace when unset), and one that does not exist contributes nothing. Accepted Gateways with no usable address give `NoTarget`. A `target` annotation replaces the computed targets, and `target.mode` filters them.

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: shop
  namespace: team-a
spec:
  parentRefs:
    - name: public
      namespace: gateways
  hostnames:
    - shop.apps.example.com
    - "*.shop.apps.example.com"
  rules:
    - backendRefs:
        - name: shop
          port: 8080
```

With a policy that lists `HTTPRoute` and a zone for `apps.example.com.`, the policy generates Records for `shop` and `*.shop` pointing at the addresses of the `gateways/public` Gateway, once that Gateway reports the route as accepted.

Changes to an HTTPRoute wake the policies in its namespace that list `HTTPRoute`; changes to any Gateway wake every policy that lists it.

## Source Annotations

All annotations are prefixed with `uddi.infoblox.com/`. The policy's `target.mode` and `target.addresses` apply by default; source annotations override on a per-source basis.

### `publish: "false"`

Excludes the source from publication. Emits a `Skipped` event with reason `Excluded`.

```yaml
metadata:
  annotations:
    uddi.infoblox.com/publish: "false"
```

### `hostname`

Adds or overrides hostnames (comma-separated). Used to publish a Route or Ingress at multiple names, or to give a Service a hostname.

```yaml
metadata:
  annotations:
    uddi.infoblox.com/hostname: "web.apps.example.com, alt.apps.example.com"
```

### `ttl`

Overrides the policy's default TTL (in seconds). Must be a valid integer.

```yaml
metadata:
  annotations:
    uddi.infoblox.com/ttl: "600"
```

### `target`

Overrides the policy's target with a comma-separated list of addresses (IPv4 or IPv6) and/or canonical names (FQDNs). IPs become A/AAAA records; FQDNs become CNAME records.

```yaml
metadata:
  annotations:
    uddi.infoblox.com/target: "192.0.2.20, api.example.com."
```

A malformed annotation value emits a `Skipped` event with reason `InvalidAnnotation`.

These annotations are read on every source kind, the new ones included (`Node`, `DNSRecord` and `HTTPRoute`). The `hostname` annotation adds names to what the source already provides; for a Service, which has no name of its own, it is the only source of hostnames.

## external-dns Compatibility

Teams that already label their objects for [external-dns](https://github.com/kubernetes-sigs/external-dns) can move to the operator without relabelling. Switch it on per policy:

```yaml
spec:
  compatibility:
    externalDNS: true
```

With it on, three external-dns annotations are read as fallbacks, on every source kind:

| external-dns annotation | Read as | Notes |
| --- | --- | --- |
| `external-dns.alpha.kubernetes.io/hostname` | `uddi.infoblox.com/hostname` | Comma-separated hostnames. |
| `external-dns.alpha.kubernetes.io/ttl` | `uddi.infoblox.com/ttl` | An integer number of seconds (`300`) or a duration (`5m`, `1h30m`), as external-dns allows. A duration is truncated to whole seconds and must come to at least one. |
| `external-dns.alpha.kubernetes.io/target` | `uddi.infoblox.com/target` | Comma-separated; IPs become A/AAAA, names become CNAME, and they replace the computed targets. |

The `uddi.infoblox.com/` annotation wins whenever both are present, annotation by annotation: the two values are never merged. A malformed external-dns value gives `InvalidAnnotation`, and the message names the external-dns key. No other external-dns annotation is read, and there is no external-dns counterpart of `publish: "false"`.

A Route that only carries the external-dns annotations keeps publishing after the policy turns the switch on:

```yaml
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: legacy
  annotations:
    external-dns.alpha.kubernetes.io/hostname: legacy.apps.example.com
    external-dns.alpha.kubernetes.io/ttl: "5m"
spec:
  host: legacy.apps.example.com
  to:
    kind: Service
    name: legacy
```

The record gets a 300-second TTL. Once every object carries the `uddi.infoblox.com/` annotations, switch compatibility off again.

## Wildcard Hostnames

A hostname that starts with `*.` is published as a wildcard Record. Wildcards arrive from an HTTPRoute's `spec.hostnames`, a DNSRecord's `spec.dnsName` (the router wildcard), an Ingress rule host, or the `hostname` annotation of any source (one leading `*.` label is accepted). Targets can never be wildcards.

Zone matching ignores the wildcard label: the labels after `*.` are matched against the `zoneRefs` fqdns like any other hostname, and the longest matching zone wins. The Record's name in the zone is `*` when those labels are the zone itself, and `*.<labels>` below it:

| Hostname | Zone fqdn | Record name in zone |
| --- | --- | --- |
| `*.apps.ocp1.example.com` | `apps.ocp1.example.com.` | `*` |
| `*.apps.ocp1.example.com` | `ocp1.example.com.` | `*.apps` |
| `*.shop.apps.example.com` | `apps.example.com.` | `*.shop` |

Because the name is `*` or `*.<labels>`, never `@`, a wildcard never lands on the zone apex and the `ApexCNAME` rule does not apply to it. A wildcard hostname that matches no zone is skipped with `NoMatchingZone`.

## Dual-Stack Targets

A source that exposes both an IPv4 and an IPv6 address yields one A Record and one AAAA Record for each hostname. This holds for Ingresses and LoadBalancer Services whose `status.loadBalancer.ingress` lists both addresses, for Nodes with both address families, for HTTPRoutes whose Gateway reports both, and for a Route whose `target.addresses` mixes the families.

- The A and AAAA Records are separate Record CRs with the same name in the zone; their CR names differ because the hash covers the type and the target.
- `target.mode: A` keeps both, since it means "address targets", not "IPv4 only".
- When `createPtr` is set, both the A and the AAAA Record carry it.
- A load balancer that reports IP addresses is published by address; its hostname is used (as a single CNAME) only when it reports no IP at all.

If the IPv6 address shows up later, the policy adds the AAAA Record and leaves the A Record alone.

## PTR Records with createPtr

`spec.createPtr: true` sets `createPtr: true` on every A and AAAA Record the policy generates; CNAME Records never carry it. The Record controller passes the `create_ptr` option when it creates the DNS record in Universal DDI, and Universal DDI creates the matching PTR record.

```yaml
spec:
  sources:
    - Node
  createPtr: true
```

The option belongs to the create call, so turning it on for a policy whose Records already exist does not change those Records. The usual use is the Node source, which gives every Node an A record and its reverse entry; host registration stays an IPAM and DHCP concern (see [docs/patterns.md](patterns.md)).

## Zones by ID

A `zoneRefs` entry names a zone in one of two ways:

```yaml
spec:
  zoneRefs:
    - name: apps-zone                                        # an AuthZone CR in this namespace
    - id: dns/auth_zone/2c1a9f0e-5b7d-4c9a-a1e2-3d4f5a6b7c8d   # a Universal DDI zone, addressed directly
      fqdn: apps.example.com.
```

The rules, which the CRD enforces:

- Exactly one of `name` or `id` is set (`exactly one of name or id must be set`).
- `fqdn` is required with `id` and valid only with `id` (`fqdn is required with id, and only valid with id`). It is a DNS name with or without the trailing dot.

How a zone given by id behaves:

- **Matching** uses the `fqdn` from the spec, lower-cased with a trailing dot added when missing. It must equal the zone's real fqdn; the operator does not check it. Zones by name and by id compete in one list and the longest matching fqdn wins.
- **It is always ready and resolved.** The policy does not read anything for it: no AuthZone CR is needed, and `ZoneNotReady` cannot happen for it. A policy with at least one zone by id never reports `ZonesNotReady`, and the `ZonesResolved` condition counts the entry as found. Only entries by `name` can make `ZonesResolved` go `False/ZoneNotFound`.
- **A wrong id shows late.** Nothing validates the id when the policy reconciles, so a mistyped id only shows when the generated Records fail to sync: each Record reports the API error on its own `Ready` condition and the policy stays `Ready=False/RecordsNotReady`.
- **Records reference the zone as `zoneRef.id`** instead of `zoneRef.name`.
- **Moving a policy between the two forms replaces Records.** A Record's `zoneRef` and its name in the zone cannot change in place. When a desired Record keeps its CR name but lands in another zone or under another name (for example a zoneRef moved from an AuthZone name to the zone id, or a longer zone now matches), the policy deletes the old Record and creates its replacement. The Record's `lifecycle.deletionPolicy` decides what happens to the DNS record in between, so with `Delete` there is a short gap.
- There is no CR behind a zone given by id, so the policy has nothing to wait for or watch: the zone is taken as given.

### Publishing from many namespaces into one zone

Policies only see objects in their own namespace, and Universal DDI objects are adopted once. The zone is therefore adopted in a platform namespace, and every team references it by id and fqdn. First the platform namespace:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: AuthZone
metadata:
  name: apps-zone
  namespace: uddi-system
spec:
  fqdn: apps.example.com.
  viewRef:
    name: default
  primaryType: cloud
  lifecycle:
    adoptExisting: true
    deletionPolicy: Orphan
```

Read the id once the zone is ready:

```bash
kubectl get authzone apps-zone -n uddi-system -o jsonpath='{.status.id}'
# dns/auth_zone/2c1a9f0e-5b7d-4c9a-a1e2-3d4f5a6b7c8d
```

Each team namespace then has its own Connection (Records use the Connection of their own namespace) and a policy that names the zone by id:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: DNSPublishPolicy
metadata:
  name: public
  namespace: team-a
spec:
  connectionRef:
    name: default
  sources:
    - Route
    - Ingress
  zoneRefs:
    - id: dns/auth_zone/2c1a9f0e-5b7d-4c9a-a1e2-3d4f5a6b7c8d
      fqdn: apps.example.com.
  tags:
    team: team-a
---
apiVersion: uddi.infoblox.com/v1alpha1
kind: DNSPublishPolicy
metadata:
  name: public
  namespace: team-b
spec:
  connectionRef:
    name: default
  sources:
    - Route
    - Ingress
  zoneRefs:
    - id: dns/auth_zone/2c1a9f0e-5b7d-4c9a-a1e2-3d4f5a6b7c8d
      fqdn: apps.example.com.
  tags:
    team: team-b
```

Do not adopt the zone again in `team-a` or `team-b`. `lifecycle.adoptExisting` only takes over an object that no other CR manages, so a second AuthZone CR for the same zone is refused with `Ready=False/AlreadyExists` and a message that names the first owner (for example `uddi-system/AuthZone/apps-zone`). Without that guard, two CRs would keep rewriting the zone's ownership tags, and deleting either could remove a zone the other still uses. Referencing the zone by id is the supported way to share it. To hand a zone over to another CR instead, remove its `uddi_operator_*` tags in Universal DDI.

## Generated Record Naming and Labeling

The policy generates Record CRs with names based on a hash of the source and target, so they are stable and unique:

```
<policy-name>-<10 hex digits of sha256(Kind|source|hostname|type|target)>
```

Example: `public-a1b2c3d4e5` for a Route named `web` publishing to `web.apps.example.com` as an A record.

Generated Records carry these labels (used for listing and filtering):

| Label | Value |
| --- | --- |
| `uddi.infoblox.com/policy` | Policy name (e.g., `public`) |
| `uddi.infoblox.com/source-kind` | `Route`, `Ingress`, `Service`, `Node`, `DNSRecord`, or `HTTPRoute` |
| `uddi.infoblox.com/source-name` | Source object name (see below for long names) |

And these annotations:

| Annotation | Value |
| --- | --- |
| `uddi.infoblox.com/hostname` | The published hostname |
| `uddi.infoblox.com/target` | The target (address or CNAME) |

Label values are limited to 63 characters, so two rules keep the labels valid:

- **The policy name is limited to 63 characters.** It is a label value on every generated Record. The CRD rejects a longer name with the message `the policy name becomes a label value on every generated Record, so it is limited to 63 characters`.
- **Long source names are shortened in the label.** A source name can be up to 253 characters. When it does not fit, the `source-name` label holds its first characters followed by `-` and 10 hex digits of the name's sha256, 63 characters at most. The full name stays in the `uddi_operator_source` tag of the DNS record (`Node/<name>` for the cluster-scoped Node source, `<Kind>/<namespace>/<name>` for the others).

Each Record's comment reads `Published by DNSPublishPolicy <namespace>/<policy> from <Kind> <name>`.

List all records generated by a policy:

```bash
kubectl get records -l uddi.infoblox.com/policy=public -n my-team -o wide

# List records from a specific source kind
kubectl get records -l "uddi.infoblox.com/policy=public,uddi.infoblox.com/source-kind=Route" -n my-team
```

## Status Fields and Skip Reasons

The policy's `status` reports publication progress:

| Field | Type | Description |
| --- | --- | --- |
| `published` | int32 | Number of desired Records currently applied |
| `ready` | int32 | Of those, how many have `Ready=True` |
| `skipped` | []Skip | Sources or hostnames not published (bounded to 50 entries) |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from |
| `conditions` | []metav1.Condition | `Ready` condition (summary) |

The `Ready` condition:
- `True/Published`: All desired Records are Ready and no `ZonesNotReady` skip remains
- `False/RecordsNotReady`: Some desired Records have `Ready=False` (still being created)
- `False/ZonesNotReady`: No zone reference has `status.id` yet

Each Skip entry has:

| Field | Description |
| --- | --- |
| `source` | "Kind/name" of the source object |
| `hostname` | The hostname that could not be published (empty if the whole source is skipped) |
| `reason` | Why it was skipped |
| `message` | Additional context |

### Skip Reasons and Remediation

| Reason | Cause | What to Do |
| --- | --- | --- |
| `Excluded` | The source carries `uddi.infoblox.com/publish: "false"`. | Remove the annotation to publish. |
| `NoMatchingZone` | No `zoneRef` FQDN is a suffix of the hostname (after lowercasing and adding a trailing dot). | Add a `zoneRef` whose `spec.fqdn` matches or is a suffix of the hostname, e.g., for `web.apps.example.com`, add a ref to a zone with `spec.fqdn: apps.example.com.` |
| `ZoneNotReady` | The matching AuthZone CR has no `status.id` yet (the zone is pending). | Wait for the AuthZone to become Ready; check `kubectl describe authzone <zone-name>`. |
| `NoTarget` | No target could be derived for the policy's `target.mode` (e.g., mode is CNAME but the source yields only IPs). | Change the policy's mode to `Auto`, or add a `target` annotation to the source with a compatible value. |
| `NoHostname` | A LoadBalancer Service lacks the `uddi.infoblox.com/hostname` annotation (and no default hostname applies). | Add `uddi.infoblox.com/hostname: "svc.apps.example.com"` to the Service. |
| `NotAdmitted` | A Route has no admitted ingress (no router has picked it up yet). | Check the Route's status condition; typically a hostname conflict or other admission issue. Resolve and the policy will retry. |
| `NotLoadBalancer` | A Service is not type `LoadBalancer` and carries no `uddi.infoblox.com/hostname` annotation. | Set the Service's `spec.type: LoadBalancer` or add the hostname annotation. |
| `ApexCNAME` | The zone apex cannot be a CNAME (DNS rule: apex must be A/AAAA). | Use mode `A` with address targets for the apex, or publish at a subdomain instead. |
| `Unsupported` | A Route uses `spec.subdomain` without `spec.host` (not yet supported). | Use `spec.host` instead, or publish via an Ingress. |
| `InvalidAnnotation` | A malformed `ttl` or `target` annotation (e.g., `ttl: "abc"` or an invalid address). | Fix the annotation value. |

### The Same Reasons for the Node, DNSRecord and HTTPRoute Sources

The new sources add no skip reasons. They reuse the ones above, and the entry's `source` reads `Node/<name>`, `DNSRecord/<name>` or `HTTPRoute/<name>`:

| Reason | Source | Cause |
| --- | --- | --- |
| `NoHostname` | Node | The Node name has no dot and there is no `hostname` annotation. Annotate the Node with a fully qualified name. |
| `NoHostname` | DNSRecord | The DNSRecord has no `spec.dnsName` and no `hostname` annotation. |
| `NoHostname` | HTTPRoute | The route has no `spec.hostnames` and no `hostname` annotation. |
| `NotAdmitted` | HTTPRoute | No parent Gateway has `Accepted=True` for the route yet. Check `kubectl get httproute <name> -o yaml` for the parent's conditions. |
| `NoTarget` | Node | The Node has no address of `nodeAddressType` (it may not be Ready yet), or `target.mode: CNAME` filtered the addresses out. |
| `NoTarget` | DNSRecord | `target.mode` filtered every target out, for example mode `CNAME` for an `A` record. |
| `NoTarget` | HTTPRoute | The accepted Gateways report no usable address in `status.addresses`, or `target.mode` filtered them out. |
| `Unsupported` | DNSRecord | `spec.recordType` is not `A`, `AAAA` or `CNAME`, `spec.dnsName` is not a valid DNS name, or a target does not fit the record type. |
| `Unsupported` | HTTPRoute | An entry of `spec.hostnames` is neither a DNS name nor a `*.` wildcard. |
| `InvalidAnnotation` | any | A malformed `uddi.infoblox.com/` annotation, or with `compatibility.externalDNS` a malformed external-dns annotation; the message names the key. |

The `ZonesResolved` condition sits next to `Ready`. It is `True/ZonesFound` while every `zoneRefs` entry by `name` names an existing AuthZone CR (entries by `id` always count as found) and `False/ZoneNotFound`, with the missing names in the message, otherwise. It never changes `Ready`.

Example: view skipped sources and troubleshoot:

```bash
kubectl describe dnspublishpolicy public -n my-team | grep -A 20 "Skipped:"

# Status:
#   Skipped:
#     - Source:    Route/legacy
#       Hostname:  legacy.old.example.com
#       Reason:    NoMatchingZone
#       Message:   No zoneRef is a suffix of the hostname
#     - Source:    Service/api
#       Hostname:  api.apps.example.com
#       Reason:    NotLoadBalancer
#       Message:   Service is not type LoadBalancer and has no hostname annotation
```

## Deletion and Cleanup

### Policy Deletion

When you delete a DNSPublishPolicy:

1. The policy's controller stops reconciling
2. All Record CRs owned by the policy are garbage-collected (removed from Kubernetes)
3. Each Record's finalizer removes the corresponding DNS record from Universal DDI
4. The policy CR is deleted

The entire DNS record lifecycle is automatic; you do not need to clean up records manually.

### Orphan Policy

To leave the DNS records in Universal DDI when you delete the policy, set:

```yaml
spec:
  recordLifecycle:
    deletionPolicy: Orphan
```

Then:
- Record CRs are garbage-collected
- Records' finalizers are removed without deleting from Universal DDI
- The DNS records remain with their ownership tags in Universal DDI

Re-create the policy later and set `recordLifecycle.adoptExisting: true` to re-adopt the orphaned records.

## Limitations

### Wildcard Router Record Without a DNSRecord

The ingress operator's default wildcard (`*.apps.<cluster>`) is published by the [DNSRecord source](#dnsrecord-sources-the-router-wildcard) wherever the ingress operator creates a `DNSRecord` for it. Some clusters have none, typically bare-metal setups where the ingress operator has no DNS zone to manage, and then there is nothing for the source to read. On those clusters a hand-written Record CR still covers wildcard DNS:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Record
metadata:
  name: apps-wildcard
spec:
  zoneRef:
    name: apps-zone
  name: "*"
  type: A
  rdata:
    a:
      address: 192.0.2.10
  ttl: 300
  comment: Wildcard for *.apps.example.com
  tags:
    managed-by: manual
```

Or if the router exposes a canonical hostname:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Record
metadata:
  name: apps-wildcard
spec:
  zoneRef:
    name: apps-zone
  name: "*"
  type: CNAME
  rdata:
    cname:
      cname: router-default.apps.example.com.
  ttl: 300
  comment: Wildcard CNAME for *.apps.example.com
```

### Route API Not Available on Plain Kubernetes

DNSPublishPolicy watches the OpenShift Route API (`route.openshift.io/v1`). On a plain Kubernetes cluster (kind, minikube, etc.) without OpenShift, the Route API is not registered and the Route source is inert (a warning is logged at startup). Ingresses and LoadBalancer Services continue to work.

### Other Optional APIs

The same holds for the two APIs behind the newer sources. The manager checks once at startup, with discovery, whether the cluster serves the ingress operator's `DNSRecord` API and the Gateway API (`Gateway` and `HTTPRoute`). When one is missing it logs `the DNSRecord API is not served, so the DNSRecord source of DNSPublishPolicy is inert` (or the HTTPRoute equivalent) and that source publishes nothing, without errors or skip entries. Installing the API later takes effect after the manager restarts. The Node source needs no optional API.

### Namespaces the Operator Watches

If the operator runs with `WATCH_NAMESPACE` set, the DNSRecord source can only read a namespace the operator watches. The default `openshift-ingress-operator` is usually outside a single-namespace install, so add it to the watched namespaces; see [DNSRecord Sources](#dnsrecord-sources-the-router-wildcard). Nodes are cluster-scoped and are not affected.

### Record Replacement Has a Gap

A Record cannot move to another zone or name in place. When a policy change moves one (see [Zones by ID](#zones-by-id)), the old Record is deleted and recreated, and with `deletionPolicy: Delete` the DNS record is briefly absent.

---

## Reference: More Examples

### Publish an Ingress with Multiple Hostnames

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: multi-host
spec:
  rules:
    - host: api.apps.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: api
                port:
                  number: 443
    - host: alt-api.apps.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: api
                port:
                  number: 443
```

The policy generates one Record per hostname.

### Publish a Service at a Static Address

```yaml
apiVersion: v1
kind: Service
metadata:
  name: lb-svc
  annotations:
    uddi.infoblox.com/hostname: "lb.apps.example.com"
    uddi.infoblox.com/target: "192.0.2.10"
spec:
  type: LoadBalancer
  selector:
    app: lb
  ports:
    - port: 443
      targetPort: 8443
```

Combined with policy mode `Auto`, this creates an A record to `192.0.2.10`.

### Use Different TTLs Per Source

```yaml
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: short-ttl
  annotations:
    uddi.infoblox.com/ttl: "120"
spec:
  host: short.apps.example.com
  to:
    kind: Service
    name: app
```

This Route's record gets a 120-second TTL, overriding the policy's default.
