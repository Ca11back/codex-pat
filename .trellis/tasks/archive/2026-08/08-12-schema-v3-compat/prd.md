# Support CPA plugin schema v3

## Goal

Keep `codex-pat` loadable and operational on CLIProxyAPI (CPA) v7.2.129 while
preserving support for the existing schema-v2 CPA range. Build against the
current v7.2.129 plugin SDK, but advertise only the RPC schema contract the PAT
plugin actually requires.

## Background

- CPA v7.2.129 raises the host plugin RPC schema from 2 to 3. Schema v3 changes
  only `response.intercept_stream_chunk`: payload chunks omit
  `OriginalRequest` and `RequestBody`, while the header-init chunk retains them.
- `codex-pat` advertises only `auth_provider` and `management_api`; it does not
  implement a stream-chunk interceptor and receives no schema-v3-specific
  payload.
- The CPA host sends its current schema in `plugin.register` and accepts a
  plugin registration whose returned schema is not newer than the host. The
  host records the plugin-returned schema and applies schema-specific behavior
  per plugin.
- CPA v7.2.129 explicitly preserves legacy plugin schemas. Its current official
  auth and management examples still return schema 1, and its request-lifecycle
  example treats the host request as a minimum-capability check rather than
  mirroring the host version.
- The official Linux amd64 binaries for CPA v7.2.103 and v7.2.129 were both
  built with Go 1.26.4, so one Go `c-shared` release artifact can be validated
  against both baselines without a toolchain mismatch.

## Requirements

- Upgrade `github.com/router-for-me/CLIProxyAPI/v7` from v7.2.103 to v7.2.129
  so the plugin is compiled and tested against the current official SDK.
- Separate the PAT plugin's supported RPC schema from the SDK's latest
  `pluginabi.SchemaVersion` constant.
- Continue to return schema 2 from plugin registration and reconfiguration.
  Schema 2 is the plugin's declared contract; it is not the SDK dependency
  version and does not claim that the host itself is schema 2.
- Accept lifecycle requests only from the currently verified host schemas 2
  and 3. Reject missing/zero, schema 1, and unknown schema 4 or newer. Do not
  implement request-version mirroring or return the host's schema version.
- Keep the advertised capability set exactly `auth_provider` and
  `management_api`; do not add stream interception or request lifecycle
  capabilities.
- Preserve PAT import, validation, persistence, management routes, host
  callbacks, and native Codex execution behavior.
- Update compatibility documentation, release metadata, tests, and the CPA
  native-plugin Trellis contract to describe the SDK/schema distinction and
  the verified CPA range.
- Do not edit the CPA source checkout and do not publish or tag a release.

## Acceptance Criteria

- [x] The module graph uses released CPA SDK v7.2.129 without a local
      `replace` directive.
- [x] Lifecycle registration and reconfiguration accept host schema 2 and 3,
      return plugin schema 2, and reject host schema 0/1/4+ or malformed JSON.
- [x] Tests prove the code does not accidentally derive the PAT plugin schema
      from `pluginabi.SchemaVersion` after the SDK constant becomes 3.
- [x] The plugin advertises only `auth_provider` and `management_api`.
- [x] Unit tests, formatting, vet, module verification, native shared-library
      build, and ABI inspection pass.
- [x] The same built Linux amd64 plugin artifact loads, registers, and completes
      its host callback against exact official CPA v7.2.103 and v7.2.129
      binaries or equivalent clean exact-tag builds using Go 1.26.4.
- [x] English and Chinese documentation accurately state that the plugin is
      built with the v7.2.129 SDK but intentionally declares RPC schema 2 for
      compatibility with CPA schema-v2 and schema-v3 hosts.
- [x] No PAT business behavior, credential format, capability, CPA source, tag,
      or published release is changed.

## Verification Record

- `make check` passed after the final implementation diff, including formatting,
  all Go unit tests, Python release tests, vet, module verification, native
  Linux amd64 `c-shared` build, and ABI inspection.
- One final `codex-pat v0.1.6` Linux amd64 artifact was built with official Go
  1.26.4 against CPA SDK v7.2.129 and passed ABI inspection.
- That exact artifact loaded, registered, and completed `host.auth.list` against
  the official CPA v7.2.103 and v7.2.129 Linux amd64 binaries; both binaries
  were independently confirmed as Go 1.26.4 builds.
- Full integration passed against the exact clean CPA v7.2.129 source tag at
  commit `934da2379d6272a704953a02322b666b2a2efa3e` using Go 1.26.4:
  `ok oaipat/integration 32.173s`.

## Out of Scope

- Adopting schema-v3 stream interception behavior.
- Returning schema 3 merely because the compile-time SDK exposes schema 3.
- Restoring schema-v1 support for CPA v7.2.102.
- Guaranteeing compatibility with unknown future schemas; schema 4 or newer is
  rejected until its host behavior is audited and explicitly added to the
  verified range.
