# Integration Patterns: A Complete Bare-Metal Setup

This guide walks through a realistic integration scenario: a bare-metal OpenShift cluster `ocp1` with base domain `example.com` and applications at `apps.ocp1.example.com`. We'll show how each policy owns different aspects of the cluster infrastructure and how they work together.

## 1. Policies and Their Roles

Each policy watches native Kubernetes objects and creates object-layer CRs to manage corresponding Universal DDI resources. The table below shows what each policy does:

| Policy | Watches | Creates | Typical Use |
| --- | --- | --- | --- |
| **HostRegistrationPolicy** | Nodes | Address, FixedAddress | Register every cluster node with its IP and MAC; DHCP or name-based |
| **DNSPublishPolicy** (Node source) | Nodes | Record (A, AAAA, PTR) | Publish node A records and reverse-DNS (PTR) entries |
| **DNSPublishPolicy** (Service source) | LoadBalancer Services | Record (A, AAAA, CNAME) | Publish service hostnames to allocated IPs |
| **DNSPublishPolicy** (DNSRecord source) | DNSRecord (ingress-operator) | Record (A, AAAA, CNAME) | Publish router wildcard `*.apps.ocp1.example.com` |
| **IPAllocationPolicy** | LoadBalancer Services | Address (IPv4 and IPv6) | Allocate IPs to services; mirror to MetalLB |

## Shared Conventions

Every policy and source object uses a common annotation namespace `uddi.infoblox.com/`:

- **`hostname`**: The DNS name and IPAM address name; used by both DNSPublishPolicy and IPAllocationPolicy. Default for Services: `<service>.<namespace>`.
- **`publish: "false"`**: Excludes a source from DNS publishing.
- **`allocate: "true"` or `"false"`**: Controls IP allocation per Service.

Every generated object carries ownership tags (non-overridable):

| Tag | Example Value |
| --- | --- |
| `uddi_operator_managed` | `true` |
| `uddi_operator_cluster` | `ocp1` (the cluster ID) |
| `uddi_operator_owner` | `uddi-system/HostRegistrationPolicy/nodes` |
| `uddi_operator_uid` | Kubernetes UID of the policy CR |

All policies in a namespace share one **Connection** CR that holds API credentials (a Secret with the Infoblox API key), client name, rate limits, and default tags to merge into every object.

## 2. Foundations

### Secret and Connection

Create the API credential Secret in the namespace where policies will run:

```bash
kubectl create secret generic uddi-api-key --from-literal=apiKey=<your Infoblox API key> -n uddi-system
```

Create the Connection CR:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Connection
metadata:
  name: default
  namespace: uddi-system
spec:
  cspURL: https://csp.infoblox.com
  secretRef:
    name: uddi-api-key
    key: apiKey
  clientName: uddi-operator
  defaultTags:
    cluster: ocp1
    environment: production
  resyncInterval: 10m
  rateLimit:
    requestsPerSecond: 10
    burst: 20
```

Verify the connection is ready:

```bash
kubectl get connection default -n uddi-system -o jsonpath='{.status.conditions[?(@.type=="Ready")]}'
# {"type":"Ready","status":"True",...}
```

### View, AuthZone, and IPSpace

Create the DNS View (usually a pre-existing object in Universal DDI that you adopt):

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: View
metadata:
  name: default
  namespace: uddi-system
spec:
  connectionRef:
    name: default
  name: default
  comment: Default DNS view for ocp1
  lifecycle:
    adoptExisting: true
    deletionPolicy: Orphan
```

Adopt the cluster base domain `ocp1.example.com` and the applications domain:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: AuthZone
metadata:
  name: cluster-zone
  namespace: uddi-system
spec:
  fqdn: ocp1.example.com.
  viewRef:
    name: default
  primaryType: cloud
  lifecycle:
    adoptExisting: true
    deletionPolicy: Orphan
