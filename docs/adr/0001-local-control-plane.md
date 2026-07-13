# ADR 0001: Native local control plane

- Status: Accepted
- Date: 2026-07-13

## Decision

Ship Rehearse as a native Go binary for macOS and Linux. It serves an embedded React/TypeScript UI on `127.0.0.1`, persists internal state in SQLite, authenticates its single owner, and uses the host's Docker-compatible runtime for disposable recovery sandboxes.

## Why

Backup credentials and restored data remain on the operator's machine. A single binary lowers adoption friction while a browser UI remains polished and cross-platform. SQLite provides durable plans and reports without requiring another service.

## Consequences

Electron, hosted tenancy, organizations/RBAC, and Windows are not v1 requirements. macOS users need Docker Desktop or another compatible container runtime for Linux workload sandboxes, but Rehearse itself runs natively.

