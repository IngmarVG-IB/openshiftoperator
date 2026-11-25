# Installing uddi-operator

## Prerequisites

- OpenShift 4.16 or newer (Kubernetes 1.29 floor; the operator's CRDs and CSV declare
  `minKubeVersion: 1.29.0`). This is the design target and is so far untested on a real
  cluster.
- `cluster-admin` (or equivalent) access, to install a CatalogSource, an OperatorGroup, a
  Subscription and the operator's ClusterRoles/RoleBindings.
- The `oc` or `kubectl` CLI logged in to the target cluster.
- A Universal DDI tenant and API key, to create the `Connection` custom resource and its
  Secret once the operator is running (see [../README.md](../README.md) and
  [uddi-api-notes.md](uddi-api-notes.md)).

## Option A: install from the file-based catalog (recommended)

> **Note:** no images or catalog have been published yet, and the release workflow has
> never run. Until the first release, the image references below do not resolve. Build and
> push your own images first (see [Option B](#option-b-developer-install-straight-from-source-no-olm)
> for building an operator image from source), and substitute your own registry for
> `quay.io/ingmarvg-ib`.

This installs the operator through OLM, from the file-based catalog image published by
[`release.yml`](../.github/workflows/release.yml) to
`quay.io/ingmarvg-ib/uddi-operator-catalog`.

### 1. Add the CatalogSource

CatalogSources are cluster-scoped but conventionally created in `openshift-marketplace`,
where OpenShift's own default catalogs live:

```yaml
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: uddi-operator-catalog
  namespace: openshift-marketplace
spec:
  sourceType: grpc
  image: quay.io/ingmarvg-ib/uddi-operator-catalog:v0.1.0   # pin to the release you want
  displayName: Universal DDI Operator Catalog
  publisher: uddi-operator authors
  updateStrategy:
    registryPoll:
      interval: 10m
```

Wait for it to report `READY`:

```console
$ oc get catalogsource uddi-operator-catalog -n openshift-marketplace -w
```

### 2. Pick a target namespace and (if needed) an OperatorGroup

The bundle's CSV supports all four install modes (`OwnNamespace`, `SingleNamespace`,
`MultiNamespace`, `AllNamespaces`), so pick whichever matches how many namespaces will hold
`Connection` and object-layer custom resources.

- **All namespaces** (simplest; watches every namespace): install into the existing
  `openshift-operators` namespace, which already has a global OperatorGroup, so step 2 can
  be skipped entirely.
- **One namespace, or a fixed list of namespaces**: create a namespace and an
  `OperatorGroup` scoped to it/them, for example:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: uddi-operator-system
---
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: uddi-operator-group
  namespace: uddi-operator-system
spec:
  targetNamespaces:
    - uddi-operator-system   # add more namespaces here for MultiNamespace
```

### 3. Create the Subscription

```yaml
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: uddi-operator
  namespace: uddi-operator-system   # or openshift-operators for the AllNamespaces route
spec:
  channel: alpha
  name: uddi-operator
  source: uddi-operator-catalog
  sourceNamespace: openshift-marketplace
  installPlanApproval: Automatic
  startingCSV: uddi-operator.v0.1.0
