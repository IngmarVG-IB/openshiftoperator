# Host Registration Guide

This guide covers HostRegistrationPolicy: how to automatically register cluster Nodes in Infoblox Universal DDI as DHCP reservations (FixedAddress) when their MAC addresses are known, or as named IPAM addresses (Address) when MAC discovery is not available or when you prefer not to bind to hardware.

## What HostRegistrationPolicy Does

A HostRegistrationPolicy watches cluster Nodes and automatically generates registration objects (Address or FixedAddress) in Universal DDI, owned by the Node. When you create a HostRegistrationPolicy and Nodes match its label selector, the operator:

1. Collects Node metadata: address (from status or annotation), MAC (from annotation, NMState or Metal3), hostname (from Node name or annotation)
2. Decides whether to register as a DHCP FixedAddress (MAC-based) or a named Address (hostname-based)
3. Creates the registration object owned by the Node, carrying the policy's tags
4. Cleans up the object when the Node is deleted or no longer matches the policy

Generated objects are garbage-collected by the Node's ownerReference, so deleting the Node removes its registration object and releases the address.

Two capabilities extend this basic flow: Metal3 `BareMetalHost`s as a third source of MAC addresses ([Metal3 BareMetalHosts](#metal3-baremetalhosts)), and extra registrations for a Node's other networks ([Additional Interfaces](#additional-interfaces)). The policy name becomes a label value on every generated Address and FixedAddress, so it is limited to 63 characters; the CRD rejects a longer name.

## Prerequisites

Before creating a HostRegistrationPolicy, you need:

1. **A ready Connection CR** in the namespace with valid credentials.
2. **An IPSpace CR** with `status.id` set (the space must be Ready). The space's `spec.name` becomes the `spaceRef` of every generated object, and the space must contain the node networks (the operator does not verify subnet membership; Universal DDI rejects an out-of-range address).

A typical setup:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPSpace
metadata:
  name: corp
spec:
  name: corp
  comment: Corporate IP space containing node networks
---
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
  tags:
    role: node
    environment: production
```

Check that the IPSpace is ready:

```bash
kubectl get ipspace corp -o jsonpath='{.status.id}'
# ipam/ip_space/... (non-empty means ready)
```

## Complete Example

Create an IPSpace and HostRegistrationPolicy for worker nodes:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPSpace
metadata:
  name: corp
spec:
  name: corp
  comment: Nodes and services
---
apiVersion: uddi.infoblox.com/v1alpha1
kind: HostRegistrationPolicy
metadata:
  name: workers
  namespace: uddi-system
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
    team: platform
    role: worker
  lifecycle:
    deletionPolicy: Delete
```

Apply it:

```bash
kubectl apply -f - <<EOF
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPSpace
metadata:
  name: corp
spec:
  name: corp
  comment: Nodes and services
---
apiVersion: uddi.infoblox.com/v1alpha1
kind: HostRegistrationPolicy
metadata:
  name: workers
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
EOF
```

The policy watches all worker Nodes. For each Node with an internal IP address:

- If a MAC is available (annotation, NMState or Metal3), a FixedAddress is created
- If no MAC is available, a named Address is created with the hostname
- The registration object is labeled and tagged with the policy name

Check the policy status:

```bash
kubectl get uddihostreg workers
# NAME      READY   REGISTERED   PENDING   AGE
# workers   True    3            0         2m

kubectl describe uddihostreg workers
# Status:
#   Conditions:
#     Last Transition Time:  2026-09-14T12:00:00Z
#     Message:               All nodes registered
#     Reason:                Registered
#     Status:                True
#     Type:                  Ready
#   Registered:  3
#   Pending:     0
```

List the generated objects:

```bash
# List all registrations for the policy
kubectl get address,fixedaddress -l uddi.infoblox.com/policy=workers
# NAME                                 READY   ADDRESS
# address.uddi.infoblox.com/workers-node-1    True    10.0.0.10
# address.uddi.infoblox.com/workers-node-2    True    10.0.0.11
# fixedaddress.uddi.infoblox.com/workers-node-3  True    10.0.0.12

# List all registrations for a specific Node
kubectl get address,fixedaddress -l uddi.infoblox.com/node=node-1
```

## Node Annotations

All annotations are prefixed with `uddi.infoblox.com/`. They override policy defaults per Node.

### `register: "false"`

Opts the Node out of registration. No object is created or maintained.

```bash
kubectl annotate node node-1 uddi.infoblox.com/register=false
```

To re-opt-in, remove or set the annotation to `"true"`:

```bash
kubectl annotate node node-1 uddi.infoblox.com/register- # removes the annotation
# or
kubectl annotate node node-1 uddi.infoblox.com/register=true --overwrite
```

### `hostname: <name>`

The hostname registered on the object (default: the Node name, which is usually an FQDN). Overrides the policy default for this Node.

```bash
kubectl annotate node node-1 uddi.infoblox.com/hostname=node-1.example.com
```

### `address: <ip>`

The address to register instead of the Node's status address. The address must be in the IPSpace. Overrides the policy's `addressType` and the Node's status address.

```bash
kubectl annotate node node-1 uddi.infoblox.com/address=10.0.0.50
```

### `mac: <mac-address>`

The Node's MAC address when no other source knows it. Format: lower-case colon-separated (e.g., `52:54:00:aa:bb:cc`). Has the highest priority in the MAC discovery chain.

```bash
kubectl annotate node node-1 uddi.infoblox.com/mac=52:54:00:aa:bb:cc
```

## Registration Modes

The policy's `spec.register.mode` controls which kind of object is created.

### `Auto` (default)

- If a MAC is discovered, create a FixedAddress (DHCP reservation bound to hardware)
- If no MAC is discovered, create a named Address (hostname-based reservation)

This mode adapts to available information and is the recommended default. When a MAC later becomes available (via annotation or NMState update), the generated object is upgraded: the Address is released and a new FixedAddress is created.

**Example:**

```yaml
spec:
  register:
    mode: Auto
```

### `Address`

Always create a named IPAM Address with the hostname. The MAC is included in the `hwaddr` field if known, but the object is not a DHCP reservation.

```yaml
spec:
  register:
    mode: Address
```

### `FixedAddress`

Always create a DHCP FixedAddress. Requires a MAC; a Node without a MAC is skipped with reason `NoMAC`.

```yaml
spec:
  register:
    mode: FixedAddress
    dhcpOptions:
      - type: option
        optionCode: dhcp/option_code/<uuid>
        optionValue: "10.0.0.1"  # gateway
```

## Generated Object Naming and Labeling

The policy generates registration objects with stable names to avoid duplicates:

```
<policy-name>-<node-name>
```

If the name would exceed 253 characters (Kubernetes limit), the node name is truncated to fit:

```
<policy-name>-<hash(10 chars)>
```

Example: `workers-node-1` for a policy named `workers` and Node named `node-1`.

Generated objects carry these labels (used for filtering):

| Label | Value |
| --- | --- |
| `uddi.infoblox.com/policy` | Policy name (e.g., `workers`) |
| `uddi.infoblox.com/node` | Node name, shortened with a hash suffix when longer than 63 characters (see below) |
| `uddi.infoblox.com/interface` | Name of the additional interface; only on registrations generated for an `additionalInterfaces` entry |

Label values are limited to 63 characters, but Node names can be longer. A Node name that does not fit is stored in the `node` label as its first characters followed by `-` and 10 hex digits of its sha256, so the label stays valid and the policy still matches the object to its Node. Pass the shortened value, not the full name, when you select by `uddi.infoblox.com/node` for such a Node.

List all registrations for a policy:

```bash
kubectl get address,fixedaddress -l uddi.infoblox.com/policy=workers
```

List registrations for a specific Node:

```bash
kubectl get address,fixedaddress -l uddi.infoblox.com/node=node-1
```

The object spec includes:
- **Ownership:** ownerReference to the Node (garbage collection on Node deletion)
- **Space:** copied from the policy's `spaceRef`
- **Address:** from the Node's status address, the `address` annotation, or derived from the Node name
- **Hostname:** from the `hostname` annotation or the Node name
- **MAC (FixedAddress only):** discovered from annotation, NMState, or annotation override
- **Comment:** "Registered by HostRegistrationPolicy <namespace>/<policy> for Node <name>"
- **Tags:** policy tags plus `uddi_operator_source: Node/<name>`
- **Lifecycle:** policy's `lifecycle` settings

## MAC Discovery

The policy discovers a Node's MAC address in priority order:

1. **Node annotation `uddi.infoblox.com/mac`** (highest priority): the MAC is directly on the Node. Set it by hand when the operator has no other way to learn it. Accepted formats (all normalized to lower-case colon form, e.g. `52:54:00:aa:bb:cc`):
   - Colon-separated: `52:54:00:aa:bb:cc`
   - Hyphen-separated: `52-54-00-aa-bb-cc`
   - Cisco dot-separated: `5254.00aa.bbcc`
   - 12 bare hex digits: `525400aabbcc`
2. **NMState NodeNetworkState** (when enabled and the API exists): the operator reads the `NodeNetworkState` object named after the Node and extracts the MAC from:
   - The interface named `spec.macSource.nmstate.interface`, if set (must be an exact interface name match)
   - The interface carrying the registered IP address when `interface` is empty (the first interface in report order whose ipv4 or ipv6 addresses include the registered address; loopback interfaces are always skipped, since loopback MACs and addresses cannot represent a Node's identity)
   - For details on NodeNetworkState structure and interface discovery, see the [NMState reporting guide](https://raw.githubusercontent.com/nmstate/kubernetes-nmstate/main/docs/content/user-guide/101-reporting.md) and [NMState examples](https://nmstate.io/examples.html).
3. **Metal3 BareMetalHost** (when `macSource.metal3.enabled` is true and the `metal3.io/v1alpha1` API exists): the operator reads the BareMetalHosts in `macSource.metal3.namespace` and takes the MAC of the NIC that carries the registered address, or else the boot MAC of the host that has the Node's name. See [Metal3 BareMetalHosts](#metal3-baremetalhosts).
4. **No MAC** (lowest priority): the MAC remains unknown.

### Enabling MAC Discovery via NMState

To read MACs from the NMState operator, set `spec.macSource.nmstate.enabled: true`:

```yaml
spec:
  macSource:
    nodeAnnotation: uddi.infoblox.com/mac
    nmstate:
      enabled: true
      interface: ""  # empty means find the interface by IP
```

The policy checks at startup whether the `nmstate.io/v1beta1 NodeNetworkState` API is present on the cluster. If it is, Nodes are watched for changes and MACs are re-read on every reconcile. If the API is absent, MAC discovery falls back to the annotation.

### Metal3 BareMetalHosts

On bare-metal IPI clusters, Metal3 inspects every machine and keeps the result on a `BareMetalHost` (`metal3.io/v1alpha1`). The policy can read the MAC from there when neither the annotation nor NodeNetworkState supplies one. It is the last source consulted.

```yaml
spec:
  macSource:
    metal3:
      enabled: true                      # default
      namespace: openshift-machine-api   # default
```

Both fields default as shown, so on a cluster that serves the BareMetalHost API this source is on unless you set `enabled: false`. The lookup runs in this order and stops at the first answer:

1. The host that has a NIC in `status.hardware.nics[]` whose `ip` equals the registered address gives that NIC's `mac`. IP addresses are compared as addresses, so any textual form of an IPv6 address matches. A dual-stack NIC is listed once per IP, with the same MAC.
2. Otherwise the host whose `status.hardware.hostname` equals the Node name (compared without case and without a trailing dot) gives its `spec.bootMACAddress`.

The inventory lives under `status.hardware` (not `status.hardwareDetails`) and is filled in by Metal3's inspection, so a host that has not been inspected has nothing to match. If several hosts claim the same IP, the first by namespace and name wins.

```yaml
apiVersion: metal3.io/v1alpha1
kind: BareMetalHost
metadata:
  name: worker-0
  namespace: openshift-machine-api
spec:
  bootMACAddress: 52:54:00:aa:bb:01
status:
  hardware:
    hostname: worker-0.ocp1.example.com
    nics:
      - name: eno1
        mac: 52:54:00:aa:bb:01
        ip: 10.10.0.11
```

For a Node whose registered `InternalIP` is `10.10.0.11`, the policy picks up `52:54:00:aa:bb:01` and, in `Auto` mode, registers a FixedAddress.

The operator reads BareMetalHosts as unstructured data and takes no dependency on the Metal3 Go module. The manager checks once at startup whether the API is served; on a cluster without it, it logs `the Metal3 BareMetalHost API is not served; Metal3 MAC discovery is inert` and the annotation and NMState sources work as before.

BareMetalHost changes wake every HostRegistrationPolicy, but only when `spec.bootMACAddress` or `status.hardware` changed. The provisioning state and heartbeat timestamps on a BareMetalHost change all the time and are filtered out, so they do not trigger reconciles. Creating or deleting a host always does.

**Running with `WATCH_NAMESPACE`.** When the operator watches only some namespaces (`OwnNamespace`, `SingleNamespace` or `MultiNamespace` install), the BareMetalHost namespace (`openshift-machine-api` by default) should be one of them. Otherwise listing the hosts fails, the policy's reconcile returns an error and retries, and no registration is created or updated until you add the namespace or set `macSource.metal3.enabled: false`.

### Manual MAC Assignment

When automatic discovery is unavailable or you need to override it, annotate the Node:

```bash
kubectl annotate node node-1 uddi.infoblox.com/mac=52:54:00:aa:bb:cc
# or update the annotation
kubectl annotate node node-1 uddi.infoblox.com/mac=52:54:00:aa:bb:cc --overwrite
```

To remove the manual MAC (so discovery reverts to NMState or the node has no MAC):

```bash
kubectl annotate node node-1 uddi.infoblox.com/mac-
```

## Additional Interfaces

A Node often sits on more than one network: a storage network, a provisioning network, a management VLAN. `spec.additionalInterfaces` registers one more address per entry, for every selected Node. At most 8 entries are allowed, and the names must be unique.

```yaml
spec:
  spaceRef:
    name: cluster
  register:
    mode: Auto
  additionalInterfaces:
    - name: storage
      cidr: 10.20.0.0/16
      spaceRef:
        name: storage        # defaults to the policy's spaceRef
      hostnameSuffix: -storage
```

| Field | Required | Description |
| --- | --- | --- |
| `name` | Yes | Identifies the interface within the policy: 1 to 63 characters, lower-case alphanumerics and `-`. It becomes part of the generated object names and the value of the `uddi.infoblox.com/interface` label. |
| `cidr` | Yes | An IPv4 or IPv6 CIDR (at most 43 characters). The first Node address inside it is the interface's address. |
| `spaceRef` | No | The IPSpace CR, in this namespace, that holds the interface address. Only a `name` is accepted (CEL: `spaceRef must set name`). Defaults to the policy's `spaceRef`. |
| `hostnameSuffix` | No | Letters, digits and `-`, up to 63 characters. Inserted after the first label of the Node's hostname: `-storage` turns `node1.example.com` into `node1-storage.example.com`. A hostname of one label gets the suffix appended. Empty keeps the hostname unchanged. |

### How an Interface Is Resolved

For each selected Node and each entry:

- **Address:** the first IP inside `cidr` among the Node's `status.addresses` (of any type), and when none matches, among the interface addresses in the Node's NodeNetworkState (when `macSource.nmstate` is enabled and the API exists). IPv4 and IPv6 both work, and an address of the other family never matches. Metal3 supplies MACs, not addresses. Nodes rarely list a secondary network in `status.addresses`, so NodeNetworkState is the usual source.
- **MAC:** the MAC of the NodeNetworkState interface carrying that address, then of the Metal3 NIC carrying it. The `mac` annotation on the Node belongs to the primary registration and is not used for additional interfaces.
- **Hostname:** the Node's registered hostname (its `hostname` annotation, else its name) with `hostnameSuffix` inserted after the first label. The suffix is lower-cased, and the result must be a valid DNS name whose first label is at most 63 characters.

### What Is Generated

Each entry becomes its own registration object, using the policy's `register.mode` (in `Auto` mode a FixedAddress when the interface's MAC is known, an Address otherwise):

- **Name:** `<policy>-<node>-<interface>`, for example `workers-node1.example.com-storage`. Above 253 characters the node part is replaced by 10 hex digits of its sha256 (`<policy>-<hash>-<interface>`), so the interface name always survives.
- **Labels:** `uddi.infoblox.com/policy`, `uddi.infoblox.com/node` and `uddi.infoblox.com/interface: <interface name>`. The Node's primary registration has no `interface` label.
- **Space:** the entry's own `spaceRef`, or the policy's. The IPSpace is watched like the policy's own.
- **Comment and tags:** `Registered by HostRegistrationPolicy <namespace>/<policy> for Node <name> interface <interface>`, and the usual `uddi_operator_source: Node/<name>`.
- **Owner:** the Node, so deleting the Node garbage-collects all its registrations.

List a Node's registrations, primary and additional, or only the interface ones:

```bash
kubectl get address,fixedaddress -l uddi.infoblox.com/node=node1.example.com -n uddi-system
kubectl get address,fixedaddress -l uddi.infoblox.com/interface=storage -n uddi-system
```

### How Interfaces Behave

- **Independent of the primary registration.** Interface and primary registrations are planned separately. A Node without an address of `addressType` still gets its interface registrations, and an interface that fails never affects the primary one.
- **Counted individually.** Every registration object counts towards `status.registered` and `status.pending`.
- **Released with the Node.** Opting a Node out with `register: "false"`, or no longer selecting it, releases all its registrations. Removing an entry from the spec releases that interface's registrations on every Node.
- **Transient problems keep what exists.** When an interface hits `NoAddress`, `NoMAC`, `InvalidInterface` or `SpaceNotReady`, an object already registered for it is left alone rather than released.
- **Skips name the interface.** A skipped interface is reported with the Node's name in `node` and a message that starts with `interface <name>:`, as in the examples under [Skip Reasons for Interfaces](#skip-reasons-for-interfaces).

## Status Fields and Conditions

The policy's `status` reports registration progress:

| Field | Type | Description |
| --- | --- | --- |
| `registered` | int32 | Number of registration objects that are Ready: one per Node, plus one per additional interface of each Node |
| `pending` | int32 | Number of registration objects that exist but are not Ready |
| `skipped` | []SkippedNode | Nodes not registered or not Ready (bounded to 50 entries, sorted by node name then reason) |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from |
| `conditions` | []metav1.Condition | Conditions: `Ready` (summary) |

The `Ready` condition:
- `True/Registered`: Nothing is pending and no `SpaceNotReady` skip remains
- `False/RegistrationsPending`: Some registration objects exist but are not Ready yet
- `False/SpaceNotReady`: The IPSpace CR is missing or has no `status.id`; this covers the policy's `spaceRef` and the `spaceRef` of every additional interface, and the message names each space that is not ready

Each SkippedNode entry has:

| Field | Type | Description |
| --- | --- | --- |
| `node` | string | Node name |
| `reason` | string | Why it was skipped |
| `message` | string | Additional context |

### Skip Reasons and Remediation

| Reason | Cause | What to Do |
| --- | --- | --- |
| `Excluded` | The Node has `uddi.infoblox.com/register=false`. | This is not an error; the Node is intentionally opted out. Set the annotation to `"true"` or remove it to re-register. |
| `NoAddress` | The Node has no address of the selected type (e.g., no InternalIP when `addressType: InternalIP`). | This usually means the Node is not Ready. Wait for the Node to become Ready and have an address. Or use the `address` annotation to override. |
| `NoMAC` | Mode is `FixedAddress` and no MAC was discovered: no annotation, NodeNetworkState or Metal3 BareMetalHost provides one. | Set the MAC manually with the `mac` annotation: `kubectl annotate node <node> uddi.infoblox.com/mac=<mac>`. Or switch the policy mode to `Auto` or `Address`. Or enable NMState or Metal3 discovery. |
| `InvalidAnnotation` | A malformed `hostname`, `address`, or `mac` annotation value. | Fix the annotation value. Check the message for details. |
| `SpaceNotReady` | The IPSpace CR is missing or has no `status.id` (the space is pending). | Wait for the IPSpace to become Ready; check `kubectl describe ipspace <name>`. Or check that the `spaceRef` name is correct. |
| `RegistrationPending` | The registration object exists but is not Ready yet (being created or updated in Universal DDI). | Wait for the object to become Ready; check `kubectl describe <object-kind> <object-name>`. Registration failures (API errors, conflicts, out-of-range address) are visible on the object's Ready condition message. |

Example: view skipped Nodes and troubleshoot:

```bash
kubectl describe uddihostreg workers | grep -A 30 "Skipped:"

# Status:
#   Skipped:
#     - Node: node-2
#       Reason: NoMAC
#       Message: mode FixedAddress needs a MAC; no annotation, NodeNetworkState or Metal3 BareMetalHost provides one
#     - Node: node-3
#       Reason: RegistrationPending
#       Message: FixedAddress workers-node-3 is not Ready yet; condition: Creating
```

### Skip Reasons for Interfaces

A skipped additional interface appears in `status.skipped` with the Node's name in `node`, and a `message` that starts with `interface <name>: `. It uses the reasons above with these meanings, plus two reasons that only exist since interfaces were added:

| Reason | Cause | What to Do |
| --- | --- | --- |
| `NoAddress` | No Node address and no NodeNetworkState interface address lies inside the entry's `cidr`. | Check the `cidr`, and that NodeNetworkState is enabled and available when the address is not in `status.addresses`. |
| `NoMAC` | Mode is `FixedAddress` and neither a NodeNetworkState interface nor a Metal3 NIC carries the interface address. | Enable NMState or Metal3 discovery, or switch the policy mode to `Auto` or `Address`. The Node's `mac` annotation does not apply to interfaces. |
| `InvalidInterface` | The entry's `cidr` does not parse, or inserting `hostnameSuffix` does not give a valid DNS name (for example a first label over 63 characters). | Fix the entry in the spec. |
| `SpaceNotReady` | The IPSpace the entry uses (its own `spaceRef`, or the policy's) is missing or has no `status.id`. | Wait for the IPSpace, or fix the `spaceRef` name. |
| `RegistrationPending` | The interface's Address or FixedAddress exists but is not Ready. | Check the object's Ready condition, as for a primary registration. |
| `NameConflict` | The registration would get the same object name as another registration of this policy. | Rename the interface, or the Node or interface that collides with it. |

`NameConflict` happens because the object name is `<policy>-<node>-<interface>`. The primary registration of a Node called `node1-storage` and the `storage` interface of Node `node1` are both `workers-node1-storage`, and dashes inside interface names allow the same clash between interfaces of different Nodes. The primary registration always wins. Between two interfaces, the first Node in name order wins. The skipped entry names the holder:

```yaml
status:
  skipped:
    - node: node1
      reason: NameConflict
      message: "interface storage: object name workers-node1-storage is already used by Node node1-storage"
    - node: node2
      reason: NoAddress
      message: "interface storage: no Node or NodeNetworkState address inside 10.20.0.0/16"
```

## Release and Deletion Behavior

### Opt-Out

When a Node's `register` annotation is set to `"false"`, the policy releases the registration:

1. The registration object (Address or FixedAddress) is deleted
2. The object's finalizer releases the address to Universal DDI (or orphans it per the lifecycle policy)

```bash
kubectl annotate node node-1 uddi.infoblox.com/register=false --overwrite
```

### Node Deletion

When a Node is deleted, its registration object is automatically garbage-collected by Kubernetes through the ownerReference. The object's finalizer then releases the address.

### Policy Deletion

When a HostRegistrationPolicy is deleted:

- Registration objects remain in place with their ownership tags (`uddi_operator_source: Node/<name>`)
- Running Nodes keep their address reservations in Universal DDI

This ensures that deleting the policy does not disrupt cluster infrastructure. To release addresses when removing a policy, delete the registration objects manually:

```bash
# Delete all registration objects for a policy
kubectl delete address,fixedaddress -l uddi.infoblox.com/policy=workers

# Or opt out each Node first, then delete the policy
kubectl annotate node -l node-role.kubernetes.io/worker "" uddi.infoblox.com/register=false --overwrite
```

### IPSpace Deletion

Deleting an IPSpace does not affect running registration objects or Nodes. The next reconcile of affected HostRegistrationPolicies will report `SpaceNotReady` and the registrations will be pending until the IPSpace is recreated and Ready.

### Adoption Never Takes Over Another CR's Object

`lifecycle` is copied to every generated Address and FixedAddress. With `adoptExisting: true`, a registration can take over an existing address or reservation in Universal DDI that no CR manages. It never takes over one that another CR already manages (it carries the managed marker and another CR's uid tag): the generated object then reports `Ready=False/AlreadyExists` with the other owner named in its message, and the policy shows the Node as `RegistrationPending`. To hand an object over deliberately, remove its `uddi_operator_*` tags in Universal DDI first.

## Combining with DNSPublishPolicy and IPAllocationPolicy

Host registration is independent of DNS publishing and IP allocation, but can be combined:

- **DNSPublishPolicy** publishes cluster endpoints (Routes, Ingresses, Services, and with the opt-in sources Nodes, DNSRecords and HTTPRoutes) as DNS records
- **IPAllocationPolicy** allocates LoadBalancer Service and EgressIP addresses from pools
- **HostRegistrationPolicy** registers Nodes as DHCP or named addresses

Node DNS, including reverse (PTR) entries, is not a host registration concern. A DNSPublishPolicy with the `Node` source and `createPtr: true` publishes each Node's A and AAAA records and asks Universal DDI to create the matching PTR records; see [Node Sources](dns-publishing.md#node-sources) and [docs/patterns.md](patterns.md). Host registration stays an IPAM and DHCP matter.

All three use the same Connection for credentials and support custom tags for tracking ownership. Use consistent tagging to track related objects:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: HostRegistrationPolicy
metadata:
  name: nodes
spec:
  tags:
    team: platform
    cluster: openshift-prod
```

## Limitations

### One Registration Per Node and Interface

Each Node has one primary registration object (one Address or one FixedAddress) for its `addressType` address, and one more per `additionalInterfaces` entry. The primary registration always lives in the policy's `spaceRef`; an interface registration lives in the IPSpace of its own `spaceRef` (default: the policy's). An entry registers only the first address it finds inside its `cidr`, and one policy holds at most 8 entries. See [Additional Interfaces](#additional-interfaces).

### IPSpace Must Contain Node Networks

The IPSpace must have subnets or address blocks that contain the Node's registered address. If the address is outside the space, Universal DDI is expected to reject the registration with an API error (not yet verified against a tenant), which surfaces as `RegistrationPending` with the error message.

### Changing the Kind Replaces the Object

The kind of object a Node gets is decided on every reconcile from the policy's `register.mode` and whether a MAC is known. When the chosen kind changes for a Node or an interface, whether because the policy's `mode` changed or because a MAC appeared or disappeared, the same reconcile applies an object of the new kind and releases the object of the old kind (deleting it, so its finalizer releases the address according to its lifecycle policy). A Node switched from `Address` to `FixedAddress` therefore ends up with a FixedAddress and no Address, without any manual step.

To switch a single Node by hand instead, opt it out and back in:

```bash
kubectl annotate node node-1 uddi.infoblox.com/register=false --overwrite
# wait for the registration object to be deleted
kubectl annotate node node-1 uddi.infoblox.com/register- # re-opt-in
```

### Metal3 Is a MAC Source Only

Metal3 `BareMetalHost`s supply MAC addresses (see [Metal3 BareMetalHosts](#metal3-baremetalhosts)). They are never a source of addresses or hostnames for registration, and the policy does not create or change BareMetalHosts.

---

## Reference: More Examples

### Register Only Control Plane Nodes

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: HostRegistrationPolicy
metadata:
  name: masters
spec:
  connectionRef:
    name: default
  selector:
    matchLabels:
      node-role.kubernetes.io/master: ""
  spaceRef:
    name: corp
  addressType: InternalIP
  register:
    mode: Auto
  tags:
    role: master
```

### Use FixedAddress for All Nodes with a Custom MAC Source

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: HostRegistrationPolicy
metadata:
  name: all-nodes-fixed
spec:
  connectionRef:
    name: default
  selector: {}  # all nodes
  spaceRef:
    name: corp
  addressType: InternalIP
  register:
    mode: FixedAddress
    dhcpOptions:
      - type: option
        optionCode: dhcp/option_code/<uuid>
        optionValue: "10.0.0.1"  # gateway
  macSource:
    nodeAnnotation: custom.company.com/mac-address
    nmstate:
      enabled: false
  tags:
    management: dhcp
```

### Register with External IP Addresses

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: HostRegistrationPolicy
metadata:
  name: external
spec:
  connectionRef:
    name: default
  selector:
    matchLabels:
      external-access: "true"
  spaceRef:
    name: dmz
  addressType: ExternalIP
  register:
    mode: Address  # hostname only, no MAC binding for public IPs
  tags:
    zone: external
```

### Override Hostname for a Node

When a Node name is not suitable for registration (e.g., a random cluster-generated name), override it per Node:

```bash
kubectl annotate node node-abc123def456 uddi.infoblox.com/hostname=node-prod-01.example.com
```

Or set it in a ConfigMap-driven init before the operator runs, if your cluster provisioning tool supports it.
