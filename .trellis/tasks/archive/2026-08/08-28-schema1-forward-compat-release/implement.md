# Implementation plan

1. Load backend/native-plugin specs and inspect the current dependency graph;
   record the exact CPA v7.2.144 SDK and official release assets/toolchain.
2. Upgrade all Go modules with the Go toolchain, then run `go mod tidy` and
   inspect the resulting `go.mod`/`go.sum` diff for API changes.
3. Change registration and lifecycle validation to plugin schema 1, host
   minimum schema 1, and no numeric host upper-bound rejection. Preserve
   malformed/zero request errors and the exact two advertised capabilities.
4. Update unit tests for schema 1/2/3/4 and a larger host value, and update
   SDK-contract assertions for CPA v7.2.144.
5. Update version defaults and integration smoke constants to `0.1.7`, use
   CPA v7.2.144 as the full integration baseline, and keep the v0.1.6 upgrade
   fixture.
6. Simplify the release workflow/downloads/smoke to one current CPA v7.2.144
   host per existing native target. Update release tests and both README files
   in the same change.
7. Run formatting, unit tests, release tests, vet, module verification, native
   ABI checks, and the Linux/amd64 integration smoke. Build or load-smoke the
   current CPA v7.2.144 artifact locally; preserve redacted logs only.
8. Run the full Trellis quality check, update the native-plugin contract spec
   with the new schema-1/minimum-only rule, review the complete diff, and make
   one release-scoped commit.

## Planned validation commands

- `go get -u all`
- `go mod tidy`
- `make check`
- `make integration CPA_SOURCE=../CLIProxyAPI`
- `go run ./cmd/cpa-load-smoke -cpa <CPA-v7.2.144-binary> -plugin <v0.1.7-library> -version 0.1.7`
- `git diff --check`

The release workflow remains the authoritative cross-platform matrix; local
validation should not claim Windows/macOS compatibility from a Linux-only run.
