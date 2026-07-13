# Fresh-task handoff

## Current state

Rehearse has an approved product definition and engineering foundation. Product code has intentionally not started. GitHub Issues and the project board define the implementation order.

## First implementation task

Start with GitHub Issue [#5](https://github.com/azizu06/rehearse/issues/5), **Scaffold the tested Go + React tracer bullet**. GitHub numbers pull requests and issues in one sequence, so automated Dependabot pull requests consumed #1-#4 during setup. Create an isolated worktree and branch named from #5. Build the smallest end-to-end tracer bullet test first; do not split into disconnected backend/frontend scaffolds that cannot prove the product loop.

## Required opening sequence

1. Read `AGENTS.md`, `CONTEXT.md`, `PRD.md`, and `docs/adr/`.
2. Inspect `git status`, worktrees, open PRs, and the [issue dependency board](https://github.com/users/azizu06/projects/4).
3. Claim one unblocked issue and create one isolated worktree.
4. Apply TDD and the review routing in `docs/agents/review-policy.md`.
5. Leave roadmap and resume claims unchanged unless acceptance evidence justifies an update.

## Decisions that are already closed

- Public Apache-2.0 repository named Rehearse
- Native Go binary with embedded React/TypeScript localhost UI
- SQLite internal state; Docker Compose sandboxes
- Single-owner/authenticated; no Electron or hosted multi-user v1
- RabbitMQ/PostgreSQL only in the optional reference workload, not core runtime
- Public v0.1 at week four; resume-ready v1 around weeks seven to eight
- Resume update waits for working v0.1 evidence

## Orchestration

The user-facing root task is the coordinator. Implementation tasks are fresh Codex tasks in isolated worktrees. Use Terra medium/high for scoped implementation and tests. Use Sol high for architecture, security-sensitive, or cross-cutting review. Sol xhigh/max/ultra requires Aziz's explicit approval.
