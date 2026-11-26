# uddi-operator

An OpenShift operator that shims OpenShift to [Infoblox Universal DDI](https://www.infoblox.com/products/universal-ddi/). The operator models Universal DDI objects (DNS views, zones, records, IP spaces, subnets, ranges, addresses, and DHCP reservations) as namespaced Kubernetes custom resources under the `uddi.infoblox.com` API group, allowing you to manage Infoblox infrastructure declaratively from OpenShift.

> uddi-operator is an independent community project. It is not affiliated with, endorsed by or supported by Infoblox Inc. or Red Hat, Inc. Infoblox and Universal DDI are trademarks of Infoblox Inc.; Red Hat and OpenShift are trademarks of Red Hat, Inc.

## Project status

**Alpha. Not yet run against a real Universal DDI tenant or a real OpenShift cluster.**

- The API is `v1alpha1`. CRD schemas may change without notice.
- Behaviour is exercised only by unit tests and envtest (a Kubernetes test control plane) against an in-memory fake of the Universal DDI API. Beyond that it has had one manual run: `v1.0.0` installed through OLM on OpenShift 4.22 (ROSA) against a real Universal DDI tenant, covering the nine object kinds and a Route-driven DNSPublishPolicy. Everything else is unproven on real infrastructure.
- A test suite for a real tenant exists (see [docs/testing.md](docs/testing.md)). It passes against a real tenant when run by hand; it is not part of CI.
- `v1.0.0` was the first release; `v1.0.1` fixes a tag-handling bug that the first real-tenant run exposed. The version number marks the first public release, not maturity: the project is alpha.
- The e2e suite is the operator-sdk scaffold smoke test only: the manager pod runs and the metrics endpoint serves.
- Use a sandbox tenant only.

Implemented: an object layer of ten kinds (Connection, View, AuthZone, Record, IPSpace, AddressBlock, Subnet, Range, Address, FixedAddress) and three policy kinds (DNS publishing, IP allocation, host registration). See [docs/limitations.md](docs/limitations.md) for known limitations and [ROADMAP.md](ROADMAP.md) for the plan.

Help is through GitHub Issues, on a best-effort basis. For security reports see [SECURITY.md](SECURITY.md).

## Features

- **DNS management:** Define Views and AuthZones, create and manage A/AAAA/CNAME/TXT/MX/NS/CAA/SRV/PTR records.
- **DNS publishing:** Automatically publish OpenShift Routes, Ingresses, and LoadBalancer Services as DNS records via DNSPublishPolicy.
- **IP allocation:** Automatically allocate IPs to LoadBalancer Services from Universal DDI pools (Subnet, Range, or AddressBlock) via IPAllocationPolicy.
- **Host registration:** Automatically register cluster Nodes in Universal DDI as DHCP or IPAM reservations via HostRegistrationPolicy.
- **IPAM management:** Create and allocate from IP spaces, address blocks, subnets, and ranges. Reserve explicit addresses or allocate the next available IP.
- **DHCP management:** Create fixed address reservations matched by MAC address or DUID. Assign DHCP options at space, subnet, range, or fixed address level.
- **Adoption:** Take over existing Universal DDI objects with `lifecycle.adoptExisting=true`.
- **Deletion control:** Use `lifecycle.deletionPolicy: Delete` (default) or `Orphan` to control cleanup.
- **Ownership tracking:** Automatic tags track which cluster and CR owns each Universal DDI object; deletion is guarded by uid tags.
- **Multi-tenancy:** Multiple Connections per namespace support sandboxing or multi-tenant setups.
- **Drift detection:** Automatic re-sync at the Connection's `resyncInterval` (default 10m) corrects out-of-band changes.
- **API key rotation:** The operator detects Secret changes and rebuilds the client.

## How It Works

The operator has two layers. The **object layer** models Universal DDI objects one-to-one as namespaced CRs and provides a generic reconcile loop: every kind uses the same steps to create, adopt, diff, update and delete in Universal DDI. The loop is parameterised by a small adapter interface, so behaviour is consistent across all kinds. The **integration layer** watches native OpenShift objects (Routes, Ingresses, LoadBalancer Services, Nodes) and expresses the desired Universal DDI state as object-layer CRs—DNS record publishing, IP allocation and host registration all inherit ownership tracking, drift correction and cleanup from the object layer.

## Quick Start

Install the operator first; see [docs/install.md](docs/install.md).

> **Warning:** the quick start creates real objects in the Universal DDI tenant that the API key belongs to. Use a sandbox tenant.

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

- **[docs/install.md](docs/install.md)** — Installation and setup (operator deployment, RBAC, OLM bundle); includes a [Hardening](docs/install.md#hardening) section.
- **[docs/usage.md](docs/usage.md)** — Task-oriented guide: DNS, IPAM, DHCP, ownership tags, deletion, status and troubleshooting.
- **[docs/dns-publishing.md](docs/dns-publishing.md)** — DNS Publishing: automatic publication of Routes, Ingresses, and LoadBalancer Services as DNS records.
- **[docs/ip-allocation.md](docs/ip-allocation.md)** — IP Allocation: automatic IP allocation to LoadBalancer Services from Universal DDI pools.
- **[docs/host-registration.md](docs/host-registration.md)** — Host Registration: automatic registration of cluster Nodes as DHCP or IPAM reservations.
- **[docs/patterns.md](docs/patterns.md)** — Integration patterns: a bare-metal cluster using all three policies together.
- **[docs/testing.md](docs/testing.md)** — Test lanes, and how to run the real-tenant integration suite.
- **[docs/api.md](docs/api.md)** — Full API reference for all kinds and shared types.
- **[docs/observability.md](docs/observability.md)** — Metrics, alerts, dashboards and runbooks for monitoring the operator.
- **[DESIGN.md](DESIGN.md)** — Architecture, decisions and implementation notes.
- **[docs/limitations.md](docs/limitations.md)** — Known limitations and unverified assumptions.
- **[ROADMAP.md](ROADMAP.md)** — The plan from here to a supported 1.0 (milestones 5 to 10).
- **[docs/uddi-api-notes.md](docs/uddi-api-notes.md)** — Background on the Universal DDI REST API and Go SDK.
- **[SECURITY.md](SECURITY.md)** — Supported versions, reporting vulnerabilities, security model.
- **[CONTRIBUTING.md](CONTRIBUTING.md)** — How to build, test and submit changes.

## Development

Prerequisites:

- Go, at the version in `go.mod`
- `make`
- A Linux or macOS shell (on Windows, use WSL; the Makefile assumes a Unix toolchain)
- A container runtime, for the image targets (`make docker-build` and friends)

`make` downloads the other tools it needs (controller-gen, kustomize, setup-envtest, golangci-lint, operator-sdk) into `bin/`.

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
| `make docker-build`, `make bundle-build`, `make catalog-build` | need a container runtime |

Tests never reach Universal DDI. `internal/uddi.Fake` is an in-memory implementation of the same interfaces the SDK client satisfies, with the same natural-key uniqueness rules, `_filter` and `_tfilter` evaluation and next-available allocation, and the controllers pick it up through the client factory hook on `internal/connection.Resolver`.

Environment variables the manager honours:

| Variable | Effect |
| --- | --- |
| `WATCH_NAMESPACE` | comma-separated namespaces to reconcile; empty or unset means all |
| `CLUSTER_ID` | overrides cluster-id discovery (ClusterVersion, then the kube-system namespace UID) |

## License

Apache 2.0, see [LICENSE](LICENSE).
