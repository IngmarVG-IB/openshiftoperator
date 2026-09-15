# API Reference: uddi.infoblox.com/v1alpha1

This document describes the custom resources of the `uddi.infoblox.com/v1alpha1` API group. All kinds are namespaced, and the object-layer kinds embed shared fields defined below.

## Shared Types

### ConnectionRef

`ConnectionRef` names a Connection in the same namespace. An empty name defaults to the Connection called "default".

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `name` | string | No | — | Name of the Connection. Defaults to "default". Max 253 characters. |

### ObjectRef

`ObjectRef` points at another object-layer CR in the same namespace, or directly at a Universal DDI object by its API id (for example `dns/view/2c1a...`). Exactly one of `name` or `id` is set (CEL validation: `has(self.name) != has(self.id)`).

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `name` | string | No | — | Name of an object-layer CR in this namespace. Max 253 characters. |
| `id` | string | No | — | Universal DDI resource id, for example "dns/view/2c1a...". Max 256 characters. |

### Lifecycle

`Lifecycle` controls adoption and deletion behaviour.

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `adoptExisting` | bool | No | false | Allow taking over an existing Universal DDI object matching the natural key but without this operator's ownership tags. When false, such an object is reported as a conflict (`Ready=False/AlreadyExists`). |
| `deletionPolicy` | string | No | Delete | `Delete` (deletes the Universal DDI object, guarded by the ownership uid tag) or `Orphan` (leaves the Universal DDI object in place). |

### DHCPOption

`DHCPOption` maps to the Universal DDI `ipam.OptionItem`. Option codes and option groups are opaque Universal DDI resource ids in v1alpha1 (CEL validation: `self.type == 'option' ? has(self.optionCode) : has(self.group)`).

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `type` | string | No | option | `option` (a single DHCP option code plus value) or `group` (a DHCP option group reference). |
| `optionCode` | string | No (required if `type=option`) | — | Universal DDI option code id, for example "dhcp/option_code/<uuid>". Max 256 characters. |
| `optionValue` | string | No | — | Value for the option code. Max 4096 characters. |
| `group` | string | No (required if `type=group`) | — | Universal DDI option group id, for example "dhcp/option_group/<uuid>". Max 256 characters. |

### ObjectStatus

`ObjectStatus` is the status shared by every object-layer kind.

| Field | Type | Description |
| --- | --- | --- |
| `id` | string | Universal DDI resource id, for example "ipam/subnet/<uuid>". |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from. |
| `lastSyncTime` | *metav1.Time | When the Universal DDI object was last read or written. |
| `conditions` | []metav1.Condition | Conditions: `Ready` (summary) and `Synced` (object matched spec at observedGeneration). |

### Common Conditions and Reasons

All object-layer kinds report these conditions:

**Ready** (summary condition)
- `True/Synced`: The Universal DDI object matches spec.
- `False/ConnectionNotReady`: The referenced Connection is missing or not verified.
- `False/DependencyNotReady`: A referenced object-layer CR has no `status.id` yet.
- `False/AlreadyExists`: An untagged object exists under the natural key and `lifecycle.adoptExisting=false`.
- `False/Creating`: The Universal DDI object is being created.
- Adoption of an existing Universal DDI object is reported through an `Adopted` event; the condition then reads `True/Synced` like a created object.
- `False/APIError`: A Universal DDI API call failed.
- `False/Deleting`: Deletion is in progress or blocked (e.g., by dependent objects in Universal DDI).

**Synced**
- `True`: The Universal DDI object matched `spec` at `observedGeneration`.
- `False`: Synced is false until the first successful reconcile writes both the id and matched status.

### Ownership Tags

Every object created or adopted by the operator carries these tags (the operator controls these; they cannot be overridden via `spec.tags`):

| Tag | Value |
| --- | --- |
| `uddi_operator_managed` | `true` |
| `uddi_operator_cluster` | Cluster ID (from `ClusterVersion.spec.clusterID` or the `kube-system` namespace UID) |
| `uddi_operator_owner` | `<namespace>/<Kind>/<name>` |
| `uddi_operator_uid` | Kubernetes UID of the CR |

The operator also merges `Connection.spec.defaultTags`, then `spec.tags` in order, so the effective tags are: ownership tags (fixed), Connection defaults (middle), CR-level tags (lowest priority).

---

## Connection

A Connection holds the credentials and client settings for one Universal DDI tenant. Every object-layer CR in a namespace uses the Connection named "default" if its `connectionRef` is empty.

**Purpose:** Create one Connection per namespace to establish API access to a Universal DDI tenant. The Connection reads an API key from a Secret, verifies the key with a cheap read call, caches the client, and detects key rotation by watching the Secret's resourceVersion. Multiple Connections in a namespace support multi-tenancy or sandboxing.

### Connection Spec

| Field | Type | Required | Default | Immutable | Description |
| --- | --- | --- | --- | --- | --- |
| `cspURL` | string | No | https://csp.infoblox.com | No | Cloud Services Portal endpoint. Max 2048 characters. |
| `secretRef.name` | string | Yes | — | No | Name of the Secret holding the API key. Max 253 characters. |
| `secretRef.key` | string | No | apiKey | No | Data key in the Secret. Max 253 characters. |
| `clientName` | string | No | uddi-operator | No | Client name reported to the API and shown in audit logs. Max 128 characters. |
| `defaultTags` | map[string]string | No | — | No | Tags merged into every object created through this Connection (below ownership tags, above CR-level tags). |
| `resyncInterval` | *metav1.Duration | No | 10m | No | Drift-detection period for objects using this Connection. |
| `rateLimit.requestsPerSecond` | int32 | No | 10 | No | Sustained rate for client-side token bucket. Min 1, max 1000. |
| `rateLimit.burst` | int32 | No | 20 | No | Token bucket size. Min 1, max 1000. |

### Connection Status

| Field | Type | Description |
| --- | --- | --- |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from. |
| `lastVerifiedTime` | *metav1.Time | When the API key last passed the verification call. |
| `conditions` | []metav1.Condition | Conditions: `Ready` (summary). |

### Connection Conditions

**Ready**
- `True/Verified`: The API key passed the verification call.
- `False/SecretNotFound`: The referenced Secret does not exist.
- `False/SecretKeyMissing`: The Secret has no data under the configured key.
- `False/APIError`: The verification call failed.

