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

## Prerequisites

- Kubernetes ≥ 1.26
- `kubectl` configured for your cluster
- `make` and `docker` (or `podman`) for building
- An [ENTSO-E security token](https://transparency.entsoe.eu/usrm/user/createPublicUser) **or** an [enever.nl API token](https://enever.nl/api)

---

## Quick Start

### 1 — Install CRDs and the operator

```bash
git clone https://github.com/tristanscholten/kube-greencosts.git
cd kube-greencosts

# Build image, install CRDs, and deploy the operator
make all
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

Cluster-scoped policy that scales idle namespaces to zero replicas. Original replica counts are preserved in an annotation and restored when an ignore window begins.

```yaml
apiVersion: greencosts.hstr.nl/v1alpha1
kind: HibernatePolicy
metadata:
  name: dev-hibernation
spec:
  selector:
    namespaceSelector:
      matchLabels:
        environment: dev
  idleDetection:
    cpuBelow: 10m            # idle when total CPU < 10 millicores
    networkBelow: 1Ki        # idle when network throughput < 1 KB/s
    noIngressRequestsFor: 30m
    ignoreDuring:
      - weekdays: [Mon, Tue, Wed, Thu, Fri]
        from:  "08:00"
        until: "18:00"
        timezone: Europe/Amsterdam
  action:
    scaleDeploymentsToZero: true
    snapshotPVCs: false
```

| Field | Required | Description |
|---|---|---|
| `selector.namespaceSelector` | ✅ | Label selector for namespaces to govern |
| `idleDetection.cpuBelow` | | CPU threshold (e.g. `10m`) via metrics-server |
| `idleDetection.networkBelow` | | Network throughput threshold via Prometheus |
| `idleDetection.noIngressRequestsFor` | | Silence window before idle declaration |
| `idleDetection.ignoreDuring` | | Recurring windows to suppress hibernation |
| `action.scaleDeploymentsToZero` | | Scale all Deployments to 0 when idle |
| `action.snapshotPVCs` | | Snapshot PVCs before scaling down |

> **Note:** Network and ingress metrics require a Prometheus instance reachable by the operator. Pass `--prometheus-url=http://prometheus:9090` as a flag to the manager.

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
# Run locally against your current kubeconfig cluster
make run

# Regenerate CRD manifests and deepcopy functions after type changes
make generate && make manifests

# Run unit tests
make test

# Build and deploy to cluster
make all
kubectl rollout restart deployment/kube-greencosts-controller-manager \
  -n kube-greencosts-system
```

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
