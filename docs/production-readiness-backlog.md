# Production Readiness Backlog

Last assessed: 2026-06-26

This backlog tracks the gaps between the current repository and a production-grade,
CNCF-worthy Kubernetes operator. It is intentionally prioritized as small,
reviewable work items rather than a broad rewrite.

## Current baseline

Evidence from this assessment:

- CI exists for unit/envtest (`.github/workflows/test.yml`), lint
  (`.github/workflows/lint.yml`), local k3s e2e (`.github/workflows/e2e.yml`),
  container build/scan (`.github/workflows/container.yml`) and Go vulnerability
  scanning (`.github/workflows/security.yml`).
- The k3s e2e suite deploys the operator, checks secure metrics, exercises a
  custom price provider plus `EnergyAwareCronJob`, hibernates/wakes Deployments
  with HPAs, covers StatefulSets, DaemonSets and standalone ReplicaSets, and
  validates `ClusterHibernatePolicy` annotation ownership.
- Runtime hardening is present in `config/manager/manager.yaml` through non-root
  execution, seccomp, probes and resource limits. NetworkPolicy manifests exist
  for metrics/webhook traffic.
- The default metrics endpoint is HTTPS/auth-protected on `:8443`; Prometheus
  manifests include a TLS patch.
- Operational docs exist in `docs/operations.md` for install, upgrade, rollback,
  alerts and common failure modes.
- Release automation builds containers, scans images with Trivy and emits SBOM
  and provenance attestations for pushed images.
- Gaps found during this assessment: no `SECURITY.md`, no `CHANGELOG.md`, no
  dedicated release process document, no image signing workflow, no OpenSSF
  Scorecard workflow, limited provider parser unit-test coverage, and no explicit
  workload/smoke gate that exercises live ENTSO-E or enever tokens.

## Priority backlog

### P0 — Keep merges safe

1. **Document required GitHub branch protection**
   - Goal: make required checks explicit for maintainers.
   - Scope: add docs listing required PR checks: Go Tests, Go Lint, Security,
     Container Image and E2E Tests where path filters apply.
   - Evidence: workflows exist, but repository policy is not documented in-tree.
   - Done when: docs explain which checks block merges and how to handle skipped
     path-filtered checks.

2. **Add `SECURITY.md`**
   - Goal: establish vulnerability reporting and supported-version policy.
   - Scope: responsible disclosure contact/process, supported versions, token
     handling expectations, dependency/update policy.
   - Evidence: security workflow exists, but no security policy file was found.
   - Done when: GitHub surfaces the repository security policy and operators know
     how to report issues without opening public bugs.

### P1 — Release hardening

3. **Add release process and upgrade notes template**
   - Goal: make releases repeatable and safe for operators.
   - Scope: document version bumping, tag rules, image publication, changelog
     update, CRD compatibility review, upgrade/rollback notes and e2e evidence.
   - Evidence: `VERSION`, `hack/bump-version.sh` and container release workflow
     exist, but there is no release runbook or changelog.
   - Done when: a maintainer can cut a release from docs alone.

4. **Add container image signing**
   - Goal: let users verify published images.
   - Scope: add keyless `cosign` signing on pushed images and document
     verification commands.
   - Evidence: container workflow publishes images with SBOM/provenance, but no
     signing step is present.
   - Done when: released images are signed and docs show `cosign verify`.

5. **Add OpenSSF Scorecard workflow**
   - Goal: continuously measure supply-chain posture.
   - Scope: scheduled and branch workflow with minimal permissions; publish
     SARIF or summary.
   - Evidence: Operator SDK scorecard config exists under `config/scorecard`, but
     no OpenSSF Scorecard workflow was found.
   - Done when: maintainers get actionable supply-chain findings in CI.

### P1 — Test depth

6. **Add provider parser unit tests**
   - Goal: keep upstream price parsing stable without live API dependency.
   - Scope: table tests for ENTSO-E XML acknowledgement/interval parsing, enever
     supplier/raw price parsing and custom JSON malformed/error cases.
   - Evidence: provider implementations have important parsing and error paths;
     current e2e covers custom provider happy path but not all parser failures.
   - Done when: `go test ./internal/providers/...` catches malformed upstream
     payloads, missing fields, bad timestamps and non-OK API responses.

7. **Add optional live provider smoke test documentation/script**
   - Goal: validate ENTSO-E and enever integration before release without putting
     secrets in CI logs.
   - Scope: local script or documented command that creates Kubernetes Secrets
     from local token files, applies sample `EnergyPriceSource` resources and
     verifies `.status.conditions[Ready]=True`.
   - Evidence: live tokens are intentionally local-only; e2e currently avoids
     external providers.
   - Done when: release checklist can include a real provider smoke gate.

### P2 — Operations and observability polish

8. **Ship Prometheus alert examples as manifests**
   - Goal: make alerting copy/paste safe.
   - Scope: add optional `config/prometheus/alerts.yaml` or documented
     PrometheusRule overlay matching the runbook alerts.
   - Evidence: alert examples are in prose only in `docs/operations.md`.
   - Done when: users of Prometheus Operator can enable alerts via kustomize.

9. **Document SLOs and failure budgets**
   - Goal: set operator expectations for production clusters.
   - Scope: define controller availability, reconcile error rate, queue backlog
     and price freshness objectives.
   - Evidence: runbook lists SLIs but not concrete SLO targets.
   - Done when: operators can turn metrics into actionable thresholds.

10. **Document disaster-recovery edge cases**
    - Goal: avoid workload surprises during controller outage or bad CR rollout.
    - Scope: explain hibernated annotation recovery, HPA detachment recovery,
      failed webhook/cert-manager recovery and safe CRD rollback limits.
    - Evidence: runbook covers common failures, but not all manual recovery
      procedures for controller-owned workload annotations.
    - Done when: an on-call can recover workloads without reading controller code.

### P2 — CNCF/operator maturity

11. **Run and document Operator SDK scorecard results**
    - Goal: track operator-framework conformance over time.
    - Scope: document command, expected results and any waived findings.
    - Evidence: scorecard config exists, but no result baseline is documented.
    - Done when: a baseline scorecard report is committed or linked from docs.

12. **Add contribution and governance basics**
    - Goal: make external contributions safer.
    - Scope: add/expand `CONTRIBUTING.md`, code of conduct reference, DCO/signoff
      stance and review expectations.
    - Evidence: README has a short contributing section, but no standalone policy.
    - Done when: new contributors can understand how to propose changes.

## Suggested next iteration

Start with item 2 (`SECURITY.md`) or item 6 (provider parser unit tests). Both are
small, high-signal improvements. Prefer item 6 if the next run can spend time on
Go tests; prefer item 2 if the next run should stay docs-only.