### Connection Example

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Connection
metadata:
  name: default
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

---

## View

A View is a DNS view in Universal DDI. It is a named namespace for DNS records and authoritative zones. Reverse lookups are associated with IP spaces bound to the view.

**Purpose:** Create a View CR to model a Universal DDI DNS view. The operator reconciles the CR to create, adopt or update the view and reports its status. Views are typically adopted from existing zones using `lifecycle.adoptExisting=true` and `lifecycle.deletionPolicy=Orphan`.

### View Spec

| Field | Type | Required | Default | Immutable | Description |
| --- | --- | --- | --- | --- | --- |
| `connectionRef` | ConnectionRef | No | — | No | Connection providing credentials. Empty means "default". |
| `name` | string | No | — (defaults to metadata.name) | No | Name of the DNS view in Universal DDI. Max 256 characters. |
| `ipSpaces` | []ObjectRef | No | — | No | IPSpace CRs or Universal DDI ip space ids bound to the view for reverse lookups. |
| `comment` | string | No | — | No | Comment stored on the Universal DDI object. Max 1024 characters. |
| `disabled` | bool | No | false | No | Turns the view off in Universal DDI. |
| `tags` | map[string]string | No | — | No | Tags merged onto the Universal DDI object. |
| `lifecycle` | Lifecycle | No | — | No | Adoption and deletion behaviour. |

### View Status

| Field | Type | Description |
| --- | --- | --- |
| `id` | string | Universal DDI resource id, e.g. "dns/view/<uuid>". |
| `name` | string | View name observed in Universal DDI. |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from. |
| `lastSyncTime` | *metav1.Time | When the Universal DDI object was last read or written. |
| `conditions` | []metav1.Condition | Conditions: `Ready` and `Synced`. |

### View Example

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: View
metadata:
  name: default
spec:
  name: default
  ipSpaces:
    - name: corp
  comment: Default DNS view
