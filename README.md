# codex-pat

A native plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)
(CPA) that imports Codex personal access tokens and manages them as CPA
credentials.

[简体中文](README.zh-CN.md)

## Scope

The plugin owns exactly one thing: the credential. It validates a PAT against
the Codex Auth API, resolves the account identity behind it, and writes a
native Codex credential file that CPA can use. Everything downstream — model
discovery, scheduling, protocol translation, request execution — belongs to
CPA and is unchanged by this plugin.

Consequences worth stating up front:

- It is not an OpenAI API-key provider. Tokens must be Codex PATs beginning
  with `at-`.
- It implements no model executor. If CPA cannot serve a Codex model, the
  plugin is not the cause.
- It exposes two SDK surfaces, `auth_provider` and `management_api`, and
  nothing else.

## Requirements

- A CPA host of schema 1 or newer. See [Version compatibility](#version-compatibility).
- A release library matching CPA's operating system **and** architecture.

| Platform | Library shipped in the release zip |
|---|---|
| Linux amd64, arm64 | `codex-pat.so` |
| macOS amd64, arm64 | `codex-pat.dylib` |
| Windows amd64, arm64 | `codex-pat.dll` |

Intel macOS and Windows ARM64 packages are available starting with v0.1.8.
Linux builds target the manylinux2014 baseline and require GLIBC 2.17 or older
symbols.

## Installation

If `codex-pat` is listed in the CPA Plugins Store, install it from there and
skip to step 3.

1. **Download and verify.** Take the zip for your platform from the latest
   GitHub Release and check it against `checksums.txt`.

2. **Install the library.** Extract the single library from the zip into CPA's
   plugin directory. Stop CPA first if it has already loaded a library at that
   path.

3. **Configure CPA.**

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
           version: "0.1.8"
   ```

4. **Verify registration.** Start CPA and confirm that `codex-pat` appears in
   `GET /v0/management/plugins` as registered and effectively enabled.

5. **Import a token.** Open the management page, supply the CPA management key,
   and paste the PAT:

   ```text
   http://127.0.0.1:8317/v0/resource/plugins/codex-pat/manage
   ```

## Managing credentials

The management page lists every credential this plugin owns and supports three
operations: importing a new PAT, reading current status, and revalidating an
existing credential against the Codex Auth API.

Multiple accounts are supported. Each credential is keyed by the account
identity derived from its token, so importing a PAT for an account that already
has one replaces that credential rather than creating a duplicate. Importing a
token whose identity conflicts with an existing credential in a way the plugin
cannot reconcile is rejected as `account_mismatch`.

A credential's reported state reflects CPA's view of the auth file, not the
token. Import returns `pending` by design, because CPA has not yet registered
the file and discovered its models. If a credential stays `pending` instead of
becoming `ready`, look at the host: the plugin's work finished when the file
was written.

### Validation requires network access

Import and revalidate each make one HTTPS request to the Codex Auth API
(`auth.openai.com/api/accounts`, overridable via `CODEX_AUTHAPI_BASE_URL`) to
confirm the token and read back the account ID, plan type, and email that go
into the credential file. So these operations can fail for reasons unrelated to
your token — the endpoint may rate-limit or be temporarily unavailable, in
which case the error is marked retryable and the import can simply be repeated.

This is the only outbound traffic the plugin generates. Rate limiting on actual
model requests is CPA's concern and never reaches the plugin.

Error responses carry `{"error": {"code", "message", "retryable"}}` and are
redacted by construction: no error path includes the token or an upstream
response body.

### HTTP endpoints

The management page is a client of these routes. They accept the CPA management
key and can be driven directly.

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/v0/management/plugins/codex-pat/import` | Import a PAT. Body: `{"pat": "at-..."}` |
| `GET` | `/v0/management/plugins/codex-pat/status` | List owned credentials and their state |
| `POST` | `/v0/management/plugins/codex-pat/revalidate` | Revalidate one credential. Body: `{"auth_index": "..."}` |

## Operational and security notes

**Trust model.** CPA native plugins are in-process trusted code, not sandboxed
extensions. Installing one grants it everything CPA has.

**Where the token lives.** The PAT is stored as `access_token` in a
`codex-pat-`-prefixed file in CPA's auth directory. Anything that can read that
directory can read the token, which extends to configuration, backups, logs,
and crash dumps of the host.

**Handling.** Never place a PAT in a URL, a command argument, an issue, a chat
message, or an unredacted log. Keep remote management bound to localhost unless
a separate network security layer protects it.

**`CODEX_AUTHAPI_BASE_URL`.** When set, validation requests go to that endpoint
with the PAT as bearer authentication. Point it only at an endpoint you
operate.

**Disabling is not revocation.** Disabling the plugin leaves imported auth
files in place and usable. Delete credentials through CPA management before
removing the plugin if they must stop working.

**Namespace collisions.** For files CPA's auth manager has not registered, auth
callbacks cannot expose syntactically invalid JSON to the plugin, so the plugin
cannot detect that class of collision (verified on CPA v7.2.102 and v7.2.103).
Do not hand-create files under the `codex-pat-` prefix, and move an invalid one
aside before importing.

## Version compatibility

| Plugin | CPA versions verified | Host schema | Schema declared |
|---|---|---:|---:|
| `v0.1.8` (current) | `v7.2.144` on all six platforms | 4 | 1 |
| `v0.1.7` | `v7.2.102`, `v7.2.103`, `v7.2.129`, `v7.2.144` | 1–4 | 1 |
| `v0.1.6` | `v7.2.103`, `v7.2.129` | 2, 3 | 2 |
| `v0.1.5` | `v7.2.103` | 2 | 2 |
| `v0.1.4` | `v7.2.102` | 1 | 1 |

v0.1.7 and v0.1.8 build against the CPA v7.2.144 SDK and declare schema 1.
Because the plugin uses only `auth_provider` and
`management_api` — unchanged since schema 1 — it accepts any host at schema 1 or
above and returns a schema-1 contract. Hosts newer than v7.2.144 are
forward-tolerated through that same subset, but are unverified and gain no new
capabilities from it.

The optional schema-2 lifecycle (request completion, active-request
termination) is neither implemented nor advertised.

A platform's zip is published only after a native runner builds it and a real
CPA v7.2.144 host loads it, registers it, and completes a host callback. Older
hosts are covered by the compatibility audit and unit contract tests, not by
the release smoke test. Sharing ABI v1 with some other CPA build does not imply
compatibility.

## Upgrade, rollback, removal

Two host behaviors govern this procedure: overwriting bytes at an
already-loaded path does not reload native code, and CPA may delete unselected
versioned plugin files at startup.

**Upgrade**

1. Copy the current artifact outside every CPA plugin discovery directory. This
   is the rollback copy, and it must live outside the discovery tree to survive
   startup cleanup.
2. Stop CPA.
3. Install the new versioned artifact and update `store.version`.
4. Restart, confirm registration, and issue one model request.

From v0.1.6, upgrading to v0.1.7 requires no CPA change. On CPA v7.2.102 you
can upgrade directly from v0.1.4 to v0.1.7; the schema-1 contract also runs on
every newer verified host.

**Rollback to v0.1.6** requires CPA v7.2.103 or newer. Reinstall the versioned
v0.1.6 artifact, restore the version pin, and restart.

**Removal.** Delete plugin-owned credentials through CPA management *while the
plugin is still enabled*, then disable it, restart CPA, and delete the library.

## Development

Requires Go 1.26, CGO, and a native C toolchain. Release artifacts pin the
exact Go 1.26.4 toolchain recorded in the official CPA v7.2.144 binary.

```bash
make check
```

Runs formatting, unit tests, release tests, vet, module verification, a native
shared-library build, and ABI checks. Registration tests cover host schemas 1
through 4 plus a larger hypothetical value, while still rejecting malformed and
zero values.

```bash
make integration CPA_SOURCE=../CLIProxyAPI
```

Runs the full lifecycle with a fake PAT against a clean CPA v7.2.144 source
checkout on Linux/amd64. It contacts neither OpenAI nor any real account.

Release maintainers additionally use `make package` for the current native
target and `make verify-release` once all four native-runner archives are
collected.

## Reporting issues

File reproducible bugs on GitHub Issues with the CPA version, plugin version,
operating system, architecture, and redacted error output. Do not attach
credentials, auth-file contents, management keys, full account IDs, or raw
logs.

Licensed under the [MIT License](LICENSE).
