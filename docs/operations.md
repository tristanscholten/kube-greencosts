# Operations Runbook

This runbook covers production installation, upgrades, rollback, monitoring and
common failure modes for kube-greencosts.

## Production Checklist

- Install cert-manager before deploying the default overlay; admission webhooks
  depend on cert-manager-managed serving certificates.
- Keep metrics on HTTPS port `8443` and use the `metrics-auth` RBAC resources
  shipped in `config/rbac`.
- Enable the Prometheus `ServiceMonitor` only in clusters with the Prometheus
  Operator installed.
- Prefer the secure ServiceMonitor TLS patch in
  `config/prometheus/monitor_tls_patch.yaml` when scraping from Prometheus.
- Store ENTSO-E and enever tokens in Kubernetes Secrets, never in CR manifests.
- Run local or CI e2e before promotion:

```bash
KUBECONFIG=~/.hermes/local-k3s-root/kubeconfig.yaml \
  E2E_IMAGE_LOADER=k3s-container \
  E2E_K3S_CONTAINER=hermes-k3s \
  go test ./test/e2e -count=1 -timeout=15m
```

## Install

Build or select an immutable controller image tag, then deploy the default
overlay:

```bash
make docker-build IMG=ghcr.io/tristanscholten/kube-greencosts:vX.Y.Z
make deploy IMG=ghcr.io/tristanscholten/kube-greencosts:vX.Y.Z
```

Confirm rollout and webhook readiness:

```bash
kubectl -n kube-greencosts-system rollout status deploy/kube-greencosts-controller-manager
kubectl get validatingwebhookconfiguration | grep kube-greencosts
kubectl get crd | grep greencosts.hstr.nl
```

## Upgrade

Before upgrading, read the release notes for CRD, annotation, provider or
default-behavior changes. Back up existing custom resources:

```bash
kubectl get energypricesources,energyawarecronjobs,hibernatepolicies -A -o yaml > kube-greencosts-resources.yaml
kubectl get clusterhibernatepolicies -o yaml > kube-greencosts-cluster-resources.yaml
```

Apply CRDs first, then roll the controller image:

```bash
make install
make deploy IMG=ghcr.io/tristanscholten/kube-greencosts:vX.Y.Z
kubectl -n kube-greencosts-system rollout status deploy/kube-greencosts-controller-manager
```

After rollout, check controller health and CR status:

```bash
kubectl -n kube-greencosts-system get pods
kubectl get energypricesources,energyawarecronjobs,hibernatepolicies -A
kubectl get clusterhibernatepolicies
```

## Rollback

If only the controller image changed, roll back to the previous image:

```bash
kubectl -n kube-greencosts-system set image \
  deploy/kube-greencosts-controller-manager \
  manager=ghcr.io/tristanscholten/kube-greencosts:vPREVIOUS
kubectl -n kube-greencosts-system rollout status deploy/kube-greencosts-controller-manager
```

If CRDs changed, do not delete CRDs as a rollback step. Deleting CRDs deletes
their custom resources. Instead, stop the controller, inspect compatibility,
then apply the previous CRD manifests if the schema is backward-compatible:

```bash
kubectl -n kube-greencosts-system scale deploy/kube-greencosts-controller-manager --replicas=0
kubectl apply --server-side --force-conflicts -f config/crd/bases
kubectl -n kube-greencosts-system scale deploy/kube-greencosts-controller-manager --replicas=1
```

## Metrics And Alerts

The controller exposes controller-runtime metrics on HTTPS `:8443/metrics`.
Use the `kube-greencosts-metrics-reader` ClusterRole for scraping.

Suggested Prometheus alerts:

```yaml
groups:
- name: kube-greencosts
  rules:
  - alert: KubeGreencostsReconcileErrors
    expr: sum(rate(controller_runtime_reconcile_errors_total{namespace="kube-greencosts-system"}[5m])) > 0
    for: 10m
    labels:
      severity: warning
    annotations:
      summary: kube-greencosts reconcile errors
  - alert: KubeGreencostsControllerDown
    expr: absent(up{namespace="kube-greencosts-system", service="kube-greencosts-controller-manager-metrics-service"} == 1)
    for: 5m
    labels:
      severity: critical
    annotations:
      summary: kube-greencosts metrics endpoint is unavailable
```

Track these service-level indicators:

- Controller availability: metrics target `up == 1`.
- Reconcile health: sustained
  `controller_runtime_reconcile_errors_total` increase should page operators.
- Reconcile latency: watch `controller_runtime_reconcile_time_seconds` for
  price-provider or Kubernetes API slowdowns.
- Queue backlog: sustained `workqueue_depth > 0` means the controller is not
  keeping up.

## Failure Modes

Provider token missing or invalid:

- `EnergyPriceSource` will not become `Ready`.
- Check the referenced Secret name and key in the same namespace.
- Inspect controller logs for `API token is empty`, `security token is empty`
  or upstream HTTP status errors.

Provider API unavailable:

- Existing cached status prices remain until `cacheTTL` expires.
- `EnergyAwareCronJob` resources with fallback scheduling can still use their
  configured fallback.
- Check provider status, network egress policy and DNS from the controller pod.

Metrics unavailable:

- Hibernation decisions that depend on CPU, network or ingress thresholds may
  fail or remain conservative.
- Confirm metrics-server is installed for CPU metrics.
- Confirm Prometheus URL and network access when network or ingress thresholds
  are configured.

Webhook certificate problems:

- Creates or updates may fail admission.
- Confirm cert-manager is running and that the serving certificate Secret was
  created in `kube-greencosts-system`.
- Check the `ValidatingWebhookConfiguration` CA injection annotation.

Unexpected hibernation state:

- Inspect ownership annotations on the workload:
  `greencosts.hstr.nl/hibernated-by-kind`,
  `greencosts.hstr.nl/hibernated-by-name` and
  `greencosts.hstr.nl/hibernated-by-namespace`.
- A namespace `HibernatePolicy` must not wake a workload hibernated by a
  `ClusterHibernatePolicy`.
- To pause hibernation, add an availability window covering the current time or
  scale the controller down while investigating.
