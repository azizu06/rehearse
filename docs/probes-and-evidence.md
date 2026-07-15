# Probe and evidence contract

Rehearse probe configuration is a strict, bounded JSON document with schema
version `rehearse.probes/v1`. The parser rejects unknown fields, duplicate IDs,
unsupported probe kinds, non-contiguous declaration ordinals, and values outside
the documented limits before a plan can enter SQLite.

Every probe declares:

- a unique `id` and contiguous `ordinal`, beginning at 1;
- whether the result is `required`;
- an absolute `deadline`, fixed `backoff`, and `max_attempts` from 1 through 100;
- exactly one typed HTTP, TCP, command, SQL, or data payload.

The deadline, backoff budget, and attempt ceiling apply together. Rehearse stops
on the first exhausted constraint and records both the number of attempts and
the limiting constraint. Probes execute sequentially in declaration order so
the report order is the real execution order.

Required and optional results remain separate truths. Any required non-pass
prevents a successful run report. An optional failure remains visible in probe
evidence without changing an otherwise successful run outcome.

## Boundary rules

- HTTP performs a bounded `GET`, never follows redirects, and records status
  evidence without persisting the response body.
- TCP performs a connect-only check against an explicit host and port.
- Command probes require persisted `trust_acknowledged: true`, a clean absolute
  executable path, direct execution without a shell, a minimal environment,
  bounded arguments, a deadline, and independent 16 KiB stdout/stderr caps.
- SQL probes contain a runtime connection ID rather than a DSN. The caller must
  supply a dedicated least-privileged PostgreSQL connection for the configured
  expected role. Rehearse uses pgx extended-protocol statement execution, a
  read-only transaction, and a statement timeout. PostgreSQL permissions and
  read-only enforcement—not prefix inspection—are the security boundary.
- Data assertions hash one regular file below a runner-supplied restored-data
  root. Relative-path validation, symlink resolution, and a 1 MiB file cap keep
  the assertion read-only and confined.

Command arguments, SQL statements, URLs, expected values, and connection IDs are
persisted plan configuration and therefore must never contain secret values.
Secrets remain out-of-band. Free-form evidence is bounded first and then passed
through an in-memory literal-marker redactor.

## Report rules

Final reports use schema version `rehearse.report/v1` and include the run/plan
identity, selected recovery point, measured stage durations, ordered probe
evidence, run outcome, and independent cleanup outcome. Stage and probe
ordinals must each be unique and contiguous.

Canonical JSON sorts only by those semantic ordinals and uses typed structs, UTC
RFC3339 timestamps, and integer nanosecond durations. The persistence boundary
reapplies redaction before writing the immutable SQLite document, and the local
HTTP API reapplies redaction before returning it. JUnit export and historical
report UI are intentionally outside this contract.

The real PostgreSQL verification is available through:

```bash
make test-integration
```

It proves a scalar query succeeds while stacked statements, DML, DDL, a
modifying CTE, and a side-effecting sequence call are rejected, and verifies the
statement timeout.