```

---

## AuthZone

An AuthZone is an authoritative DNS zone in Universal DDI. It is served either by Universal DDI DNS hosts (primary_type: cloud) or by external nameservers (primary_type: external). Nameserver assignment is mid-migration in the API; v1alpha1 exposes only the legacy fields the SDK v0.4.0 carries: nameserver groups, internal secondaries, and external primaries/secondaries. The API rejects an NSG combined with internal secondaries, and rejects more than one NSG per zone (both enforced via CEL).

**Purpose:** Create an AuthZone CR to model a Universal DDI authoritative zone. Most zones pre-exist; adopt them with `lifecycle.adoptExisting=true` and typically `lifecycle.deletionPolicy=Orphan`. New zones are created with primary_type: cloud.

### AuthZone Spec

| Field | Type | Required | Default | Immutable | Description |
| --- | --- | --- | --- | --- | --- |
| `connectionRef` | ConnectionRef | No | — | No | Connection providing credentials. Empty means "default". |
| `fqdn` | string | Yes | — | Yes | Zone FQDN, trailing dot included. Max 256 characters. Immutable (CEL: `self == oldSelf`). |
| `viewRef` | ObjectRef | Yes | — | Yes | View CR or Universal DDI view id. Immutable (CEL: `self == oldSelf`). |
| `primaryType` | string | No | cloud | Yes | `cloud` (served by Universal DDI) or `external`. Immutable (CEL: `self == oldSelf`). |
| `nsgs` | []string | No | — | No | Nameserver group ids. Max 1 per zone. Max items 1. |
| `internalSecondaries` | []InternalSecondary | No | — | No | DNS hosts serving the zone (primaryType: cloud). Cannot be set together with nsgs (CEL: `!(has(self.nsgs) && size(self.nsgs) > 0 && has(self.internalSecondaries) && size(self.internalSecondaries) > 0)`). |
| `internalSecondaries[].host` | string | Yes | — | No | Universal DDI DNS host id, e.g. "dns/host/<uuid>". Max 256 characters. |
| `externalPrimaries` | []ExternalPrimary | No | — | No | Nameservers outside Universal DDI (primaryType: external). |
| `externalPrimaries[].type` | string | No | primary | No | `primary` or `nsg` (placeholder for NSG references). |
| `externalPrimaries[].address` | string | No | — | No | Nameserver IP address. Max 64 characters. |
| `externalPrimaries[].fqdn` | string | No | — | No | Nameserver FQDN, trailing dot included. Max 256 characters. |
| `externalPrimaries[].nsg` | string | No | — | No | NSG id this primary belongs to. Max 256 characters. |
| `externalPrimaries[].tsigEnabled` | bool | No | false | No | Enable TSIG for transfers from this primary. |
| `externalSecondaries` | []ExternalSecondary | No | — | No | Secondary nameservers outside Universal DDI. |
| `externalSecondaries[].address` | string | Yes | — | No | Nameserver IP address. Max 64 characters. |
| `externalSecondaries[].fqdn` | string | Yes | — | No | Nameserver FQDN, trailing dot included. Max 256 characters. |
| `externalSecondaries[].stealth` | bool | No | false | No | Keep the secondary out of the zone's NS records. |
| `externalSecondaries[].tsigEnabled` | bool | No | false | No | Enable TSIG for transfers to this secondary. |
| `comment` | string | No | — | No | Comment stored on the Universal DDI object. Max 1024 characters. |
| `disabled` | bool | No | false | No | Turns the zone off in Universal DDI. |
| `tags` | map[string]string | No | — | No | Tags merged onto the Universal DDI object. |
| `lifecycle` | Lifecycle | No | — | No | Adoption and deletion behaviour. |

### AuthZone Status

| Field | Type | Description |
| --- | --- | --- |
| `id` | string | Universal DDI resource id, e.g. "dns/auth_zone/<uuid>". |
| `fqdn` | string | Zone FQDN observed in Universal DDI, in protocol form. |
| `view` | string | Universal DDI view id the zone lives in. |
| `warnings` | []string | API warnings on the last create or update, as "<name>: <message>" lines. |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from. |
| `lastSyncTime` | *metav1.Time | When the Universal DDI object was last read or written. |
| `conditions` | []metav1.Condition | Conditions: `Ready` and `Synced`. |

### AuthZone Example

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

---

## Record

A Record is a DNS resource record in Universal DDI. Universal DDI stores one rdata per record object, so two A records for the same name are two separate CRs. Only records whose `source` is `STATIC` are managed by the operator; `SYSTEM`, `DYNAMIC`, `DELEGATED` and `DTC` records are reported as conflicts.

**Purpose:** Create a Record CR to model a DNS resource record (A, AAAA, CNAME, TXT, PTR, SRV, MX, NS, or CAA). The operator reconciles the CR to create, adopt or update the record in Universal DDI. Multiple Record CRs are needed for multiple records with the same name, or for multi-valued records.

### Record Spec

| Field | Type | Required | Default | Immutable | Description |
| --- | --- | --- | --- | --- | --- |
| `connectionRef` | ConnectionRef | No | — | No | Connection providing credentials. Empty means "default". |
| `zoneRef` | ObjectRef | Yes | — | Yes | AuthZone CR or Universal DDI auth zone id. Immutable (CEL: `self == oldSelf`). |
| `name` | string | No | — | Yes | Record name inside the zone. "@" or empty means the apex. Max 256 characters. Immutable (spec-level CEL, so adding or removing the field is rejected as well as changing it; the rule lives on the spec rather than the field because `optionalOldSelf` needs Kubernetes 1.30 and the OpenShift 4.16 floor is 1.29). |
| `type` | RecordType | Yes | — | Yes | Record type: A, AAAA, CNAME, TXT, PTR, SRV, MX, NS, or CAA. Immutable (CEL: `self == oldSelf`). |
| `rdata` | RecordRData | Yes | — | No | Exactly one typed rdata block matching `type` (CEL validation ensures this). |
| `rdata.a.address` | string | Yes (if type A) | — | No | IPv4 address. Max 45 characters. |
| `rdata.aaaa.address` | string | Yes (if type AAAA) | — | No | IPv6 address. Max 45 characters. |
| `rdata.cname.cname` | string | Yes (if type CNAME) | — | No | Canonical name, trailing dot included. Max 256 characters. |
| `rdata.txt.text` | string | No (if type TXT) | — | No | Character string. Max 4096 characters. |
| `rdata.ptr.dname` | string | Yes (if type PTR) | — | No | Target domain name, trailing dot included. Max 256 characters. |
| `rdata.srv.priority` | int32 | Yes (if type SRV) | — | No | Target host priority. 0–65535. |
| `rdata.srv.weight` | int32 | No (if type SRV) | 0 | No | Weight for records with same priority. 0–65535. |
| `rdata.srv.port` | int32 | Yes (if type SRV) | — | No | Target port. 0–65535. |
| `rdata.srv.target` | string | Yes (if type SRV) | — | No | Target host, trailing dot included. Max 256 characters. |
| `rdata.mx.preference` | int32 | Yes (if type MX) | — | No | Preference of this exchange. 0–65535. |
| `rdata.mx.exchange` | string | Yes (if type MX) | — | No | Exchange host, trailing dot included. Max 256 characters. |
| `rdata.ns.dname` | string | Yes (if type NS) | — | No | Nameserver name, trailing dot included. Max 256 characters. |
| `rdata.caa.flags` | int32 | No (if type CAA) | 0 | No | Flags byte. 0–255. |
| `rdata.caa.tag` | string | Yes (if type CAA) | — | No | Tag, e.g. "issue", "issuewild", "iodef". Max 64 characters. |
| `rdata.caa.value` | string | Yes (if type CAA) | — | No | Value for the tag. Max 1024 characters. |
| `ttl` | *int32 | No | — | No | TTL in seconds. Omitted means inherit from zone. 0–2147483647. |
| `createPtr` | *bool | No | false | No | Ask the API to create the matching PTR record (A/AAAA only; CEL: `!has(self.createPtr) \|\| !self.createPtr \|\| self.type == 'A' \|\| self.type == 'AAAA'`). |
| `comment` | string | No | — | No | Comment stored on the Universal DDI object. Max 1024 characters. |
| `disabled` | bool | No | false | No | Turns the record off in Universal DDI. |
| `tags` | map[string]string | No | — | No | Tags merged onto the Universal DDI object. |
| `lifecycle` | Lifecycle | No | — | No | Adoption and deletion behaviour. |

### Record Status

| Field | Type | Description |
| --- | --- | --- |
| `id` | string | Universal DDI resource id, e.g. "dns/record/<uuid>". |
| `absoluteName` | string | Fully qualified name observed in Universal DDI. |
| `source` | []string | API source: STATIC, SYSTEM, DYNAMIC, DELEGATED, or DTC. Only STATIC records are adopted, updated or deleted. |
| `zone` | string | Universal DDI auth zone id the record lives in. |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from. |
| `lastSyncTime` | *metav1.Time | When the Universal DDI object was last read or written. |
| `conditions` | []metav1.Condition | Conditions: `Ready` and `Synced`. |

### Record Example

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

---

## IPSpace

An IPSpace is an IP space in Universal DDI. IP spaces partition the address space for IPAM and DHCP management.

**Purpose:** Create an IPSpace CR to model a Universal DDI IP space. An IP space is the top-level container for address blocks, subnets, ranges and addresses. Create one for each isolated address partition.

### IPSpace Spec

| Field | Type | Required | Default | Immutable | Description |
| --- | --- | --- | --- | --- | --- |
| `connectionRef` | ConnectionRef | No | — | No | Connection providing credentials. Empty means "default". |
| `name` | string | No | — (defaults to metadata.name) | No | Name of the IP space in Universal DDI. Max 256 characters. |
| `comment` | string | No | — | No | Comment stored on the Universal DDI object. Max 1024 characters. |
| `dhcpOptions` | []DHCPOption | No | — | No | DHCP options applied to everything in the space. |
| `tags` | map[string]string | No | — | No | Tags merged onto the Universal DDI object. |
| `lifecycle` | Lifecycle | No | — | No | Adoption and deletion behaviour. |

### IPSpace Status

| Field | Type | Description |
| --- | --- | --- |
| `id` | string | Universal DDI resource id, e.g. "ipam/ip_space/<uuid>". |
| `name` | string | IP space name observed in Universal DDI. |
| `utilization` | *Utilization | Utilization breakdown (abandonUtilization, abandoned, dynamic, free, static, total, used, utilization percentages). |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from. |
| `lastSyncTime` | *metav1.Time | When the Universal DDI object was last read or written. |
| `conditions` | []metav1.Condition | Conditions: `Ready` and `Synced`. |

### IPSpace Example

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPSpace
metadata:
  name: corp
spec:
  name: corp
  comment: Corporate IP space
```

