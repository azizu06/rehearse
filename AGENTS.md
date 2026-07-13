# Rehearse agent instructions

Read `CONTEXT.md`, `PRD.md`, and the relevant ADRs before changing product behavior. Read `HANDOFF.md` at the start of a fresh implementation task.

## Delivery workflow

- GitHub Issues are the source of truth. Do not begin a feature without an acceptance-tested issue.
- Keep one issue to one branch to one isolated worktree to one pull request.
- Respect `Blocked by #N` dependencies. The root task coordinates; fresh tasks own implementation issues.
- Use tracer-bullet TDD: write the smallest failing acceptance path, make it pass end to end, then expand unit and boundary coverage.
- Keep the core runtime single-owner, localhost-first, SQLite-backed, and independent of RabbitMQ/PostgreSQL.
- Treat backup credentials, restored data, command adapters, Docker access, and cleanup as security-sensitive surfaces.
- Never claim a source or target adapter is supported until its real integration test and failure-path cleanup test pass.

## Verification

- Go: `go test ./...`, `go test -race ./...`, table-driven tests, `httptest`, targeted fuzzing, and Testcontainers at external boundaries.
- Web: Vitest + React Testing Library for components and Playwright for critical recovery flows.
- Infrastructure: `terraform fmt -check`, `terraform validate`, and TFLint.
- Static/security: `go vet`, golangci-lint, govulncheck, and Trivy when the relevant surface exists.
- Small/scoped PRs request `@codex review`. Feature-dense, security-sensitive, or cross-cutting PRs use the no-mistakes gate under `docs/agents/review-policy.md`.

## Agent skills

### Issue tracker

Work is tracked in GitHub Issues for `azizu06/rehearse`. See `docs/agents/issue-tracker.md`.

### Triage labels

Use the standard five-label triage state machine. See `docs/agents/triage-labels.md`.

### Domain docs

This is a single-context repository with root `CONTEXT.md` and `docs/adr/`. See `docs/agents/domain.md`.

