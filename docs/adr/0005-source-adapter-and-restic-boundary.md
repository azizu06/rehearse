# ADR 0005: Source adapter and restic process boundary

- Status: Accepted
- Date: 2026-07-15

## Decision

Expose each configured backup source through three vendor-independent operations:
capability preflight, recovery-point listing, and acquisition of one selected
recovery point. Recovery-point metadata, aggregate progress, directory artifacts,
and failures are typed. Cancellation and deadlines are controlled only by the
caller's context.

Implement the first adapter through restic 0.18.0 or newer. Invoke the restic
binary directly without a shell, request its documented JSON formats, bound
stdout and stderr independently, tolerate additive JSON fields and message
types, and never expose raw command output through errors, progress, logs, or
artifacts. Every JSON-lines object must carry a present, non-null string
`message_type`; unknown string values remain forward-compatible and are ignored.
Repository reads use `--no-lock` and `--no-cache`.

Keep provider configuration typed. Restic receives a repository-password file
and, for S3-compatible storage, an AWS shared-credentials file. Both are created
inside a permission-restricted temporary directory from validated credential
references, use mode `0600`, and are removed after every success or failure.
Secret values never enter argv or persisted adapter configuration.

Acquire into a hidden staging child of an empty caller-owned workspace. Promote
the staging directory atomically only after restic exits successfully and emits
one decoded `message_type: "summary"` completion marker. Restore counters are
advisory: restic's JSON contract permits zero-valued fields to be omitted, so
omission decodes as zero while explicit nulls, wrong types, and inconsistent
restored/total values fail closed. The adapter removes partial staging after
process failure, corrupt or oversized output, cancellation, timeout, or startup
failure. After successful promotion, orchestration owns cleanup of the returned
artifact.

## Why

The orchestration core must not branch on restic output or inherit its evolving
CLI schema. Requiring restic 0.18.0 establishes the structured JSON fatal-error
boundary needed for typed, redacted failure handling. Temporary files avoid
putting credentials in process arguments or value-bearing plan fields. Staging
prevents a partial restore from masquerading as a usable recovery artifact.

## Consequences

- Adding a fundamental source operation changes the adapter interface; adding a
  provider keeps its configuration outside orchestration.
- New restic JSON fields and message types are ignored. A missing completion
  marker or invalid known field fails closed; omitted zero-valued counters do
  not.
- Local and S3-compatible restic repositories are supported only after their
  real integration, failure, immutability, redaction, and cleanup tests pass.
- Successful artifact cleanup belongs to later orchestration and janitor work;
  failed acquisition cleanup remains the source adapter's responsibility.
- Local archive, plain S3-object, custom-command, target, Docker, UI, and auth
  behavior remain outside this decision.
