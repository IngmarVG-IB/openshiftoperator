# Contributing

Contributions are welcome. This project is alpha, so the API and internals can change.

## Before large changes

Open an issue first to discuss anything beyond a small fix.

## Prerequisites

- Go, at the version in `go.mod`
- `make`
- A Linux or macOS shell (on Windows, use WSL)
- A container runtime, for the image targets (`make docker-build` and friends)

`make` downloads the other tools it needs (controller-gen, kustomize, setup-envtest,
golangci-lint, operator-sdk) into `bin/`. See the Development section of
[README.md](README.md) for the full list of targets.

## Before opening a pull request

Run these and fix what they report:

```sh
make manifests generate   # regenerate CRDs, RBAC and deepcopy code
make lint
make test                 # unit and envtest suites, no real tenant needed
make test-floor           # the same against the Kubernetes 1.29 floor
make bundle               # regenerate the OLM bundle
```

Generated files (CRDs under `config/crd/bases`, `zz_generated.*`, the applyconfiguration
package) and the `bundle/` directory must be committed up to date. CI runs `make bundle` and
fails if the committed bundle differs.

Tests run against `internal/uddi.Fake`, an in-memory fake of the Universal DDI API. The
real-tenant integration suite is described in [docs/testing.md](docs/testing.md); do not run
it against a production tenant.

## What not to commit

No secrets, API keys, or real tenant data (identifiers, hostnames, addresses) in commits,
fixtures, samples or documentation. Use placeholders and `example.com` names.

## Licence

By contributing you agree that your contribution is licensed under the Apache License 2.0,
the same as the project (see [LICENSE](LICENSE)).