---
apiVersion: uddi.infoblox.com/v1alpha1
kind: AuthZone
metadata:
  name: apps-zone
  namespace: uddi-system
spec:
  fqdn: apps.ocp1.example.com.
  viewRef:
    name: default
  primaryType: cloud
  lifecycle:
    adoptExisting: true
    deletionPolicy: Orphan
```

Create the IPSpace for all cluster networks:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPSpace
metadata:
  name: cluster
  namespace: uddi-system
spec:
  name: cluster
  comment: OpenShift cluster networks
```

### Node Network: Subnet and Range

Create a Subnet for the node network `10.10.0.0/24` and a Range for the MetalLB pool:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Subnet
metadata:
  name: node-network
  namespace: uddi-system
spec:
  spaceRef:
    name: cluster
  address: 10.10.0.0
  cidr: 24
  name: node-network
  comment: OpenShift node network
---
apiVersion: uddi.infoblox.com/v1alpha1
kind: Range
metadata:
  name: metallb-pool
  namespace: uddi-system
spec:
  spaceRef:
    name: cluster
  start: 10.10.0.200
  end: 10.10.0.250
  name: metallb-pool
  comment: MetalLB LoadBalancer pool
```

### Storage Network

Create a second Subnet and Range for the storage network `10.20.0.0/24` (used by node secondary interfaces):

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Subnet
metadata:
  name: storage-network
  namespace: uddi-system
spec:
  spaceRef:
    name: cluster
  address: 10.20.0.0
  cidr: 24
  name: storage-network
  comment: Storage network for node secondary interfaces
---
apiVersion: uddi.infoblox.com/v1alpha1
kind: Range
metadata:
  name: storage-pool
  namespace: uddi-system
spec:
  spaceRef:
    name: cluster
  start: 10.20.0.10
  end: 10.20.0.200
  name: storage-pool
  comment: Addresses for node storage interfaces
```

### IPv6 Range for Dual-Stack Services

Create the IPv6 subnet `fd00:10::/64` and a Range in it for the dual-stack service pool. A
Range must lie inside an existing subnet, so the Subnet comes first:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Subnet
metadata:
  name: service-network-v6
  namespace: uddi-system
spec:
  spaceRef:
    name: cluster
  address: "fd00:10::"
  cidr: 64
  name: service-network-v6
  comment: IPv6 network for dual-stack LoadBalancer Services
---
apiVersion: uddi.infoblox.com/v1alpha1
kind: Range
metadata:
  name: metallb-pool-v6
  namespace: uddi-system
spec:
  spaceRef:
    name: cluster
  start: fd00:10::1
  end: fd00:10::ffff
  name: metallb-pool-v6
  comment: MetalLB IPv6 pool
```

Verify the pools are ready before proceeding:

```bash
kubectl get subnet,range -n uddi-system -o custom-columns=NAME:.metadata.name,ID:.status.id,READY:.status.conditions[?(@.type=="Ready")].status
```

## 3. Nodes: Registration and PTR Records

### Node Registration Policy

Register every worker node with its internal IP and MAC address as a FixedAddress (DHCP) when the MAC is known, or as a named Address (IPAM) when it is not:

```yaml
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
    name: cluster
  addressType: InternalIP
  register:
    mode: Auto
    dhcpOptions: []
  macSource:
    nodeAnnotation: uddi.infoblox.com/mac
    nmstate:
      enabled: true
      interface: ""
    metal3:
      enabled: true
      namespace: openshift-machine-api
  additionalInterfaces:
    - name: storage
      cidr: 10.20.0.0/24
      spaceRef:
        name: cluster
      hostnameSuffix: -storage
  tags:
    role: worker
  lifecycle:
    deletionPolicy: Delete
