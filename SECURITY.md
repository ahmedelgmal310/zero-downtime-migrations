# Security Policy

## Supported versions

This is a reference implementation. Only the latest commit on `main` receives fixes.

## Reporting a vulnerability

Please report vulnerabilities privately through
[GitHub Security Advisories](https://github.com/ahmedelgmal310/zero-downtime-migrations/security/advisories/new).
Do not open a public issue. You can expect an acknowledgement within 72 hours.

## Controls in this repository

- No credentials are stored in the repository. The e2e generates a random PostgreSQL
  password per CI run (`openssl rand`), stores it only in a Kubernetes Secret, and masks it
  in the job log. The Go integration tests use a throwaway service container that is
  reachable only from its own job.
- Every GitHub Action is pinned to a full commit SHA; base, tool and test images are
  pinned by digest; downloaded binaries (squawk, kubeconform) are verified against a
  recorded SHA-256. See [docs/versions.md](docs/versions.md).
- Workflows run with `contents: read` only.
- The image is distroless, runs as UID 65532 with a read-only root filesystem, no
  capabilities and the `RuntimeDefault` seccomp profile. Service account tokens are not
  mounted.
- CI gates: Gitleaks (full history), govulncheck, golangci-lint (gosec), hadolint,
  shellcheck, actionlint, and the squawk migration linter, which blocks schema changes
  that would take long locks or break running code.
