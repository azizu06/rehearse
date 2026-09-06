# Review policy

## Small and scoped pull requests

Run the relevant local tests and use the Matt Pocock `code-review` workflow with one fresh Standards reviewer and one fresh Spec reviewer. Address concrete correctness, security, data-integrity, acceptance, and missing-test findings before merge.

## Feature-dense or high-risk pull requests

Use the same fresh Standards and Spec reviewers, plus proportional failure-path, race, integration, static, and security checks for the affected surface. Exact-head required CI remains mandatory. Reuse valid evidence after focused corrections instead of restarting broad review pipelines.

As explicitly directed on 2026-09-05, Rehearse does not use No Mistakes or another heavy autonomous review pipeline. GitHub AI review is optional extra evidence and is not a merge gate.

## Merge gate

- Acceptance criteria are mapped to tests or reproducible evidence.
- Required CI is green.
- Cleanup and failure paths receive equal scrutiny to the success path.
- Documentation and examples describe only behavior the branch actually ships.
- Merge remains a human decision.
