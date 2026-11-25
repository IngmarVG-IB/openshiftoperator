# Usage Guide

This guide covers common tasks with the `uddi-operator`. Assumes a running OpenShift cluster and access to a Universal DDI tenant with an API key.

## Prerequisites: Create the API Key Secret and Connection

Every namespace needs a Secret holding the API key and a Connection CR. The Connection reads the Secret, verifies the key with the API, and provides credentials to all object-layer CRs in the namespace.

### Create the Secret

First, obtain your API key from the Infoblox Cloud Services Portal. Then create the Secret in your namespace:

```bash
kubectl -n <namespace> create secret generic uddi-api-key --from-literal=apiKey=<your-key>
```

The Secret name and data key are configurable; the defaults are `uddi-api-key` and `apiKey`.

### Create the Connection

Create a Connection named `default`. If you use a different name, object-layer CRs must reference it with `spec.connectionRef.name`.

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Connection
metadata:
  name: default
  namespace: <namespace>
spec:
  cspURL: https://csp.infoblox.com
  secretRef:
    name: uddi-api-key
    key: apiKey
  clientName: uddi-operator
  defaultTags:
    env: prod
  resyncInterval: 10m
  rateLimit:
    requestsPerSecond: 10
    burst: 20
```

Apply it:

```bash
kubectl apply -f - <<EOF
apiVersion: uddi.infoblox.com/v1alpha1
kind: Connection
metadata:
  name: default
spec:
  secretRef:
    name: uddi-api-key
EOF
```

Check that it is ready:

```bash
kubectl get connection default
# Output:
# NAME      READY   ENDPOINT                    VERIFIED            AGE
# default   True    https://csp.infoblox.com   2026-09-14T...     5s
```

---

## Manage DNS

### Create a View

A View is a named DNS namespace. Most operators use the default view, but you can create more:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: View
metadata:
  name: default
spec:
  name: default
```

To bind IP spaces for reverse lookups, add them to `ipSpaces`:

```yaml
spec:
  ipSpaces:
    - name: corp-space
```

Check status:

```bash
kubectl describe view default
```

### Adopt an Existing AuthZone

Most zones already exist in Universal DDI. Adopt them with `lifecycle.adoptExisting: true` and typically `lifecycle.deletionPolicy: Orphan` (so deleting the CR leaves the zone in place).

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: AuthZone
metadata:
  name: example-com
spec:
  fqdn: example.com.
  viewRef:
    name: default
  primaryType: cloud
  lifecycle:
    adoptExisting: true
    deletionPolicy: Orphan
```

The operator finds the zone by its FQDN and view, adds its ownership tags, emits an `Adopted` event and reports `Ready=True` with reason `Synced`.

### Create a New AuthZone

To create a new zone, omit `lifecycle.adoptExisting` (defaults to false) and typically use `lifecycle.deletionPolicy: Delete`:

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
  comment: Applications zone
```

### Create DNS Records

Create records one rdata per CR. Multiple A records for the same name are separate CRs. Only records with `source: STATIC` are managed by the operator.

**A record (explicit IP):**

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Record
metadata:
  name: www-a
spec:
  zoneRef:
    name: apps-zone
  name: www
  type: A
  rdata:
    a:
      address: 192.0.2.10
  ttl: 300
```

**Apex record (@ or empty name):**

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Record
metadata:
  name: apex
spec:
  zoneRef:
    name: apps-zone
  name: ""
  type: A
  rdata:
    a:
      address: 192.0.2.1
  ttl: 300
```

**CNAME record:**

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Record
metadata:
  name: app-cname
spec:
  zoneRef:
    name: apps-zone
  name: app
  type: CNAME
  rdata:
    cname:
      cname: www.apps.example.com.
  ttl: 300
```

**TXT record:**

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Record
metadata:
  name: spf
spec:
  zoneRef:
    name: apps-zone
  name: ""
  type: TXT
  rdata:
    txt:
      text: "v=spf1 -all"
  ttl: 300
```

**MX record:**

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Record
metadata:
  name: mx
spec:
  zoneRef:
    name: apps-zone
  name: ""
  type: MX
  rdata:
    mx:
      preference: 10
      exchange: mail.example.com.
  ttl: 300
