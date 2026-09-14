# uddi-operator

An OpenShift operator that shims OpenShift to [Infoblox Universal DDI](https://www.infoblox.com/products/universal-ddi/). The operator models Universal DDI objects (DNS views, zones, records, IP spaces, subnets, ranges, addresses, and DHCP reservations) as namespaced Kubernetes custom resources under the `uddi.infoblox.com` API group, allowing you to manage Infoblox infrastructure declaratively from OpenShift.

## Status

Pre-alpha. Milestone 1 is in: all ten object-layer kinds (Connection, View, AuthZone, Record, IPSpace, AddressBlock, Subnet, Range, Address, FixedAddress) are implemented end to end - CRDs, adapters, controllers, field indexes, ownership tags, adoption, drift correction and deletion - on top of the Universal DDI client layer with its in-memory fake and the generic object-layer reconcile loop. The integration layer (DNSPublishPolicy, IPAllocationPolicy, HostRegistrationPolicy) is next.

## Features

- **DNS management:** Define Views and AuthZones, create and manage A/AAAA/CNAME/TXT/MX/NS/CAA/SRV/PTR records.
- **IPAM management:** Create and allocate from IP spaces, address blocks, subnets, and ranges. Reserve explicit addresses or allocate the next available IP.
- **DHCP management:** Create fixed address reservations matched by MAC address or DUID. Assign DHCP options at space, subnet, range, or fixed address level.
- **Adoption:** Take over existing Universal DDI objects with `lifecycle.adoptExisting=true`.
- **Deletion control:** Use `lifecycle.deletionPolicy: Delete` (default) or `Orphan` to control cleanup.
- **Ownership tracking:** Automatic tags track which cluster and CR owns each Universal DDI object; deletion is guarded by uid tags.
- **Multi-tenancy:** Multiple Connections per namespace support sandboxing or multi-tenant setups.
- **Drift detection:** Automatic re-sync at the Connection's `resyncInterval` (default 10m) corrects out-of-band changes.
- **API key rotation:** The operator detects Secret changes and rebuilds the client.

## How It Works

The operator has two layers. The **object layer** models Universal DDI objects one-to-one as namespaced CRs and provides a generic reconcile loop: every kind uses the same steps to create, adopt, diff, update and delete in Universal DDI. The loop is parameterised by a small adapter interface, so behaviour is consistent across all kinds. The **integration layer** (forthcoming) watches native OpenShift objects (Routes, Ingresses, LoadBalancer Services, Nodes) and expresses the desired Universal DDI state as object-layer CRs—DNS record publishing, IP allocation and host registration all inherit ownership tracking, drift correction and cleanup from the object layer.

## Quick Start

Ensure a Secret with an API key exists in your namespace:

```bash
kubectl create secret generic uddi-api-key --from-literal=apiKey=<your-api-key>
```

Create a Connection to establish access:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Connection
metadata:
  name: default
spec:
  secretRef:
    name: uddi-api-key
```

Create a View:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: View
metadata:
  name: default
spec:
  name: default
```

Create an AuthZone, adopting an existing zone:

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

Create an A record:

```yaml
apiVersion: uddi.infoblox.com/v1alpha1
kind: Record
metadata:
  name: www
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

Check status:

```bash
kubectl get authzone apps-zone
kubectl describe record www
```

See [docs/usage.md](docs/usage.md) for complete task-oriented documentation.

## Documentation

- **[docs/install.md](docs/install.md)** — Installation and setup (operator deployment, RBAC, OLM bundle).
- **[docs/usage.md](docs/usage.md)** — Task-oriented guide: DNS, IPAM, DHCP, ownership tags, deletion, status and troubleshooting.
- **[docs/api.md](docs/api.md)** — Full API reference for all ten kinds and shared types.
- **[DESIGN.md](DESIGN.md)** — Architecture, decisions and implementation notes.
- **[PLAN.md](PLAN.md)** — Implementation roadmap.
- **[docs/uddi-api-notes.md](docs/uddi-api-notes.md)** — Background on the Universal DDI REST API and Go SDK.

## Development

Neither operator-sdk nor kubebuilder ships Windows binaries and the generated Makefile assumes a Linux toolchain, so every Go, `make` and `operator-sdk` command runs in WSL. On this machine the toolchain is not on the login-shell PATH, so use:

```sh
wsl -e bash -lc 'export PATH=/home/ingmar/goroot/go/bin:/home/ingmar/go/bin:$PATH; cd /mnt/c/Users/Ingmar/src/openshiftoperator && <command>'
```

Builds on `/mnt/c` go through the 9p filesystem and are slow; allow several minutes for `make test` and `go mod tidy`.

| Target | What it does |
| --- | --- |
| `make manifests` | regenerates the CRDs in `config/crd/bases` and the RBAC in `config/rbac/role.yaml` |
| `make generate` | regenerates `zz_generated.deepcopy.go` |
| `make fmt vet` | `go fmt` and `go vet` over the module |
| `make build` | builds `bin/manager` |
| `make test` | downloads the envtest binaries if needed, then runs every unit and envtest suite |
| `make lint` | golangci-lint |
| `make run` | runs the manager against the cluster in your current kubeconfig |
| `make install` / `make uninstall` | applies or removes the CRDs |
| `make docker-build`, `make bundle-build`, `make catalog-build` | need a container runtime, which this WSL install does not have |

Tests never reach Universal DDI. `internal/uddi.Fake` is an in-memory implementation of the same interfaces the SDK client satisfies, with the same natural-key uniqueness rules, `_filter` and `_tfilter` evaluation and next-available allocation, and the controllers pick it up through the client factory hook on `internal/connection.Resolver`.

Environment variables the manager honours:

| Variable | Effect |
| --- | --- |
| `WATCH_NAMESPACE` | comma-separated namespaces to reconcile; empty or unset means all |
| `CLUSTER_ID` | overrides cluster-id discovery (ClusterVersion, then the kube-system namespace UID) |

## License

Apache 2.0, see [LICENSE](LICENSE).
