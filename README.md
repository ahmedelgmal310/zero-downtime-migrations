# zero-downtime-migrations

[![CI](https://github.com/ahmedelgmal310/zero-downtime-migrations/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/ahmedelgmal310/zero-downtime-migrations/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Shipping a **breaking database schema change** and three app releases on Kubernetes with
**zero failed requests**. A Go API on PostgreSQL splits `name` into `first_name` /
`last_name` with the **expand → migrate → contract** pattern. Migrations run as a Helm
pre-upgrade hook Job with `lock_timeout` and `statement_timeout`. The backfill is a batched,
throttled Job. A fleet guard refuses the destructive step while old code is still running.

Every push to `main` re-proves this on a throwaway **kind** cluster under continuous
**k6** load (reads + writes). The run covers rolling upgrades v1 → v2 → v3, every migration,
and a **rollback from v2 to v1 while the backfill is running**. CI fails unless every step has
0 failed requests, p95 under the SLO, and passing data consistency checks.

Results: see [Results from CI](#results-from-ci).