---

## AddressBlock

An AddressBlock is a contiguous block of IP addresses in Universal DDI, partitioned under an IPSpace. Blocks can be hierarchical; a child block is allocated from a parent block.

**Purpose:** Create an AddressBlock CR to model a Universal DDI address block. Blocks partition an IP space into subnets, ranges and individual addresses. Set `address` and `cidr` explicitly to manage a specific block, or omit `address` and set `parentRef` and `cidr` to allocate the next available block from the parent.

### AddressBlock Spec

| Field | Type | Required | Default | Immutable | Description |
| --- | --- | --- | --- | --- | --- |
| `connectionRef` | ConnectionRef | No | — | No | Connection providing credentials. Empty means "default". |
| `spaceRef` | ObjectRef | Yes | — | No | IPSpace CR or Universal DDI ip space id. |
| `address` | string | No | — | Yes | Block address, e.g. "10.0.0.0". Omit to allocate next available from parentRef. Max 45 characters. Immutable (spec-level CEL, so adding or removing the field is rejected as well as changing it; the rule lives on the spec rather than the field because `optionalOldSelf` needs Kubernetes 1.30 and the OpenShift 4.16 floor is 1.29). Requires `parentRef` when omitted (CEL: `has(self.address) \|\| has(self.parentRef)`). |
| `cidr` | int32 | Yes | — | Yes | CIDR prefix length. 0–128. Immutable (CEL: `self == oldSelf`). |
| `parentRef` | *ObjectRef | No (required if `address` omitted) | — | Yes | Parent AddressBlock CR or Universal DDI address block id. Required when address is omitted. Immutable (spec-level CEL, so adding or removing the field is rejected as well as changing it; the rule lives on the spec rather than the field because `optionalOldSelf` needs Kubernetes 1.30 and the OpenShift 4.16 floor is 1.29). |
| `name` | string | No | — | No | Block name in Universal DDI. Max 256 characters. |
| `comment` | string | No | — | No | Comment stored on the Universal DDI object. Max 1024 characters. |
| `dhcpOptions` | []DHCPOption | No | — | No | DHCP options applied to everything in the block. |
| `tags` | map[string]string | No | — | No | Tags merged onto the Universal DDI object. |
| `lifecycle` | Lifecycle | No | — | No | Adoption and deletion behaviour. |

### AddressBlock Status

| Field | Type | Description |
| --- | --- | --- |
| `id` | string | Universal DDI resource id, e.g. "ipam/address_block/<uuid>". |
| `address` | string | Block address observed in Universal DDI. For next-available blocks, this is the allocated network address. |
| `cidr` | int32 | CIDR prefix length observed in Universal DDI. |
| `space` | string | Universal DDI ip space id. |
| `utilization` | *Utilization | Utilization breakdown. |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from. |
| `lastSyncTime` | *metav1.Time | When the Universal DDI object was last read or written. |
| `conditions` | []metav1.Condition | Conditions: `Ready` and `Synced`. |

### AddressBlock Example

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
```

---

## Subnet

A Subnet is a contiguous block of IP addresses in Universal DDI allocated for DHCP. Subnets are derived from AddressBlocks and can host DHCP leases.

**Purpose:** Create a Subnet CR to model a Universal DDI subnet. Subnets are where DHCP clients get leases. Set `address` and `cidr` explicitly to manage a specific subnet, or omit `address` and set `parentRef` to allocate the next available subnet from the parent block. Assign a DHCP service host with `dhcpHost`.

### Subnet Spec

| Field | Type | Required | Default | Immutable | Description |
| --- | --- | --- | --- | --- | --- |
| `connectionRef` | ConnectionRef | No | — | No | Connection providing credentials. Empty means "default". |
| `spaceRef` | ObjectRef | Yes | — | No | IPSpace CR or Universal DDI ip space id. |
| `address` | string | No | — | Yes | Subnet address, e.g. "10.0.10.0". Omit to allocate next available from parentRef. Max 45 characters. Immutable (spec-level CEL, so adding or removing the field is rejected as well as changing it; the rule lives on the spec rather than the field because `optionalOldSelf` needs Kubernetes 1.30 and the OpenShift 4.16 floor is 1.29). Requires `parentRef` when omitted (CEL: `has(self.address) \|\| has(self.parentRef)`). |
| `cidr` | int32 | Yes | — | Yes | CIDR prefix length. 0–128. Immutable (CEL: `self == oldSelf`). |
| `parentRef` | *ObjectRef | No (required if `address` omitted) | — | Yes | Parent AddressBlock CR or Universal DDI address block id. Required when address is omitted. Immutable (spec-level CEL, so adding or removing the field is rejected as well as changing it; the rule lives on the spec rather than the field because `optionalOldSelf` needs Kubernetes 1.30 and the OpenShift 4.16 floor is 1.29). |
| `name` | string | No | — | No | Subnet name in Universal DDI. Max 256 characters. |
| `comment` | string | No | — | No | Comment stored on the Universal DDI object. Max 1024 characters. |
| `dhcpHost` | string | No | — | No | Universal DDI DHCP service host id, e.g. "dhcp/host/<uuid>". Max 256 characters. |
| `dhcpOptions` | []DHCPOption | No | — | No | DHCP options applied to everything in the subnet. |
| `disableDhcp` | bool | No | false | No | Stop Universal DDI serving DHCP from this subnet. |
| `tags` | map[string]string | No | — | No | Tags merged onto the Universal DDI object. |
| `lifecycle` | Lifecycle | No | — | No | Adoption and deletion behaviour. |

### Subnet Status

| Field | Type | Description |
| --- | --- | --- |
| `id` | string | Universal DDI resource id, e.g. "ipam/subnet/<uuid>". |
| `address` | string | Subnet address observed in Universal DDI. For next-available subnets, this is the allocated network address. |
| `cidr` | int32 | CIDR prefix length observed in Universal DDI. |
| `space` | string | Universal DDI ip space id. |
| `utilization` | *Utilization | Utilization breakdown. |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from. |
| `lastSyncTime` | *metav1.Time | When the Universal DDI object was last read or written. |
| `conditions` | []metav1.Condition | Conditions: `Ready` and `Synced`. |

### Subnet Example

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
  dhcpOptions: []
```

