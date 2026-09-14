# uddi-operator

An OpenShift operator that provides a shim between OpenShift and
[Infoblox Universal DDI](https://www.infoblox.com/products/universal-ddi/).

It models Universal DDI objects (DNS views, zones, records; IPAM spaces,
address blocks, subnets, ranges, addresses; DHCP fixed addresses and options)
as Kubernetes custom resources under the `uddi.infoblox.com` API group, and
builds higher-level integrations on top of them: DNS record sync for Routes,
Ingresses and LoadBalancer Services, IP allocation from Universal DDI, and
host / DHCP registration.

See [DESIGN.md](DESIGN.md) for the architecture and decisions, and
[PLAN.md](PLAN.md) for the implementation plan.

## Status

Pre-alpha. Milestone 1 wave 1 is in: the CRDs for all ten kinds, the Universal DDI
client layer with an in-memory fake, the generic object-layer reconcile loop, the
Connection controller and the View controller as the reference kind. The other eight
controllers are registered but reconcile to a no-op until their wave 2 adapters land.

## Development

Neither operator-sdk nor kubebuilder ships Windows binaries and the generated Makefile
assumes a Linux toolchain, so every Go, `make` and `operator-sdk` command runs in WSL.
On this machine the toolchain is not on the login-shell PATH, so use:

```sh
wsl -e bash -lc 'export PATH=/home/ingmar/goroot/go/bin:/home/ingmar/go/bin:$PATH;   cd /mnt/c/Users/Ingmar/src/openshiftoperator && <command>'
```

Builds on `/mnt/c` go through the 9p filesystem and are slow; allow several minutes for
`make test` and `go mod tidy`.

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

Tests never reach Universal DDI. `internal/uddi.Fake` is an in-memory implementation of
the same interfaces the SDK client satisfies, with the same natural-key uniqueness rules,
`_filter` and `_tfilter` evaluation and next-available allocation, and the controllers
pick it up through the client factory hook on `internal/connection.Resolver`.

Environment variables the manager honours:

| Variable | Effect |
| --- | --- |
| `WATCH_NAMESPACE` | comma-separated namespaces to reconcile; empty or unset means all |
| `CLUSTER_ID` | overrides cluster-id discovery (ClusterVersion, then the kube-system namespace UID) |

## License

Apache 2.0, see [LICENSE](LICENSE).
