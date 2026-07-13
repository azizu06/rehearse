# Issue tracker

Issues live in GitHub Issues at `azizu06/rehearse`.

## Workflow

1. Select an issue labeled `ready-for-agent` whose `Blocked by` dependencies are closed.
2. Create one isolated worktree and branch for that issue.
3. Keep acceptance criteria and verification evidence in the issue and pull request.
4. Open one pull request that closes the issue.
5. Do not merge dependent work before its prerequisite is merged.

Issue bodies use these sections: Problem, Outcome, Scope, Acceptance criteria, Verification, Dependencies, and Out of scope. Dependencies use exact `Blocked by #N` lines and GitHub sub-issues where the relationship is parent/child rather than sequential.

