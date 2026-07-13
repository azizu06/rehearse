# Agent orchestration

## Ownership model

The root Codex task is the user-facing coordination hub. Each substantive implementation issue belongs to a fresh user-owned Codex task and isolated worktree. The coordinator checks dependency readiness, dispatches bounded work, monitors evidence, and reviews completion against the live issue before reporting it done.

## Dependency rules

- Only issues with no open blockers may begin.
- Parallel work is limited to independent files and contracts.
- Contract-defining work lands before adapters or UI consumers that depend on it.
- One issue, branch, worktree, and pull request remain paired through merge.

## Model routing

- **Terra medium:** documentation, narrow tests, routine UI, and mechanical scoped work.
- **Terra high:** feature implementation with multiple files or real integration tests.
- **Sol high:** architecture, threat boundaries, data integrity, cleanup semantics, cross-cutting review, and final release audits.
- **Sol xhigh/max/ultra:** never select without Aziz's explicit approval.

## Completion contract

An agent report must include changed behavior, exact verification run, remaining risks, issue/PR links, and one recommended continuation route. A coordinator does not accept “tests should pass” or unverified screenshots as completion evidence.

