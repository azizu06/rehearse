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

Select checks for the changed surface; the list below is not a requirement to
install absent stacks or run every tool for a documentation-only change. Use
current CI and repository configuration to identify required gates. Documentation
changes need source/link verification and `git diff --check`; behavior changes
need acceptance evidence and the applicable checks below.

- Go: `go test ./...`, `go test -race ./...`, table-driven tests, `httptest`, targeted fuzzing, and Testcontainers at external boundaries.
- Web: Vitest + React Testing Library for components and Playwright for critical recovery flows.
- Infrastructure: `terraform fmt -check`, `terraform validate`, and TFLint.
- Static/security: `go vet`, golangci-lint, govulncheck, and Trivy when the relevant surface exists.
- Every PR uses one fresh Standards reviewer and one fresh Spec reviewer through the Matt Pocock `code-review` workflow. Feature-dense, security-sensitive, or cross-cutting PRs add proportional failure-path/security checks and exact-head CI; Rehearse does not use No Mistakes or another heavy autonomous review pipeline. GitHub AI review is optional extra evidence, not a merge gate.

## Agent skills

### Issue tracker

Work is tracked in GitHub Issues for `azizu06/rehearse`. See `docs/agents/issue-tracker.md`.

### Triage labels

Use the standard five-label triage state machine. See `docs/agents/triage-labels.md`.

### Domain docs

This is a single-context repository with root `CONTEXT.md` and `docs/adr/`. See `docs/agents/domain.md`.

## Graphify

Use an existing current graph only when it helps a cross-module investigation,
and verify conclusions against source. A missing graph does not block targeted
source inspection. Do not generate or commit graph output for routine edits.
