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

Pre-alpha. Nothing is usable yet.

## License

Apache 2.0, see [LICENSE](LICENSE).