```

This policy:
- Selects all worker nodes via label selector
- Registers the node's InternalIP (primary network)
- Discovers the MAC from the annotation, NMState, or Metal3 BareMetalHost
- Creates a secondary registration for the storage network interface with hostname suffix `-storage`
- Cleans up when the node is deleted

### Node DNS Publishing

Publish node A records and reverse-DNS (PTR) entries. This is separate from registration: registration concerns IPAM and DHCP, while DNS publishing creates the records:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: DNSPublishPolicy
metadata:
  name: nodes
  namespace: uddi-system
spec:
  connectionRef:
    name: default
  sources:
    - Node
  selector:
    matchLabels:
      node-role.kubernetes.io/worker: ""
  zoneRefs:
    - name: cluster-zone
  target:
    mode: A
    addresses:
      - "10.10.0.1"  # not used; nodes publish their own IPs
  createPtr: true
  ttl: 300
  tags:
    role: worker
  recordLifecycle:
    adoptExisting: false
    deletionPolicy: Delete
```

This policy:
- Publishes each node's name and InternalIP as an A record
- Creates matching PTR (reverse DNS) records via `createPtr: true`
- Targets the cluster zone `ocp1.example.com`

Check the registrations and DNS records:

```bash
# List all registrations for the policy
kubectl get address,fixedaddress -l uddi.infoblox.com/policy=workers -n uddi-system

# List DNS records generated by the node policy
kubectl get record -l uddi.infoblox.com/policy=nodes -n uddi-system
```

## 4. Router Wildcard: `*.apps.ocp1.example.com`

The ingress operator creates a DNSRecord object named `default-wildcard` that publishes the router's canonical hostname as `*.apps.ocp1.example.com`. The operator can read this and publish it to Universal DDI. For bare-metal clusters without a DNSRecord, a hand-written Record covers wildcard DNS.

### Via DNSRecord (when the ingress operator has created one)

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: DNSPublishPolicy
metadata:
  name: router
  namespace: uddi-system
spec:
  connectionRef:
    name: default
  sources:
    - DNSRecord
  dnsRecordNamespace: openshift-ingress-operator
  selector:
    matchLabels:
      # Set by the ingress operator on the DNSRecords of each IngressController. It only
      # creates the wildcard record when the IngressController publishes through a
      # LoadBalancer Service.
      ingresscontroller.operator.openshift.io/owning-ingresscontroller: default
  zoneRefs:
    - name: apps-zone
  target:
    mode: Auto
  ttl: 300
  tags:
    role: router
  recordLifecycle:
    adoptExisting: false
    deletionPolicy: Delete
```

Check the policy status:

```bash
kubectl get dnspublishpolicy router -n uddi-system
```

### Via Hand-Written Record (for bare-metal without DNSRecord)

If the ingress operator does not create DNSRecords on your cluster, create a Record CR directly:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Record
metadata:
  name: router-wildcard
  namespace: uddi-system
spec:
  connectionRef:
    name: default
  zoneRef:
    name: apps-zone
  name: "*"
  type: A
  rdata:
    a:
      address: 10.10.0.1
  ttl: 300
  comment: Wildcard for *.apps.ocp1.example.com
  tags:
    role: router
```

Or if the router exposes a canonical hostname (e.g., from a load balancer):

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Record
metadata:
  name: router-wildcard
  namespace: uddi-system
spec:
  connectionRef:
    name: default
  zoneRef:
    name: apps-zone
  name: "*"
  type: CNAME
  rdata:
    cname:
      cname: router.example.com.
  ttl: 300
  comment: Wildcard CNAME for *.apps.ocp1.example.com
  tags:
    role: router