---

## Range

A Range is a contiguous range of addresses within a Subnet, used for DHCP pool allocation. The parent subnet is derived by Universal DDI from the start and end addresses.

**Purpose:** Create a Range CR to model a Universal DDI address range. Ranges define pools of addresses for DHCP clients. Assign a DHCP service host with `dhcpHost`, and exclude specific addresses with `exclusionRanges`.

### Range Spec

| Field | Type | Required | Default | Immutable | Description |
| --- | --- | --- | --- | --- | --- |
| `connectionRef` | ConnectionRef | No | — | No | Connection providing credentials. Empty means "default". |
| `spaceRef` | ObjectRef | Yes | — | No | IPSpace CR or Universal DDI ip space id. |
| `start` | string | Yes | — | Yes | Range start address, inclusive. Max 45 characters. Immutable (CEL: `self == oldSelf`). |
| `end` | string | Yes | — | Yes | Range end address, inclusive. Max 45 characters. Immutable (CEL: `self == oldSelf`). |
| `name` | string | No | — | No | Range name in Universal DDI. Max 256 characters. |
| `comment` | string | No | — | No | Comment stored on the Universal DDI object. Max 1024 characters. |
| `dhcpHost` | string | No | — | No | Universal DDI DHCP service host id, e.g. "dhcp/host/<uuid>". Max 256 characters. |
| `dhcpOptions` | []DHCPOption | No | — | No | DHCP options applied to leases from this range. |
| `exclusionRanges` | []ExclusionRange | No | — | No | Ranges carved out of the pool. |
| `exclusionRanges[].start` | string | Yes | — | No | Exclusion start address, inclusive. Max 45 characters. |
| `exclusionRanges[].end` | string | Yes | — | No | Exclusion end address, inclusive. Max 45 characters. |
| `exclusionRanges[].comment` | string | No | — | No | Comment for the exclusion. Max 1024 characters. |
| `disableDhcp` | bool | No | false | No | Stop Universal DDI serving DHCP from this range. |
| `tags` | map[string]string | No | — | No | Tags merged onto the Universal DDI object. |
| `lifecycle` | Lifecycle | No | — | No | Adoption and deletion behaviour. |

### Range Status

| Field | Type | Description |
| --- | --- | --- |
| `id` | string | Universal DDI resource id, e.g. "ipam/range/<uuid>". |
| `start` | string | Range start address observed in Universal DDI. |
| `end` | string | Range end address observed in Universal DDI. |
| `space` | string | Universal DDI ip space id. |
| `parent` | string | Universal DDI subnet id derived from start and end. |
| `utilization` | *Utilization | Utilization breakdown. |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from. |
| `lastSyncTime` | *metav1.Time | When the Universal DDI object was last read or written. |
| `conditions` | []metav1.Condition | Conditions: `Ready` and `Synced`. |

### Range Example

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
  dhcpOptions: []
```

---

## Address

An Address is a reservation for a specific address in Universal DDI. It can be explicitly set or allocated as the next available address from a Subnet, Range, or AddressBlock. The allocated address is read from `status.address`.

**Purpose:** Create an Address CR to reserve or allocate an IP address in Universal DDI. Set `address` to reserve a specific IP, or set `nextAvailableFrom` to allocate from a parent. Consumers read the allocated address from `status.address`. Multiple names can be attached via the `names` field.

### Address Spec

| Field | Type | Required | Default | Immutable | Description |
| --- | --- | --- | --- | --- | --- |
| `connectionRef` | ConnectionRef | No | — | No | Connection providing credentials. Empty means "default". |
| `spaceRef` | ObjectRef | Yes | — | No | IPSpace CR or Universal DDI ip space id. |
| `address` | string | No | — | Yes | Address to reserve, e.g. "10.0.10.7". Omit to allocate next available from nextAvailableFrom. Max 45 characters. Immutable (spec-level CEL, so adding or removing the field is rejected as well as changing it; the rule lives on the spec rather than the field because `optionalOldSelf` needs Kubernetes 1.30 and the OpenShift 4.16 floor is 1.29). Exactly one of address or nextAvailableFrom must be set (CEL: `has(self.address) != has(self.nextAvailableFrom)`). |
| `nextAvailableFrom` | *NextAvailableFrom | No | — | Yes | Selects the parent to allocate from. Immutable (spec-level CEL, so adding or removing the field is rejected as well as changing it; the rule lives on the spec rather than the field because `optionalOldSelf` needs Kubernetes 1.30 and the OpenShift 4.16 floor is 1.29). Exactly one of address or nextAvailableFrom must be set (CEL: `has(self.address) != has(self.nextAvailableFrom)`). Exactly one of subnetRef, rangeRef or addressBlockRef must be set (CEL validation on the struct). |
| `nextAvailableFrom.subnetRef` | *ObjectRef | No (one required) | — | No | Subnet CR or Universal DDI subnet id. |
| `nextAvailableFrom.rangeRef` | *ObjectRef | No (one required) | — | No | Range CR or Universal DDI range id. |
| `nextAvailableFrom.addressBlockRef` | *ObjectRef | No (one required) | — | No | AddressBlock CR or Universal DDI address block id. |
| `names` | []AddressName | No | — | No | Names attached to the address. |
| `names[].name` | string | Yes | — | No | Address name. Max 256 characters. |
| `names[].type` | string | No | user | No | Name type, e.g. "user". Max 64 characters, min 1. The CRD default only applies when the key is absent from the submitted object; the operator also treats an empty type as "user" when it builds the request. |
| `hwaddr` | string | No | — | No | MAC address associated with the reservation. Max 64 characters. |
| `interface` | string | No | — | No | Interface name associated with the reservation. Max 256 characters. |
| `comment` | string | No | — | No | Comment stored on the Universal DDI object. Max 1024 characters. |
| `tags` | map[string]string | No | — | No | Tags merged onto the Universal DDI object. |
| `lifecycle` | Lifecycle | No | — | No | Adoption and deletion behaviour. |

### Address Status

| Field | Type | Description |
| --- | --- | --- |
| `id` | string | Universal DDI resource id, e.g. "ipam/address/<uuid>". |
| `address` | string | Allocated or reserved IP address. Read this to get the allocated address. |
| `space` | string | Universal DDI ip space id. |
| `state` | string | Address state reported by Universal DDI, e.g. "used" or "free". |
| `parent` | string | Universal DDI subnet or range id containing the address. |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from. |
| `lastSyncTime` | *metav1.Time | When the Universal DDI object was last read or written. |
| `conditions` | []metav1.Condition | Conditions: `Ready` and `Synced`. |

### Address Example

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

---

## FixedAddress

A FixedAddress is a DHCP fixed address reservation in Universal DDI. It reserves an address for a specific DHCP client identified by MAC address (or DUID, client identifier, etc., depending on match type).

**Purpose:** Create a FixedAddress CR to reserve an address for a specific DHCP client. Set the client identifier with `matchType` and `matchValue`, and optionally assign a hostname. Use `disableDhcp=true` to turn the reservation into an exclusion.

### FixedAddress Spec

| Field | Type | Required | Default | Immutable | Description |
| --- | --- | --- | --- | --- | --- |
| `connectionRef` | ConnectionRef | No | — | No | Connection providing credentials. Empty means "default". |
| `spaceRef` | ObjectRef | Yes | — | No | IPSpace CR or Universal DDI ip space id. |
| `address` | string | Yes | — | Yes | Address to reserve, e.g. "10.0.10.50". Max 45 characters. Immutable (CEL: `self == oldSelf`). |
| `matchType` | FixedAddressMatchType | No | mac | Yes | Client identifier type: mac, client_text, client_hex, relay_text, relay_hex (IPv4 only), or duid (IPv6 only). Immutable (CEL: `self == oldSelf`). |
| `matchValue` | string | Yes | — | No | Client identifier, e.g. a MAC address. Max 256 characters. |
| `name` | string | No | — | No | Reservation name in Universal DDI. Max 256 characters. |
| `hostname` | string | No | — | No | Hostname handed to the client. Max 256 characters. |
| `comment` | string | No | — | No | Comment stored on the Universal DDI object. Max 1024 characters. |
| `disableDhcp` | bool | No | false | No | Turn the reservation into an exclusion without deleting it. |
| `dhcpOptions` | []DHCPOption | No | — | No | DHCP options applied to this reservation. |
| `tags` | map[string]string | No | — | No | Tags merged onto the Universal DDI object. |
| `lifecycle` | Lifecycle | No | — | No | Adoption and deletion behaviour. |

### FixedAddress Status

| Field | Type | Description |
| --- | --- | --- |
| `id` | string | Universal DDI resource id, e.g. "dhcp/fixed_address/<uuid>". |
| `address` | string | Address observed in Universal DDI. |
| `ipSpace` | string | Universal DDI ip space id. |
| `parent` | string | Universal DDI subnet id containing the address. |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from. |
| `lastSyncTime` | *metav1.Time | When the Universal DDI object was last read or written. |
| `conditions` | []metav1.Condition | Conditions: `Ready` and `Synced`. |

### FixedAddress Example

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
```

