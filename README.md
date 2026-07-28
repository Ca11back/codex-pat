# Codex PAT for CLIProxyAPI

[简体中文](README.zh-CN.md)

`codex-pat` is a native [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)
(CPA) plugin for importing and managing Codex personal access tokens. It
validates a PAT, stores a CPA-compatible native Codex credential, and leaves
model discovery, scheduling, translation, and execution to CPA.

The plugin is not an OpenAI API-key provider and does not implement a model
executor.

## Compatibility

| Plugin release | Required CPA release | Native ABI | Plugin RPC schema | Status |
|---|---|---:|---:|---|
| `v0.1.4` | `v7.2.102` | 1 | 1 | Final schema-v1 release |
| `v0.1.5` | `v7.2.103` | 1 | 2 | Current supported release |

`codex-pat v0.1.5` is schema-v2-only and does not register on CPA v7.2.102.
CPA v7.2.102 users must remain on `codex-pat v0.1.4`. Schema v2's optional
request-completion and active-request-termination lifecycle is not implemented
or advertised: this plugin remains limited to credential management and exposes
exactly `auth_provider` and `management_api`.

- Release targets: Linux amd64/arm64, macOS arm64 (Apple Silicon only), and
  Windows amd64. A
  platform zip is published only after a matching native runner builds it and
  CPA v7.2.103 loads it, registers it, and completes a host callback.
- Published Linux assets use the manylinux2014 baseline and are rejected if
  they require GLIBC newer than 2.17.

Use a release library built for the same operating system and architecture as
CPA. Compatibility with other CPA versions is not implied by ABI v1 alone.
macOS Intel is unsupported because Go does not support this in-process
Go-host/Go-`c-shared` multi-runtime shape on Darwin amd64.

## Install

Install from the CPA Plugins Store when `codex-pat` is listed there. For a
manual install, download the zip for your platform from the latest GitHub
Release and extract its single library into CPA's plugin directory. Release
archives use these root names:

| Platform | Archive library |
|---|---|
| Linux | `codex-pat.so` |
| macOS | `codex-pat.dylib` |
| Windows | `codex-pat.dll` |

Verify the archive against `checksums.txt`, stop CPA before replacing a loaded
library, and enable the plugin:

```yaml
remote-management:
  allow-remote: false
  secret-key: "replace-with-a-strong-management-key"

plugins:
  enabled: true
  configs:
    codex-pat:
      enabled: true
      store:
        version: "0.1.5"
```

Start CPA and confirm `codex-pat` is registered and effectively enabled in
`GET /v0/management/plugins`. Then open:

```text
http://127.0.0.1:8317/v0/resource/plugins/codex-pat/manage
```

Enter the CPA management key and PAT in the page. Initial status may remain
`pending` briefly while CPA registers the credential and its models.

## Security

- CPA native plugins are trusted in-process code, not sandboxed extensions.
- The PAT is stored as `access_token` in CPA's auth directory. Protect the CPA
  host, config, auth directory, backups, logs, and crash dumps.
- Keep remote management on localhost unless it is protected by a separate
  network security layer.
- Never put a PAT in a URL, command argument, issue, chat, or unredacted log.
- `CODEX_AUTHAPI_BASE_URL`, when set, receives the PAT as bearer authentication;
  use only an operator-controlled HTTP(S) endpoint.
- Disabling the plugin does not delete imported auth files. Delete credentials
  from CPA management before removal when they must no longer be usable.

CPA v7.2.102 and v7.2.103 cannot expose syntactically invalid, unregistered JSON
files to the plugin for collision checks. Do not manually create files in the
`codex-pat-` namespace; move an invalid collision aside before importing.

## Upgrade or remove

For an upgrade, keep a rollback copy outside every CPA plugin discovery
directory, stop CPA, install the new versioned artifact, update `store.version`,
restart, and verify registration plus one model request. Replacing bytes at the
same loaded path does not reload native code. CPA may remove unselected
versioned plugin files after startup, so the rollback copy must remain outside
the discovery tree.

Upgrade CPA v7.2.102 / `codex-pat v0.1.4` to CPA v7.2.103 /
`codex-pat v0.1.5` together. Rolling the plugin back to v0.1.4 also requires
restoring CPA v7.2.102, reinstalling the versioned v0.1.4 artifact, updating the
version pin, and restarting CPA.

For removal, delete plugin-owned credentials through CPA management while the
plugin is enabled, disable it, restart CPA, and remove the library.

## Build and test

Go 1.26, CGO, and a native C toolchain are required. Release artifacts use the
exact Go 1.26.4 toolchain recorded by the official CPA v7.2.103 binaries.

```bash
make check
make integration CPA_SOURCE=../CLIProxyAPI
```

`make check` runs formatting, unit tests, vet, module verification, a native
shared-library build, and ABI checks. `make integration` runs the full fake-PAT
lifecycle against a clean CPA `v7.2.103` source checkout on Linux/amd64; it
does not contact OpenAI or require a real credential.

Release maintainers can use `make package` for the current native target and
`make verify-release` after collecting all four native-runner archives.

## Support

Report reproducible bugs through GitHub Issues without credentials, auth-file
contents, management keys, complete account IDs, or unredacted logs. Include
the CPA version, plugin version, operating system, architecture, and redacted
error details.

Licensed under the [MIT License](LICENSE).