```

**CAA record:**

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Record
metadata:
  name: caa
spec:
  zoneRef:
    name: apps-zone
  name: ""
  type: CAA
  rdata:
    caa:
      flags: 0
      tag: issue
      value: "letsencrypt.org"
  ttl: 300
```

Create the PTR record automatically for an A/AAAA record:

```yaml
spec:
  type: A
  rdata:
    a:
      address: 192.0.2.10
  createPtr: true
```

---

## Manage IPAM

### Create an IP Space

An IP space is the top-level container for addresses. Create one per isolated address partition:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPSpace
metadata:
  name: corp
spec:
  name: corp
  comment: Corporate address space
```

### Create an Address Block

Address blocks partition an IP space into subnets. You can allocate blocks explicitly or ask for the next available block:

**Explicit address block:**

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: AddressBlock
metadata:
  name: block-10
spec:
  spaceRef:
    name: corp
  address: 10.0.0.0
  cidr: 16
  comment: /16 block for corp
```

**Next-available block:**

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: AddressBlock
metadata:
  name: suballoc-1
spec:
  spaceRef:
    name: corp
  cidr: 18
  parentRef:
    name: block-10
```

The operator allocates the next available /18 from `block-10` and reports the allocated network in `status.address`.

### Create a Subnet

Subnets hold DHCP leases. Like blocks, they can be explicit or next-available:

**Explicit subnet:**

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Subnet
metadata:
  name: openshift-lb
spec:
  spaceRef:
    name: corp
  address: 10.0.10.0
  cidr: 24
  name: openshift-lb
  comment: LoadBalancer pool
```

**Next-available subnet:**

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Subnet
metadata:
  name: openshift-lb
spec:
  spaceRef:
    name: corp
  cidr: 24
  parentRef:
    name: block-10
```

Assign a DHCP service host:

```yaml
spec:
  dhcpHost: dhcp/host/<uuid>
```

### Create an Address Range

Ranges define pools for DHCP clients within a subnet:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Range
metadata:
  name: lb-pool
spec:
  spaceRef:
    name: corp
  start: 10.0.10.100
  end: 10.0.10.199
  name: lb-pool
  comment: LoadBalancer pool
  dhcpHost: dhcp/host/<uuid>
```

Exclude specific addresses from the pool:

```yaml
spec:
  start: 10.0.10.100
  end: 10.0.10.199
  exclusionRanges:
    - start: 10.0.10.150
      end: 10.0.10.159
      comment: Reserved for management
```

### Reserve an Explicit Address

Reserve a specific address:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Address
metadata:
  name: gateway
spec:
  spaceRef:
    name: corp
  address: 10.0.10.1
  names:
    - name: gateway.example.com
      type: user
  comment: Gateway IP
```

### Allocate the Next Available Address

Allocate from a subnet:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Address
metadata:
  name: svc-web
spec:
  spaceRef:
    name: corp
  nextAvailableFrom:
    subnetRef:
      name: openshift-lb
  names:
    - name: svc-web.team-a
      type: user
```

Or from a range:

```yaml
spec:
  nextAvailableFrom:
    rangeRef:
      name: lb-pool
```

Or from an address block:

```yaml
spec:
  nextAvailableFrom:
    addressBlockRef:
      name: block-10
```

**Read the allocated address:** After the Address CR is Ready, the allocated IP is in `status.address`:

```bash
kubectl get address svc-web -o jsonpath='{.status.address}'
# Output: 10.0.10.101
```

---

## Manage DHCP

### Create a Fixed Address

A fixed address reserves an IP for a specific DHCP client, identified by MAC address or other client identifier:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: FixedAddress
metadata:
  name: node-1
spec:
  spaceRef:
    name: corp
  address: 10.0.10.50
  matchType: mac
  matchValue: "52:54:00:aa:bb:cc"
  hostname: node-1.example.com
  comment: First cluster node
```

Match types:
- `mac`: MAC address (IPv4, IPv6)
- `client_text`: Client identifier as text (IPv4, IPv6)
- `client_hex`: Client identifier as hex (IPv4, IPv6)
- `relay_text`: Relay agent identifier as text (IPv4 only)
- `relay_hex`: Relay agent identifier as hex (IPv4 only)
- `duid`: DHCPv6 DUID (IPv6 only)

### Assign DHCP Options

DHCP options can be assigned at three levels: IP space, subnet/range, or fixed address. They cascade.

**IP space-level options:**

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPSpace
metadata:
  name: corp
spec:
  dhcpOptions:
    - type: option
      optionCode: dhcp/option_code/<uuid>
      optionValue: "10.0.10.1"
```