---

## DNSPublishPolicy

A DNSPublishPolicy watches OpenShift Routes, Ingresses, and LoadBalancer Services and automatically generates Record CRs that publish them in Universal DDI. The policy selects source objects by namespace and label, matches hostnames to zones, extracts targets (addresses or canonical names), and generates Records owned by the policy. Generated Records are garbage-collected when the policy is deleted.

**Purpose:** Create a DNSPublishPolicy CR to automatically publish cluster ingress endpoints as DNS records in Universal DDI. The policy handles hostname-to-zone matching, target extraction (Auto mode selects CNAME for routes and hostnames, A/AAAA for IPs), and Record lifecycle including adoption and deletion.

**Short name:** `uddipublish`

**Print columns:**
| Column | Type | JSONPath |
| --- | --- | --- |
| `Ready` | string | `.status.conditions[?(@.type=="Ready")].status` |
| `Published` | integer | `.status.published` |
| `Ready-count` | integer | `.status.ready` |
| `Age` | date | `.metadata.creationTimestamp` |

### DNSPublishPolicy Spec

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `connectionRef` | ConnectionRef | No | — | Connection providing credentials. Empty means "default". Used for every generated Record. |
| `sources` | []SourceKind | No | [Route, Ingress, Service] | Source object kinds to watch: any combination of `Route`, `Ingress`, or `Service` (means LoadBalancer Services). MinItems 1. |
| `selector` | *metav1.LabelSelector | No | — | Label selector to filter source objects in the namespace. Empty selects all. |
| `zoneRefs` | []ObjectRef | Yes | — | AuthZone CRs in this namespace that define the target zones. Only `name` is supported (CEL: every entry must have `name` set and not empty). MinItems 1. |
| `target` | DNSPublishTarget | No | — | DNS record type selection. |
| `target.mode` | TargetMode | No | Auto | `Auto` (CNAME for routes and load balancer hostnames, A/AAAA for IPs), `CNAME` (keep only CNAME targets), or `A` (keep only A/AAAA targets). |
| `target.addresses` | []string | No | — | Static A/AAAA targets (IPv4 or IPv6 literals). Each entry must be a valid IPv4 or IPv6 address. Used by mode A and by Auto mode for Routes. |
| `target.canonicalHostname` | string | No | — | Override the Route's routerCanonicalHostname for Auto and CNAME modes. Max 256 characters. |
| `ttl` | *int32 | No | 300 | Default TTL in seconds for generated Records. Minimum 1. Per-source annotations override. |
| `tags` | map[string]string | No | — | Tags merged into every generated Record's `spec.tags`. |
| `recordLifecycle` | Lifecycle | No | — | Adoption and deletion behaviour for generated Records (defaults: `adoptExisting: false`, `deletionPolicy: Delete`). |

### SourceKind (enum)

| Value | Meaning |
| --- | --- |
| `Route` | OpenShift Route |
| `Ingress` | Kubernetes Ingress |
| `Service` | Kubernetes Service of type LoadBalancer |

### TargetMode (enum)

| Value | Meaning |
| --- | --- |
| `Auto` | CNAME for Routes and load balancers exposing a hostname; A/AAAA for load balancers exposing IPs or when `target.addresses` is set. |
| `CNAME` | Keep only CNAME targets. |
| `A` | Keep only A and AAAA targets. |

### DNSPublishPolicy Status

