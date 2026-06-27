# Security Policy

## Supported versions

Security fixes are released from `main` until kube-greencosts starts publishing versioned releases.

| Version | Supported |
| ------- | --------- |
| `main`  | Yes       |

## Reporting a vulnerability

Please report suspected vulnerabilities privately by opening a GitHub private vulnerability report for this repository.

Do not open public issues for vulnerabilities, tokens, exploit details, or cluster-specific secrets.

Include:

- affected commit, tag, or image digest;
- the vulnerable CRD, controller, webhook, workflow, or manifest;
- reproduction steps or a minimal manifest when safe to share;
- expected impact and any known mitigations.

## Response target

The maintainer should acknowledge reports within 7 days, then coordinate a fix and disclosure timeline based on severity.

Critical issues that allow secret disclosure, privilege escalation, remote code execution, or unauthorized cluster changes should be prioritized for immediate patching.

## Operator security assumptions

kube-greencosts runs with Kubernetes permissions to read its CRDs, update status, create Jobs for `EnergyAwareCronJob`, and scale supported workloads for hibernation. Treat access to its service account, webhook certificates, metrics token, and provider API tokens as cluster-sensitive.

Before sharing diagnostics, redact:

- ENTSO-E and enever.nl API tokens;
- Kubernetes service account tokens;
- kubeconfigs and client certificates;
- private registry credentials;
- workload names or namespaces that reveal sensitive business context.

## Security checks

Pull requests and protected branches should keep these gates green:

- Go tests and envtest;
- golangci-lint;
- govulncheck;
- container build and image scan;
- local k3s e2e for Kubernetes behavior changes;
- OpenSSF Scorecard for repository security posture.