**Subnet-level options:**

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Subnet
metadata:
  name: openshift-lb
spec:
  dhcpOptions:
    - type: option
      optionCode: dhcp/option_code/<uuid>
      optionValue: "10.0.10.1"
```

**Range-level options:**

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Range
metadata:
  name: lb-pool
spec:
  dhcpOptions:
    - type: option
      optionCode: dhcp/option_code/<uuid>
      optionValue: "10.0.10.1"
```

**Fixed address-level options:**

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: FixedAddress
metadata:
  name: node-1
spec:
  dhcpOptions:
    - type: option
      optionCode: dhcp/option_code/<uuid>
      optionValue: "node-1.example.com"
```

Instead of an option code id, an entry can name its option code. The operator looks the name
up in the tenant's option codes through the Connection and sends the id:

```yaml
dhcpOptions:
  - type: option
    optionName: routers
    optionValue: "10.0.10.1"
  - type: option
    optionName: ntp-servers
    optionSpace: dhcp/option_space/<uuid>   # only needed when several option spaces define the name
    optionValue: "10.0.0.123"
```

Names are matched exactly, case included. Option codes are cached per Connection for 10
minutes, and a name that is not found triggers at most one refresh per minute, so a code
created in Universal DDI is picked up within about a minute. A name that does not resolve,
or that several option spaces define while `optionSpace` is empty, sets the object to
`Ready=False/DependencyNotReady` with the option name and spaces in the message, and the
object is retried. Universal DDI stores only the option code id, so the tenant UI shows ids
rather than names. The same fields work in every `dhcpOptions` list, including
`HostRegistrationPolicy.spec.register.dhcpOptions`.

To use an option group:

```yaml
dhcpOptions:
  - type: group
    group: dhcp/option_group/<uuid>
```

---

## Ownership Tags and Deletion Behaviour

### How Ownership Tags Work

Every object created or adopted by the operator carries four ownership tags:

| Tag | Value | Example |
| --- | --- | --- |
| `uddi_operator_managed` | `true` | — |
| `uddi_operator_cluster` | Cluster ID | `<ClusterVersion.spec.clusterID>` or `<kube-system namespace UID>` |
| `uddi_operator_owner` | CR location | `team-a/AuthZone/apps-zone` |
| `uddi_operator_uid` | Kubernetes UID | `550e8400-e29b-41d4-a716-446655440000` |

These tags guard destructive operations: delete and update paths compare the `uddi_operator_uid` tag before acting. If the tag matches, the operator owns the object and can manage it. If a tag mismatch is found, the operator logs a warning, emits an event, and orphans the object (leaves it in place).

In addition to ownership tags, the operator merges:
1. Connection's `spec.defaultTags` (middle priority)
2. CR's `spec.tags` (lowest priority)

So the effective tags are: ownership tags (fixed) > Connection defaults > CR-level tags.

### Tag Removal

When a tag key is removed from `Connection.spec.defaultTags` or `spec.tags`, the operator removes it from the Universal DDI object on the next reconcile. The operator tracks the keys it wrote at the last successful sync in `status.appliedTags`, and on the next sync it removes keys that were applied before but are no longer wanted.

Keys that the operator never wrote (for example, tags set by hand or by other systems) are always kept, so the operator does not claim exclusive ownership of the Universal DDI object's tags and will not delete tags it did not manage.

**Upgrade caveat:** The first sync after upgrading from a version that did not record `appliedTags` will record the keys but not remove any. If you drop a tag key from `spec.tags` in the same sync as the upgrade, that key stays on the object until removed by hand or by a second reconcile after the upgrade is complete.

### Deletion: Delete vs Orphan

The `spec.lifecycle.deletionPolicy` controls what happens when the CR is deleted:

**Delete (default):**

```yaml
lifecycle:
  deletionPolicy: Delete
```

When the CR is deleted, the operator deletes the matching Universal DDI object (guarded by the uid tag). If a tag mismatch is found, the object is orphaned instead.

**Orphan:**

```yaml
lifecycle:
  deletionPolicy: Orphan