| Field | Type | Description |
| --- | --- | --- |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from. |
| `conditions` | []metav1.Condition | Conditions: `Ready` (summary). |
| `published` | int32 | Number of desired Records currently applied. |
| `ready` | int32 | Of those, how many have `Ready=True`. |
| `skipped` | []SkippedSource | Sources or hostnames not published (bounded to 50 entries, sorted by source then hostname). |

### SkippedSource

| Field | Type | Description |
| --- | --- | --- |
| `source` | string | Source object, rendered as "Kind/name". Max 256 characters. |
| `hostname` | string | Hostname that was skipped (empty if the whole source was skipped). Max 256 characters. |
| `reason` | string | Machine-readable skip reason: `Excluded`, `NoMatchingZone`, `ZoneNotReady`, `NoTarget`, `NoHostname`, `NotAdmitted`, `NotLoadBalancer`, `ApexCNAME`, `Unsupported`, or `InvalidAnnotation`. Max 64 characters. |
| `message` | string | Human-readable explanation. Max 512 characters. |

### DNSPublishPolicy Conditions

**Ready** (summary condition)
- `True/Published`: Every desired Record is Ready and no `ZoneNotReady` skip remains.
- `False/RecordsNotReady`: Some desired Records have `Ready=False` (still being created).
- `False/ZonesNotReady`: No `zoneRef` resolves to a CR with `status.id`.

### Source Annotations

All annotations are prefixed `uddi.infoblox.com/`. They override policy settings per source:
- `publish: "false"` — exclude the source from publication.
- `hostname` — comma-separated hostnames (adds or replaces computed hostnames; required for LoadBalancer Services).
- `ttl` — integer seconds, overrides policy default TTL.
- `target` — comma-separated addresses and/or canonical names, overrides computed targets.

### DNSPublishPolicy Example

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: DNSPublishPolicy
metadata:
  name: public
spec:
  connectionRef:
    name: default
  sources:
    - Route
    - Ingress
  selector:
    matchLabels:
      dns: public
  zoneRefs:
    - name: apps-zone
  target:
    mode: Auto
  ttl: 300
  tags:
    team: platform
  recordLifecycle:
    adoptExisting: false
    deletionPolicy: Delete
```

Reference: [DESIGN.md](../DESIGN.md) section 4.7.

---

## IPAllocationPolicy

An IPAllocationPolicy watches LoadBalancer Services and automatically generates Address CRs that allocate IPs from a Universal DDI pool. The policy selects Services by namespace and label, allocates from a pool (Subnet, Range, or AddressBlock), and assigns the allocated IP to the Service via an annotation (MetalLB, Cilium, kube-vip) or `spec.loadBalancerIP`.

**Purpose:** Create an IPAllocationPolicy CR to automatically allocate LoadBalancer Service IPs from Universal DDI, integrating IP management with Kubernetes service discovery.

**Short name:** `uddiipalloc`

**Print columns:**
| Column | Type | JSONPath |
| --- | --- | --- |
| `Ready` | string | `.status.conditions[?(@.type=="Ready")].status` |
| `Allocated` | integer | `.status.allocated` |
| `Assigned` | integer | `.status.assigned` |
| `Age` | date | `.metadata.creationTimestamp` |

### IPAllocationPolicy Spec

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `connectionRef` | ConnectionRef | No | — | Connection providing credentials. Empty means "default". Used for every generated Address. |
| `selector` | *metav1.LabelSelector | No | — | Label selector to filter Services in the namespace. Empty selects all. |
| `requireAnnotation` | *bool | No | true | When true, only Services annotated `uddi.infoblox.com/allocate: "true"` are allocated. When false, all matching Services are allocated by default. |
| `allocateFrom` | AllocateFrom | Yes | — | The pool CR to allocate from: exactly one of `subnetRef`, `rangeRef`, or `addressBlockRef` (CEL: exactly one of subnetRef, rangeRef or addressBlockRef must be set). |
| `allocateFrom.subnetRef` | *ObjectRef | No (one required) | — | Subnet CR or Universal DDI subnet id to allocate from. |
| `allocateFrom.rangeRef` | *ObjectRef | No (one required) | — | Range CR or Universal DDI range id to allocate from. |
| `allocateFrom.addressBlockRef` | *ObjectRef | No (one required) | — | AddressBlock CR or Universal DDI address block id to allocate from. |
| `assignVia` | AssignVia | No | — | How to write the allocated IP into the Service (default: Annotation mode with `metallb.io/loadBalancerIPs`). |
| `assignVia.mode` | AssignMode | No | Annotation | `Annotation` (write to a Service annotation, default) or `LoadBalancerIP` (write to `spec.loadBalancerIP`). |
| `assignVia.annotation` | string | No | metallb.io/loadBalancerIPs | The annotation key to write when mode is Annotation. Defaults to MetalLB key; also supports `lbipam.cilium.io/ips`, `kube-vip.io/loadbalancerIPs`, and legacy `metallb.universe.tf/loadBalancerIPs`. Pattern: valid annotation key format. Max 317 characters. |
| `tags` | map[string]string | No | — | Tags merged into every generated Address's `spec.tags`. |
| `addressLifecycle` | Lifecycle | No | — | Adoption and deletion behaviour for generated Addresses (defaults: `adoptExisting: false`, `deletionPolicy: Delete`). |

### IPAllocationPolicy Status

| Field | Type | Description |
| --- | --- | --- |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from. |
| `conditions` | []metav1.Condition | Conditions: `Ready` (summary). |
| `allocated` | int32 | Number of Services with an Address CR. |
| `assigned` | int32 | Of those, how many have the IP written to the assignment field. |
| `skipped` | []SkippedService | Services not allocated (bounded to 50 entries, sorted by service name). |

### SkippedService

| Field | Type | Description |
| --- | --- | --- |
| `service` | string | Service name. Max 253 characters. |
| `reason` | string | Machine-readable skip reason: `NotLoadBalancer`, `InvalidAnnotation`, `PoolNotReady`, `ManuallyAssigned`, or `AddressPending`. Max 64 characters. |
| `message` | string | Human-readable explanation. Max 512 characters. |

### IPAllocationPolicy Conditions

**Ready** (summary condition)
- `True/Assigned`: Every allocated Service has its IP assigned, and no `PoolNotReady` or `AddressPending` skip remains.
- `False/AddressesPending`: Some allocated Services have pending Addresses (not Ready yet).
- `False/PoolNotReady`: The policy's pool is missing or has no `status.id` yet.

### Service Annotations

All annotations are prefixed `uddi.infoblox.com/`. They override policy settings per Service:
- `allocate: "true"` or `"false"` — opt in or out of allocation.
- `allocate-from: <pool-spec>` — override the policy pool with `subnet/<name>`, `range/<name>`, or `addressblock/<name>`.
- `address: <ip>` — reserve a specific IP instead of allocating next-available.
- `hostname: <name>` — the hostname registered on the Address (default `<service>.<namespace>`).

### IPAllocationPolicy Example

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPAllocationPolicy
metadata:
  name: lb
spec:
  connectionRef:
    name: default
  selector:
    matchLabels:
      allocate-ip: "true"
  requireAnnotation: true
  allocateFrom:
    rangeRef:
      name: lb-pool
  assignVia:
    mode: Annotation
    annotation: metallb.io/loadBalancerIPs
  tags:
    team: platform
  addressLifecycle:
    adoptExisting: false
    deletionPolicy: Delete
```

