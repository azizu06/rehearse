# ADR 0003: Tracer-bullet TDD and risk-routed review

- Status: Accepted
- Date: 2026-07-13
- Amended: 2026-09-05

## Decision

Build vertical product slices with test-driven development. Use Go's standard test stack for domain behavior, Testcontainers for real external boundaries, Vitest/React Testing Library for frontend behavior, Playwright for critical flows, and one fresh Standards reviewer plus one fresh Spec reviewer through the Matt Pocock `code-review` workflow.

## Why

Recovery software can report success while silently leaving unusable data or abandoned resources. Real services and failure-path tests are necessary; mocks alone cannot verify backup tools, databases, or Docker lifecycle behavior.

## Consequences

The initial issue establishes one minimal end-to-end drill before broad scaffolding. Race detection and targeted fuzzing run on the Go surfaces where concurrency and untrusted parsing justify them. Feature-dense and high-risk PRs add proportional failure-path, integration, static, security, and exact-head CI evidence. Rehearse does not use No Mistakes or another heavy autonomous review pipeline; GitHub AI review is optional extra evidence rather than a merge gate.