```

OLM creates the InstallPlan, deploys the `uddi-operator-controller-manager` Deployment and
installs the thirteen `uddi.infoblox.com` CRDs. Confirm with:

```console
$ oc get csv -n uddi-operator-system
$ oc get pods -n uddi-operator-system
$ oc get crds | grep uddi.infoblox.com
```

Then create a `Connection` custom resource and its Secret in each namespace that will own
object-layer resources (see the samples under [`../config/samples`](../config/samples)).

## Option B: developer install straight from source (no OLM)

Useful for iterating on the operator itself, or on a cluster without OLM/OperatorHub. Run
from the repository root, in a Linux or macOS shell (on Windows, use WSL; the Makefile
assumes a Unix toolchain):

```console
$ export KUBECONFIG=/path/to/your/kubeconfig
$ make install                                     # applies the CRDs only
$ make deploy IMG=quay.io/ingmarvg-ib/uddi-operator:v0.1.0
```

`make deploy` renders `config/default` with `kustomize`, sets the manager image to `IMG`,
and applies it, creating the `uddi-operator-system` namespace, RBAC, and the
`controller-manager` Deployment. To build and push your own image first:

```console
$ make docker-build docker-push IMG=quay.io/<your-namespace>/uddi-operator:dev
$ make deploy IMG=quay.io/<your-namespace>/uddi-operator:dev
```

(Image builds need a container runtime.)

## Uninstall

**OLM install:**

```console
$ oc delete subscription uddi-operator -n uddi-operator-system
$ oc delete csv uddi-operator.v0.1.0 -n uddi-operator-system
$ oc delete catalogsource uddi-operator-catalog -n openshift-marketplace
```

CRDs are not removed automatically by OLM (to avoid deleting custom resources by
accident). Remove them explicitly once you have deleted or backed up any `Connection` and
object-layer custom resources that depend on them:

```console
$ oc get crds -o name | grep uddi.infoblox.com | xargs oc delete
```

**Developer install:**

```console
$ make undeploy
$ make uninstall
```

## Hardening

The default `config/default` deployment applies the following pod security settings:

**Pod and container hardening:**
- Pod-level: `runAsNonRoot: true`, seccomp profile set to `RuntimeDefault`
- Container-level: no privilege escalation, read-only root filesystem, all Linux
  capabilities dropped
- Explicit resource requests (CPU 10m, memory 64Mi) and limits (CPU 500m, memory 256Mi)
- A temporary `/tmp` volume (emptyDir) for the metrics server's self-signed certificate generation

These settings are designed to be compatible with the OpenShift `restricted-v2` security
context constraint. This has not yet been verified on a cluster.

### High-availability overlay

For production deployments, use the `config/ha` overlay to run two replicas with a
PodDisruptionBudget and soft pod anti-affinity (the default stays one replica without a
PDB, since a PDB on a single leader-elected replica only blocks node drains):

```console
$ make kustomize                                   # installs bin/kustomize if missing
$ bin/kustomize build config/ha | oc apply -f -
```

This overlay sets:
- Two replicas of the manager Deployment
- A PodDisruptionBudget with `maxUnavailable: 1` (one replica can be disrupted at a time)
- Soft pod anti-affinity preferring to spread replicas across different nodes
  (`kubernetes.io/hostname`)

Leader election is already enabled, so only one replica reconciles at a time; the second
provides redundancy.

### Network policies

To restrict ingress and egress traffic to and from the manager pods, apply the
`config/network-policy` overlay. It is opt-in and not included in the default:

```console
$ make kustomize                                   # installs bin/kustomize if missing
$ bin/kustomize build config/network-policy | oc apply -f -
```

**Ingress:** Allows metrics scraping (TCP 8443) from OpenShift's monitoring namespaces:
- `openshift-monitoring` (system monitoring)
- `openshift-user-workload-monitoring` (user-workload monitoring for ServiceMonitors)

**Egress:** Allows the following traffic from the manager pods:
- DNS: UDP and TCP ports 53 and 5353 to the `openshift-dns` namespace
- Kubernetes API: TCP ports 443 and 6443 to any address (cluster API server and admission webhooks)
- HTTPS: TCP 443 to any address for `csp.infoblox.com` (Universal DDI API)

Note: NetworkPolicy cannot match host names; the HTTPS rule allows all TCP 443 egress.
If your cluster needs stricter egress controls, you may need to layer an Egress policy or
egress firewalls on top.

### Bundle and OLM

The pod security settings in `config/manager/manager.yaml` are picked up by `make bundle`
when the operator's ClusterServiceVersion (CSV) is regenerated. If you enable the HA
overlay or network policies in production, the CSV's install instructions should document
that the security settings are already applied and refer to this section.

## Configuring the release workflow's quay.io credentials

[`release.yml`](../.github/workflows/release.yml) pushes the manager image, the OLM bundle
image and the file-based catalog image to `quay.io/ingmarvg-ib` on every `v*` tag. It
authenticates with `docker/login-action` using two repository secrets:

1. In quay.io, create (or reuse) a robot account under the `ingmarvg-ib` organization with
   write access to the `uddi-operator`, `uddi-operator-bundle` and `uddi-operator-catalog`
   repositories (quay.io lets a single robot account be granted write access to multiple
   repositories, or to the whole organization).
2. In the GitHub repository, go to **Settings > Secrets and variables > Actions** and add:
   - `QUAY_USERNAME` - the robot account name, e.g. `ingmarvg-ib+uddi_operator_ci`.
   - `QUAY_PASSWORD` - the robot account's token (from quay.io's robot account "Credentials"
     tab; treat it like a password, not a personal login password).
3. Push a tag matching `v*` (e.g. `git tag v0.1.0 && git push origin v0.1.0`) to trigger the
   workflow.

No other secrets are required: `ci.yml`'s `bundle` and `image` jobs never push, so they run
without registry credentials.
