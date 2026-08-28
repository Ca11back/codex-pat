# Allow schema-1 CPA hosts and release codex-pat

## Goal

Publish a new `codex-pat` release that declares only the schema-1 plugin
contract and accepts CPA hosts from schema 1 onward. This removes the current
false incompatibility with newer CPA hosts while restoring compatibility with
the schema-1 host used by CPA v7.2.102.

## Confirmed facts

- `codex-pat` exposes only `auth_provider` and `management_api`; it does not
  use request-lifecycle, stream-chunk, or WebSocket observer capabilities.
- The current registration constants are plugin schema 2, host minimum 2, and
  host maximum 3 in `internal/plugin/registration.go`.
- CPA host registration sends its host schema as a capability value and rejects
  only a plugin response newer than the host. CPA v7.2.102, v7.2.103,
  v7.2.129, and v7.2.144 use host schemas 1, 2, 3, and 4 respectively.
- CPA's official auth plugin examples return plugin schema 1, including the
  schema-1 host line, so auth-provider registration does not require plugin
  schema 2.
- The repository currently builds against CPA SDK `v7.2.129`, defaults to
  plugin version `0.1.6`, and release verification only smokes CPA v7.2.103
  and v7.2.129. The root module has no other direct requirements; its CPA
  transitive modules are not imported by the plugin package.

## Requirements

1. Change the PAT plugin registration contract to schema 1.
2. Accept lifecycle requests whose host schema is at least 1. Do not reject a
   future host solely because its numeric schema is greater than a local upper
   bound; the plugin must continue to rely only on the stable schema-1
   auth/management subset.
3. Upgrade the direct CPA SDK dependency to the current audited CPA source
   version (`v7.2.144`) and upgrade all modules in the Go module graph to the
   latest versions selected by the Go toolchain, resolving any resulting API
   or behavior changes rather than leaving a partial dependency update.
4. Bump the plugin release version from `0.1.6` to `0.1.7` in source defaults,
   tests, packaging, and release workflow inputs/notes.
5. Update English and Chinese README compatibility, upgrade, build, and
   release-validation text to describe plugin schema 1, host schema 1+
   forward tolerance, the new SDK baseline, and the new release version.
6. Update unit tests and smoke/integration defaults for the new contract. Keep
   the release workflow focused on one current CPA v7.2.144 load/register smoke
   rather than multiplying the native release matrix with historical hosts.

## Out of scope

- Implementing any schema-2-or-newer request lifecycle, stream interception,
  or WebSocket observer capability.
- Changing native ABI exports or credential/auth/management behavior.
- Expanding native release targets beyond the existing four target matrix.
- Adding new runtime features unrelated to schema negotiation or dependency
  compatibility.

## Acceptance Criteria

- [ ] Registration responses advertise schema 1 and exactly the existing two
      capabilities.
- [ ] Lifecycle requests with host schemas 1, 2, 3, 4, and a larger future
      value register successfully in unit tests; schema 0, missing, and
      malformed requests retain their existing error behavior.
- [ ] CPA SDK is updated to `v7.2.144`; an all-module upgrade is attempted and
      any modules needed by the root package are retained, while `go.mod` and
      `go.sum` remain tidy and verified. The full Go test/vet checks pass.
- [ ] Release defaults and both READMEs consistently describe `v0.1.7`, the
      schema-1 plugin contract, and the supported/forward-tolerant host policy.
- [ ] Release validation performs one current CPA v7.2.144 load/register smoke
      per native target and produces the same four native archives/checksum
      contract without changing target support.
- [ ] A current CPA load/register smoke succeeds with the released plugin
      artifact, and the repository is clean after validation.

## Resolved scope decision

- Upgrade all modules with available newer versions, as explicitly requested.
  Use the Go module graph as the source of truth and validate the resulting
  changes with tests, vet, integration smoke, native ABI checks, and CPA host
  registration smoke. Because the root package imports only the CPA SDK
  surface, `go mod tidy` may legitimately remove upgraded CPA transitive
  modules from the root manifest; do not add unused indirect pins solely to
  make the diff larger.
