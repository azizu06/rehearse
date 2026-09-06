# Rehearse

**Know your recovery works before the incident.**

Rehearse is an open-source recovery-drill platform for self-hosted applications. It restores a real backup into an isolated Docker Compose environment, starts the application, runs application-level probes, records recovery time and failures, and removes the temporary environment.

> Project status: foundation phase. The tested Go/React tracer bullet, durable drill journal, isolated Docker Compose runner with startup reconciliation, vendor-independent source contract, and read-only restic adapter for local and S3-compatible repositories are in place. These integration boundaries are not yet wired into a complete recovery drill; that work is tracked in [Issue #11](https://github.com/azizu06/rehearse/issues/11).

## Why Rehearse

A successful backup only proves that bytes were written. It does not prove that the data can be restored, the application can boot from it, or users can complete the workflows that matter. Teams often discover stale credentials, broken restore commands, schema incompatibilities, and missing dependencies during an actual incident.

Rehearse turns that manual recovery rehearsal into a repeatable job with evidence.

## The product loop

```text
backup source -> isolated restore -> boot services -> run probes -> report -> cleanup
```

- One native Go application for macOS and Linux
- Polished React dashboard served from `localhost`
- Embedded SQLite state; no hosted control plane required
- Docker Compose isolation for safe, disposable drills
- Source adapters and restore-target adapters instead of a restic-only design
- Prometheus metrics, optional Grafana dashboard, and CI-friendly reports

## Planned v1 support

| Layer | v1 adapters |
|---|---|
| Backup sources | restic repositories, local files/archives, plain S3 objects, trusted custom commands |
| Restore targets | files/Docker volumes, PostgreSQL, MySQL/MariaDB, SQLite, trusted custom commands |
| Verification | HTTP, TCP, command, SQL, and data assertions |
| Output | Web report, JSON, JUnit, Prometheus metrics |

The core runtime does **not** require RabbitMQ or PostgreSQL. An optional queued-orders reference application uses both to prove that Rehearse can recover and verify a realistic multi-service workload.

## Roadmap

- **v0.1 — four-week public milestone:** one trustworthy end-to-end restic-to-PostgreSQL recovery drill, UI timeline and report, metrics, reference workload, and release packaging.
- **v1.0 — resume-ready product:** the full source/target adapter matrix, safety hardening, CI mode, AWS/Terraform proof, external onboarding, and release audit.
- **Later:** Kubernetes runner, Windows, native RDS/Supabase/MongoDB adapters, and team tenancy.

See [PRD.md](PRD.md), [CONTEXT.md](CONTEXT.md), and the [architecture decisions](docs/adr/) for the committed scope. Work is tracked in [GitHub Issues](https://github.com/azizu06/rehearse/issues) and the public [Rehearse delivery board](https://github.com/users/azizu06/projects/4).

## Engineering standards

Rehearse is developed issue-first with tracer-bullet TDD. Go code uses the standard `testing` package, race detection, fuzz tests where parsers cross trust boundaries, and Testcontainers for real service integration. The frontend uses Vitest, React Testing Library, and Playwright. Every pull request follows the review policy in [docs/agents/review-policy.md](docs/agents/review-policy.md).

## Development

The tracer-bullet toolchain requires the Go version declared in `go.mod` and Node.js 22.

```bash
npm --prefix web ci
make test
make test-race
make test-adapters
make lint
make static
make build
```

`make test-adapters` exercises real local and S3-compatible restic repositories. It requires restic 0.18.0 or newer plus a Docker-compatible runtime for the Testcontainers-managed S3 service. Orchestration must successfully preflight every newly configured adapter instance before listing or acquisition. Use `make test-adapters-race` to run the same boundary checks with Go's race detector.

`make security` adds `govulncheck` plus a pinned Trivy filesystem scan. It requires Trivy 0.72.0 on `PATH`, or `TRIVY=/path/to/trivy`; CI installs the same scanner version.

The real sandbox boundary is opt-in locally because it creates short-lived
labeled Docker resources. It requires the pinned Alpine fixture image to exist
before the runner starts; the runner itself never builds or pulls images:

`DockerRunner.Run` accepts a trusted in-process orchestration callback. The
callback must honor its context and return before the runner cleans the sandbox.
Interruptible workload operations should use context-bound subprocess or Docker
boundaries rather than detached goroutines.

```bash
docker pull alpine@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
REHEARSE_DOCKER_INTEGRATION=1 go test -race ./internal/sandbox -count=1
```

`make test` is the single command for the Go and frontend unit suites. To run the browser smoke path against a freshly built native binary:

```bash
npm --prefix web exec -- playwright install chromium
make test-browser
```

The binary listens on `127.0.0.1:8484` by default. Start it with `./build/rehearse`, then open <http://127.0.0.1:8484>.

## License

Apache License 2.0. See [LICENSE](LICENSE).
