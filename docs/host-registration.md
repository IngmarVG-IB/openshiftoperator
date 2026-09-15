# Host Registration Guide

This guide covers HostRegistrationPolicy: how to automatically register cluster Nodes in Infoblox Universal DDI as DHCP reservations (FixedAddress) when their MAC addresses are known, or as named IPAM addresses (Address) when MAC discovery is not available or when you prefer not to bind to hardware.

## What HostRegistrationPolicy Does

A HostRegistrationPolicy watches cluster Nodes and automatically generates registration objects (Address or FixedAddress) in Universal DDI, owned by the Node. When you create a HostRegistrationPolicy and Nodes match its label selector, the operator:

1. Collects Node metadata: address (from status or annotation), MAC (from annotation or NMState), hostname (from Node name or annotation)
2. Decides whether to register as a DHCP FixedAddress (MAC-based) or a named Address (hostname-based)
3. Creates the registration object owned by the Node, carrying the policy's tags
4. Cleans up the object when the Node is deleted or no longer matches the policy

Generated objects are garbage-collected by the Node's ownerReference, so deleting the Node removes its registration object and releases the address.

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

- If a MAC is available (annotation or NMState), a FixedAddress is created
- If no MAC is available, a named Address is created with the hostname
- The registration object is labeled and tagged with the policy name

Check the policy status:

```bash
kubectl get hostregistrationpolicy workers
# NAME      READY   REGISTERED   PENDING   AGE
# workers   True    3            0         2m

kubectl describe hostregistrationpolicy workers
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
| `uddi.infoblox.com/node` | Node name |

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

1. **Node annotation `uddi.infoblox.com/mac`** (highest priority): the MAC is directly on the Node. Set it by hand when the operator has no other way to learn it.
2. **NMState NodeNetworkState** (when enabled and the API exists): the operator reads the `NodeNetworkState` object named after the Node and extracts the MAC from:
   - The interface named `spec.macSource.nmstate.interface`, if set
   - The interface carrying the registered IP address (when `interface` is empty)
3. **No MAC** (lowest priority): the MAC remains unknown.

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

## Status Fields and Conditions

The policy's `status` reports registration progress:

| Field | Type | Description |
| --- | --- | --- |
| `registered` | int32 | Number of Nodes whose registration object is Ready |
| `pending` | int32 | Number of registration objects that exist but are not Ready |
| `skipped` | []Skip | Nodes not registered (bounded to 50 entries) |
| `observedGeneration` | int64 | The `metadata.generation` the status was last computed from |
| `conditions` | []metav1.Condition | `Ready` condition (summary) |

The `Ready` condition:
- `True/Registered`: All selected Nodes are registered (registered == total selected), and no `RegistrationsPending` skip remains
- `False/RegistrationsPending`: Some Nodes have pending registration objects (not Ready yet)
- `False/SpaceNotReady`: The IPSpace CR is missing or has no `status.id`

Each Skip entry has:

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
| `NoMAC` | Mode is `FixedAddress` and no MAC was discovered. | Set the MAC manually with the `mac` annotation: `kubectl annotate node <node> uddi.infoblox.com/mac=<mac>`. Or switch the policy mode to `Auto` or `Address`. Or enable NMState discovery. |
| `InvalidAnnotation` | A malformed `hostname`, `address`, or `mac` annotation value. | Fix the annotation value. Check the message for details. |
| `SpaceNotReady` | The IPSpace CR is missing or has no `status.id` (the space is pending). | Wait for the IPSpace to become Ready; check `kubectl describe ipspace <name>`. Or check that the `spaceRef` name is correct. |
| `RegistrationPending` | The registration object exists but is not Ready yet (being created or updated in Universal DDI). | Wait for the object to become Ready; check `kubectl describe <object-kind> <object-name>`. Registration failures (API errors, conflicts, out-of-range address) are visible on the object's Ready condition message. |

Example: view skipped Nodes and troubleshoot:

```bash
kubectl describe hostregistrationpolicy workers | grep -A 30 "Skipped:"

# Status:
#   Skipped:
#     - Node: node-2
#       Reason: NoMAC
#       Message: mode FixedAddress needs a MAC; no annotation and no NodeNetworkState
#     - Node: node-3
#       Reason: RegistrationPending
#       Message: FixedAddress workers-node-3 is not Ready yet; condition: Creating
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

## Combining with DNSPublishPolicy and IPAllocationPolicy

Host registration is independent of DNS publishing and IP allocation, but can be combined:

- **DNSPublishPolicy** publishes cluster endpoints (Routes, Ingresses, Services) as DNS records
- **IPAllocationPolicy** allocates LoadBalancer Service IPs from pools
- **HostRegistrationPolicy** registers Nodes as DHCP or named addresses

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

### One Registration Per Node

Each Node can have at most one registration object (one Address or one FixedAddress). A Node cannot be registered in multiple IPSpaces via the same policy. To register a Node with multiple addresses, create multiple policies.

### IPSpace Must Contain Node Networks

The IPSpace must have subnets or address blocks that contain the Node's registered address. If the address is outside the space, Universal DDI rejects the registration with an API error, which surfaces as `RegistrationPending` with the error message.

### Changing Modes Does Not Move Existing Objects

If you change the policy's `mode` from `Address` to `FixedAddress`, existing Address objects are not automatically converted to FixedAddresses. To switch a Node's registration:

1. Release it (set `register: "false"`)
2. Wait for the object to be deleted
3. Change the policy mode
4. Re-opt-in (remove or set `register: "true"`)

```bash
kubectl annotate node node-1 uddi.infoblox.com/register=false --overwrite
# wait for the registration object to be deleted
kubectl annotate node node-1 uddi.infoblox.com/register- # re-opt-in
```

### Metal3 BareMetalHost Not Yet Supported

In bare-metal IPI clusters, Metal3 manages hardware via `BareMetalHost` objects that carry MAC addresses. The operator does not yet read MACs from BareMetalHost; manual annotation or NMState discovery are the current options.

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
