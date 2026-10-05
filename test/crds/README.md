# test/crds

CRDs that envtest needs but that do not live in `config/crd/bases` because they belong to
an API this operator consumes rather than defines.

## route.openshift.io_routes.yaml

The OpenShift Route CRD (`routes.route.openshift.io`), used by
`internal/routeapi`'s envtest suite and by any future DNSPublishPolicy controller
envtest that needs a real Route resource.

Copied verbatim (not generated, not hand-edited) from the Go module cache:

```
github.com/openshift/api@v0.0.0-20260914154154-9edb1b2245e2/route/v1/zz_generated.crd-manifests/routes.crd.yaml
```

that is,

```
$(go env GOMODCACHE)/github.com/openshift/api@<pseudo-version>/route/v1/zz_generated.crd-manifests/routes.crd.yaml
```

`<pseudo-version>` is whatever `go.mod` currently pins for `github.com/openshift/api`
(an untagged module tracked at `@master`; see `go.mod` for the exact version). This
particular file already carries the `api.openshift.io/merged-by-featuregates: "true"`
annotation, meaning it is openshift/api's fully merged CRD (the union of every feature
gate's variant), so it is the right single file for envtest rather than one of the
per-feature-gate variants under `zz_generated.featuregated-crd-manifests/`.

### Refreshing

1. Bump the pin: `go get github.com/openshift/api@master && go mod tidy`.
2. Find the current file (the name has moved before and may again):
   ```
   find "$(go env GOMODCACHE)/github.com/openshift/api@"* -iname '*routes*crd*'
   ```
3. Copy whichever `routes*.crd.yaml` carries `api.openshift.io/merged-by-featuregates`
   over `route.openshift.io_routes.yaml` unmodified, and update the pseudo-version above.
4. Re-run the `internal/routeapi` envtest suite to confirm it still loads and that
   `Available` still returns true.

## nmstate.io_nodenetworkstates.yaml

Used by `internal/nmstateapi`'s envtest suite and by any future HostRegistrationPolicy
controller envtest that needs a real NodeNetworkState resource.

