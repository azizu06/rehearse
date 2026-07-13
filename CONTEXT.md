# Rehearse domain context

## Purpose

Rehearse proves that an application's backup can become a working application again. It performs disposable, application-level recovery drills and produces evidence without touching production.

## Core language

- **Drill plan:** Versioned configuration describing the backup source, restore target, sandbox, probes, limits, and cleanup policy.
- **Backup source:** Where recovery input is obtained, such as restic, a local archive, S3 objects, or a trusted command.
- **Restore target:** How recovered bytes become usable state, such as a Docker volume, PostgreSQL import, or SQLite file.
- **Sandbox:** An isolated Docker Compose environment created for one drill.
- **Probe:** An assertion about the recovered application: HTTP, TCP, command, SQL, or data-level.
- **Run:** One immutable execution of a plan with timestamps, logs, artifacts, and outcome.
- **Recovery point:** The backup snapshot/object chosen for a run.
- **RTO evidence:** Measured time from run start until required probes pass. This is observed evidence, not a contractual RTO guarantee.
- **Janitor:** Startup and periodic cleanup that finds abandoned Rehearse resources after crashes.
- **Reference workload:** Optional queued-orders application used to exercise RabbitMQ and PostgreSQL. It is not a Rehearse runtime dependency.

## Invariants

1. A drill must not mutate the source backup or production services.
2. Every created container, network, volume, temporary file, and secret mount must be labeled and attributable to one run.
3. Cleanup must be idempotent and run after success, failure, cancellation, timeout, and process restart.
4. Secret values never enter SQLite, logs, reports, metrics, or frontend payloads.
5. Adapters expose capabilities and typed failures; orchestration does not branch on vendor-specific shell output.
6. A successful run requires every required probe to pass against restored state.
7. Core installation remains a native Go binary plus a Docker-compatible runtime; optional integrations cannot become mandatory dependencies.

## Scope boundaries

v1 is single-owner and localhost-first. It does not include organizations, RBAC, a hosted SaaS control plane, Kubernetes execution, Windows support, or native Supabase/RDS/MongoDB adapters. The localhost web UI is the completed product surface; Electron is not a completion requirement.

