# IP Allocation Guide

This guide covers IPAllocationPolicy: how to automatically allocate IPs from Infoblox Universal DDI to LoadBalancer Services in your OpenShift cluster and publish them via your load balancer implementation.

## What IPAllocationPolicy Does

An IPAllocationPolicy watches LoadBalancer Services in a namespace and automatically generates Address CRs that allocate IPs from a Universal DDI pool (Subnet, Range, or AddressBlock). When you create a LoadBalancer Service and opt in with an annotation, the operator:

1. Creates an Address CR owned by the Service to allocate the next available IP from the pool
2. Waits for the Address to become Ready with the allocated IP
3. Patches the Service to write the IP into the load balancer implementation's annotation or `spec.loadBalancerIP`
4. Cleans up the Address when the Service is deleted or opts out

Addresses are garbage-collected by the Service's ownerReference, so deleting the Service removes the Address and releases the IP.

Beyond that basic flow, the policy handles three more cases, each in its own section below: a dual-stack Service gets one Address per IP family ([Dual-Stack Services](#dual-stack-services)), an OVN-Kubernetes EgressIP gets as many addresses as it asks for ([EgressIP Allocation](#egressip-allocation)), and the pools the policy draws from can be mirrored into MetalLB so MetalLB announces what Universal DDI allocates ([MetalLB Pool Mirroring](#metallb-pool-mirroring)).

The policy name becomes a label value on every generated Address, so it is limited to 63 characters; the CRD rejects a longer name.

## Prerequisites

Before creating an IPAllocationPolicy, you need:

1. **A ready Connection CR** in the namespace with valid credentials.
2. **A pool CR** (Subnet, Range, or AddressBlock) with `status.id` set (the pool must be Ready and allocated or created, not just pending). The pool's `spec.spaceRef` is copied to each generated Address.

A typical setup:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPSpace
metadata:
  name: corp
spec:
  name: corp
  comment: Corporate IP space
---
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
---
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
```

Check that the pool is ready:

```bash
kubectl get subnet openshift-lb -o jsonpath='{.status.id}'
# ipam/subnet/... (non-empty means ready)
```

### Pool CRs Must Live in the Policy Namespace

`allocateFrom` (and `allocateFromIPv6`, and the `allocate-from` annotation) name pool CRs, and the policy only looks for them in its own namespace. A Universal DDI id is not accepted there, because the pool has to exist as a CR for its `spec.spaceRef` to be read and its `status.id` to be watched (the CRD requires `name` on `subnetRef`, `rangeRef` and `addressBlockRef`). The same holds for the Services: the policy only sees Services in its own namespace.

A team that wants addresses from a shared Subnet therefore creates its own Range CR in its own namespace, inside that Subnet, and points the policy at it. The Range can name the IPSpace by id (`spaceRef: {id: ipam/ip_space/...}`), so the IPSpace need not be adopted again. [docs/patterns.md](patterns.md) walks through this for a tenant namespace.

### Load Balancer Support

The operator writes the allocated IP into the Service by patching an annotation (default) or `spec.loadBalancerIP`. Your load balancer implementation must honour the configured key. Tested implementations:

| Implementation | Annotation Key | Format | Documentation |
| --- | --- | --- | --- |
| **MetalLB** (default) | `metallb.io/loadBalancerIPs` | Single IP or comma-separated list for dual-stack | https://metallb.io/usage/#requesting-specific-ips |
| **Cilium LB-IPAM** | `lbipam.cilium.io/ips` | Comma-separated list of IPs (single IP is one-element list) | https://docs.cilium.io/en/stable/network/lb-ipam/#requesting-ips |
| **kube-vip** | `kube-vip.io/loadbalancerIPs` | Single IP, DNS name, or comma-separated pair for dual-stack ("0.0.0.0"/"::") | https://kube-vip.io/docs/usage/kubernetes-services/ |
| **MetalLB** (legacy) | `metallb.universe.tf/loadBalancerIPs` | Single IP or comma-separated list (deprecated) | https://metallb.io/usage/#requesting-specific-ips |

For other implementations, set `spec.assignVia.annotation` to the key your implementation reads.

## Complete Example

Create an IPAllocationPolicy:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPAllocationPolicy
metadata:
  name: lb
  namespace: my-team
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
    team: my-team
    environment: production
  addressLifecycle:
    deletionPolicy: Delete
```

Create a LoadBalancer Service with the allocation annotation:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: web
  namespace: my-team
  labels:
    allocate-ip: "true"
  annotations:
    uddi.infoblox.com/allocate: "true"
    uddi.infoblox.com/hostname: "web.apps.example.com"
spec:
  type: LoadBalancer
  selector:
    app: web
  ports:
    - port: 443
      targetPort: 8443
```

The policy watches the Service and automatically creates an Address CR named `lb-web`. Once the Address is Ready and has an allocated IP, the policy patches the Service's `metallb.io/loadBalancerIPs` annotation. Check the policy status:

```bash
kubectl get ipallocationpolicy lb -n my-team
# or using the short name:
kubectl get uddiipalloc lb -n my-team
# NAME   READY   ALLOCATED   ASSIGNED   AGE
# lb     True    1           1          2m

kubectl describe ipallocationpolicy lb -n my-team
# Status:
#   Allocated:            1
#   Assigned:             1
#   Observed Generation:  1
#   Conditions:
#     Last Transition Time:  2026-09-14T12:00:00Z
#     Message:               All addresses allocated and assigned
#     Observed Generation:   1
#     Reason:                Assigned
#     Status:                True
#     Type:                  Ready
```

List the generated Address:

```bash
kubectl get address -l uddi.infoblox.com/policy=lb -n my-team
# NAME      READY   ADDRESS      AGE
# lb-web    True    10.0.10.101  2m

# Or list all addresses for a Service
kubectl get address -l uddi.infoblox.com/service=web -n my-team
```

Check the allocated IP:

```bash
kubectl get address lb-web -n my-team -o jsonpath='{.status.address}'
# 10.0.10.101

# Check the Service annotation
kubectl get service web -n my-team -o jsonpath='{.metadata.annotations.metallb\.io/loadBalancerIPs}'
# 10.0.10.101
```

## Service Annotations

All annotations are prefixed with `uddi.infoblox.com/`. The policy's `allocateFrom` and `assignVia` apply by default; Service annotations override on a per-Service basis.

### `allocate: "true"` or `allocate: "false"`

Controls whether this Service gets an allocated IP.

- `"true"`: opts in; the policy allocates an Address and writes the IP to the assignment field
- `"false"`: opts out; if the Service has an Address, it is released
- absent: when `requireAnnotation=true` (default), the Service is skipped; when `requireAnnotation=false`, the Service is opted in by default

Example:

```yaml
metadata:
  annotations:
    uddi.infoblox.com/allocate: "true"
```

### `allocate-from: <pool-spec>`

Overrides the policy's default pool for this Service. Format is `<kind>/<name>`, where `<kind>` is `subnet`, `range`, or `addressblock`, and `<name>` is the pool CR name in the same namespace.

```yaml
metadata:
  annotations:
    uddi.infoblox.com/allocate-from: "range/secondary-pool"
```

### `address: <ip>`

Reserves a specific IP instead of allocating the next available. The IP must be in the selected pool. Invalid addresses are reported as `InvalidAnnotation`.

```yaml
metadata:
  annotations:
    uddi.infoblox.com/address: "10.0.10.50"
```

### `hostname: <name>`

The hostname registered on the Address's `names` field (default `<service>.<namespace>`). Combined with DNSPublishPolicy, this same annotation names the Address and publishes the DNS record.

```yaml
metadata:
  annotations:
    uddi.infoblox.com/hostname: "web.apps.example.com"
```

## Generated Address Naming and Labeling

The policy generates Address CRs with stable names to avoid duplicates:

```
<policy-name>-<service-name>
```

If the name would exceed 253 characters (Kubernetes limit), the service name is truncated to fit:

```
<policy-name>-<hash(10 chars)>
```

Example: `lb-web` for a policy named `lb` and Service named `web`.

Generated Addresses carry these labels (used for listing and filtering):

| Label | Value |
| --- | --- |
| `uddi.infoblox.com/policy` | Policy name (e.g., `lb`) |
| `uddi.infoblox.com/service` | Service name |

Addresses generated today also carry `uddi.infoblox.com/target-kind`, `uddi.infoblox.com/target-name` and `uddi.infoblox.com/slot`, and the Address of a second IP family or an EgressIP slot has a suffixed name (`lb-web-1`, `lb-egressip-<name>-0`); see [Dual-Stack Services](#dual-stack-services) and [EgressIP Allocation](#egressip-allocation).

List all addresses for a policy:

```bash
kubectl get address -l uddi.infoblox.com/policy=lb -n my-team

# List addresses for a specific Service
kubectl get address -l uddi.infoblox.com/service=web -n my-team
```

The Address spec includes:
- **Ownership:** ownerReference to the Service (garbage collection on Service deletion)
- **Space:** copied from the pool's `spec.spaceRef`
- **Allocation:** `nextAvailableFrom` pointing to the selected pool (or `address` when a specific IP is requested)
- **Names:** hostname from the annotation or default
- **Comment:** "Allocated by IPAllocationPolicy <namespace>/<policy> for Service <name>"
- **Tags:** policy tags plus `uddi_operator_source: Service/<namespace>/<service-name>`
- **Lifecycle:** policy's `addressLifecycle` settings

## How Assignment Works

Once an Address is Ready with a `status.address`, the policy patches the Service:

- **Annotation mode** (default): Sets `spec.assignVia.annotation` (default `metallb.io/loadBalancerIPs`) to the IP
- **LoadBalancerIP mode**: Sets `spec.loadBalancerIP` to the IP

The assigned IP is shown in the policy's status and in the Service annotations or spec.

### ManuallyAssigned Skip Reason

A Service is skipped with `ManuallyAssigned` when its assignment field already holds a value that this policy did not produce. This prevents the policy from overwriting manual assignments:

```yaml
metadata:
  annotations:
    metallb.io/loadBalancerIPs: "10.0.10.9"  # manually set, not from an Address
```

The policy emits a Skipped event with reason `ManuallyAssigned` and does not allocate. To move to policy-managed allocation, clear the annotation and opt in again:

```bash
kubectl annotate service web uddi.infoblox.com/allocate=true --overwrite
kubectl annotate service web metallb.io/loadBalancerIPs- # remove the manual value
```

## Dual-Stack Services

The policy allocates one Address per IP family a Service wants. Each Address is one *slot* of the Service's claim.

**Which families.** The wanted families are the Service's `spec.ipFamilies`, in that order. Only the first one counts for a `SingleStack` Service (or one without `spec.ipFamilyPolicy`), so single-stack Services behave exactly as before.

**Which pools.** A pool's IP family comes from its CR: a Subnet or AddressBlock from `status.address` (falling back to `spec.address`), a Range from `spec.start`.

- IPv4 slots draw from `allocateFrom`.
- IPv6 slots draw from `allocateFromIPv6`, or from `allocateFrom` when that pool is itself an IPv6 pool.
- The `allocate-from` annotation replaces the policy pool of its own family only. If its pool serves a family the Service does not want, the Service is skipped with `NoPoolForFamily`.

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPAllocationPolicy
metadata:
  name: lb
  namespace: my-team
spec:
  allocateFrom:
    rangeRef:
      name: lb-pool            # IPv4 Range
  allocateFromIPv6:
    rangeRef:
      name: lb-pool-v6         # IPv6 Range
  assignVia:
    mode: Annotation
---
apiVersion: v1
kind: Service
metadata:
  name: web
  namespace: my-team
  annotations:
    uddi.infoblox.com/allocate: "true"
spec:
  type: LoadBalancer
  ipFamilyPolicy: PreferDualStack
  ipFamilies:
    - IPv4
    - IPv6
  selector:
    app: web
  ports:
    - port: 443
```

**Slots and names.** A new claim numbers its slots in family order, so slot 0 is the Service's first family. Slot 0 keeps the name an Address always had, `<policy>-<service>` (`lb-web`), so upgrading never moves an allocated address. Slot 1 is `<policy>-<service>-1` (`lb-web-1`). An existing Address keeps its slot number, and a slot never changes family. The Addresses carry these labels:

| Label | Value |
| --- | --- |
| `uddi.infoblox.com/policy` | Policy name |
| `uddi.infoblox.com/service` | Service name (Service slots only) |
| `uddi.infoblox.com/target-kind` | `Service` or `EgressIP` |
| `uddi.infoblox.com/target-name` | Name of the Service or EgressIP |
| `uddi.infoblox.com/slot` | Slot number, `0`, `1`, ... |

An Address from before dual-stack support has no `target-kind` and is read as slot 0 of the Service in its `service` label. When a target name is longer than a label value allows, `target-name` holds a shortened form with a hash suffix and the annotation `uddi.infoblox.com/target-name` carries the full name.

**Assignment.** In `Annotation` mode the annotation gets the slot addresses comma-joined in slot order, for example `metallb.io/loadBalancerIPs: 10.0.10.101,2001:db8:10::100`. MetalLB, Cilium and kube-vip all accept that form. The annotation is written only once every slot's Address is Ready; until then the Service is reported as `AddressPending`, with the slot named in the message (`slot 1: ...`).

**Prefer or require.**

| `ipFamilyPolicy` | A family without a pool |
| --- | --- |
| `PreferDualStack` | The family is dropped silently and the Service gets the families that have a pool. If the Service already holds an address of that family, it is skipped with `NoPoolForFamily` instead and keeps its Addresses. |
| `RequireDualStack` | The Service is skipped with `NoPoolForFamily`, naming the missing pool. |

For example, a `RequireDualStack` Service under a policy without `allocateFromIPv6`, whose `allocateFrom` is an IPv4 Range, shows:

```yaml
status:
  skipped:
    - service: web
      reason: NoPoolForFamily
      message: "no IPv6 pool: allocateFrom range/lb-pool is not IPv6 and allocateFromIPv6 is unset"
```

**`LoadBalancerIP` mode.** `spec.loadBalancerIP` carries one address, so only one slot is allocated per Service: the first family, or the family the Service already holds. A `RequireDualStack` Service cannot be served and is skipped with `DualStackUnsupported`.

**Specific addresses.** The `address` annotation takes one IP per wanted family, comma-separated, and each address goes to the slot of its family:

```yaml
metadata:
  annotations:
    uddi.infoblox.com/allocate: "true"
    uddi.infoblox.com/address: "10.0.10.50,2001:db8:10::50"
```

Giving one address leaves the other slot to next-available. Two addresses of one family, or an address of a family the Service does not want, is an `InvalidAnnotation`.

**Manual assignments.** A Service is `ManuallyAssigned` when its current assignment contains any address that no slot of this Service holds. Each entry of a comma-separated list is checked.

**Changing pools affects new slots only.** Once a slot has its Address, that Address keeps its pool and address (they are immutable on the Address). Changing `allocateFrom`, `allocateFromIPv6`, the `allocate-from` annotation or the `address` annotation therefore only affects slots created afterwards. To move a Service, release it and opt in again (see [Changing the Pool Does Not Move Existing Services](#changing-the-pool-does-not-move-existing-services)).

**Dropping a slot.** When a Service stops wanting a family that was planned in full (it became `SingleStack`, or the policy switched to `LoadBalancerIP` mode), the Address of the dropped slot is released.

**Name clashes.** Slot 1 of Service `web` and slot 0 of a Service called `web-1` would both be named `lb-web-1`. Slot 0 names are reserved first and an existing Address is never taken over, so the Service that would take the clashing name is skipped with `NameConflict`, and its existing Addresses stay as they are. Rename one of the Services to resolve it.

## EgressIP Allocation

An OVN-Kubernetes `EgressIP` (`k8s.ovn.org/v1`, cluster-scoped) can take its addresses from Universal DDI too. List the target in the policy:

```yaml
spec:
  targets:
    - EgressIP          # default is [Service]; list both to serve Services as well
```

The EgressIP API is read as unstructured data. If the cluster does not serve it, the target is inert: no EgressIP is read, Addresses already allocated for EgressIPs are left alone, and the policy reports an `EgressIP` entry with `APIUnavailable`. An address allocated for a target kind that you later remove from `targets` is left in place as well.

**Which EgressIPs.** `spec.selector` is matched against the EgressIP's labels, cluster-wide: the policy's namespace does not limit which EgressIPs it sees, so two policies with overlapping selectors would both claim the same EgressIP. An EgressIP takes part only with the annotation `uddi.infoblox.com/allocate: "true"`, whatever `requireAnnotation` says (it applies to Services only). `"false"`, or removing the annotation, releases it.

**Annotations on the EgressIP**, prefix `uddi.infoblox.com/`:

| Annotation | Meaning |
| --- | --- |
| `allocate` | `"true"` opts in (required). |
| `count` | Number of addresses, an integer from 1 to 16 (default 1). Anything else is an `InvalidAnnotation`. |
| `allocate-from` | Pool override, `subnet/<name>`, `range/<name>` or `addressblock/<name>`. |

EgressIP slots have no IP family of their own: they use `allocateFrom` (or the annotation's pool) and never `allocateFromIPv6`. The pool should be the node subnet, as OVN-Kubernetes requires.

**What is generated.** One Address per slot, named `<policy>-egressip-<name>-<i>` with `i` counting from 0, and owned by the EgressIP (so deleting the EgressIP garbage-collects them). They carry the labels above with `target-kind: EgressIP` and the tag `uddi_operator_source: EgressIP/<name>`; no hostname is registered on them.

**Assignment.** When every slot is Ready, the policy sets the EgressIP's `spec.egressIPs` to the slot addresses in slot order. Until then the EgressIP is reported as `AddressPending` under the entry `EgressIP/<name>`. A non-empty `spec.egressIPs` that contains an address no slot holds is `ManuallyAssigned`: nothing is allocated and nothing is written. An EgressIP waiting for its addresses carries `egressIPs: []`, which is what the upstream CRD requires, and is fine.

**Count changes and opt-out.** Lowering `count` releases the highest slots, raising it adds slots. Opting out clears `spec.egressIPs` (to `[]`) if it still holds only this policy's addresses, then deletes the Addresses. The EgressIP itself is never deleted.

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPAllocationPolicy
metadata:
  name: egress
  namespace: uddi-system
spec:
  targets:
    - EgressIP
  selector:
    matchLabels:
      egress: uddi
  allocateFrom:
    rangeRef:
      name: egress-pool       # a Range inside the node subnet
---
apiVersion: k8s.ovn.org/v1
kind: EgressIP
metadata:
  name: team-a-egress
  labels:
    egress: uddi
  annotations:
    uddi.infoblox.com/allocate: "true"
    uddi.infoblox.com/count: "2"
spec:
  egressIPs: []
  namespaceSelector:
    matchLabels:
      team: a
```

This creates the Addresses `egress-egressip-team-a-egress-0` and `egress-egressip-team-a-egress-1` in `uddi-system`, and fills `spec.egressIPs` with their two addresses once both are Ready.

## MetalLB Pool Mirroring

MetalLB only announces an address that lies in one of its own `IPAddressPool`s. Universal DDI allocates the address and the policy writes it into the Service, but MetalLB would ignore an address outside every pool of its own. With `metallb.mirrorPools` the policy mirrors every pool it uses into MetalLB, so what Universal DDI allocates is announced.

```yaml
spec:
  metallb:
    mirrorPools: true
    namespace: metallb-system    # default
```

**Which pools.** `allocateFrom`, `allocateFromIPv6`, every pool named by an `allocate-from` annotation, and every pool an existing Address was allocated from. A pool still in use keeps its mirror even while it cannot be rebuilt for a moment, so a transient problem never withdraws an announced range.

**What is created.** One `metallb.io/v1beta1 IPAddressPool` per pool, in `metallb.namespace`:

- **Name:** `uddi-<policy namespace>-<policy>-<pool kind>-<pool name>`, with the kind in lower case, for example `uddi-my-team-lb-range-lb-pool`. A name above 253 characters is truncated and ends in `-` plus 10 hex digits of the sha256 of the full name.
- **`spec.addresses`:** one entry, the Subnet or AddressBlock CIDR in network form (`10.0.10.0/24`), or the Range as `start-end`.
- **`spec.autoAssign: false`**, so MetalLB never hands these addresses out on its own; only Services that ask for one through the annotation get it.
- **Labels:** `uddi.infoblox.com/policy`, `uddi.infoblox.com/policy-namespace` and `app.kubernetes.io/managed-by: uddi-operator`.

The operator applies them server-side with the field manager `uddi-operator/ipallocationpolicy-metallb`, and manages only the three labels, `spec.addresses` and `spec.autoAssign`. Other fields someone sets on the pool (`serviceAllocation`, `avoidBuggyIPs`) are left alone. MetalLB also needs an `L2Advertisement` or `BGPAdvertisement` that selects the pools; an `L2Advertisement` without a pool selector covers all of them. The operator creates none.

**Finalizer.** A pool in another namespace cannot be owned by the policy. While mirroring is on, the policy therefore carries the finalizer `uddi.infoblox.com/metallb-pools`. Deleting the policy, or turning `mirrorPools` off, deletes the mirrored pools and removes the finalizer. On a cluster without the MetalLB API the finalizer is never added, so deleting the policy never waits.

**Conflicts.** If a pool of the same name already exists without this policy's labels, the policy leaves it alone and reports `<Kind>/<name>` with `MetalLBPoolConflict`. A pool that cannot be mirrored, for example a Subnet whose address is not known yet or an unparsable range, is reported with `MetalLBPoolInvalid`. Neither changes `Ready`.

**Drift and pruning.** The mirrored pools are not watched. The policy repairs an edited or deleted mirror, and deletes mirrors of pools it no longer uses, on each of its own reconciles (triggered by changes to its Services, Addresses, pools or itself), not the instant the MetalLB object changes.

**Moving the pools.** Changing `metallb.namespace` creates the pools in the new namespace and deletes the policy's pools in the old one.

On a cluster that does not serve `metallb.io/v1beta1 IPAddressPool`, mirroring is inert and the policy reports an `IPAddressPool` entry with `APIUnavailable`.

## Status Fields and Skip Reasons

The policy's `status` reports allocation progress:

| Field | Type | Description |
| --- | --- | --- |
| `allocated` | int32 | Number of Services with an Address CR |
| `assigned` | int32 | Of those, how many have the IP written to the assignment field |
| `skipped` | []Skip | Services not allocated (bounded to 50 entries) |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from |
| `conditions` | []metav1.Condition | `Ready` condition (summary) |

The `Ready` condition:
- `True/Assigned`: All allocated Services have their IPs assigned, and no `PoolNotReady` or `AddressPending` skip remains
- `False/AddressesPending`: Some allocated Services have pending Addresses (the Address is not Ready yet)
- `False/PoolNotReady`: The policy's pool is missing or has no `status.id` yet

Each Skip entry has:

| Field | Type | Description |
| --- | --- | --- |
| `service` | string | Service name |
| `reason` | string | Why it was skipped |
| `message` | string | Additional context |

### Skip Reasons and Remediation

| Reason | Cause | What to Do |
| --- | --- | --- |
| `NotLoadBalancer` | The Service's `type` is not `LoadBalancer` and it carries no `uddi.infoblox.com/allocate` annotation (or it is `"false"`). | Set the Service's `spec.type: LoadBalancer` or annotate with `uddi.infoblox.com/allocate: "true"`. |
| `InvalidAnnotation` | A malformed `allocate-from`, `address`, or `hostname` annotation value (e.g., invalid pool reference or IP address). | Fix the annotation value. Check the message for details. |
| `PoolNotReady` | The selected pool (from policy or annotation) is missing or has no `status.id` yet (the pool is pending). | Wait for the pool to become Ready; check `kubectl describe <pool-kind> <pool-name>`. Or check that the annotation value (`allocate-from`) is correct. |
| `ManuallyAssigned` | The assignment field already holds a value that no Address of this policy produced. | This is not an error; it means the IP is set manually. To switch to policy-managed allocation, clear the manual value and toggle the allocate annotation off and back on. |
| `AddressPending` | The Address CR exists but is not Ready yet (still being allocated in Universal DDI). | Wait for the Address to become Ready; check `kubectl describe address <address-name>`. Address failures (API errors, conflicts, pool exhaustion) are visible on the Address's Ready condition message. |

Example: view skipped Services and troubleshoot:

```bash
kubectl describe ipallocationpolicy lb -n my-team | grep -A 20 "Skipped:"

# Status:
#   Skipped:
#     - Service: web
#       Reason:  AddressPending
#       Message: Address lb-web is not Ready yet; condition: Creating
#     - Service: api
#       Reason:  PoolNotReady
#       Message: Subnet openshift-lb has no status.id
```

### Skipped Entry Formats

The `service` field of a skipped entry no longer always holds a Service name. It can be up to 266 characters, and it reads:

| `service` value | What it names |
| --- | --- |
| `web` | A Service, by its bare name (unchanged from before) |
| `EgressIP/team-a-egress` | An EgressIP, as `EgressIP/<name>` |
| `Range/lb-pool` | A pool that cannot be mirrored into MetalLB, as `<Kind>/<name>` with `Subnet`, `Range` or `AddressBlock` as the kind |
| `EgressIP` or `IPAddressPool` | The whole target or mirroring, when the cluster does not serve that API |

### Skip Reasons Added with Dual-Stack, EgressIP and MetalLB Mirroring

| Reason | Entry | Cause | What to Do |
| --- | --- | --- | --- |
| `NoPoolForFamily` | Service | A wanted IP family has no pool: a `SingleStack` or `RequireDualStack` Service with no pool for one of its families, an `allocate-from` pool that serves none of the wanted families, or `PreferDualStack` that would drop a family the Service already holds an address of. | Set `allocateFrom` and `allocateFromIPv6` to pools of the wanted families, or relax the Service's `ipFamilyPolicy`. |
| `DualStackUnsupported` | Service | `assignVia.mode: LoadBalancerIP` carries one address, but the Service is `RequireDualStack`. | Use `Annotation` mode, or make the Service `PreferDualStack` or single-stack. |
| `NameConflict` | Service | The Address name a slot would get already belongs to another target's slot, for example slot 1 of `web` and slot 0 of `web-1` (both `<policy>-web-1`). The existing Address is never taken over. | Rename one of the Services. |
| `APIUnavailable` | `EgressIP` or `IPAddressPool` | `targets` lists `EgressIP` but the cluster does not serve `k8s.ovn.org/v1 EgressIP`, or `metallb.mirrorPools` is on but it does not serve `metallb.io/v1beta1 IPAddressPool`. That target or mirroring is inert. | Install the API, or drop the target or the mirroring from the policy. |
| `MetalLBPoolInvalid` | `<Kind>/<name>` | A pool the policy uses cannot be mirrored, for example a Subnet that has no address yet. | Wait for the pool to get its address, or check its `spec`. |
| `MetalLBPoolConflict` | `<Kind>/<name>` | The IPAddressPool the pool would be mirrored into already exists without this policy's labels. It is left alone. | Rename or delete the foreign pool. |

The existing reasons `InvalidAnnotation`, `PoolNotReady`, `ManuallyAssigned` and `AddressPending` apply to EgressIPs too (entry `EgressIP/<name>`); `NotLoadBalancer` is for Services only.

A claim that reaches any of `InvalidAnnotation`, `PoolNotReady`, `NoPoolForFamily`, `DualStackUnsupported`, `ManuallyAssigned` or `NameConflict` after it is known to want addresses is frozen: its existing Addresses are kept as they are, neither re-applied nor released. Addresses are released only when the target is gone, opts out, stops being a LoadBalancer Service, or no longer wants a slot (a lowered EgressIP `count`, a Service back to one IP family, `LoadBalancerIP` mode).

### Counters and Ready

- `allocated` and `assigned` count claims, not Addresses. A dual-stack Service or an EgressIP with three slots counts once. `allocated` is the number of Services and EgressIPs that have their Address CRs, and `assigned` how many of those already carry every slot's Ready address in the assignment field.
- `APIUnavailable`, `MetalLBPoolInvalid` and `MetalLBPoolConflict` never change the `Ready` condition. `Ready` follows the rules above: `True/Assigned` needs `assigned == allocated` and no `PoolNotReady` or `AddressPending` skip.
- A Warning `Skipped` event is emitted per distinct reason, with a count and up to three example entries.

## Release and Deletion Behavior

### Opt-Out

When a Service's `allocate` annotation is set to `"false"`, the policy releases the address:

1. The Service's assignment field is cleared if it still equals the Address's address
2. The Address CR is deleted
3. The Address's finalizer releases the IP to Universal DDI (or orphans it per the lifecycle policy)

```bash
kubectl annotate service web uddi.infoblox.com/allocate="false" --overwrite
```

### Service Deletion

When a LoadBalancer Service is deleted, its owned Address CR is automatically garbage-collected by Kubernetes. The Address's finalizer then releases the IP.

### Policy Deletion

When an IPAllocationPolicy is deleted:

- Addresses remain in place (the policy does not own them; the Service does)
- Assignments remain in the Service annotations/spec
- Running Services keep their IPs

This ensures that deleting the policy does not disrupt running workloads. To release IPs when removing a policy, opt out each Service first, or delete the Addresses manually:

```bash
# Delete all Addresses for a policy to release IPs
kubectl delete address -l uddi.infoblox.com/policy=lb -n my-team
```

### Pool Deletion

Deleting a pool (Subnet, Range, AddressBlock) does not affect running Addresses and Services (the Address owns the IP once allocated). New Addresses cannot be allocated from the pool until a new one is created and Ready.

### Adoption Never Takes Over Another CR's Address

`addressLifecycle` is copied to every generated Address. With `adoptExisting: true`, an Address can take over an existing IP in Universal DDI that no CR manages, for example a reservation matching an `address` annotation. It never takes over an IP that another CR already manages (one carrying the managed marker and another CR's uid tag). That Address reports `Ready=False/AlreadyExists` with the other owner named in its message, and the policy shows it as `AddressPending`. Two CRs rewriting one object's ownership tags would let either one's deletion remove an address the other still uses. To hand an address over deliberately, remove its `uddi_operator_*` tags in Universal DDI first.

The same rule applies to the objects pools are built on. A team that needs an IPSpace another namespace already adopted names it by id in the `spaceRef` of its own Range, instead of adopting the IPSpace a second time (a pool itself, though, must be a CR in the policy's namespace); see [docs/patterns.md](patterns.md).

## Combining with DNSPublishPolicy

IP allocation and DNS publishing use the same `hostname` annotation:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: web
  annotations:
    uddi.infoblox.com/allocate: "true"
    uddi.infoblox.com/hostname: "web.apps.example.com"
spec:
  type: LoadBalancer
  selector:
    app: web
  ports:
    - port: 443
      targetPort: 8443
```

- **IPAllocationPolicy** uses the hostname to name the Address in Universal DDI
- **DNSPublishPolicy** uses the same hostname to publish a DNS record pointing to the allocated IP

This integrates IP allocation and DNS publishing: one annotation sets both up.

## Limitations

### One Address Per IP Family

A Service gets at most one address per IP family: one IPv4 and one IPv6 address for a dual-stack Service, never two of the same family (see [Dual-Stack Services](#dual-stack-services)). In `LoadBalancerIP` mode a Service gets one address in total, because `spec.loadBalancerIP` holds only one. Only an EgressIP can claim several addresses of one kind, through its `count` annotation (see [EgressIP Allocation](#egressip-allocation)).

### Changing the Pool Does Not Move Existing Services

If you change the policy's `allocateFrom` or a Service's `allocate-from` annotation to point to a different pool, the Service's existing Address is not moved. To switch a Service to a different pool:

1. Release the Service (set `allocate: "false"`)
2. Clear the manual assignment from the annotation/spec
3. Opt back in with the new pool annotation (if needed) or change the policy

```bash
kubectl annotate service web uddi.infoblox.com/allocate="false" --overwrite
kubectl annotate service web metallb.io/loadBalancerIPs- # clear manual value
kubectl annotate service web uddi.infoblox.com/allocate="true" --overwrite
```

### Pool Exhaustion

If the selected pool has no available IPs, the Address is not allocated and the policy reports `AddressPending` with an API error message. Expand the pool or release some addresses to proceed.

### Pools and Services Stay in the Policy Namespace

The policy reads pool CRs and Services in its own namespace only; see [Pool CRs Must Live in the Policy Namespace](#pool-crs-must-live-in-the-policy-namespace). EgressIPs are the exception because they are cluster-scoped: the policy sees every EgressIP its selector matches.

### Optional APIs Are Detected Once

Whether the cluster serves `k8s.ovn.org/v1 EgressIP` and `metallb.io/v1beta1 IPAddressPool` is checked once at startup, and logged. Installing either later takes effect after the manager restarts; until then the policy reports `APIUnavailable` for the target or mirroring that needs it.

---

## Reference: More Examples

### Allocate from a Specific Range

```yaml
apiVersion: v1
kind: Service
metadata:
  name: external-api
  annotations:
    uddi.infoblox.com/allocate: "true"
    uddi.infoblox.com/allocate-from: "range/external-pool"
    uddi.infoblox.com/hostname: "api.example.com"
spec:
  type: LoadBalancer
  selector:
    app: api
  ports:
    - port: 443
      targetPort: 8443
```

### Use LoadBalancerIP Instead of Annotation

For implementations that honour `spec.loadBalancerIP` instead of annotations:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPAllocationPolicy
metadata:
  name: lb-spec
spec:
  allocateFrom:
    rangeRef:
      name: lb-pool
  assignVia:
    mode: LoadBalancerIP
```

This sets `spec.loadBalancerIP` on the Service instead of an annotation.

### Multiple Policies in One Namespace

Use different label selectors and pools to run multiple policies:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPAllocationPolicy
metadata:
  name: internal-lb
spec:
  selector:
    matchLabels:
      network: internal
  allocateFrom:
    rangeRef:
      name: internal-pool
---
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPAllocationPolicy
metadata:
  name: external-lb
spec:
  selector:
    matchLabels:
      network: external
  allocateFrom:
    rangeRef:
      name: external-pool
```

Services select a policy by label:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: internal-svc
  labels:
    network: internal
  annotations:
    uddi.infoblox.com/allocate: "true"
spec:
  type: LoadBalancer
  ...
```

### Reserve Specific IPs

```yaml
apiVersion: v1
kind: Service
metadata:
  name: static-svc
  annotations:
    uddi.infoblox.com/allocate: "true"
    uddi.infoblox.com/address: "10.0.10.50"
spec:
  type: LoadBalancer
  ...
```

The IP must be in the selected pool; allocation fails with an API error if the address is already used or out of range.
