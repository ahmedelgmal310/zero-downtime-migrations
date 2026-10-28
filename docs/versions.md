# Pinned versions

Every tool, image and action is pinned. Images use `tag@sha256:digest`, actions use a
full commit SHA with the version in a comment, downloaded binaries are verified against a
recorded SHA-256, and Go modules are locked by `go.sum`. A version is adopted only after
it has been released for at least two weeks; Dependabot proposes updates with a 14-day
cooldown (`.github/dependabot.yml`).

Where each pin lives: build images in `Dockerfile`, PostgreSQL in `deploy/postgres.yaml`
and the `go` job's service container, everything else in `.github/workflows/ci.yml`.
This file is the index; update it in the same change.

Pins as of 2026-10-28.

## Runtime

| Component | Version | Released | Pin |
|---|---|---|---|
| PostgreSQL (e2e and Go integration tests) | 18.6 (alpine3.24) | 2026-08-13 | `sha256:10a5c69a83035ab5239f5620d708520e8b28f5aa1f2b20143e6024532edadfb5` |
| kind | v0.33.0 | 2026-08-26 | release binary via `helm/kind-action` |
| Kubernetes node image | kindest/node v1.36.4 | | `sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed` |
| kubectl | v1.36.4 | | installed by `helm/kind-action` |
| Helm | v4.3.1 | | installed by `azure/setup-helm` |
| k6 | 2.3.0 | 2026-09-21 | `grafana/k6@sha256:2dd4756ec6e14e62778916e1eb850108e4b499304c02df56760fa0bdb0fba0b8` |

## Service image (`Dockerfile`)

| Stage | Image | Digest |
|---|---|---|
| build | golang 1.27.3-alpine3.24 | `sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb` |
| runtime | gcr.io/distroless/static-debian13:nonroot | `sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99` |

## Go (`go.mod`)

| Module | Version | Released |
|---|---|---|
| Go toolchain | 1.27.3 | |
| github.com/pressly/goose/v3 | v3.29.0 | 2026-10-11 |
| github.com/jackc/pgx/v5 | v5.11.0 | 2026-09-07 |
| github.com/jackc/puddle/v2 (indirect) | v2.2.3 | 2026-10-05 |
| github.com/sethvargo/go-retry (indirect, via goose) | v0.5.0 | 2026-10-05 |
| golang.org/x/text (indirect, via goose) | v0.43.0 | 2026-10-09 |
| golang.org/x/sync (indirect, via goose) | v0.24.0 | 2026-09-23 |

## CI (`.github/workflows/ci.yml`)

| Tool | Version | Pin |
|---|---|---|
| actions/checkout | v7.0.1 | `3d3c42e5aac5ba805825da76410c181273ba90b1` |
| actions/setup-go | v7.0.0 | `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e` |
| actions/upload-artifact | v7.0.2 | `cf430e030ddbb5b0abf93d22962f4752f3646cd9` |
| golangci/golangci-lint-action | v9.3.0 (golangci-lint v2.15.0) | `ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a` |
| docker/setup-buildx-action | v4.4.1 | `f87e5991a6d7451dcb8d9637bfbc97413f497069` |
| docker/build-push-action | v7.4.0 | `c3c9e263c25d99ce0380d002d59b67737d91b0dc` |
| helm/kind-action | v1.15.1 | `1544676c07bb3570fcb70166beaa07c4528fd4aa` |
| azure/setup-helm | v5.0.1 | `9bc31f4ebc9c6b171d7bfbaa5d006ae7abdb4310` |
| squawk (migration linter) | v2.68.0 (2026-10-08) | `squawk-linux-x64` `sha256:53070672cbe0b8adce55372931d4435cc2395edd8b5736819a82e483b08d4d09` |
| govulncheck | v1.9.0 | module version |
| kubeconform | v0.8.0 | tarball `sha256:9bc2bffbf71f261128533edaf912153948b7ff238f9a531ae6d34466ec287883` |
| actionlint | 1.7.12 | `sha256:b1934ee5f1c509618f2508e6eb47ee0d3520686341fec936f3b79331f9315667` |
| hadolint | v2.15.1 | `sha256:32dac94127fd60b7b7e3fbfc65e1383b9b5e25c9bfd7b8536de7a539fe68a12d` |
| Gitleaks | v8.30.1 | `sha256:c00b6bd0aeb3071cbcb79009cb16a60dd9e0a7c60e2be9ab65d25e6bc8abbb7f` |
| shellcheck, jq, psql, python3 | runner image | preinstalled on `ubuntu-latest` |

PostgreSQL 18.6 itself is older than two weeks; the Docker Official Image digest is a
rebuild of that version (base OS patches), which is how that image is published.