Unlike `route.openshift.io_routes.yaml` above, this one is **hand-written, not copied**
from the upstream project. The real `nodenetworkstates.nmstate.io` CRD (from
[kubernetes-nmstate](https://github.com/nmstate/kubernetes-nmstate)) spells out the full
NMState network-state schema in `status.currentState` — the same schema documented at
https://nmstate.io/ and in that project's
[reporting guide](https://github.com/nmstate/kubernetes-nmstate/blob/main/docs/content/user-guide/101-reporting.md)
— and is large enough that vendoring it verbatim would mean tracking an upstream schema
this operator does not otherwise depend on (DESIGN.md 4.7: "NodeNetworkState is read as
unstructured data; the operator takes no dependency on the NMState Go module"). Since
`internal/nmstateapi` and `internal/hostreg` only ever read `status.currentState` as
unstructured data and never validate against the real schema, this file instead declares
the bare minimum envtest needs: the right group/version/kind, cluster scope, a status
subresource, and `spec`/`status` as wide-open objects
(`x-kubernetes-preserve-unknown-fields: true`) so any shape of `currentState` can be
written and read back. It is not meant to reject malformed NodeNetworkStates the way the
real CRD would; it exists purely so tests can create one and exercise the status
subresource against a real apiserver.

## Milestone 7 fixtures

Milestone 7 added six more files for the APIs the new DNSPublishPolicy sources, the
IPAllocationPolicy EgressIP target and MetalLB mirroring, and the HostRegistrationPolicy
Metal3 MAC source consume. One is copied from upstream, five are hand-written.

`internal/controller`'s envtest suite installs every file in this directory (next to
`config/crd/bases`), so all of them are present for the controller specs. Each also has a
small envtest of its own in the package that wraps the API, which proves that the file
loads and that the package's `Available` discovery check turns true once it is installed.

Why the hand-written ones can be minimal: the operator reads Gateways, HTTPRoutes,
EgressIPs, IPAddressPools and BareMetalHosts as unstructured data (DESIGN.md 4.7 takes no
Go module dependency on those projects), and only ever touches a handful of fields. A
fixture therefore needs the right group, version, kind and scope, a status subresource
where the real CRD has one, and enough typing for the fields the code reads and writes.
Everything else is left open with `x-kubernetes-preserve-unknown-fields: true`, so a test
can write any shape. The cost is that these CRDs do not reject malformed objects the way
the upstream ones do, and that a schema change upstream cannot show up here. The field
names in each file's header comment were checked against the upstream API docs instead.

### operatoringress.operator.openshift.io_dnsrecords.yaml

**Copied verbatim**, not hand-written. The OpenShift ingress operator's `DNSRecord`
(`dnsrecords.ingress.operator.openshift.io`, namespaced, `v1`), used by
`internal/ingressapi`'s envtest suite (`TestDNSRecordCRDEnvtest`) and by the DNSPublishPolicy
controller specs for the DNSRecord source. The operator reads this one typed, like Routes
(`github.com/openshift/api/operatoringress/v1`), so the real schema is worth having:
it enforces the required `dnsManagementPolicy`, `dnsName`, `recordTTL`, `recordType` and
`targets`, and limits `recordType` to `CNAME` and `A`.

The source is:

```
github.com/openshift/api@v0.0.0-20260914154154-9edb1b2245e2/operatoringress/v1/zz_generated.crd-manifests/0000_50_dns_01_dnsrecords-Default.crd.yaml
```

The first line of the fixture is a comment recording that path; everything after it is
byte-for-byte the upstream file. There is no single merged manifest for this API (the
directory holds one file per feature set: `Default`, `CustomNoUpgrade`,
`DevPreviewNoUpgrade`, `OKD` and `TechPreviewNoUpgrade`), so the `Default` file is the one
used, because it is what a standard cluster ships.

#### Refreshing

1. Bump the pin: `go get github.com/openshift/api@master && go mod tidy`.
2. Find the current file:
   ```
   find "$(go env GOMODCACHE)/github.com/openshift/api@"* -name '*dnsrecords-Default*'
   ```
3. Copy it over `operatoringress.operator.openshift.io_dnsrecords.yaml`, keep the first
   comment line, and update the pseudo-version in it.
4. Re-run the `internal/ingressapi` envtest and the controller suite.

### gateway.networking.k8s.io_gateways.yaml and gateway.networking.k8s.io_httproutes.yaml

**Hand-written**, minimal. Namespaced `Gateway` and `HTTPRoute` in
`gateway.networking.k8s.io/v1`, each with a status subresource and `spec` and `status` left
as free-form objects. Used by `internal/gatewayapi`'s envtest suite and by the DNSPublishPolicy
controller specs for the HTTPRoute source. The upstream Gateway API CRDs are large and ship
in several release channels, none of which matter here. The fields the code reads are:

- HTTPRoute: `spec.hostnames`, `spec.parentRefs`, and `status.parents[]` with `parentRef`,
  `controllerName` and `conditions` (the `Accepted` condition).
- Gateway: `status.addresses[]` with `type` and `value`.

The group ends in `.k8s.io`, so the API server only accepts the CRDs with an `api-approved`
annotation; they carry the "unapproved" form, which is meant for CRDs that are not part of
the upstream API.

### k8s.ovn.org_egressips.yaml

**Hand-written**, minimal. The OVN-Kubernetes `EgressIP` (`egressips.k8s.ovn.org`,
cluster-scoped, `v1`), used by `internal/egressip`'s envtest suite and by the
IPAllocationPolicy controller specs for the EgressIP target. IPAllocationPolicy only touches
metadata and `spec.egressIPs`, so the file types `spec.egressIPs` as a list of strings and,
as upstream does, requires `spec.egressIPs` and `spec.namespaceSelector` (an EgressIP that is
waiting for addresses carries `egressIPs: []`, which is why the operator clears the field to
an empty list rather than removing it). `podSelector` and `status` stay open. It declares no
status subresource; the operator only patches `spec.egressIPs`.

### metallb.io_ipaddresspools.yaml

**Hand-written**, minimal. MetalLB's `IPAddressPool` (`ipaddresspools.metallb.io`,
namespaced, `v1beta1`), used by `internal/metallb`'s envtest suite (which installs this one
file by path) and by the IPAllocationPolicy controller specs for pool mirroring. It requires
`spec.addresses` (a list of strings) and gives `spec.autoAssign` its upstream default of
`true` and `spec.avoidBuggyIPs` its default of `false`, so server-side defaulting behaves as on
a real cluster; the mirroring code sets `autoAssign: false` explicitly and the tests check that
it survives. `spec.serviceAllocation` and `status` are left open because the operator neither
reads nor writes them. The field names follow https://metallb.io/apis/.

### metal3.io_baremetalhosts.yaml

**Hand-written**, minimal. The Metal3 `BareMetalHost` (`baremetalhosts.metal3.io`, namespaced,
`v1alpha1`, short names `bmh` and `bmhost`), used by `internal/metal3api`'s envtest suite and by
the HostRegistrationPolicy controller specs for the Metal3 MAC source. The real CRD describes
the whole provisioning and inspection schema; the operator reads only three things, which the
file types and checks against the upstream API:

- `spec.bootMACAddress`
- `status.hardware.hostname`
- `status.hardware.nics[]` with `name`, `mac` and `ip`

Note that the inspection data is under `status.hardware`, not `status.hardwareDetails`. It has
a status subresource and keeps everything else unvalidated.