```

## 5. LoadBalancer Services: IP Allocation and DNS Publishing

Services on the cluster can opt into IP allocation and DNS publishing via annotations. MetalLB reads the allocated IP from the service annotation and announces it.

A policy only sees objects in its own namespace: the Services it selects, the pool CRs it allocates from, and the AuthZone CRs it names. A `web` Service in `team-a` is therefore served by policies that live in `team-a`, not by policies in `uddi-system`. The platform team keeps the shared foundations from section 2 in `uddi-system` (the Connection, View, AuthZones, IPSpace, Subnets and the platform's own Ranges). Each tenant namespace adds a small set of its own objects, which this section builds for `team-a`:

| In `team-a` | Why it cannot be shared |
| --- | --- |
| Secret and Connection | A Connection is resolved in the namespace of the object that uses it. |
| Range, inside the platform subnet | The pool CR for `allocateFrom` must be in the policy's namespace. |
| IPAllocationPolicy | It only sees Services in its own namespace. |
| DNSPublishPolicy | It only sees Routes, Ingresses and Services in its own namespace. |

Section 2's objects are created once, in `uddi-system`. The tenant objects that need one of them (the Range needs the IPSpace, the DNS policy needs the zone) refer to it by Universal DDI id, not by name, which is the supported way to share an object across namespaces.

### Tenant Namespace: Secret and Connection

Create the namespace, the credential Secret and the Connection. The tenant can use the same API key as the platform or one of its own:

```bash
kubectl create namespace team-a
kubectl create secret generic uddi-api-key --from-literal=apiKey=<your Infoblox API key> -n team-a
```

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Connection
metadata:
  name: default
  namespace: team-a
spec:
  cspURL: https://csp.infoblox.com
  secretRef:
    name: uddi-api-key
    key: apiKey
  clientName: uddi-operator
  defaultTags:
    cluster: ocp1
    team: team-a
  resyncInterval: 10m
```

```bash
kubectl get connection default -n team-a -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
# True
```

### Tenant Pools: a Range Inside the Platform Subnet

`team-a` gets its own slice of the node network `10.10.0.0/24` (the `node-network` Subnet in section 2) and of the IPv6 network. Each slice is a Range CR in `team-a`. The Range's `spaceRef` names the platform's IPSpace by id, because the `cluster` IPSpace CR lives in `uddi-system` and a name is only resolved in the Range's own namespace. Read the id once:

```bash
kubectl get ipspace cluster -n uddi-system -o jsonpath='{.status.id}'
# ipam/ip_space/5d2f8a10-7c3e-4b69-9a14-0e8b6c1d3f27
```

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Range
metadata:
  name: lb-pool
  namespace: team-a
spec:
  connectionRef:
    name: default
  spaceRef:
    id: ipam/ip_space/5d2f8a10-7c3e-4b69-9a14-0e8b6c1d3f27
  start: 10.10.0.100
  end: 10.10.0.119
  name: team-a-lb-pool
  comment: LoadBalancer addresses for team-a
---
apiVersion: uddi.infoblox.com/v1alpha1
kind: Range
metadata:
  name: lb-pool-v6
  namespace: team-a
spec:
  connectionRef:
    name: default
  spaceRef:
    id: ipam/ip_space/5d2f8a10-7c3e-4b69-9a14-0e8b6c1d3f27
  start: fd00:10::1:1
  end: fd00:10::1:ff
  name: team-a-lb-pool-v6
  comment: IPv6 LoadBalancer addresses for team-a
```

The slices must not overlap the platform's own Ranges (`10.10.0.200` to `10.10.0.250`, and `fd00:10::1` to `fd00:10::ffff`) or each other, so the platform team hands out the boundaries. The IPSpace is not adopted a second time: the Ranges are new objects, and only name the space by id. Check that both are ready:

```bash
kubectl get range -n team-a -o custom-columns=NAME:.metadata.name,ID:.status.id,READY:.status.conditions[?(@.type=="Ready")].status
```

### IP Allocation Policy

Allocate IPs from `team-a`'s own pools to the LoadBalancer Services in `team-a` that opt in:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: IPAllocationPolicy
metadata:
  name: lb
  namespace: team-a
spec:
  connectionRef:
    name: default
  selector: {}
  requireAnnotation: true
  targets:
    - Service
  allocateFrom:
    rangeRef:
      name: lb-pool          # the Range CRs in team-a, created above
  allocateFromIPv6:
    rangeRef:
      name: lb-pool-v6
  assignVia:
    mode: Annotation
    annotation: metallb.io/loadBalancerIPs
  metallb:
    mirrorPools: true        # needs team-a in the operator's --cluster-target-namespaces
  tags:
    purpose: loadbalancer
  addressLifecycle:
    deletionPolicy: Delete
```

