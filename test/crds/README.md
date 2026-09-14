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
