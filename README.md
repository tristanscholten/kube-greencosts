<div align="center">

# ⚡ kube-greencosts

**Schedule smarter. Spend less. Go greener.**

*A Kubernetes operator that aligns your workloads with the cheapest — and cleanest — moments on the electricity grid.*

[![Go Version](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Kubernetes](https://img.shields.io/badge/kubernetes-1.33-326CE5?logo=kubernetes&logoColor=white)](https://kubernetes.io)
[![Operator SDK](https://img.shields.io/badge/operator--sdk-v1.42-EE0000?logo=redhat&logoColor=white)](https://sdk.operatorframework.io)
[![License](https://img.shields.io/badge/license-Apache%202.0-green)](LICENSE)
[![CRDs](https://img.shields.io/badge/CRDs-3-blueviolet)](#crds)

</div>

---

## What it does

kube-greencosts watches real-time electricity spot prices from [ENTSO-E](https://transparency.entsoe.eu) or [enever.nl](https://enever.nl) and uses them to make two decisions automatically:

1. **When to run batch jobs** — `EnergyAwareCronJob` acts as a drop-in replacement for a Kubernetes `CronJob`, but instead of firing at exactly the scheduled time it picks the cheapest energy price slot within a configurable window after each cron trigger.
2. **When to hibernate idle namespaces** — `HibernatePolicy` scales idle workloads to zero replicas during off-hours, restoring them before your team's working hours begin.

No code changes needed. Add two YAML files to your cluster and start saving.

---

## Features

| | |
|---|---|
| 🔋 **Energy-aware scheduling** | Runs jobs at the cheapest slot within a configurable window |
| 🌙 **Namespace hibernation** | Scales idle namespaces to zero based on CPU / network / ingress thresholds |
| ⚡ **Live price data** | 48-hour price windows from ENTSO-E (24 EU zones) or enever.nl (NL retail tariffs) |
| 🔌 **Custom providers** | Plug in any JSON price API via `customProvider` |
| 📅 **Fallback scheduling** | Configurable time-of-day fallback when price data is unavailable |
| 💰 **Negative prices** | Optionally bias toward hours when the grid pays *you* to consume |
| 🛡️ **Ignore windows** | Suppress hibernation during business hours on specific weekdays |

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     kube-greencosts operator                │
│                                                             │
│  ┌──────────────────────┐   ┌───────────────────────────┐  │
│  │  EnergyPriceSource   │   │   EnergyAwareCronJob      │  │
│  │  controller          │──▶│   controller              │  │
│  │                      │   │                           │  │
│  │  • Fetches 48h of    │   │  • Reads price intervals  │  │
│  │    price intervals   │   │  • Picks cheapest window  │  │
│  │  • Caches in .status │   │  • Creates a k8s Job      │  │
│  └──────────────────────┘   └───────────────────────────┘  │
│          │                                                  │
│  ┌───────┴────────────────────────────────────┐            │
│  │          Provider registry                 │            │
│  │  entsoe │ enever │ customProvider          │            │
│  └───────┬────────────────────────────────────┘            │
│          │                                                  │
│  ┌───────▼────────────────────────────────────────────┐    │
│  │              HibernatePolicy controller            │    │
│  │  • Queries metrics-server (CPU) + Prometheus (net) │    │
│  │  • Scales idle Deployments to 0 replicas           │    │
│  │  • Restores originals on ignore-window start       │    │
│  └────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘
         │                 │                    │
   ENTSO-E API        enever.nl API       metrics-server
   (XML/REST)         (JSON/REST)         + Prometheus
```

---

## Observability

### Prometheus metrics

The operator exposes standard **controller-runtime** metrics at `:8443/metrics` (requires the `metrics-auth` token by default). The metrics endpoint is guarded by RBAC — see `config/rbac/metrics_auth_role.yaml` for the required `ClusterRole`.

Key metrics:

| Metric | Description |
|--------|-------------|
| `controller_runtime_reconcile_total` | Total reconcile calls per controller, labelled by `controller` and `result` |
| `controller_runtime_reconcile_errors_total` | Error count per controller |
| `controller_runtime_reconcile_time_seconds` | Reconcile latency histogram |

### Distributed tracing

Tracing is **always on**. The operator exports spans via OTLP gRPC to `localhost:4317` by default (the [OTel SDK default](https://opentelemetry.io/docs/specs/otel/protocol/exporter/)). If no collector is reachable the exporter retries silently in the background — the operator keeps running and drops spans without any impact.

Point it at your collector with a single env var:

```yaml
env:
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "http://<collector-service>.<namespace>:4317"
```

All standard [OTel SDK environment variables](https://opentelemetry.io/docs/specs/otel/configuration/sdk-environment-variables/) are respected — no code changes needed.

#### Configuration reference

| Environment variable | Default | Description |
|----------------------|---------|-------------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | OTLP gRPC collector endpoint |
| `OTEL_SERVICE_NAME` | `kube-greencosts` | Service name shown in trace UIs |
| `OTEL_RESOURCE_ATTRIBUTES` | _(unset)_ | Comma-separated extra attributes, e.g. `k8s.cluster.name=prod` |
| `OTEL_EXPORTER_OTLP_HEADERS` | _(unset)_ | Auth headers, e.g. `Authorization=Bearer <token>` |
| `OTEL_TRACES_SAMPLER` | `always_on` | Sampler — see values below |
| `OTEL_TRACES_SAMPLER_ARG` | `1.0` | Ratio for `*_traceidratio` samplers (`0.0`–`1.0`) |
| `OTEL_EXPORTER_OTLP_INSECURE` | `false` | Set `true` for plain-text gRPC (most in-cluster collectors) |

> **Note:** Most in-cluster collectors (Jaeger all-in-one, OTel Collector, Grafana Agent) listen on plain-text gRPC. Set `OTEL_EXPORTER_OTLP_INSECURE=true` **or** use an `http://` prefix in the endpoint URL.

#### Sampler values

| `OTEL_TRACES_SAMPLER` | Behaviour |
|-----------------------|-----------|
| `always_on` _(default)_ | Record every span |
| `always_off` | Disable tracing entirely — zero overhead |
| `traceidratio` | Sample `OTEL_TRACES_SAMPLER_ARG` fraction of traces (e.g. `0.1` = 10 %) |
| `parentbased_always_on` | Always sample; respect parent decision |
| `parentbased_always_off` | Never sample; respect parent decision |
| `parentbased_traceidratio` | Ratio-based; respect parent decision _(recommended for production)_ |

#### Quick-start examples

**Jaeger (all-in-one, in-cluster)**

```yaml
env:
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "http://jaeger.monitoring:4317"
  - name: OTEL_EXPORTER_OTLP_INSECURE
    value: "true"
```

**Grafana Tempo (in-cluster)**

```yaml
env:
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "http://tempo-distributor.monitoring:4317"
  - name: OTEL_EXPORTER_OTLP_INSECURE
    value: "true"
  - name: OTEL_RESOURCE_ATTRIBUTES
    value: "k8s.cluster.name=production"
```

**Grafana Cloud (hosted Tempo)**

```yaml
env:
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "https://otlp-gateway-prod-eu-west-0.grafana.net/otlp"
  - name: OTEL_EXPORTER_OTLP_HEADERS
    value: "Authorization=Bearer <grafana-cloud-token>"
```

**Production — 10 % sampling**

```yaml
env:
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "http://otel-collector.monitoring:4317"
  - name: OTEL_EXPORTER_OTLP_INSECURE
    value: "true"
  - name: OTEL_TRACES_SAMPLER
    value: "parentbased_traceidratio"
  - name: OTEL_TRACES_SAMPLER_ARG
    value: "0.1"
```

**Disable tracing entirely**

```yaml
env:
  - name: OTEL_TRACES_SAMPLER
    value: "always_off"
```

> All env vars can be uncommented directly from `config/manager/manager.yaml`.

### Span inventory

| Span name | Tracer scope | Key attributes |
|-----------|-------------|----------------|
| `EnergyPriceSource.Reconcile` | `greencosts.hstr.nl/controller` | `k8s.resource.name`, `k8s.resource.namespace` |
| `EnergyPriceSource.resolveToken` | `greencosts.hstr.nl/controller` | — |
| `EnergyPriceSource.fetchPrices` | `greencosts.hstr.nl/controller` | `provider` |
| `EnergyPriceSource.patchStatus` | `greencosts.hstr.nl/controller` | — |
| `EnergyAwareCronJob.Reconcile` | `greencosts.hstr.nl/controller` | `k8s.resource.name`, `k8s.resource.namespace` |
| `EnergyAwareCronJob.dispatchJob` | `greencosts.hstr.nl/controller` | `k8s.resource.name`, `k8s.resource.namespace`, `scheduled_time` |
| `HibernatePolicy.Reconcile` | `greencosts.hstr.nl/controller` | `k8s.resource.name`, `k8s.resource.namespace` |
| `ClusterHibernatePolicy.Reconcile` | `greencosts.hstr.nl/controller` | `k8s.resource.name` |
| `ClusterHibernatePolicy.collectWorkloads` | `greencosts.hstr.nl/controller` | `policy.name`, `workload.count` |
| `suspendHPA` | `greencosts.hstr.nl/controller` | `k8s.namespace.name`, `workload.kind`, `workload.name` |
| `restoreHPA` | `greencosts.hstr.nl/controller` | `k8s.namespace.name`, `workload.kind`, `workload.name` |
| `enever.FetchPrices` | `greencosts.hstr.nl/providers` | `provider` |
| `enever.fetchDay` | `greencosts.hstr.nl/providers` | `day` (`vandaag`/`morgen`) |
| `entsoe.FetchPrices` | `greencosts.hstr.nl/providers` | `provider`, `area_code` |
| `custom.FetchPrices` | `greencosts.hstr.nl/providers` | `provider`, `url` |
| HTTP client calls (all providers) | `greencosts.hstr.nl/providers` | Standard `http.*` attributes via `otelhttp` |
| `metrics.QueryNamespaceCPU` | `greencosts.hstr.nl/metrics` | `k8s.namespace.name` |
| `metrics.QueryNamespaceNetwork` | `greencosts.hstr.nl/metrics` | `k8s.namespace.name`, `window` |
| `metrics.QueryNamespaceIngressRPS` | `greencosts.hstr.nl/metrics` | `k8s.namespace.name`, `window` |

---

## Prerequisites

- Kubernetes ≥ 1.26
- `kubectl` configured for your cluster
- `make` and `podman` (or Docker via `CONTAINER_TOOL=docker`) for building
- An [ENTSO-E security token](https://transparency.entsoe.eu/usrm/user/createPublicUser) **or** an [enever.nl API token](https://enever.nl/api)

---

## Quick Start

### 1 — Install CRDs and the operator

```bash
git clone https://github.com/tristanscholten/kube-greencosts.git
cd kube-greencosts

# Build the controller image
make docker-build

# Install CRDs and deploy the operator to your current kubectl context
make deploy
```

### 2 — Create an API token secret

```bash
# enever.nl
kubectl create secret generic enever-token \
  --from-literal=token=<YOUR_TOKEN> \
  -n my-namespace

# ENTSO-E
kubectl create secret generic entsoe-token \
  --from-literal=token=<YOUR_SECURITY_TOKEN> \
  -n my-namespace
```

### 3 — Apply sample resources

```bash
kubectl apply -f config/samples/greencosts_v1alpha1_energypricesource_enever.yaml
kubectl apply -f config/samples/greencosts_v1alpha1_energyawarecronjob.yaml
```

### 4 — Watch it work

```bash
# See fetched price intervals
kubectl get eps -o wide
kubectl get eps energypricesource-enever \
  -o jsonpath='{.status.prices}' | jq length

# See when the next job fires
kubectl get eacj -o wide
```

---

## CRDs

### EnergyPriceSource (`eps`)

Fetches 48 hours of price intervals from an energy provider and stores them in `.status.prices`. The controller re-fetches on a cron schedule and caches results for `cacheTTL`.

```yaml
apiVersion: greencosts.hstr.nl/v1alpha1
kind: EnergyPriceSource
metadata:
  name: my-prices
spec:
  provider: enever           # entsoe | enever | customProvider
  biddingZone: NL            # market zone code
  cacheTTL: 350m             # how long fetched prices stay valid
  # refreshSchedule defaults to every 6 hours — override if needed:
  # refreshSchedule: "0 */6 * * *"
  providers:
    eneverConfig:
      secretRef:
        name: enever-token
        key: token
      supplier: ANWB         # optional: omit for raw EPEX spot price
```

| Field | Required | Description |
|---|---|---|
| `provider` | ✅ | `entsoe`, `enever`, or `customProvider` |
| `biddingZone` | ✅ | Market zone (e.g. `NL`, `DE-LU`, `FR`) |
| `cacheTTL` | ✅ | Duration string — keep below `refreshSchedule` interval |
| `refreshSchedule` | | Cron expression. Default: `0 0,6,12,18 * * *` |
| `providers.eneverConfig` | | Required when `provider: enever` |
| `providers.entsoeConfig` | | Required when `provider: entsoe` |
| `providers.customProviderConfig` | | Required when `provider: customProvider` |

**Status fields** — `EnergyPriceSource`

| Field | Description |
|---|---|
| `.status.lastUpdated` | Timestamp of the last successful fetch |
| `.status.prices[]` | Array of `{start, end, eurPerMWh}` intervals |
| `.status.conditions[]` | Standard Kubernetes condition array (type `Ready`) |

**Status fields** — `EnergyAwareCronJob`

| Field | Description |
|---|---|
| `.status.nextCronWindow` | When the next scheduling window opens (raw cron occurrence). Set immediately on reconcile, before price data is fetched |
| `.status.nextScheduledTime` | Energy-optimised fire time within the current window. Set once price data has been evaluated |
| `.status.lastScheduleTime` | When the last Job was created |
| `.status.lastSuccessfulTime` | When the last Job completed successfully |
| `.status.active[]` | References to currently running Jobs |
| `.status.conditions[]` | Standard Kubernetes condition array |

---

### EnergyAwareCronJob (`eacj`)

Schedules a Kubernetes `Job` at the cheapest energy price slot within a configurable window around each cron fire time. The controller fully implements standard Kubernetes CronJob semantics (`concurrencyPolicy`, `suspend`, `startingDeadlineSeconds`, job history limits, etc.) and adds energy-price optimisation on top.

```yaml
apiVersion: greencosts.hstr.nl/v1alpha1
kind: EnergyAwareCronJob
metadata:
  name: nightly-ml-train
spec:
  energyPriceSource:
    name: energypricesource-enever

  energyStrategy:
    strategy: LowestPrice
    estimatedDuration: 2h     # expected run time of the job
    scheduleWindow: 6h        # how long after the cron trigger the job may run

  cronJob:
    schedule: "0 22 * * *"   # cron expression — window opens at 22:00 each day
    timeZone: Europe/Amsterdam
    concurrencyPolicy: Forbid
    successfulJobsHistoryLimit: 3
    failedJobsHistoryLimit: 1
    jobTemplate:
      spec:
        template:
          spec:
            containers:
              - name: trainer
                image: my-org/ml-trainer:latest
            restartPolicy: OnFailure
```

| Field | Required | Description |
|---|---|---|
| `energyPriceSource.name` | ✅ | Name of the `EnergyPriceSource` in the same namespace |
| `energyStrategy.strategy` | ✅ | How to pick the slot. `LowestPrice` or `HighestPrice` |
| `energyStrategy.estimatedDuration` | ✅ | Expected run time of the job (e.g. `2h`, `30m`) — used to find a slot that fits |
| `energyStrategy.scheduleWindow` | ✅ | How long after the cron trigger the job may run (e.g. `6h`). Must be ≥ `estimatedDuration` |
| `cronJob.schedule` | ✅ | Standard 5-field cron expression (e.g. `"0 22 * * *"`) |
| `cronJob.timeZone` | | IANA timezone for the schedule (e.g. `Europe/Amsterdam`). Default: `UTC` |
| `cronJob.concurrencyPolicy` | | `Allow` / `Forbid` / `Replace`. Default: `Allow` |
| `cronJob.suspend` | | Set to `true` to pause scheduling |
| `cronJob.startingDeadlineSeconds` | | Max seconds past schedule to still start the job |
| `cronJob.successfulJobsHistoryLimit` | | Number of completed jobs to retain. Default: `3` |
| `cronJob.failedJobsHistoryLimit` | | Number of failed jobs to retain. Default: `1` |
| `cronJob.jobTemplate` | ✅ | Standard Kubernetes `JobTemplateSpec` |

---

### HibernatePolicy (`hp`)

Namespace-scoped policy that hibernates workloads in the **same namespace** on a recurring schedule. Hibernated workloads are restored at the start of each availability window.

```yaml
apiVersion: greencosts.hstr.nl/v1alpha1
kind: HibernatePolicy
metadata:
  name: dev-hibernation
  namespace: my-app
spec:
  # workloadTypes lists which Kubernetes workload kinds this policy hibernates.
  workloadTypes:
    - Deployment
    - StatefulSet
    - DaemonSet

  # availabilityWindows defines recurring time slots when hibernation is suppressed.
  # Hibernated workloads are restored at the start of each window.
  availabilityWindows:
    - weekdays: [Mon, Tue, Wed, Thu, Fri]
      from: "08:00"
      until: "18:00"
      timezone: Europe/Amsterdam

  # action.sleepDaemonSet and action.maxReplicas target different workload types
  # and can be freely combined.
  action:
    # sleepDaemonSet hibernates DaemonSets by injecting a non-schedulable
    # nodeSelector. The original nodeSelector is restored on wake.
    # Has NO effect on Deployments, StatefulSets or ReplicaSets.
    sleepDaemonSet: true

    # maxReplicas caps Deployments, StatefulSets and ReplicaSets to N replicas.
    # Set to 0 to scale them completely to zero.
    # Workloads already at or below the cap are left unchanged (no-op).
    # Any HPA targeting an affected workload is suspended to the same cap and
    # restored on wake. Has NO effect on DaemonSets.
    maxReplicas: 0
```

| Field | Required | Description |
|---|---|---|
| `workloadTypes` | ✅ | List of workload kinds to hibernate. Valid values: `Deployment`, `StatefulSet`, `DaemonSet`, `ReplicaSet` |
| `availabilityWindows` | | Recurring windows during which hibernation is suppressed. Workloads are restored at the start of each window |
| `availabilityWindows[].weekdays` | ✅ | Days this window applies to (`Mon`–`Sun`) |
| `availabilityWindows[].from` | ✅ | Wall-clock start time (HH:MM) in the given timezone |
| `availabilityWindows[].until` | ✅ | Wall-clock end time (HH:MM) in the given timezone |
| `availabilityWindows[].timezone` | | IANA timezone name. Default: `UTC` |
| `action.sleepDaemonSet` | | Hibernates **DaemonSets only** via nodeSelector injection. Default: `false`. No effect on other workload types |
| `action.maxReplicas` | | Caps **Deployments, StatefulSets and ReplicaSets** to N replicas. Set to `0` to scale them to zero. HPAs are suspended to the same cap. No effect on DaemonSets |

---

### ClusterHibernatePolicy (`chp`)

Cluster-scoped policy that hibernates workloads across multiple namespaces. Workloads opt in via an annotation on the workload itself or on the namespace (namespace annotation governs all pod-deploying resources in that namespace). If a workload annotation points to a different policy than the namespace annotation, **the workload annotation takes precedence**.

```yaml
apiVersion: greencosts.hstr.nl/v1alpha1
kind: ClusterHibernatePolicy
metadata:
  name: business-hours
spec:
  availabilityWindows:
    - weekdays: [Mon, Tue, Wed, Thu, Fri]
      from: "08:00"
      until: "18:00"
      timezone: Europe/Amsterdam

  # sleepDaemonSet and maxReplicas can be freely combined.
  action:
    sleepDaemonSet: true   # hibernate all DaemonSets governed by this policy
    maxReplicas: 0         # scale all other workloads to zero

  # includedResources restricts the policy to only these workload kinds.
  # Mutually exclusive with excludedResources.
  # includedResources:
  #   - Deployment
  #   - StatefulSet

  # excludedResources prevents the policy from affecting these workload kinds.
  # Mutually exclusive with includedResources.
  # excludedResources:
  #   - DaemonSet
```

**Opt-in annotations**

```bash
# Apply to a single workload
kubectl annotate deployment my-app greencosts.hstr.nl/clusterhibernatepolicy=business-hours

# Apply to all workloads in a namespace
kubectl annotate namespace staging greencosts.hstr.nl/clusterhibernatepolicy=business-hours
```

| Field | Required | Description |
|---|---|---|
| `availabilityWindows` | | Same structure as `HibernatePolicy` |
| `action.sleepDaemonSet` | | Hibernates **DaemonSets only** via nodeSelector injection. Default: `false`. No effect on other workload types |
| `action.maxReplicas` | | Caps **Deployments, StatefulSets and ReplicaSets** to N replicas. Set to `0` to scale to zero. HPAs are suspended. No effect on DaemonSets |
| `includedResources` | | Restrict the policy to only these workload kinds. Mutually exclusive with `excludedResources` |
| `excludedResources` | | Prevent the policy from affecting these workload kinds. Mutually exclusive with `includedResources` |

**Annotation behaviour**

| Annotation | Supported resources | Purpose |
|---|---|---|
| `greencosts.hstr.nl/clusterhibernatepolicy` | `Namespace`, `Deployment`, `StatefulSet`, `DaemonSet`, standalone `ReplicaSet` | Opts resources into a `ClusterHibernatePolicy` by name |
| `greencosts.hstr.nl/hibernated` | Workloads managed by the controller | Marks resources currently hibernated by kube-greencosts |
| `greencosts.hstr.nl/original-replicas` | `Deployment`, `StatefulSet`, standalone `ReplicaSet` | Stores the original replica count before hibernation so wake-up restores it |
| `greencosts.hstr.nl/original-nodeselector` | `DaemonSet` | Stores the original node selector before DaemonSet hibernation |
| `greencosts.hstr.nl/original-hpa-min` / `greencosts.hstr.nl/original-hpa-max` | `HorizontalPodAutoscaler` | Stores HPA replica bounds while an affected workload is hibernated |

Namespace annotations apply to every supported workload in that namespace unless
the workload has its own `greencosts.hstr.nl/clusterhibernatepolicy` annotation.
When a workload annotation is present, it wins over the namespace annotation,
including when it points to another policy.

`EnergyAwareCronJob` also preserves annotations from
`spec.cronJob.jobTemplate.metadata.annotations` on the Jobs it creates, so
runbooks, observability metadata and policy annotations can travel with the
generated Job.

---

## Provider Configuration

### enever.nl

Provides NL retail electricity tariffs from 25+ suppliers plus the raw EPEX spot price. Prices for today are always available; tomorrow's prices are published around 14:00 CET.

```yaml
providers:
  eneverConfig:
    secretRef:
      name: enever-token
      key: token
    supplier: ANWB   # optional — omit for raw EPEX spot price
```

<details>
<summary>Supported supplier codes</summary>

`ANWB` `BE` `CB` `ED` `EE` `EG` `EN` `ES` `EVO` `EZ` `FR` `GSL` `HE` `IN` `MDE` `NE` `PE` `QU` `SS` `TI` `VDB` `VF` `VON` `WE` `ZP`

</details>

---

### ENTSO-E Transparency Platform

Provides day-ahead wholesale prices for 24 European bidding zones.

```yaml
providers:
  entsoeConfig:
    secretRef:
      name: entsoe-token
      key: token
    # areaCode: "10YNL----------L"  # optional; inferred from biddingZone
```

<details>
<summary>Supported bidding zones</summary>

| Code | Zone | Code | Zone |
|------|------|------|------|
| `NL` | Netherlands | `BE` | Belgium |
| `DE` | Germany | `DE-LU` | Germany-Luxembourg |
| `FR` | France | `ES` | Spain |
| `PT` | Portugal | `AT` | Austria |
| `CH` | Switzerland | `FI` | Finland |
| `CZ` | Czech Republic | `PL` | Poland |
| `DK1` | Denmark West | `DK2` | Denmark East |
| `NO1`–`NO5` | Norway zones | `SE1`–`SE4` | Sweden zones |

</details>

---

### Custom Provider

Point at any JSON endpoint that returns an array of price intervals:

```yaml
providers:
  customProviderConfig:
    url: https://my-internal-api.example.com/prices
    secretRef:            # optional — sent as "Authorization: Bearer ..."
      name: my-api-token
      key: token
```

Expected response format:

```json
[
  { "start": "2026-05-16T00:00:00+02:00", "end": "2026-05-16T01:00:00+02:00", "eurPerMWh": 42.10 },
  { "start": "2026-05-16T01:00:00+02:00", "end": "2026-05-16T02:00:00+02:00", "eurPerMWh": 38.50 }
]
```

The controller appends `?biddingZone=<zone>&date=<YYYY-MM-DD>` query parameters to every request.

---

## Metrics

The operator exposes standard [controller-runtime](https://book.kubebuilder.io/reference/metrics-reference) Prometheus metrics on `:8443/metrics` (HTTPS, auth-protected):

| Metric | Description |
|---|---|
| `controller_runtime_reconcile_total` | Total reconcile calls, labelled by controller and result |
| `controller_runtime_reconcile_errors_total` | Failed reconcile calls |
| `controller_runtime_reconcile_time_seconds` | Reconcile duration histogram |
| `workqueue_depth` | Current reconcile queue depth per controller |

Scrape configuration is in [`config/prometheus/`](config/prometheus/).

---

## Development

```bash
# Run the controller locally against your current kubeconfig cluster
go run ./cmd/main.go

# CRD manifests and generated Go files are committed under config/ and api/.
# Run these after type changes when generator tooling is installed.
make generate && make manifests

# Show or bump the SemVer used for image tags
make version
make bump-patch   # or bump-minor / bump-major

# Run unit tests
make setup-envtest
make test

# Run e2e tests against a disposable Kind cluster
make test-e2e

# Build and deploy to your current kubectl context
make docker-build
make deploy
```

Local image builds use [`VERSION`](VERSION) as the SemVer source of truth. By
default `make docker-build` tags the image as
`docker.io/tristanscholten/kube-greencosts-controller:v<version>` and `latest`.
Override `IMAGE_REPOSITORY`, `IMG` or `IMAGE_TAGS` when publishing elsewhere.

The GitHub Actions workflow in
[`.github/workflows/container.yml`](.github/workflows/container.yml) builds every
pull request and pushes Docker Hub images on `main`, `v*.*.*` tags and manual
dispatches. Pull request and `main` builds calculate the image tag with
`gandarez/semver-action`, using `VERSION` as the base version; release tag builds
validate that the Git tag matches `VERSION`. The workflow expects repository
secrets named `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN`.

---

## Contributing

1. Fork the repository
2. Create a feature branch: `git checkout -b feat/my-feature`
3. Make your changes; run `make generate && make manifests && go build ./...`
4. Ensure `make test` passes
5. Open a pull request

Please follow the existing code style (see `.golangci.yml`) and keep commits focused.

---

## License

Distributed under the [Apache 2.0 License](LICENSE).

---

<div align="center">

Made with ⚡ and ☕ — because cheaper energy bills are just better engineering.

</div>
