# CPA schema compatibility evidence

Collected 2026-08-28 from the local CPA checkout at
`/home/ca11back/Downloads/conver/RelayPlane/reference/CLIProxyAPI` and its
official GitHub release metadata.

## Version mapping

| CPA release | Host `pluginabi.SchemaVersion` | Relevant change |
|---|---:|---|
| v7.2.102 | 1 | Original auth/management plugin contract |
| v7.2.103 | 2 | Optional request lifecycle completion/termination |
| v7.2.129 | 3 | Stream-chunk payload request-body omission |
| v7.2.143/v7.2.144 | 4 | Optional upstream WebSocket response observer |

The native ABI remains version 1 across these releases.

## Negotiation behavior

`internal/pluginhost/rpc_client.go` sends the host's current schema in the
lifecycle request and rejects only a plugin response whose schema is newer
than that host. It accepts response schema 1 on hosts 1 through 4. The official
auth and management examples return response schema 1 even in current CPA
trees.

`codex-pat` advertises only `auth_provider` and `management_api`, so none of
the schema-2/3/4 capability-specific behavior is part of its contract. The
plugin should therefore select response schema 1 and validate only that the
host offers schema 1 or newer.

## Release assets

Official releases v7.2.102, v7.2.103, v7.2.129, and v7.2.144 provide the
existing Linux amd64/arm64, Darwin arm64, and Windows amd64 assets used by the
release matrix. CPA v7.2.144's release workflow pins Go 1.26.4, matching the
plugin's current native build toolchain requirement.
