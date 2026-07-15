# Fresh-task handoff

## Current state

See the [README project status](README.md) for the shipped repository surface. GitHub Issues and the project board define the implementation order and current status.

## Next implementation task

Select one issue whose dependencies are closed from the live [GitHub Project](https://github.com/users/azizu06/projects/4), then create an isolated worktree and issue-named branch. The dependency shape is summarized in [docs/roadmap/issue-map.md](docs/roadmap/issue-map.md), but the live project remains authoritative for actionability.

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