This policy:
- Allocates dual-stack (IPv4 and IPv6) addresses to Services in `team-a` with `uddi.infoblox.com/allocate: "true"`, from `team-a`'s two Ranges
- Mirrors those two Ranges into MetalLB's IPAddressPool objects (`uddi-team-a-lb-range-lb-pool` and `uddi-team-a-lb-range-lb-pool-v6` in `metallb-system`, with `autoAssign: false` and usable by `team-a`'s Services only) so MetalLB announces them, once the cluster administrator lists `team-a` in `--cluster-target-namespaces`
- Writes the allocated IPs to `metallb.io/loadBalancerIPs` for MetalLB to use

The platform team's own Ranges in `uddi-system` are unaffected: a policy in `uddi-system` would serve only Services in `uddi-system`.

### Dual-Stack LoadBalancer Service Example

Create a LoadBalancer Service and opt it into allocation and DNS publishing:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: web
  namespace: team-a
  annotations:
    uddi.infoblox.com/allocate: "true"
    uddi.infoblox.com/hostname: "web.apps.ocp1.example.com"
  labels:
    app: web
spec:
  type: LoadBalancer
  ipFamilies:
    - IPv4
    - IPv6
  ipFamilyPolicy: PreferDualStack
  selector:
    app: web
  ports:
    - port: 443
      targetPort: 8443
      name: https
```

When the Service is created:

1. **IPAllocationPolicy** creates an Address CR named `lb-web` in `team-a`, allocating the next IPv4 address from `lb-pool`, and another CR `lb-web-1` for IPv6 from `lb-pool-v6`
2. Once both are Ready, the policy patches the Service with `metallb.io/loadBalancerIPs: 10.10.0.100,fd00:10::1:1`
3. MetalLB reads the annotation and announces both IPs

### DNS Publishing for Services

The `apps.ocp1.example.com.` zone is adopted once, as the `apps-zone` AuthZone in `uddi-system` (section 2). `team-a` does not adopt it again. Its policy names the zone by Universal DDI id and fqdn, so the generated Records reference the zone directly and no AuthZone CR is needed in `team-a`. Read the id from the platform's CR:

```bash
kubectl get authzone apps-zone -n uddi-system -o jsonpath='{.status.id}'
# dns/auth_zone/2c1a9f0e-5b7d-4c9a-a1e2-3d4f5a6b7c8d
```

Publish the service hostname to its allocated IPs:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: DNSPublishPolicy
metadata:
  name: services
  namespace: team-a
spec:
  connectionRef:
    name: default
  sources:
    - Service
  selector: {}
  zoneRefs:
    - id: dns/auth_zone/2c1a9f0e-5b7d-4c9a-a1e2-3d4f5a6b7c8d
      fqdn: apps.ocp1.example.com.
  target:
    mode: A
  ttl: 300
  tags:
    purpose: service
  recordLifecycle:
    adoptExisting: false
    deletionPolicy: Delete
```

The `id` and `fqdn` go together: `fqdn` is what hostnames are matched against, so it must equal the zone's real fqdn, and the operator does not check the id. A mistyped id only shows when the Records fail to sync.

**Why `team-a` must not adopt the zone again.** It is tempting to give `team-a` its own AuthZone CR for `apps.ocp1.example.com.` with `adoptExisting: true`, as the platform did. That is refused. `adoptExisting` only takes over an object that no other CR manages, and the zone carries the ownership tags of the platform's `apps-zone`. The `team-a` AuthZone would sit at `Ready=False/AlreadyExists`, with a message like `Universal DDI AuthZone dns/auth_zone/... is already managed by uddi-system/AuthZone/apps-zone; adoptExisting only takes over objects no other CR manages`. The refusal protects the zone: two CRs adopting it would keep rewriting its ownership tags, and deleting either CR could remove a zone the other still publishes into. Referencing the zone by id, as above, gives `team-a` what it needs without a second owner. To hand the zone to another CR on purpose, remove its `uddi_operator_*` tags in Universal DDI first.

