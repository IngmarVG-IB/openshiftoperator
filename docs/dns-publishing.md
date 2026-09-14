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
| `uddi.infoblox.com/source-kind` | `Route`, `Ingress`, or `Service` |
| `uddi.infoblox.com/source-name` | Source object name |

And these annotations:

| Annotation | Value |
| --- | --- |
| `uddi.infoblox.com/hostname` | The published hostname |
| `uddi.infoblox.com/target` | The target (address or CNAME) |

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

### Wildcard Router Record

The operator does not yet publish the ingress operator's default wildcard route (`*.apps.<cluster>`). A hand-written Record CR covers wildcard DNS for now:

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
