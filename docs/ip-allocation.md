# IP Allocation Guide

This guide covers IPAllocationPolicy: how to automatically allocate IPs from Infoblox Universal DDI to LoadBalancer Services in your OpenShift cluster and publish them via your load balancer implementation.

## What IPAllocationPolicy Does

An IPAllocationPolicy watches LoadBalancer Services in a namespace and automatically generates Address CRs that allocate IPs from a Universal DDI pool (Subnet, Range, or AddressBlock). When you create a LoadBalancer Service and opt in with an annotation, the operator:

1. Creates an Address CR owned by the Service to allocate the next available IP from the pool
2. Waits for the Address to become Ready with the allocated IP
3. Patches the Service to write the IP into the load balancer implementation's annotation or `spec.loadBalancerIP`
4. Cleans up the Address when the Service is deleted or opts out

Addresses are garbage-collected by the Service's ownerReference, so deleting the Service removes the Address and releases the IP.

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

### Load Balancer Support

The operator writes the allocated IP into the Service by patching an annotation (default) or `spec.loadBalancerIP`. Your load balancer implementation must honour the configured key. Tested implementations:

- **MetalLB** (default): reads `metallb.io/loadBalancerIPs` (or the legacy `metallb.universe.tf/loadBalancerIPs`)
- **Cilium**: reads `lbipam.cilium.io/ips`
- **kube-vip**: reads `kube-vip.io/loadbalancerIPs`

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

### One Address Per Service

In this milestone, each Service can have at most one allocated IP (no dual-stack IPv4 + IPv6). Allocate from an IPv4 or IPv6 pool, and the Address will be of the same family.

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
