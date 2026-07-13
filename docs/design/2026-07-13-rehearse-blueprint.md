# Rehearse architecture blueprint

## System shape

```text
React UI / CLI
      |
Authenticated local API
      |
Durable drill orchestrator ---- SQLite run journal
      |
source adapter -> staging artifact -> target adapter
                                      |
                              Docker Compose sandbox
                                      |
                          probes + logs + metrics
                                      |
                             report + cleanup
```

## Runtime components

- **API/control plane:** plan validation, authentication, scheduling, run commands, report queries, event stream.
- **Orchestrator:** durable state machine with stages `queued`, `preflight`, `acquire`, `restore`, `boot`, `probe`, `report`, `cleanup`, and terminal outcomes.
- **Adapter registry:** capability discovery and typed contracts for sources and targets.
- **Docker runner:** namespaced resources, labels, limits, cancellation, event capture, and idempotent teardown.
- **Probe engine:** composable assertions with deadlines, retry policies, redaction, and required/optional status.
- **Evidence store:** SQLite metadata plus bounded filesystem artifacts; secrets never persisted.
- **Janitor:** reconciles non-terminal runs and labeled Docker resources at startup.

## User experience

The main workflow is a guided drill-plan wizard, preflight validation, live stage timeline, probe details, cleanup result, and historical comparison. A user can run the same plan manually, on a schedule, or from CI. The reference demo is fully automated after the user starts `make demo`; the README explains what happened rather than requiring the user to manually imitate every stage.

## Safety model

- Default localhost bind and owner authentication
- Read-only backup-source access where providers support it
- Explicit destination allowlists and generated resource names
- No production network connectivity by default
- Resource/time/output limits
- Secret redaction at ingestion, execution, storage, API, and UI boundaries
- Cleanup proof recorded independently from application probe success

