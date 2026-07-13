# Rehearse

**Know your recovery works before the incident.**

Rehearse is an open-source recovery-drill platform for self-hosted applications. It restores a real backup into an isolated Docker Compose environment, starts the application, runs application-level probes, records recovery time and failures, and removes the temporary environment.

> Project status: foundation phase. The public v0.1 tracer bullet is being built in the open; installation instructions will appear once the first complete recovery drill is reliable.

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

See [PRD.md](PRD.md), [CONTEXT.md](CONTEXT.md), and the [architecture decisions](docs/adr/) for the committed scope. Work is tracked in [GitHub Issues](https://github.com/azizu06/rehearse/issues).

## Engineering standards

Rehearse is developed issue-first with tracer-bullet TDD. Go code uses the standard `testing` package, race detection, fuzz tests where parsers cross trust boundaries, and Testcontainers for real service integration. The frontend uses Vitest, React Testing Library, and Playwright. Every pull request follows the review policy in [docs/agents/review-policy.md](docs/agents/review-policy.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).

