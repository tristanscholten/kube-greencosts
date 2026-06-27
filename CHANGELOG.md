# Changelog

All notable user-facing changes to kube-greencosts are documented here.

This project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and uses SemVer from [`VERSION`](VERSION) for controller image tags.

## [Unreleased]

### Added

- Release checklist covering version bumps, validation, tagging, and post-release
  verification.

## 0.1.0 - 2026-06-26

### Added

- Initial operator release with `EnergyPriceSource`, `EnergyAwareCronJob`,
  `HibernatePolicy`, and `ClusterHibernatePolicy` CRDs.
- Energy price providers for ENTSO-E, enever.nl, and custom JSON APIs.
- Namespace and cluster hibernation for Deployments, StatefulSets, DaemonSets,
  standalone ReplicaSets, and HPA-managed workloads.
- Prometheus metrics, opt-in OpenTelemetry tracing, secure metrics auth, and
  local k3s e2e coverage.

[Unreleased]: https://github.com/tristanscholten/kube-greencosts/compare/main...HEAD
