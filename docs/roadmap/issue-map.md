# Implementation dependency map

The live [GitHub Project](https://github.com/users/azizu06/projects/4) is authoritative for status. This file preserves the intended dependency shape for fresh agents.

| Issue | Outcome | Blocked by | Phase |
|---|---|---|---|
| [#5](https://github.com/azizu06/rehearse/issues/5) | Tested Go + React tracer bullet | None | v0.1 |
| [#6](https://github.com/azizu06/rehearse/issues/6) | Durable state machine + SQLite | #5 | v0.1 |
| [#7](https://github.com/azizu06/rehearse/issues/7) | Source contract + restic | #5, #6 | v0.1 |
| [#8](https://github.com/azizu06/rehearse/issues/8) | Docker Compose runner + janitor | #5, #6 | v0.1 |
| [#9](https://github.com/azizu06/rehearse/issues/9) | Target contract + PostgreSQL/volumes | #5, #6, #8 | v0.1 |
| [#10](https://github.com/azizu06/rehearse/issues/10) | Probes + evidence reports | #5, #6 | v0.1 |
| [#11](https://github.com/azizu06/rehearse/issues/11) | First real recovery drill | #6-#10 | v0.1 |
| [#12](https://github.com/azizu06/rehearse/issues/12) | Authenticated plan/run/history UI | #5, #6, #11 | v0.1 |
| [#13](https://github.com/azizu06/rehearse/issues/13) | Scheduling + Prometheus/Grafana | #6, #11, #12 | v0.1 |
| [#14](https://github.com/azizu06/rehearse/issues/14) | RabbitMQ/PostgreSQL reference workload | #11 | v0.1 |
| [#15](https://github.com/azizu06/rehearse/issues/15) | Public v0.1 package and release | #12-#14 | v0.1 |
| [#16](https://github.com/azizu06/rehearse/issues/16) | Plain local/S3 object + command sources | #7, #11 | v1.0 |
| [#17](https://github.com/azizu06/rehearse/issues/17) | MySQL/SQLite/command targets | #9, #11 | v1.0 |
| [#18](https://github.com/azizu06/rehearse/issues/18) | Reliability and security hardening | #11, #12 | v1.0 |
| [#19](https://github.com/azizu06/rehearse/issues/19) | CI mode + JSON/JUnit | #11, #12 | v1.0 |
| [#20](https://github.com/azizu06/rehearse/issues/20) | AWS/S3 Terraform proof | #16, #18 | v1.0 |
| [#21](https://github.com/azizu06/rehearse/issues/21) | External beta + product demo | #15-#20 | v1.0 |
| [#22](https://github.com/azizu06/rehearse/issues/22) | Final completion audit + v1 release | #16-#21 | v1.0 |

For current actionability, use the live project. When a prerequisite closes, the coordinator verifies all remaining dependencies before removing the `blocked` label from a newly actionable issue.
