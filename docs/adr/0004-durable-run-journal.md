# ADR 0004: Durable drill-run journal

- Status: Accepted
- Date: 2026-07-15

## Decision

Persist drill plans and runs with the pure-Go `modernc.org/sqlite` driver. Each
run uses a deterministic, vendor-independent state machine. Every accepted
mutation appends an immutable `run_events` row and updates the current `runs`
projection in the same SQLite transaction.

Keep execution outcome and cleanup status orthogonal. Success, failure,
cancellation, and timeout are distinct run outcomes and event kinds; cleanup
success or failure is recorded afterward as a separate event.

Persist only a typed, secret-free `PlanSpec`. Credentials are represented by
validated environment, absolute-file, or keychain references. The persisted
type has no secret-value or arbitrary configuration map.

Embed monotonically numbered SQL migrations in the Go binary. Apply each
migration transactionally and record it in `schema_migrations`, making repeated
startup from an empty or current database idempotent. When an existing database
is reopened, unfinished runs receive durable restart-reconciliation metadata and
an immutable reconciliation event.

Opening the journal first acquires a non-blocking OS-backed exclusive ownership
lock beside the database and holds it for the `Store` lifetime. A second live
runtime receives a typed already-owned result before migrations or restart
reconciliation can mutate durable state. Process exit releases the kernel lock;
the next owner then performs the normal interrupted-run reconciliation.

## Why

The append-only journal preserves what happened, while the projection makes
restart queries inexpensive. Updating both atomically prevents the audit trail
and current state from disagreeing. Separate cleanup truth avoids reporting a
fully clean success when recovery passed but temporary resources were not
removed.

The pure-Go driver preserves Rehearse's single native-binary installation model
without a CGO toolchain. Typed credential references enforce the PRD's
out-of-band secret policy at the persistence boundary.

## Consequences

- State transitions are serialized through the SQLite owner and remain safe for
  concurrent callers.
- v0.1 has one live journal owner; multi-process leases or distributed ownership
  require a separate architecture decision.
- `run_events` cannot be updated or deleted; corrections must be new events in a
  future migration or domain extension.
- Future adapters may add typed, secret-free plan fields through migrations, but
  cannot introduce arbitrary value-bearing configuration into this schema.
- Future startup orchestration and the janitor consume reconciliation metadata;
  this issue records it but does not create Docker resources or perform cleanup.