```

When the CR is deleted, the operator removes the CR but leaves the Universal DDI object in place. The ownership tags remain, so a later CR can re-adopt it.

Typical patterns:
- **Adopted pre-existing zones:** Use `adoptExisting: true` and `deletionPolicy: Orphan` so the zone survives when the CR is deleted.
- **Newly created zones:** Use defaults (`adoptExisting: false`, `deletionPolicy: Delete`).
- **Allocated addresses:** Use `deletionPolicy: Delete` to release the address when the CR is deleted.

### Tag Mismatch Never Deletes

If a Universal DDI object carries no ownership tags (or tags from a different cluster or operator instance), the operator never deletes it, regardless of `deletionPolicy`. This prevents accidental deletion of unmanaged objects.

---

## Check Status

### Get Overview

```bash
kubectl get <kind> [<name>]
# Shows Ready status and other key fields

kubectl get connection
# NAME      READY   ENDPOINT                    VERIFIED
# default   True    https://csp.infoblox.com   2026-09-14T...

kubectl get authzone
# NAME         READY   FQDN                   ID
# apps-zone    True    apps.example.com.      dns/auth_zone/...
# example-com  True    example.com.           dns/auth_zone/...
```

### Describe an Object

```bash
kubectl describe <kind> <name>
# Shows spec, status, conditions, and events

kubectl describe authzone apps-zone
# Name:         apps-zone
# Namespace:    default
# Labels:       <none>
# Annotations:  <none>
# API Version:  uddi.infoblox.com/v1alpha1
# Kind:         AuthZone
# Spec:
#   Fqdn:      apps.example.com.
#   View Ref:
#     Name:    default
#   ...
# Status:
#   Conditions:
#     Last Transition Time:  2026-09-14T10:00:00Z
#     Message:               Successfully synced to Universal DDI
#     Observed Generation:   1
#     Reason:                Synced
#     Status:                True
#     Type:                  Ready
#   Id:                      dns/auth_zone/...
#   Last Sync Time:          2026-09-14T10:05:00Z
# Events:
#   Type    Reason   Age   Message
#   ----    ------   ---   -------
#   Normal  Created  5m    Created Universal DDI object dns/auth_zone/...
```

### Read Conditions

All object-layer CRs report **Ready** (summary) and **Synced** conditions. Ready is True when the object is successfully synced to Universal DDI. Synced is True when the object matches spec at the current observedGeneration.

```bash
kubectl get authzone apps-zone -o jsonpath='{.status.conditions[?(@.type=="Ready")]}'
# {
#   "lastTransitionTime": "2026-09-14T10:00:00Z",
#   "message": "Successfully synced to Universal DDI",
#   "observedGeneration": 1,
#   "reason": "Synced",
#   "status": "True",
#   "type": "Ready"
# }
```

### Watch for Status Changes

```bash
kubectl get authzone --watch
# Watch the Ready column update in real time as the operator reconciles
```

### Check the Universal DDI ID

The operator stores the Universal DDI resource id in `status.id`:

```bash
kubectl get authzone apps-zone -o jsonpath='{.status.id}'
# dns/auth_zone/b3f5c1a2-d9e8-4f2b-9a3c-5e7d6f8a1b2c
```

Use this to look up the object directly in Universal DDI APIs if needed.

### Verify an Address Allocation

For Address CRs with `nextAvailableFrom`, the allocated IP is in `status.address`:

```bash
kubectl get address svc-web -o jsonpath='{.status.address}'
# 10.0.10.101

