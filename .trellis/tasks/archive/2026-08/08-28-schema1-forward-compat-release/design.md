# Technical design

## Contract boundary

Keep the three version concepts independent:

- `pluginSchemaVersion` is the contract selected by `codex-pat` in its
  registration response. Set it to `1`, because the plugin uses only the
  stable auth-provider and management-api surface.
- `minHostSchemaVersion` is the lowest host capability required by the
  lifecycle implementation. Set it to `1` so CPA v7.2.102 is supported.
- There is no host upper-bound rejection. The dispatcher accepts any parsed
  `uint32` host schema at or above 1 and ignores capabilities the plugin does
  not advertise. This is an explicit forward-tolerance policy, not an
  assertion that every future CPA behavior is compatible.

The SDK's `pluginabi.SchemaVersion` remains an independent compile-time value
from the upgraded CPA SDK. Native ABI remains v1. No schema-2-or-newer
capability is added.

## Dependency upgrade

Update the direct CPA SDK to `github.com/router-for-me/CLIProxyAPI/v7
v7.2.144`, then run the Go toolchain's all-module upgrade followed by
`go mod tidy`. Review the complete `go.mod`/`go.sum` diff, resolve compilation
or test failures caused by API changes, and retain no stale module pin merely
because it is inconvenient. The Go language/toolchain line stays at the
repository's current 1.26 baseline unless a dependency explicitly requires a
different version. The root module currently imports only the CPA SDK package
surface, so unused CPA transitive modules are expected to be removed again by
`go mod tidy`; the final manifest must not contain artificial indirect pins.

## Release and validation surfaces

- Bump the release identity to `0.1.7` in Makefile defaults, integration and
  release tests, build helper defaults, and workflow dispatch/publish notes.
- Update the README files in both languages as one synchronized compatibility
  contract. Document verified CPA releases v7.2.102, v7.2.103, v7.2.129, and
  v7.2.144 (host schemas 1-4), while describing future-host acceptance as
  forward tolerance over the schema-1 subset rather than verified behavior.
- Keep the release workflow's host validation to one current CPA v7.2.144
  binary per supported platform. Unit tests cover host schema 1/2/3/4 and
  future values; historical CPA binaries are not downloaded in the release
  job. Keep the four native plugin target matrix and Go 1.26.4 CPA toolchain
  verification unchanged unless the official v7.2.144 release proves a
  different recorded toolchain.
- Move the full integration baseline to CPA v7.2.144 and the new plugin
  version, with the previous plugin fixture set to v0.1.6 for the existing
  upgrade/removal path.

## Compatibility and rollback

The change is wire-compatible for existing auth/management calls. A released
artifact can be rolled back by restoring the previous versioned library and
version pin. If the all-module upgrade causes unrelated regressions, revert the
dependency set as one atomic change rather than retaining a partially upgraded
graph.
