# Security policy

## Supported versions

This project is alpha (`v1alpha1`). Only the latest release and the `main` branch are
supported. No releases have been published yet.

## Reporting a vulnerability

Report vulnerabilities privately, using GitHub's "Report a vulnerability" button under the
repository's Security tab (a private Security Advisory). Do not open a public issue for a
vulnerability.

Never paste API keys, Secrets or tenant identifiers into issues, advisories or pull
requests. If a key has been exposed, revoke it in Universal DDI first.

Responses are best effort. There is no service-level agreement.

## Security model

The operator holds a Universal DDI API key for each `Connection`. It reads the key from a
Secret in the Connection's own namespace.

Anyone who can create or edit a `Connection` in a namespace can make the operator use any
Secret in that namespace as the API key. Grant edit rights on `Connection` objects only to
people who are already trusted with the Secrets in that namespace.

The operator acts on Universal DDI with the permissions of the key. Use a key scoped to
what the operator needs, and use a sandbox tenant while the project is alpha. See
[docs/install.md](docs/install.md#hardening) for the pod hardening, high-availability and
network-policy options.
