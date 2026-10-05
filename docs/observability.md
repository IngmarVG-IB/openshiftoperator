# Observability

The operator exports Prometheus metrics, and `config/observability` ships what you need to
act on them on OpenShift: a ServiceMonitor, a PrometheusRule with seven alerts, a Grafana
dashboard and the same dashboard for the OpenShift console. This page covers how to turn it
on, every metric the operator serves, and a runbook for each alert.

Status and events are the first place to look for a single object (see the troubleshooting
section of [usage.md](usage.md)). Metrics answer the questions around them: is anything not
Ready anywhere, is Universal DDI healthy from the operator's side, and is the operator itself
failing.

Events are written through the `events.k8s.io/v1` API. Each carries a reason (unchanged from
earlier releases, so alerts and scripts that match on reasons keep working) and an action
such as `Create`, `Update`, `Delete`, `Publish`, `Allocate` or `Register`. Allocation and
registration events name the Service or Node involved as the related object. A message
longer than 1024 bytes, such as a long Universal DDI error, is cut to fit and ends in `...`,
because the API server drops longer events entirely.

- [Enable monitoring](#enable-monitoring)
- [What Prometheus sees](#what-prometheus-sees)
- [Metric reference](#metric-reference)
- [Alerts](#alerts)
- [Dashboards](#dashboards)
- [Validating the rules](#validating-the-rules)
- [Runbooks](#runbooks)

## Enable monitoring

`config/observability` is not part of `config/default` or the OLM bundle, so installing the
operator never needs the Prometheus Operator CRDs. Apply it afterwards.

Prerequisites on OpenShift:

1. User-workload monitoring is on. If it is not, a cluster administrator enables it:

   ```yaml
   apiVersion: v1
   kind: ConfigMap
   metadata:
     name: cluster-monitoring-config
     namespace: openshift-monitoring
   data:
     config.yaml: |
       enableUserWorkload: true
   ```

2. The operator's namespace does not carry the label `openshift.io/cluster-monitoring: "true"`.
   That label hands a namespace to the platform Prometheus, which ignores ServiceMonitors and
   PrometheusRules from user workloads.
3. The operator is installed, so the `uddi-operator-metrics-reader` ClusterRole exists, and its
   metrics Service `uddi-operator-controller-manager-metrics-service` is up.
4. You are `cluster-admin`: the scrape credentials need a ClusterRoleBinding, and the console
   dashboard lives in `openshift-config-managed`.

Then apply it from the repository root:

```console
$ kubectl apply -k config/observability
```

This creates, in the operator's namespace `uddi-operator-system`:

| Object | Name | Purpose |
| --- | --- | --- |
| ServiceMonitor | `uddi-operator` | Scrapes the metrics Service over HTTPS every 30 seconds. |
| ServiceAccount, Secret, ClusterRoleBinding | `uddi-operator-metrics-scraper`, `uddi-operator-metrics-scraper-token` | A token that may do nothing but `get /metrics`; the ServiceMonitor sends it as a bearer token. |
| PrometheusRule | `uddi-operator` | The seven alerts below. |

and, in `openshift-config-managed`, the ConfigMap `uddi-operator-dashboard` that the console
lists under Observe, Dashboards.

Check that it works:

```console
$ kubectl -n uddi-operator-system get servicemonitor,prometheusrule
$ kubectl -n uddi-operator-system get secret uddi-operator-metrics-scraper-token -o jsonpath='{.data.token}' | wc -c
```

The second command must print a number above zero: Kubernetes fills the token in a few seconds
after the Secret is created. In the console, Observe, Targets (source: User) lists
`uddi-operator-controller-manager-metrics-service` as Up after a minute, and the query
`uddi_operator_objects` returns series. The alerts appear under Observe, Alerting, Alerting
rules; they reach your receivers through Alertmanager like any other alert.

### Another namespace

The directory `config/observability/monitoring` holds everything that belongs in the
operator's namespace and is built with `namespace: uddi-operator-system`. If the operator runs
elsewhere, for example `openshift-operators` after an all-namespaces OLM install, build it from
your own overlay with a different namespace, and apply `config/observability/dashboards`
separately:

```yaml
# kustomization.yaml of your overlay
namespace: openshift-operators
resources:
  - path/to/config/observability/monitoring
```

The ClusterRoleBinding's subject follows the namespace. A ClusterRoleBinding is cluster scoped,
so one cluster has one set of scrape credentials; install the overlay once.

### Without OpenShift

On a cluster with a Prometheus Operator, apply `config/observability/monitoring` alone and
import `config/observability/dashboards/uddi-operator.json` into Grafana (Dashboards, New,
Import). Your Prometheus must select the ServiceMonitor and PrometheusRule through its
`serviceMonitorSelector` and `ruleSelector`; add the labels it expects to the two objects in an
overlay.

### TLS and the scrape certificate

The metrics server serves a self-signed certificate that controller-runtime generates at
start-up, so the ServiceMonitor sets `insecureSkipVerify: true`. The bearer token
authenticates the scraper to the operator, but the scraper does not verify the operator. To
close that gap, serve a certificate from the OpenShift service CA: annotate the metrics Service
with `service.beta.openshift.io/serving-cert-secret-name`, mount the Secret into the manager
Deployment and start it with `--metrics-cert-path`, then give the ServiceMonitor a
`tlsConfig.ca` that reads `service-ca.crt` from the `openshift-service-ca.crt` ConfigMap and a
`serverName` of `<service>.<namespace>.svc`. That touches `config/default`, so it is not done
here.

## What Prometheus sees

Three details of how the series arrive in Prometheus matter when you write your own queries.

- **`exported_namespace` is the namespace of the custom resource.** The operator's object and
  policy metrics have a `namespace` label with the namespace of the Connection, View, policy
  and so on. A scrape adds a `namespace` target label of its own, the operator's namespace, so
  Prometheus keeps the operator's value as `exported_namespace`. OpenShift user-workload
  monitoring does this too, and enforces the operator's namespace as `namespace`. The metric
  reference below lists the label as the operator exports it; the alerts and dashboards use
  `exported_namespace`.
- **Two replicas export every object.** With the `config/ha` overlay both replicas, not only
  the leader, serve `uddi_operator_objects` and the policy metrics, because both keep a cache.
  Collapse the replicas before you add anything up, for example
  `sum by (kind) (max without (instance, pod) (uddi_operator_objects))`. A plain `sum` counts
  every object twice. The API request counters are different: only the leader calls Universal
  DDI, so `rate()` summed over the replicas is right.
- **`job="uddi-operator"`.** The ServiceMonitor's `jobLabel` sets the job name from the
  Service's `app.kubernetes.io/name` label. The reconcile error alert selects on it so it does
  not pick up the controller-runtime metrics of other operators. If you scrape the operator
  some other way, give the series the same job or change the rule.

## Metric reference

### Custom resource state

These four come from a collector in `internal/metrics` that lists the manager's cache each time
Prometheus scrapes. The reconcile loops do not maintain them, and a series disappears as soon
as the objects behind it do. All lists in one scrape share a 5 second deadline and run
concurrently.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `uddi_operator_objects` | gauge | `kind`, `namespace`, `ready` | Number of objects of a kind in a namespace, by the value of their `Ready` condition. |
| `uddi_operator_policy_objects` | gauge | `kind`, `namespace`, `policy`, `state` | A count from a policy's status. |
| `uddi_operator_policy_skipped` | gauge | `kind`, `namespace`, `policy`, `reason` | Number of entries in a policy's `status.skipped` with a given reason. |
| `uddi_operator_collector_errors_total` | counter | `kind` | Scrapes in which listing a kind from the cache failed. |

`uddi_operator_objects`:

- `kind` is one of `Connection`, `View`, `AuthZone`, `Record`, `IPSpace`, `AddressBlock`,
  `Subnet`, `Range`, `Address` or `FixedAddress`.
- `ready` is `true` when the `Ready` condition is `True`, `false` when it is `False`, and
  `unknown` when it is `Unknown` or missing, which is what a newly created object that no
  controller has looked at yet looks like.
- Every namespace that holds at least one object of a kind gets all three `ready` values for
  it, with zero as a value, so a count that falls to zero is a zero rather than a gap. A kind
  with no objects has no series.

`uddi_operator_policy_objects`: `kind` and `state` are paired as follows, and both states are
always exported, at zero while a policy's status is still empty.

| `kind` | `state` | Source |
| --- | --- | --- |
| `DNSPublishPolicy` | `published` | `status.published`, the Records the policy applied. |
| `DNSPublishPolicy` | `ready` | `status.ready`, of those, the ones that are Ready. |
| `IPAllocationPolicy` | `allocated` | `status.allocated`, Services that have an Address object. |
| `IPAllocationPolicy` | `assigned` | `status.assigned`, of those, the ones whose assignment field carries the address. |
| `HostRegistrationPolicy` | `registered` | `status.registered`, Nodes whose registration object is Ready. |
| `HostRegistrationPolicy` | `pending` | `status.pending`, registration objects that exist but are not Ready. |

`uddi_operator_policy_skipped`: `reason` is the machine-readable reason of the skipped entry,
for example `NoMatchingZone`, `PoolNotReady` or `NoMAC`, and `Unknown` for an entry without
one. A policy with nothing skipped has no series. `status.skipped` holds at most 50 entries per
policy, so a count of 50 can be a lower bound.

`uddi_operator_collector_errors_total` starts at zero for all thirteen kinds, so the first
failure is visible to `increase()`. A failed list drops that kind's series for the scrape and
leaves the others alone. The usual cause is a scrape that arrives before the informer caches
have synced right after start-up; anything that keeps growing is worth a look at the operator
log with `--zap-log-level=debug`, where each failure is logged.

### Universal DDI API client

Defined in `internal/uddi/metrics.go`. They count and time what the operator sends to
Universal DDI. Every label comes from a small fixed set, never from an object id, name,
address or tag, and never from the API key.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `uddi_operator_api_requests_total` | counter | `connection`, `domain`, `method`, `code` | Every HTTP attempt, retries included. |
| `uddi_operator_api_request_duration_seconds` | histogram | `connection`, `domain`, `method` | Duration of one HTTP round trip, without the wait for the client-side limiter or the backoff between retries. Default Prometheus buckets, 5 ms to 10 s. |
| `uddi_operator_api_retries_total` | counter | `connection`, `domain`, `method`, `code` | Retries performed, labeled with the status code of the response that caused the retry. |
| `uddi_operator_api_ratelimit_wait_seconds` | histogram | `connection` | Time a request waited for a token from the client-side limiter, zero waits included. Buckets from 1 ms to 30 s. |

- `connection` identifies the Connection the client belongs to, as `<namespace>/<name>`.
- `domain` is `dns`, `ipam` or `dhcp`, taken from the request path, and `other` for anything
  else.
- `method` is the HTTP verb and the API resource, for example `POST record` or
  `PATCH subnet`, and `<verb> unknown` for a path the operator does not recognise.
- `code` is the HTTP status, or `error` when no response arrived (a refused connection, a
  timeout, a cancelled request).

The client retries `429`, `502`, `503` and `504` for `GET`, `PATCH` and `DELETE`, up to three
times within 30 seconds, honouring `Retry-After`. `POST` is never retried, because the API
has no idempotency key. Each attempt is its own sample of `uddi_operator_api_requests_total`,
so a request that was throttled twice and then succeeded adds two `429` and one `200`.

### controller-runtime

The manager also serves the standard controller-runtime metrics. The alerts and dashboards use
these:

| Metric | Labels | Meaning |
| --- | --- | --- |
| `controller_runtime_reconcile_total` | `controller`, `result` | Reconciles per controller. |
| `controller_runtime_reconcile_errors_total` | `controller` | Reconciles that returned an error. |
| `controller_runtime_reconcile_time_seconds` | `controller` | Reconcile duration, a histogram. |

The `controller` label is the lowercase kind: `connection`, `view`, `authzone`, `record`,
`ipspace`, `addressblock`, `subnet`, `range`, `address`, `fixedaddress`, `dnspublishpolicy`,
`ipallocationpolicy` and `hostregistrationpolicy`. The workqueue, client-go and Go runtime
metrics come along as well.

## Alerts

All alerts are in the PrometheusRule `uddi-operator`. Each has `severity`, `summary`,
`description` and a `runbook_url` that points at the section of this page with the same name.

| Alert | Severity | For | Fires when |
| --- | --- | --- | --- |
| [UDDIConnectionNotReady](#uddiconnectionnotready) | critical | 10m | A Connection has a `Ready` condition that is not `True`. |
| [UDDIObjectsNotReady](#uddiobjectsnotready) | warning | 15m | An object of any of the nine object-layer kinds is not Ready. |
| [UDDIAPIServerErrors](#uddiapiservererrors) | warning | 10m | Universal DDI answers with 5xx at more than 0.1 per second. |
| [UDDIAPIThrottled](#uddiapithrottled) | warning | 10m | Universal DDI answers with 429 at more than 0.05 per second. |
| [UDDIAddressesPending](#uddiaddressespending) | warning | 15m | An IPAllocationPolicy has fewer addresses assigned than allocated. |
| [UDDIRegistrationsPending](#uddiregistrationspending) | warning | 15m | A HostRegistrationPolicy has registrations that are not Ready. |
| [UDDIReconcileErrors](#uddireconcileerrors) | warning | 15m | A controller keeps returning reconcile errors. |

The expressions, with the replica collapse described above:

```promql
# UDDIConnectionNotReady
sum by (exported_namespace) (
  max without (instance, pod) (uddi_operator_objects{kind="Connection", ready!="true"})
) > 0

# UDDIObjectsNotReady
sum by (exported_namespace, kind) (
  max without (instance, pod) (uddi_operator_objects{kind!="Connection", ready!="true"})
) > 0

# UDDIAPIServerErrors
sum by (connection, domain) (rate(uddi_operator_api_requests_total{code=~"5.."}[5m])) > 0.1

# UDDIAPIThrottled
sum by (connection, domain) (rate(uddi_operator_api_requests_total{code="429"}[5m])) > 0.05

# UDDIAddressesPending
(
  max without (instance, pod) (uddi_operator_policy_objects{kind="IPAllocationPolicy", state="allocated"})
  - ignoring (state)
  max without (instance, pod) (uddi_operator_policy_objects{kind="IPAllocationPolicy", state="assigned"})
) > 0

# UDDIRegistrationsPending
max without (instance, pod, state) (
  uddi_operator_policy_objects{kind="HostRegistrationPolicy", state="pending"}
) > 0

# UDDIReconcileErrors
sum by (controller) (rate(controller_runtime_reconcile_errors_total{job="uddi-operator"}[5m])) > 0
```

An alert about objects in `unknown` also fires when the operator is not reconciling at all, for
example because the manager is crash-looping or no replica holds the leader lease: objects
nobody has looked at have no `Ready` condition. Check the pods first when many kinds alert at
once.

## Dashboards

`config/observability/dashboards/uddi-operator.json` is one dashboard, "Universal DDI
Operator", in the classic Grafana format that both Grafana and the OpenShift console render:

- **Readiness**: Connections not Ready, objects not Ready, objects Ready and pending host
  registrations, then objects not Ready by kind and a table of every kind by `ready` value.
- **Universal DDI API**: requests per second by domain and by status code, and request
  duration p50, p95 and p99 by domain.
- **Retries and throttling**: retries per second by status code, 429 responses by domain, and
  the wait for the client-side rate limiter.
- **Policies**: the state of every policy, and its skipped entries by reason.
- **Controllers**: reconcile errors and reconciles per second by controller, and failed
  collector lists.

On OpenShift, `kubectl apply -k config/observability` creates the ConfigMap
`uddi-operator-dashboard` in `openshift-config-managed` with the label
`console.openshift.io/dashboard: "true"`, generated from that same JSON file. It shows up under
Observe, Dashboards, as "Universal DDI Operator". The administrator perspective queries the
cluster's Thanos Querier, which includes user-workload metrics.

For Grafana, import the JSON file and pick your Prometheus data source in the "Data source"
variable.

## Validating the rules

`hack/check-rules.sh` runs in CI and locally. It extracts the PrometheusRule's `spec` to a plain
rules file with `yq` (mikefarah, v4) or, if there is none, `python3` with PyYAML, then:

1. checks that every alert has a severity, a summary, a description and a `runbook_url` whose
   anchor is a heading in this page and that every alert is named in a unit test;
2. runs `promtool check rules`;
3. runs `promtool test rules` on `config/observability/tests/*_test.yaml`, which fire and
   silence every alert from synthetic series, including two replicas exporting the same
   objects.

Install promtool from the `prometheus-<version>.linux-amd64.tar.gz` release tarball (it is in
the archive next to `prometheus`), verify it against the release's `sha256sums.txt` as the
`rules` job in `.github/workflows/ci.yml` does, and either put it on the `PATH`, copy it to
`bin/promtool`, or point `PROMTOOL` at it:

```console
$ PROMTOOL=/tmp/promtool hack/check-rules.sh
```

## Runbooks

Each section follows the same order: what the alert means, the likely causes, and what to
check. The commands use the default namespace `uddi-operator-system`; the operator's own log
is `kubectl -n uddi-operator-system logs deployment/uddi-operator-controller-manager`.

### UDDIConnectionNotReady

**What it means.** For at least 10 minutes, a Connection in the namespace named in the alert
has had a `Ready` condition that is `False` or `Unknown`. Every object that uses the Connection
reports `ConnectionNotReady` and stops syncing with Universal DDI, so
[UDDIObjectsNotReady](#uddiobjectsnotready) usually follows.

**Likely causes.**

- The Secret named in `spec.secretRef` is missing (`SecretNotFound`) or has no usable value
  under the configured key (`SecretKeyMissing`).
- The API key was revoked, expired or lost the role it needs. The verification call, which
  lists one DNS view, is rejected and the condition reads `APIError` with the status in the
  message.
- Universal DDI is unreachable from the cluster: a wrong `spec.cspURL`, a proxy, or egress
  that is blocked by a NetworkPolicy or a firewall. `UDDIAPIServerErrors` or `UDDIAPIThrottled`
  may be firing too if it is the service that is failing.
- The condition is `Unknown`: the Connection has never been reconciled, which means the operator
  is not running or has no leader.

**What to check.**

```console
$ kubectl get connection -A
$ kubectl -n <namespace> describe connection <name>
$ kubectl -n <namespace> get connection <name> -o jsonpath='{.status.conditions[?(@.type=="Ready")]}{"\n"}'
$ kubectl -n <namespace> describe secret <secretRef.name>
```

The condition reason and message say which case applies. `describe connection` also lists the
Warning event `VerificationFailed` with the API error. `describe secret` shows the data keys
and their sizes without the values: confirm that the Secret is in the same namespace as the
Connection and holds the key from `spec.secretRef.key` (default `apiKey`), and avoid printing
the key itself into a shared terminal. If the key is the suspect, create a fresh one in
Universal DDI and update the Secret: the Connection controller watches Secrets and verifies
the Connection again right away. Finally read the operator log for the Connection and check
that the manager pod is running and holds the lease.

### UDDIObjectsNotReady

**What it means.** For at least 15 minutes, one or more objects of the `kind` in the alert, in
the namespace in the alert, have a `Ready` condition that is not `True`. The kinds are `View`,
`AuthZone`, `Record`, `IPSpace`, `AddressBlock`, `Subnet`, `Range`, `Address` and
`FixedAddress`; Connections have their own alert. An object that is stuck deleting counts too.
The generated objects of the policies are in these kinds, so a failing Record of a
DNSPublishPolicy raises this alert as well as [UDDIRegistrationsPending](#uddiregistrationspending)
or [UDDIAddressesPending](#uddiaddressespending) for the policy that made it.

**Likely causes.** The reason on the `Ready` condition tells them apart:

| Reason | Meaning |
| --- | --- |
| `ConnectionNotReady` | The Connection is missing or not Ready. Fix it first, see [UDDIConnectionNotReady](#uddiconnectionnotready). |
| `DependencyNotReady` | A referenced object, such as a view or space, has no `status.id` yet. Look at the referenced object. |
| `AlreadyExists` | An object that this operator does not own already has the same natural key and `lifecycle.adoptExisting` is false. |
| `APIError` | A call to Universal DDI failed. The condition message has the status and the API's explanation. |
| `Creating` | The object is being created; it should not stay in this state for long. |
| `Deleting` | The deletion is blocked, for example by objects that still depend on it in Universal DDI. |
| no condition | Nothing has reconciled the object: the operator is down, or no replica is the leader. |

**What to check.**

```console
$ kubectl -n <namespace> get <kind> -o wide
$ kubectl -n <namespace> get <kind> <name> -o jsonpath='{.status.conditions}{"\n"}'
$ kubectl -n <namespace> describe <kind> <name>
```

The Events at the end of `describe` carry the operator's view: `APIError`, `Conflict`,
`Recreated`, `DeleteBlocked` and `ConnectionMissing`, all as warnings. Then read the operator
log for the object. The troubleshooting table in [usage.md](usage.md#object-not-ready) lists
what to do for each reason. If many kinds alert at once, check the operator pods and the
Connection before anything else.

### UDDIAPIServerErrors

**What it means.** Universal DDI has answered the operator's requests with HTTP 5xx at more
than 0.1 per second for 10 minutes, for the Connection and API domain (`dns`, `ipam` or `dhcp`)
in the alert. Each attempt counts, so one request that is retried three times and keeps
failing adds four.

**Likely causes.**

- An incident or maintenance in the Universal DDI service, or in one of its components. If
  only one `domain` alerts, the other components are healthy.
- A proxy or gateway between the cluster and Universal DDI answering 502 or 504.
- A request that the API cannot handle and answers with 500 every time. Look at `method` in
  the metric: if one call dominates, one object probably sends it again on every reconcile.

**What to check.**

```promql
sum by (domain, method, code) (rate(uddi_operator_api_requests_total{code=~"5.."}[5m]))
sum by (domain, code) (rate(uddi_operator_api_retries_total[5m]))
```

The first shows which call fails and with which status, the second whether retries are
absorbing it. Check whether Infoblox reports an incident for Universal DDI. Read the operator
log and the `APIError` events on the objects of that domain: the message carries the status
and the body of the response, never the API key. Objects whose calls fail show `APIError` and
are retried by the controllers with backoff, so there is nothing to do on the cluster while
the service recovers, and the alert resolves by itself. If it does not, and one `method`
dominates, find the object behind it with the operator log and fix or remove it.

This alert counts responses. Requests that get no response at all, such as a refused
connection or a timeout, have `code="error"` and are not in it; watch
`rate(uddi_operator_api_requests_total{code="error"}[5m])` as well, and
[UDDIConnectionNotReady](#uddiconnectionnotready) will fire when the Connection check fails.

### UDDIAPIThrottled

**What it means.** Universal DDI has answered with HTTP 429 at more than 0.05 per second for
10 minutes, for the Connection and domain in the alert. The operator retries a throttled
`GET`, `PATCH` or `DELETE` after the `Retry-After` it is given, so syncs are slow rather than
lost, but a `POST` is not retried and fails until the next reconcile.

**Likely causes.**

- Many objects syncing at once, for example the first sync of a large import or after a
  Connection's key was rotated.
- A short `spec.resyncInterval` on a Connection with many objects: every object is read back
  from Universal DDI on each interval.
- Other clients using the same tenant or API key, such as Terraform runs, scripts or other
  clusters, consuming the same quota.
- A `spec.rateLimit` that is set too high for what the tenant allows. The default is 10
  requests per second with a burst of 20.

**What to check.**

```promql
sum by (method) (rate(uddi_operator_api_requests_total{code="429"}[5m]))
sum by (connection) (rate(uddi_operator_api_requests_total[5m]))
histogram_quantile(0.95, sum by (le, connection) (rate(uddi_operator_api_ratelimit_wait_seconds_bucket[5m])))
```

The first shows which calls are throttled, the second how much the operator sends in total,
and the third how long requests queue in the client-side limiter. Then, in the Connection:
lower `spec.rateLimit.requestsPerSecond` so the operator stays below the tenant's limit,
raise `spec.resyncInterval` if the objects rarely change out of band, and find out who else
calls the tenant with the same key. The alert resolves once the 429 rate stays under 0.05 per
second.

### UDDIAddressesPending

**What it means.** An IPAllocationPolicy, named in the alert with its namespace, has had fewer
Services with an assigned address (`status.assigned`) than Services with an allocated Address
object (`status.allocated`) for 15 minutes. The value of the alert is the difference. The
Address objects exist but the address has not reached the Service, either because an Address
is not Ready yet or because the write to the Service failed.

**Likely causes.**

- An Address is not Ready: Universal DDI rejected the allocation (the pool is exhausted, the
  address is taken or out of range) or the call keeps failing. Its `Ready` message has the
  reason.
- The pool, Connection or space behind the policy is not Ready (`PoolNotReady`).
- The write to the Service was rejected, for instance by an admission policy, or the Service's
  assignment field was changed by someone else.

**What to check.**

```console
$ kubectl -n <namespace> get ipallocationpolicy <policy> -o jsonpath='{.status}{"\n"}'
$ kubectl -n <namespace> describe ipallocationpolicy <policy>
$ kubectl -n <namespace> get address -o wide
$ kubectl -n <namespace> describe address <name>
```

`status.skipped` names the Services and the reason: `AddressPending` for an Address that is
not Ready, `PoolNotReady` for a missing or unready pool. The condition `Ready` reads
`AddressesPending` or `PoolNotReady`. Warning events with the reason `Skipped` repeat the
same. For an `AddressPending` Service, describe its Address and fix what its `Ready` message
says; see [ip-allocation.md](ip-allocation.md) for the skip reasons and what each one needs.
Services skipped with `ManuallyAssigned` are not allocated and do not raise this alert.

### UDDIRegistrationsPending

**What it means.** A HostRegistrationPolicy, named in the alert with its namespace, has had
registration objects that exist but are not Ready (`status.pending`) for 15 minutes. The value
of the alert is how many.

**Likely causes.**

- Universal DDI rejected the registration: the Node's address is outside the IPSpace's
  subnets, the address or MAC is already registered, or the call keeps failing. The failing
  object's `Ready` message says which.
- The IPSpace or Connection behind the policy is not Ready (`SpaceNotReady`).
- A FixedAddress needs a MAC that is wrong or conflicts with another reservation.

**What to check.**

```console
$ kubectl -n <namespace> get hostregistrationpolicy <policy> -o jsonpath='{.status}{"\n"}'
$ kubectl -n <namespace> describe hostregistrationpolicy <policy>
$ kubectl -n <namespace> get address,fixedaddress
$ kubectl -n <namespace> describe fixedaddress <name>
```

`status.skipped` lists the Nodes with `RegistrationPending` and the generated object's name;
describe that object and fix what its `Ready` condition says, which is also what raises
[UDDIObjectsNotReady](#uddiobjectsnotready) for the `Address` or `FixedAddress` kind. Nodes
skipped with `NoMAC`, `NoAddress` or `InvalidAnnotation` are not pending and do not raise this
alert. [host-registration.md](host-registration.md) explains each reason.

### UDDIReconcileErrors

**What it means.** The controller named in the alert has returned reconcile errors, at a
non-zero rate, for 15 minutes. `controller` is the lowercase kind it reconciles. A reconcile
that returns an error is retried with exponential backoff. States that are expected, such as
an object waiting for a dependency that has no id yet, are reported as conditions and do not
count. A failed Universal DDI call does: the controller records `APIError` on the object, and a
deletion that Universal DDI refuses records `DeleteBlocked`, and both return the error so the
reconcile is retried. So this alert is often the controller's view of a problem that
[UDDIAPIServerErrors](#uddiapiservererrors), [UDDIAPIThrottled](#uddiapithrottled) or
[UDDIConnectionNotReady](#uddiconnectionnotready) reports from the API side, and when none of
them fires it points at something else.

**Likely causes.**

- Failing Universal DDI calls on the objects of that kind: a rejected request (a value the API
  does not accept), an address or name that is taken, or a deletion blocked by dependent
  objects. The `APIError` and `DeleteBlocked` events say which.
- The operator cannot read or write a resource in the cluster: missing RBAC, an admission
  policy that rejects its updates, or an API server that is overloaded.
- A CRD that is missing or out of date after an upgrade.
- A defect in the operator, most likely when only one controller is affected and the errors
  start right after an upgrade.

**What to check.**

```promql
sum by (controller) (rate(controller_runtime_reconcile_errors_total{job="uddi-operator"}[5m]))
```

```console
$ kubectl -n uddi-operator-system logs deployment/uddi-operator-controller-manager | grep -i '"error"'
$ kubectl -n uddi-operator-system get pods
$ kubectl get events -A --field-selector type=Warning,reason=APIError
$ kubectl get crd | grep uddi.infoblox.com
```

The log lines carry the controller, the object and the error. The `APIError` events list the
objects behind an error that comes from Universal DDI. Look for `forbidden` (RBAC), for HTTP
statuses from Universal DDI, and for `conflict` errors that repeat. Compare the operator
version with the CRDs that are installed. If the errors are in the operator itself, the
version and the log lines are what to put in an issue.