This policy reads the `uddi.infoblox.com/hostname` annotation from every LoadBalancer Service in `team-a` and creates A and AAAA records pointing to the allocated IP. The flow:

1. Service gets an IP from IPAllocationPolicy → stored in `metallb.io/loadBalancerIPs`
2. DNSPublishPolicy reads the hostname annotation and the IP from `status.loadBalancer.ingress`
3. A Record CR is created with the IP as the target
4. The Record is applied to Universal DDI, creating A (and AAAA for IPv6) records

Check the allocation and DNS status:

```bash
# Check service annotation for allocated IPs
kubectl get service web -n team-a -o jsonpath='{.metadata.annotations.metallb\.io/loadBalancerIPs}'
# 10.10.0.100,fd00:10::1:1

# Check generated Address CRs
kubectl get address -l uddi.infoblox.com/service=web -n team-a

# Check generated Record CRs
kubectl get record -l uddi.infoblox.com/source-kind=Service -n team-a

# Check the policies
kubectl get ipallocationpolicy,dnspublishpolicy -n team-a
```

## 6. Migrating from external-dns

If you are migrating from external-dns, enable compatibility mode to read existing `external-dns.alpha.kubernetes.io/` annotations on Routes, Ingresses, and Services. The policy lives in `team-a` next to the objects it publishes, and it uses the Connection created for `team-a` in section 5. As there, it names the platform's `apps.ocp1.example.com.` zone by id and fqdn instead of adopting it again; the id is the one read from `uddi-system/apps-zone` in section 5:

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
    - Service
  zoneRefs:
    - id: dns/auth_zone/2c1a9f0e-5b7d-4c9a-a1e2-3d4f5a6b7c8d
      fqdn: apps.ocp1.example.com.
  target:
    mode: Auto
  compatibility:
    externalDNS: true
  ttl: 300
  tags:
    team: team-a
  recordLifecycle:
    adoptExisting: true
    deletionPolicy: Delete
```

With `compatibility.externalDNS: true`:
- The policy reads `external-dns.alpha.kubernetes.io/hostname`, `/ttl` and `/target` in addition to the matching `uddi.infoblox.com/` annotations, on every source kind
- The external-dns `ttl` may be an integer number of seconds or a duration such as `5m`
- The `uddi.infoblox.com` annotation wins when both are set, annotation by annotation (the values are never merged)
- Existing external-dns users can migrate without relabelling objects
- `recordLifecycle.adoptExisting: true` lets the Records take over DNS records external-dns already created, as long as no other CR manages them

Example: a Route with an existing external-dns annotation will continue to publish without modification:

```yaml
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: legacy
  annotations:
    external-dns.alpha.kubernetes.io/hostname: legacy.apps.example.com
    external-dns.alpha.kubernetes.io/ttl: "120"
spec:
  host: legacy.apps.example.com
  to:
    kind: Service
    name: legacy
```

The policy reads the external-dns annotations and publishes the record. Once the migration is complete, you can drop the compatibility setting and relabel to `uddi.infoblox.com/`.

## 7. Checking Status and Troubleshooting

### Verify Connections and Policies Are Ready

```bash
# Check the Connection
kubectl get connection default -n uddi-system -o wide

# Check all policies
kubectl get hostregistrationpolicy,dnspublishpolicy,ipallocationpolicy -n uddi-system
```

### Check Node Registrations

```bash
# View registration status
kubectl get uddihostreg workers -n uddi-system
kubectl describe uddihostreg workers -n uddi-system

# List all registration objects
kubectl get address,fixedaddress -l uddi.infoblox.com/policy=workers -n uddi-system

