# Metrics and the optional Grafana dashboard

Rehearse serves Prometheus metrics at `GET /metrics` on the control-plane
address (default `http://127.0.0.1:8484/metrics`). Prometheus and Grafana are
optional. Rehearse builds, tests, and runs the same way without them, and the
stack below lives in its own compose file.

## Exported drill metrics

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `rehearse_drill_runs_total` | counter | `outcome` | Runs that reached an execution outcome: `succeeded`, `failed`, `cancelled`, or `timed_out`. |
| `rehearse_drill_stage_duration_seconds` | histogram | `stage` | Time spent in each state-machine stage: `queued`, `preflight`, `acquire`, `restore`, `boot`, `probe`, `report`, `cleanup`. |
| `rehearse_drill_cleanup_failures_total` | counter | none | Cleanup attempts that failed and left resources for the janitor. |
| `rehearse_drill_last_success_timestamp_seconds` | gauge | none | Unix time of the newest succeeded outcome this process saw, or 0. |

The endpoint also includes the standard Go runtime and process metrics from
`client_golang`.

Labels only ever carry the closed outcome and stage values above. Plan IDs,
plan names, run IDs, paths, and secret values never become labels, which keeps
series counts fixed and honors the invariant that secrets never reach metrics.

The metrics are fed by the SQLite journal. A journal opened with
`journal.WithRunObserver(recorder)` reports every committed run change to the
recorder after its transaction commits, so rejected or rolled-back changes are
never counted. A stage interval that spans a Rehearse restart is not measured,
because restart reconciliation makes its start time meaningless.

## Start the optional stack

Run Rehearse natively, then start Prometheus and Grafana with Docker Compose:

```bash
make build
./build/rehearse -addr 127.0.0.1:8484
docker compose -f deploy/observability/compose.yaml up -d
```

- Prometheus: <http://127.0.0.1:9090> scrapes `host.docker.internal:8484`
  every 15 seconds and keeps 30 days of data.
- Grafana: <http://127.0.0.1:3000> opens the provisioned "Rehearse recovery
  drills" dashboard for anonymous viewers. Sign in with Grafana's default
  administrator account only if you want to edit it.

Both ports bind to loopback. Stop the stack with
`docker compose -f deploy/observability/compose.yaml down` and add `-v` to
delete its stored data.

On Docker Desktop, containers reach a loopback-bound Rehearse through
`host.docker.internal`. On Linux, `host.docker.internal` resolves to the Docker
bridge gateway (commonly `172.17.0.1`), so start Rehearse on that address, for
example `./build/rehearse -addr 172.17.0.1:8484`. Avoid `0.0.0.0`: the
control plane is not authenticated yet.

## Dashboard panels

The dashboard JSON is
[`deploy/observability/grafana/dashboards/rehearse.json`](../deploy/observability/grafana/dashboards/rehearse.json).
Every panel reads the dashboard time range (default: last 7 days).

- **Time since last successful drill:** age of the newest succeeded outcome in
  the range. Empty means nothing succeeded in that range.
- **Cleanup failures:** failed cleanup attempts in the range. Any value above
  zero means labeled resources waited for the janitor.
- **Drill outcomes:** runs per outcome in the range.
- **Mean stage duration:** average time per stage in the range.
- **p95 stage duration:** 95th percentile time per stage, estimated from the
  histogram buckets.

A test checks that the dashboard parses, that each panel has a description and
a provisioned data source, and that it queries only metrics Rehearse registers.

## Current limitations

- The native binary serves `/metrics`, but it does not execute drills yet, so
  its drill series stay at zero until the end-to-end recovery workflow writes
  runs through a journal opened with the recorder.
- Counters and the last-success gauge reset when Rehearse restarts. The
  dashboard uses `increase()` and `max_over_time()` so Prometheus history
  covers the gap.
- `/metrics` is unauthenticated like the rest of the current control plane and
  relies on the loopback default bind address.
