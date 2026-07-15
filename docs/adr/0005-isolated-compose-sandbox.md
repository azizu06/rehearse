# ADR 0005: Bounded Docker Compose sandbox ownership

- Status: Accepted
- Date: 2026-07-15

## Decision

Run each drill in one local Docker Compose project through shell-free
`docker compose` subprocesses. Rehearse supplies the project name, a generated
last-file-wins safety override, `--no-build`, and `--pull never`.

The project name is `rehearse-` plus the first 24 lowercase hexadecimal
characters of the SHA-256 digest of the durable run ID. A 96-bit digest has an
approximate birthday-collision probability of 6.3e-18 at one million runs and
6.3e-12 at one billion runs. The full digest is retained in the
`dev.rehearse.run-fingerprint` label. A cross-process advisory lock serializes
the bounded project name, and any labeled full-digest mismatch fails closed.

Service, network, and volume keys are limited to 26 lowercase alphanumeric or
dash characters; v0.1 permits one replica. Together with the 33-character
project name, generated container aliases and resource names remain at most 62
characters. Explicit hostnames and aliases must be single RFC 1123 labels.

The generated override applies CPU, memory, PID, restart, pull, bounded local
logging, and ownership policy to every service. Named networks are internal and
all named networks and volumes receive ownership labels. Compose interpolation
uses an explicitly empty environment file. The runner rejects external or
custom-named resources and Compose features that can escape or weaken the
sandbox boundary, including builds, caller-owned resource/logging policy,
privileged mode, host namespaces, published ports, bind/anonymous mounts,
Docker socket/API access, host devices, extra capabilities, security-profile
overrides, and non-local resource drivers.

The v0.1 fail-closed field set rejects top-level `configs`, `models`, and
`secrets`; service `build`, `blkio_config`, `cap_add`, host `cgroup`,
`cgroup_parent`, all caller CPU/memory/PID settings, `credential_spec`,
`configs`, `container_name`, deploy placement/rollout/resources or scaling,
`device_cgroup_rules`, `devices`, custom DNS, `env_file`, `extra_hosts`,
`external_links`, GPUs, `group_add`, host IPC/PID/UTS/network/user namespaces,
`isolation`, `label_file`, links, caller logging, custom MAC/IP policy,
`models`, OOM/shm/storage/tmpfs/ulimit policy, lifecycle hooks, ports,
`privileged`, host-executed `provider`, caller pull/restart policy, custom
runtime, `secrets`, `security_opt`, sysctls, `use_api_socket`, unsupported
mounts, image-declared `VOLUME` targets without an attributable named-volume or
tmpfs mount, and `volumes_from`. Networks also reject attachability, caller
IPAM, driver options, external/custom names, and per-service interface/address
policy; volumes reject external/custom names, driver options, and non-local
drivers.

The original Compose inputs are rendered with the generated policy into one
final JSON snapshot. Rehearse validates those exact bytes and `up` receives
only that snapshot, so it cannot reread the original files or environment
inputs. Because rendering can interpolate secrets, the snapshot and transient
policy live in a deterministic Rehearse-owned 0700 directory as 0600 files,
are never included in command errors, and are removed by both normal cleanup
and startup reconciliation after a process crash.

Cleanup lists, inspects, and deletes only resources matching the Rehearse
managed, project, and full run-fingerprint labels. It uses a fresh bounded
context after success, failure, cancellation, timeout, or output overflow.
Container removal never cascades into attached volumes; every volume deletion
comes from the exact ownership-filtered volume list.
After an initial 500 ms daemon-settle interval, cleanup requires two empty
label-filtered scans separated by another 500 ms. Failure to prove quiescence is
recorded as retryable cleanup failure.

The startup janitor consumes Issue #6's durable queue:
`needs_reconciliation = 1`, cleanup status `pending` or `failed`, or a failed
sandbox cleanup claim.
Before any Docker command can create resources, the runner adds a one-shot
cleanup claim for an eligible non-reconciling durable run. A live pending claim
stays out of the janitor queue; restart reconciliation activates it after
process death, while a failed claim enters the queue immediately. Successful
cleanup closes the claim without permitting a second sandbox lifecycle for
that run.
`cleanup_failed` remains queued; `BeginCleanupRetry` moves only cleanup back to
pending and appends immutable reconciliation evidence. Only
`cleanup_succeeded` clears reconciliation ownership.

## Why

Compose supplies the accepted local multi-service boundary without embedding a
second orchestration engine. Stable labels survive process termination, while
the SQLite journal remains the authority for which run the startup janitor may
reconcile. Bounded names and resource policies avoid daemon-specific naming
failures, cross-run convergence, and accidental interaction with unrelated
Docker objects.

## Consequences

- Images must already exist locally; acquisition belongs to installation or a
  future explicit image policy, not a drill run.
- v0.1 Compose inputs intentionally form a constrained subset. Expanding host
  mounts, devices, capabilities, networking, or scaling requires a new security
  decision and real cleanup tests.
- macOS Docker Desktop and Ubuntu Docker integration are both pre-merge gates.
- The label keys, 96-bit project identity, and durable cleanup retry semantics
  are compatibility contracts for future runners and janitors.