# Check a specific node's registrations (primary and secondary)
kubectl get address,fixedaddress -l uddi.infoblox.com/node=worker-1 -n uddi-system
```

### Check DNS Publishing

```bash
# View policy status for all DNS sources
kubectl get dnspublishpolicy -n uddi-system
kubectl describe dnspublishpolicy nodes -n uddi-system  # check published count and skipped reasons

# List generated Records
kubectl get record -n uddi-system -o wide

# List Records from a specific policy
kubectl get record -l uddi.infoblox.com/policy=services -n team-a

# Check a Record's status
kubectl describe record <record-name> -n uddi-system
```

### Check IP Allocation

```bash
# View policy status
kubectl get ipallocationpolicy lb -n team-a
kubectl describe ipallocationpolicy lb -n team-a

# List all allocated Addresses
kubectl get address -l uddi.infoblox.com/policy=lb -n team-a

# Check MetalLB pools created by mirroring
kubectl get ipaddresspool -n metallb-system -o wide
```

### Troubleshooting Order

When something is not working, follow this order:

1. **Connection ready?**
   ```bash
   kubectl get connection default -n uddi-system -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
   # Should be True. If False, check the Secret and API key.
   ```

2. **IPSpace, Subnets, and Ranges ready?**
   ```bash
   kubectl get ipspace,subnet,range -n uddi-system -o custom-columns=NAME:.metadata.name,READY:.status.conditions[?(@.type=="Ready")].status,ID:.status.id
   # All should have Ready=True and a status.id.
   ```

3. **AuthZones ready?**
   ```bash
   kubectl get authzone -n uddi-system -o custom-columns=NAME:.metadata.name,FQDN:.spec.fqdn,READY:.status.conditions[?(@.type=="Ready")].status,ID:.status.id
   # Both zones should have Ready=True and a status.id.
   ```

4. **Host registrations ready?**
   ```bash
   kubectl get address,fixedaddress -n uddi-system --sort-by=.metadata.name -o wide
   # All should have Ready=True. If not, check the individual object's Ready condition and message.
   ```

5. **DNS records ready?**
   ```bash
   kubectl get record -n uddi-system --sort-by=.metadata.name
   # Check published count in policy status; skipped reasons point to problems.
   ```

6. **Service IPs allocated?**
   ```bash
   kubectl get service <svc-name> -o jsonpath='{.metadata.annotations.metallb\.io/loadBalancerIPs}'
   # Should show the allocated IPs. If empty, check the Address status.
   ```

### Common Issues

| Symptom | Check | Likely Cause |
| --- | --- | --- |
| Connection not Ready | `kubectl describe connection default` | API key invalid, Secret missing, or Infoblox tenant unreachable |
| Policy stays "False/ZonesNotReady" or "False/SpaceNotReady" | Zone or space Ready condition | The referenced CR is not Ready; check its status |
| Records published but not in Universal DDI | Check Record Ready condition and message | API error or insufficient permissions; message shows the error |
| Service has no allocated IP | Check Address Ready condition | Pool exhausted, Address has API error, or allocation failed |
| Node has no registration | Check HostRegistrationPolicy skipped list | Node excluded, no address, no MAC (if FixedAddress mode), or space not ready |

### Debug Logging

The manager logs through controller-runtime's zap logger, so the level is the
`--zap-log-level` flag on the manager container. For an install made with `make deploy`,
add the flag to the container's arguments:

```bash
oc -n uddi-operator-system patch deployment uddi-operator-controller-manager --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--zap-log-level=debug"}]'
```

Under OLM, the ClusterServiceVersion owns the Deployment and reverts direct edits; add the
same argument to the deployment spec inside the ClusterServiceVersion instead. Then follow
the logs:

```bash
oc -n uddi-operator-system logs deployment/uddi-operator-controller-manager -f
```

The logs name the controller and object for each reconcile and carry API errors with the
key redacted.