Reference: [DESIGN.md](../DESIGN.md) section 4.7.

---

## HostRegistrationPolicy

A HostRegistrationPolicy watches cluster Nodes and automatically generates Address or FixedAddress CRs that register them in Universal DDI. The policy selects Nodes by label, discovers MAC addresses from annotations or NMState, and generates registration objects (Address for hostname-based reservations, FixedAddress for DHCP MAC-based reservations). Generated objects are owned by the Node, so deleting a Node releases its reservation. The policy supports three modes: `Auto` (chooses based on MAC availability), `Address` (always IPAM), and `FixedAddress` (always DHCP, requires MAC).

**Purpose:** Create a HostRegistrationPolicy CR to automatically register cluster Nodes as DHCP or IPAM reservations in Universal DDI, integrating node infrastructure with Infoblox IP management.

**Short name:** `uddihostreg`

**Print columns:**
| Column | Type | JSONPath |
| --- | --- | --- |
| `Ready` | string | `.status.conditions[?(@.type=="Ready")].status` |
| `Registered` | integer | `.status.registered` |
| `Pending` | integer | `.status.pending` |
| `Age` | date | `.metadata.creationTimestamp` |

### HostRegistrationPolicy Spec

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `connectionRef` | ConnectionRef | No | — | Connection providing credentials. Empty means "default". Used for every generated Address or FixedAddress. |
| `selector` | *metav1.LabelSelector | No | — | Label selector to filter Nodes. Empty selects all. |
| `spaceRef` | ObjectRef | Yes | — | IPSpace CR in this namespace that contains the node networks. Must be Ready (have a `status.id`). |
| `addressType` | AddressType | No | InternalIP | The type of Node address to register: `InternalIP` or `ExternalIP`. |
| `register` | Register | No | — | Registration mode and options. |
| `register.mode` | RegisterMode | No | Auto | `Auto` (FixedAddress when MAC known, Address otherwise), `Address` (always IPAM), or `FixedAddress` (always DHCP, requires MAC). |
| `register.dhcpOptions` | []DHCPOption | No | — | DHCP options applied to generated FixedAddresses. Ignored for Addresses. |
| `macSource` | MACSource | No | — | Where to discover Node MAC addresses. |
| `macSource.nodeAnnotation` | string | No | uddi.infoblox.com/mac | Annotation key on Nodes holding the MAC address. Pattern: valid annotation key format. Max 317 characters. |
| `macSource.nmstate.enabled` | \*bool | No | true | Read NodeNetworkState from the NMState operator when the API is present. |
| `macSource.nmstate.interface` | string | No | — | Interface name to read the MAC from. Empty means the interface carrying the registered address. Max 256 characters. |
| `tags` | map[string]string | No | — | Tags merged into every generated Address or FixedAddress's `spec.tags`. |
| `lifecycle` | Lifecycle | No | — | Adoption and deletion behaviour for generated objects (defaults: `adoptExisting: false`, `deletionPolicy: Delete`). |

### HostRegistrationPolicy Status

| Field | Type | Description |
| --- | --- | --- |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from. |
| `conditions` | []metav1.Condition | Conditions: `Ready` (summary). |
| `registered` | int32 | Number of Nodes whose registration object is Ready. |
| `pending` | int32 | Number of registration objects that exist but are not Ready. |
| `skipped` | []SkippedNode | Nodes not registered (bounded to 50 entries, sorted by node name). |

### SkippedNode

| Field | Type | Description |
| --- | --- | --- |
| `node` | string | Node name. Max 253 characters. |
| `reason` | string | Machine-readable skip reason: `Excluded`, `NoAddress`, `NoMAC`, `InvalidAnnotation`, `SpaceNotReady`, or `RegistrationPending`. Max 64 characters. |
| `message` | string | Human-readable explanation. Max 512 characters. |

### HostRegistrationPolicy Conditions

**Ready** (summary condition)
- `True/Registered`: Every selected Node is registered, and no `SpaceNotReady` skip remains.
- `False/RegistrationsPending`: Some Nodes have pending registration objects (not Ready yet).
- `False/SpaceNotReady`: The IPSpace CR is missing or has no `status.id` yet.

### Node Annotations

All annotations are prefixed `uddi.infoblox.com/`. They override policy settings per Node:
- `register: "false"` or `"true"` — opt out or in of registration.
- `hostname: <name>` — override the registered hostname (default: Node name).
- `address: <ip>` — override the address to register (default: from Node status or addressType).
- `mac: <mac>` — supply the MAC address when automatic discovery is not available (format: `52:54:00:aa:bb:cc`).

### HostRegistrationPolicy Example

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: HostRegistrationPolicy
metadata:
  name: nodes
spec:
  connectionRef:
    name: default
  selector:
    matchLabels:
      node-role.kubernetes.io/worker: ""
  spaceRef:
    name: corp
  addressType: InternalIP
  register:
    mode: Auto
    dhcpOptions: []
  macSource:
    nodeAnnotation: uddi.infoblox.com/mac
    nmstate:
      enabled: true
      interface: ""
  tags:
    role: node
  lifecycle:
    deletionPolicy: Delete
```

Reference: [DESIGN.md](../DESIGN.md) section 4.7.
