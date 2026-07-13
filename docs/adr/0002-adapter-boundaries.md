# ADR 0002: Separate source and restore-target adapters

- Status: Accepted
- Date: 2026-07-13

## Decision

Model backup acquisition and data restoration as independent adapter contracts. The orchestrator consumes typed capabilities, artifacts, progress events, and failures rather than vendor-specific command output.

## Why

Restic answers where and how encrypted backup snapshots are stored; it does not natively restore a PostgreSQL logical dump into a running database. Separating the axes lets one S3 or restic source feed several restore targets and prevents Rehearse from being restic-only.

## Consequences

Every advertised adapter needs contract, integration, cancellation, redaction, and cleanup tests. Trusted custom-command adapters are v1 escape hatches with explicit security warnings, restricted environments, timeouts, and output limits.