# Or describe for full context
kubectl describe address svc-web
# Status:
#   Address:  10.0.10.101
#   Parent:   ipam/subnet/...
#   Space:    ipam/ip_space/...
#   State:    used
```

---

## Troubleshooting

### Object Not Ready

Check the Ready condition reason:

| Reason | Cause | What to Check |
| --- | --- | --- |
| `ConnectionNotReady` | The referenced Connection is missing or not verified. | `kubectl get connection <name>` — if missing, create it; if not Ready, check its status (`lastVerifiedTime`). Check Connection events for API errors. |
| `DependencyNotReady` | A referenced object (e.g., viewRef, spaceRef) has no `status.id` yet. | `kubectl get <kind> <name> -o jsonpath='{.status.id}'` on the referenced object. Reconciles are triggered when dependencies get an id; wait or re-apply the parent. |
| `AlreadyExists` | An untagged Universal DDI object exists under the natural key and `lifecycle.adoptExisting=false`. | Set `lifecycle.adoptExisting: true` to adopt the object, or delete it from Universal DDI if it is unmanaged. |
| `Creating` | The Universal DDI object is being created. | Wait; the operator retries on backoff. |
| `APIError` | A Universal DDI API call failed. | Check the condition message for details; usually a network or permission issue. Check the Connection Secret and API key. |
| `Deleting` | Deletion is blocked, e.g., by dependent objects in Universal DDI. | The message says what blocks it. Delete the blocking object first (e.g., records in a zone before deleting the zone). |
| `ConnectionMissing` | Not a condition but a Warning event: the CR was deleted while the Connection it names no longer existed, so Universal DDI could not be reached. The operator orphaned the object and removed the finalizer instead of blocking forever. | The Universal DDI object is still there with its ownership tags. Delete it by hand, or re-create the Connection and a CR with the same natural key to re-adopt it. Deleting a Connection before the objects that use it is what causes this. |

### Policy Status: ZoneNotFound (DNSPublishPolicy)

The DNSPublishPolicy has a second condition, `ZonesResolved`:

| Condition/Reason | Cause | What to Check |
| --- | --- | --- |
| `ZonesResolved=False/ZoneNotFound` | One or more `zoneRefs` name no existing AuthZone CR. | The message lists the missing zone names. Create the named AuthZone CRs, or fix the typo in `spec.zoneRefs`. This condition does not affect `Ready`, so the policy continues to publish records in zones that are found; only hostnames that would have matched the missing zones are skipped. |

### Check Connection Logs

```bash
# Get the manager pod
kubectl -n uddi-operator get pod

# View logs
kubectl -n uddi-operator logs -f <pod-name> --tail=50 | grep Connection

# Or watch all reconciles
kubectl -n uddi-operator logs -f <pod-name> | grep <namespace>/<kind>/<name>
```

### Verify API Key

Test the Secret and Connection:

```bash
kubectl get secret uddi-api-key -o jsonpath='{.data.apiKey}' | base64 -d
# (outputs the API key; verify it's correct)

kubectl describe connection default
# Check lastVerifiedTime and any error messages
```

### Check Events

```bash
kubectl describe <kind> <name>
# Shows the Events section at the bottom with recent reconcile messages
```

### Monitoring and Alerts

For metrics on API calls, rate limiting, retries, and alerts on Connection readiness, API errors, and pending allocations, see [docs/observability.md](observability.md).

### Adopted vs. Created

The reconcile event tells you whether the operator adopted or created the object:

```bash
kubectl describe authzone example-com
# Events:
#   Type    Reason   Age    Message
#   ----    ------   ---    -------
#   Normal  Adopted  5m     Adopted existing Universal DDI object dns/auth_zone/...
```

vs.

```bash
kubectl describe authzone apps-zone
# Events:
#   Type    Reason   Age    Message
#   ----    ------   ---    -------
#   Normal  Created  5m     Created Universal DDI object dns/auth_zone/...
```

### Natural Keys and Adoption

The operator looks up objects by natural key to support adoption:

| Kind | Natural Key |
| --- | --- |
| View | name |
| AuthZone | fqdn + view |
| Record | zone + name + type + rdata |
| IPSpace | name |
| AddressBlock | address + cidr + space |
| Subnet | address + cidr + space |
| Range | start + end + space |
| Address | address + space |
| FixedAddress | address + ip_space |

If `lifecycle.adoptExisting=true` and an untagged object matches the natural key, the operator adopts it (adds its tags). If `lifecycle.adoptExisting=false` (default), a match is reported as a conflict (`Ready=False/AlreadyExists`).

### Drift Detection

The operator reconciles each object at least once per `Connection.spec.resyncInterval` (default 10m) to detect drift. If the Universal DDI object has changed out-of-band, the operator reapplies the CR's spec. Check `status.lastSyncTime`:

```bash
kubectl get authzone apps-zone -o jsonpath='{.status.lastSyncTime}'
# 2026-09-14T10:05:00Z
```

If `lastSyncTime` is old and the object should be actively managed, the operator may be backoff-retrying. Check events and logs.
