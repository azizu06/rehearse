# ADR 0003: Tracer-bullet TDD and risk-routed review

- Status: Accepted
- Date: 2026-07-13

## Decision

Build vertical product slices with test-driven development. Use Go's standard test stack for domain behavior, Testcontainers for real external boundaries, Vitest/React Testing Library for frontend behavior, Playwright for critical flows, and risk-routed AI review.

## Why

Recovery software can report success while silently leaving unusable data or abandoned resources. Real services and failure-path tests are necessary; mocks alone cannot verify backup tools, databases, or Docker lifecycle behavior.

## Consequences

The initial issue establishes one minimal end-to-end drill before broad scaffolding. Race detection and targeted fuzzing run on the Go surfaces where concurrency and untrusted parsing justify them. Small PRs use Codex review; cross-cutting or high-risk PRs use no-mistakes under an interactive three-round cap.

