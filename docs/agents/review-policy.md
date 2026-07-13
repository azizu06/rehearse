# Review policy

## Small and scoped pull requests

Run the relevant local tests and request `@codex review` on the pull request. Address concrete correctness, security, data-integrity, acceptance, and missing-test findings before merge.

## Feature-dense or high-risk pull requests

Use the no-mistakes remote from the feature worktree before pushing the final branch to origin. This includes orchestration state changes, Docker lifecycle/cleanup, authentication, secret handling, adapter execution, persistence migrations, Terraform, and cross-cutting releases.

The no-mistakes policy is interactive and capped at three autonomous review/fix rounds. Never use `--yes`. Auto-fix only error-level findings that threaten security, data integrity, external side effects, acceptance criteria, required verification, or unrecoverable reliability. Escalate all `ask-user` findings and optional hardening to Aziz.

## Merge gate

- Acceptance criteria are mapped to tests or reproducible evidence.
- Required CI is green.
- Cleanup and failure paths receive equal scrutiny to the success path.
- Documentation and examples describe only behavior the branch actually ships.
- Merge remains a human decision.

