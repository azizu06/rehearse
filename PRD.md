# Rehearse product requirements

**Status:** Approved for implementation  
**Owner:** Abduaziz Umarov  
**Target:** Public v0.1 in four focused weeks; polished v1.0 around weeks seven to eight

## Problem

Backup jobs answer “were bytes copied?” Operators still need to answer “can those bytes recreate a working application?” Recovery tests are often manual, infrequent, undocumented, or limited to storage integrity. Configuration drift, expired credentials, incompatible schemas, incomplete dependencies, and broken boot order remain hidden until an incident.

## Product promise

Given an existing backup and a drill plan, Rehearse creates an isolated environment, restores application state, starts the workload, verifies meaningful behavior, reports evidence, and cleans up automatically.

## Primary users

- Solo developers and small teams running Docker Compose applications
- DevOps/SRE engineers responsible for recovery readiness
- Maintainers using restic or object-storage-based backups who lack application-level restore verification

## v0.1 acceptance outcome

A new user on macOS or Linux can install Rehearse, configure the included queued-orders workload, select a real restic recovery point, run a drill from the UI or CLI, watch each stage, receive a pass/fail report with measured recovery time, and verify that all temporary resources were removed.

## Functional requirements

1. **Plans:** Create, validate, version, enable, disable, and schedule drill plans.
2. **Sources:** Resolve and fetch a selected recovery point through a source-adapter interface.
3. **Targets:** Restore files or databases through a separate target-adapter interface.
4. **Sandbox:** Create uniquely labeled Docker Compose resources with CPU, memory, time, and network policy limits.
5. **Orchestration:** Persist a deterministic state machine supporting cancellation, timeout, retries only where safe, and restart reconciliation.
6. **Probes:** Run required and optional HTTP, TCP, command, SQL, and data assertions.
7. **Evidence:** Store redacted logs, stage durations, selected recovery point, probe results, cleanup result, and final status.
8. **Interfaces:** Offer authenticated localhost web UI plus a CI-friendly CLI returning JSON and JUnit.
9. **Observability:** Export Prometheus metrics and provide an optional Grafana dashboard.
10. **Notifications:** Support at least one webhook-compatible completion/failure notification in v1.

## Adapter matrix

### v0.1

- Source: restic
- Target: PostgreSQL plus generic files/Docker volumes
- Sandbox: local Docker Compose
- Workload: queued-orders API, RabbitMQ worker, PostgreSQL

### v1.0

- Sources: restic, local files/archives, S3 objects, trusted custom command
- Targets: generic files/volumes, PostgreSQL, MySQL/MariaDB, SQLite, trusted custom command

Custom commands require explicit trust, a restricted environment, timeouts, output limits, redaction, and clear warnings.

## Non-functional requirements

- Native signed release artifacts for macOS and Linux
- Embedded React/TypeScript production assets and SQLite database
- Default bind address `127.0.0.1`; authenticated access even locally
- No secret persistence; environment/keychain/file references only
- Crash-safe cleanup and no interference with non-Rehearse Docker resources
- Deterministic reports suitable for CI retention
- Accessible UI covering keyboard navigation, focus, contrast, and screen-reader labels

## Success measures

### Product proof

- Five consecutive clean end-to-end reference drills on macOS and Linux CI/hosts
- Injected failure cases produce the expected stage and typed reason
- Forced termination leaves resources that the janitor removes on restart
- A user unfamiliar with the code completes the quickstart from documentation

### Portfolio proof

- Public tagged v1.0 release and demo recording
- Architecture and failure-mode documentation
- CI, security scanning, Terraform deployment proof, Grafana dashboard, and real service integration tests visible in the repository
- Resume bullets written only from measured shipped behavior

## Explicit non-goals for v1

Electron, Windows, Kubernetes, hosted multi-tenancy, organizations/RBAC, production failover automation, native managed-cloud database APIs, and exhaustive backup-provider coverage.

## Release gates

v1 is complete when the adapter matrix works through the same orchestration contract; cleanup, cancellation, crash recovery, and secret redaction are tested; macOS/Linux packages and checksums are published; CI mode and reports are documented; Terraform provisions the AWS demonstration path; the quickstart is independently reproducible; and the final security/accessibility/project-completion audits have no unresolved release blockers.

