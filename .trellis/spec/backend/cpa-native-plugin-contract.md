# CPA Native Plugin Contract

## Scenario: Credential-managing native plugin

### 1. Scope / Trigger

Use this contract when implementing or changing a CLIProxyAPI (CPA) native
dynamic-library plugin that:

- calls CPA host callbacks;
- registers management routes or resources;
- parses or persists authentication files;
- handles credentials or other bounded upstream responses.

The current verified baseline is `codex-pat v0.1.6`, built with the CPA
`v7.2.129` SDK, native ABI v1, and plugin RPC schema v2. The same artifact is
verified on CPA host schema v2 (`v7.2.103`) and host schema v3 (`v7.2.129`).
`codex-pat v0.1.4` remains the final release for CPA `v7.2.102` / schema v1;
newer releases intentionally do not negotiate or fall back to schema v1.

### 2. Signatures

Required exported C ABI entry points:

```c
int cliproxy_plugin_init(const cliproxy_host_api *host,
                         cliproxy_plugin_api *plugin);
int cliproxyPluginCall(char *method, uint8_t *request, size_t request_len,
                       cliproxy_buffer *response);
void cliproxyPluginFree(void *ptr, size_t len);
void cliproxyPluginShutdown(void);
```

Release builds that CPA loads in-process use these verification/build shapes:

```text
go version -m <official-cpa-binary>
go build -trimpath -buildvcs=false -buildmode=c-shared \
  -ldflags "-s -w -X main.version=<version>" ...
```

Credential-management host methods used by this project:

```text
host.http.do_stream
host.http.stream_read
host.http.stream_close
host.auth.list
host.auth.get
host.auth.get_runtime
host.auth.save
```

Plugin management routes are exact routes under `/v0/management/`; browser
resources are exact routes under `/v0/resource/plugins/<plugin-id>/`.

CPA renders a plugin resource menu inside a same-origin iframe. The HTML
resource response must therefore allow that host explicitly:

```text
Content-Security-Policy: ...; frame-ancestors 'self'
```

### 3. Contracts

#### Registration

- Set the native ABI to `pluginabi.ABIVersion`. Keep the PAT plugin RPC schema
  in an explicit plugin-owned constant; do not derive it from the latest SDK's
  `pluginabi.SchemaVersion`.
- Treat lifecycle request `schema_version` as the host schema being offered and
  registration response `schema_version` as the plugin contract selected for
  that registration. They are not required to be equal.
- For v0.1.6, accept only the audited host schemas 2 and 3, and return plugin
  schema 2 for both. Reject schema v1, missing/zero schema, malformed JSON, and
  unknown schema v4 or newer rather than silently claiming compatibility.
- Updating the SDK does not itself require increasing the plugin schema. Raise
  the plugin schema only when the plugin adopts behavior introduced by the new
  RPC contract. Expand the accepted host set only after auditing and testing
  that host schema.
- `Metadata.Name`, `Version`, `Author`, and `GitHubRepository` must all be
  non-empty. CPA rejects registration otherwise, even if the shared library
  loaded successfully.
- Advertise only capabilities the dispatcher actually implements.
- Schema v2 request-lifecycle completion/termination is optional. This
  credential-management plugin must continue to advertise only
  `auth_provider` and `management_api`; do not add request interception or
  lifecycle ownership without a separate product requirement.
- The library basename determines the runtime plugin ID. A file named
  `codex-pat-v0.1.3.so` resolves to plugin ID `codex-pat` and version `0.1.3`.

#### Versioned upgrades

- When an integration test claims compatibility with an exact CPA release,
  require both `git describe --tags --exact-match HEAD` to equal that release
  and `git status --porcelain` to be empty before building CPA. An exact tag
  identifies `HEAD`; it does not prove the worktree has no source modifications.
- When the selected filesystem path is unchanged, CPA reuses the loaded native
  library and only reconfigures it; replacing bytes at that path does not load
  new code into the running process.
- Install an upgrade under a new `-v<version>` filename, update
  `plugins.configs.<id>.store.version`, and restart CPA for a deterministic
  clean load of the selected path.
- On the first successful plugin load in a new CPA process, the verified CPA
  v7.2.102-v7.2.103 behavior removes unselected versioned files for the same
  plugin ID from its discovery directories. Keep rollback artifacts outside
  the discovery tree, then reinstall the prior filename and version pin before
  a rollback restart.

#### Release toolchain compatibility

- The exact v0.1.6 release set is Linux amd64, Linux arm64, Darwin arm64, and
  Windows amd64. Darwin/amd64 is unsupported for this Go `c-shared` plugin and
  must be rejected by build/release validation rather than emitted as an
  untested asset. This is a project support boundary, not a claim that CPA or
  non-Go dylibs do not support Intel macOS.
- A Go `c-shared` plugin is loaded into CPA's Go process and crosses cgo/runtime
  boundaries. Pin the release workflow to the exact Go patch version recorded
  by `go version -m` on the official CPA binary for the supported CPA release.
  Do not infer the toolchain from `go.mod`, float to the latest patch, or accept
  a passing architecture as proof for another architecture.
- When one artifact claims compatibility with multiple CPA releases, inspect
  every supported official CPA binary with `go version -m`. The verified
  v7.2.103 and v7.2.129 Linux amd64 binaries both use Go 1.26.4; a future
  mismatch requires separate release artifacts or a narrower support claim.
- The same pinned `GOROOT` must build every native asset, including Linux
  assets built inside manylinux containers.
- A bind-mounted manylinux source tree may not expose Git metadata in a form Go
  accepts for automatic VCS stamping. Build release libraries with
  `-buildvcs=false`; the explicit version linker flag is the release identity.
  Do not make container ownership or Git safe-directory state part of the
  binary's reproducibility contract.
- On Windows, Go's external linker emits a module name from the requested DLL
  basename into `export_file.def`. Build through an identifier-safe basename
  such as `codex_pat.dll`, then rename the completed DLL to the versioned
  artifact path before ABI inspection and packaging. The inspected and packaged
  file must still use the normal versioned release path and zip-root name.

#### Initialization and host-table lifetime

- Treat `cliproxy_plugin_init` as the minimal first entry into a Go `c-shared`
  runtime loaded by a Go host. It must not call back into C and must not eagerly
  construct the dispatcher. Validate `abi_version`, `call`, and `free_buffer`
  by reading the C-owned host table directly, publish the plugin function table,
  retain the host pointer in synchronized state, and return.
- Initialize the dispatcher lazily on the first actual plugin call. Registration
  must still work on that first call without requiring a host callback.
- CPA owns the host table and keeps it alive until plugin shutdown returns.
  Hold a host-state read lock from selecting the pointer through the host call,
  response copy, and `host.free_buffer`; shutdown must clear the pointer under
  the matching write lock so it cannot return while a callback still uses the
  table or a host-owned response buffer.
- Never copy host-owned callback buffers into retained Go state or free them
  with the plugin allocator. Copy the response while the host table is pinned,
  then release it exactly once through that same table's `free_buffer` callback.

#### Browser resources

- Keep plugin HTML resources restrictive with `default-src 'none'` and
  explicit same-origin allowances for only the assets and management requests
  they use.
- Set `frame-ancestors 'self'` because CPA embeds the resource page in a
  same-origin iframe.
- Do not use `frame-ancestors 'none'`: the route still returns HTTP 200, but the
  browser blocks the document before its CSS and JavaScript subresources load.
- Do not remove `frame-ancestors` or broaden it to `*`; the management center is
  the only intended embedding context.

#### Native memory ownership

- A side frees only buffers allocated by that side.
- Plugin responses allocated with `C.CBytes` are released through the plugin
  `free_buffer` function.
- Host callback buffers are released through `host.free_buffer`.
- Do not pass Go interfaces, maps, slices, contexts, or errors across the C ABI.
- Recover panics before returning across the ABI boundary.

#### Host callback context and response bounds

- Propagate `host_callback_id` from `management.handle` into outbound host
  callbacks so CPA restores request cancellation, proxy, transport, and logging
  context.
- Do not use `host.http.do` for credential endpoints whose response must be
  bounded: CPA fully reads that body before returning it to the plugin.
- Use `host.http.do_stream`, read chunks up to an explicit byte limit, and close
  the stream on success, error, cancellation, and overflow.

#### Auth persistence

- Persist through `host.auth.save` so CPA owns auth-directory resolution and
  runtime registration.
- Give new records a stable provider/plugin namespace followed by an immutable
  principal identity component, then optional bounded human-readable metadata.
  For this plugin the canonical shape is
  `codex-pat-<24-hex-principal-hash>-<optional-email>-<optional-plan>.json`;
  `pat` stays immediately after `codex`, and the hash covers the normalized
  ChatGPT user ID plus workspace/account ID.
- Resolve replacement targets by decoding the persisted user/workspace pair,
  not by reconstructing a filename from mutable email or plan metadata.
  Preserve a matching canonical physical filename so CPA's path-derived auth
  index remains stable and the update does not create a duplicate.
- Require both the persisted discriminator and the strict plugin filename
  grammar before mutating an existing record. Parser ownership alone must not
  authorize overwriting another plugin's filename.
- For strict plugin filenames, verify the principal hash against both persisted
  identity fields; fail closed on mismatches.
- Apply that strict namespace to management status, revalidation, and deletion
  links as well; runtime parser compatibility must not grant UI management
  ownership over another plugin's file.
- Before saving a new canonical target, inspect any host-visible occupied
  filename and fail closed rather than overwrite native/OAuth, third-party,
  malformed plugin-marked, or different-principal data.
- CPA v7.2.102-v7.2.103 auth callbacks cannot preflight a syntactically invalid
  disk file that was not registered in the auth manager. Document this
  limitation and do not claim no-clobber protection for such a path without a
  host stat/create callback.
- Treat the returned physical path as untrusted until its basename, regular-file
  type, and non-symlink status are verified.
- Treat the final credential rewrite as one atomic file-identity operation, not
  as a path check followed by a later path-based write. Create a new private
  regular file with mode `0600` relative to a pinned parent-directory handle,
  write the exact final bytes, `Sync`, close, and atomically replace only the
  expected basename. After replacement, do not reopen, truncate, write, inspect,
  or `Chmod` the destination by path.
- On Windows, use Go 1.26 `os.Root` operations for the private
  `O_CREATE|O_EXCL` temporary file and final `Root.Rename`. An existing or raced
  symlink, junction, or reparse-point destination must be replaced as a
  directory entry, never followed. On Unix, the existing `0600` temporary file
  mode survives atomic rename; a post-rename path-based `Chmod` is both
  unnecessary and unsafe.
- Request mode `0600` when creating the Windows temporary file, but do not use
  `FileMode.Perm()` as a Windows security assertion: Go exposes Windows files
  with synthetic permission bits. Assert a regular non-symlink result and prove
  no-follow replacement with the native adversarial reparse test. Unix tests
  must continue to require an exact `0600` result.
- CPA watcher/token-store work can persist an older auth version after a newer
  management mutation. Save a bearer-free PAT-shaped staging payload, wait until
  runtime reports that staging file as disabled, then atomically write the final
  authoritative JSON with Unix mode `0600`.
- Before returning, require final runtime disabled state and update time to
  agree with the final file, and require the file to remain content-equivalent
  through a settling window. Repair a stale overwrite and repeat the check.

#### Parser and refresh isolation

- A parser sharing a native provider identifier must use a strict persisted
  discriminator and return `Handled: false` for native credentials it does not
  own.
- Treat the persisted discriminator as primary ownership. Use a strict
  canonical filename grammar only when undecodable JSON in the plugin namespace
  must fail closed; a valid non-PAT or non-provider record is still declined
  even when its filename resembles the plugin namespace.
- A malformed plugin-owned file must remain handled and disabled so it cannot
  fall through as active generic auth.
- `auth.parse` in CPA v7.2.102-v7.2.103 has no callback ID; keep it offline.
- `auth.refresh` has no handled flag. Pass through ordinary provider storage
  unchanged; never disable an unrelated native/OAuth credential because it is
  not plugin-owned.

#### Readiness

- `host.auth.save` immediately exposes a generic runtime record before watcher
  parsing and model registration necessarily complete.
- Runtime existence alone is not readiness.
- For file-backed plugin auth, require a runtime update at or after the final
  auth-file modification time and a short settling interval. Integration tests
  must additionally verify the native model surface before declaring the
  credential operational.

#### Environment

```text
CODEX_AUTHAPI_BASE_URL=<operator-controlled base path>
```

Environment values that receive bearer credentials must be operator-controlled,
must use HTTP(S), and must never be logged with the credential.

### 4. Validation & Error Matrix

| Condition | Required behavior |
|---|---|
| ABI mismatch or incomplete function table | Reject plugin initialization |
| Lifecycle request uses audited host schema v2 or v3 | Return plugin schema-v2 registration metadata with exactly `auth_provider` and `management_api` |
| Lifecycle request uses schema v1, zero/missing schema, or unknown schema v4+ | Return HTTP-400 `unsupported_schema`; do not register or reconfigure |
| Lifecycle request JSON is malformed | Return HTTP-400 `invalid_request` with a stable redacted message |
| Schema-v2 optional request-lifecycle capability is not implemented | Omit `request_lifecycle_plugin`; CPA must not dispatch `request.complete` to this plugin |
| Plugin Go patch version differs from the supported CPA release binary | Reject the release candidate; rebuild every asset with CPA's exact recorded toolchain |
| Requested release target is Darwin/amd64 or otherwise outside the exact four-target set | Reject before compilation and exclude it from archives, checksums, and public support claims |
| Bind-mounted manylinux build cannot obtain VCS status | Build with `-buildvcs=false`; do not weaken Git ownership globally inside the image |
| First `cliproxy_plugin_init` entry performs a nested Go-to-C call or eagerly constructs the dispatcher | Reject the release candidate; keep init to direct table validation/storage and defer dispatcher construction to the first plugin call |
| Shutdown races an active host callback | The host-state write lock waits for callback, copy, and host-buffer free to finish before clearing the host pointer and returning |
| Windows external linker rejects the versioned DLL basename in `export_file.def` | Build with an identifier-safe temporary DLL basename, then rename before inspection and packaging |
| CPA integration source has the expected exact tag but a dirty worktree | Reject the test target before building |
| Missing required registration metadata | Registration test must fail |
| Missing management authentication | CPA returns 401 before plugin handler |
| Resource HTML uses `frame-ancestors 'none'` | Same-origin management iframe is blocked despite an HTTP 200 response |
| Upstream response exceeds configured bound | Close stream; return redacted bounded-response error |
| Invalid credential input or authoritative 401/403 | Do not create active auth; disable existing auth during revalidation |
| Network, 429, or upstream 5xx | Preserve last valid file; return retryable error |
| User or workspace identity changes during revalidation | Do not rebind; disable the existing record |
| Malformed owned auth file | `Handled: true`, `Disabled: true`, no usable bearer |
| Ordinary native/OAuth auth file | Parser declines; refresh passes through unchanged |
| Existing same-principal canonical file | Preserve its physical filename and replace in place |
| Same workspace, different user | Create a distinct canonical file and auth index |
| Host-visible canonical filename occupied by unowned or different-principal data | Reject the save without modifying the target |
| Syntactically invalid unregistered file occupies the generated path | ABI v1 cannot preflight it; document the limitation and require operator cleanup |
| Delayed watcher writes an older credential version | Repair final JSON and wait for the staging/final runtime barriers before returning |
| Host returns unexpected/symlink/non-regular auth path | Reject secure rewrite and surface redacted persistence error |
| Auth destination becomes a symlink/reparse point after validation | Atomically replace the directory entry from a private temporary file; leave the link target's content and mode unchanged |
| Temporary write, sync, close, or atomic rename fails | Return a wrapped persistence error, clean up the temporary entry, and never fall back to a path-based truncate/write/chmod |
| Plugin disabled | Plugin routes disappear; persisted native-shaped files remain until explicitly deleted |

### 5. Good/Base/Bad Cases

- Good: management import streams a bounded validation response, saves through
  the host, atomically writes `0600`, reports pending until watcher readiness,
  and reuses the native executor.
- Good: v0.1.6, built with the v7.2.129 SDK, receives host schema 2 or 3,
  returns its explicit plugin schema 2, and advertises only auth-provider and
  management capabilities.
- Good: the resource HTML permits only CPA's same-origin management iframe with
  `frame-ancestors 'self'`.
- Good: a versioned upgrade keeps a rollback copy outside plugin discovery,
  selects the new path by version pin, restarts CPA, and verifies the reported
  metadata version.
- Good: release compatibility smoke checks require the expected exact tag and a
  clean CPA worktree before compiling the test binary.
- Good: inspect the official CPA binary with `go version -m`, pin that exact Go
  patch version for all four assets, and pass `-buildvcs=false` in the
  bind-mounted manylinux build.
- Good: aggregate exactly Linux amd64/arm64, Darwin arm64, and Windows amd64;
  reject an unexpected Darwin/amd64 archive even when it compiles.
- Good: init validates the C host table without a nested C call, stores it under
  a lock, publishes the four-entry ABI, and leaves dispatcher construction to
  the first real plugin call.
- Good: a Windows build links `codex_pat.dll`, renames it to the versioned
  artifact path, then inspects and packages that renamed DLL.
- Good: a same-principal import finds both persisted identity fields inside an
  existing canonical file and updates that exact path without creating a
  duplicate.
- Good: a Windows rewrite pins the auth directory with `os.Root`, writes a new
  exclusive `0600` temporary file, then uses `Root.Rename` to replace the
  expected basename without resolving the destination.
- Base: a plugin-owned auth file reloads offline from validated cached metadata.
- Bad: returning `frame-ancestors 'none'` from a resource page that CPA embeds.
- Bad: treating unchanged native ABI v1 as proof that a schema-v1 plugin can
  register on a schema-v2-only release.
- Bad: accepting schema v1 in v0.1.6 or advertising request lifecycle merely
  because the SDK added it.
- Bad: returning `pluginabi.SchemaVersion` merely because the dependency was
  updated; that couples the plugin's claimed behavior to unrelated SDK changes.
- Bad: accepting every host schema `>= 2`; unknown future schemas remain
  unsupported until their compatibility is audited and tested.
- Bad: treating immediate `host.auth.save` runtime registration as proof that
  plan-aware models are ready.
- Bad: using an API-key attribute for an account-scoped bearer credential.
- Bad: returning a disabled auth when `auth.refresh` receives unrelated native
  provider storage.
- Bad: treating an exact CPA tag as sufficient version evidence while building
  uncommitted modifications from that checkout.
- Bad: building a Go `c-shared` plugin with a different Go patch version because
  one native architecture happened to load it successfully.
- Bad: requiring `FileMode.Perm() == 0600` on Windows or treating synthetic
  Windows permission bits as proof against reparse-point following.
- Bad: calling a C compatibility helper or constructing the dispatcher from
  `cliproxy_plugin_init`; both add avoidable work to the first cross-runtime
  callback.
- Bad: copying the host pointer under a lock, releasing the lock, then invoking
  or freeing through it while shutdown may clear and CPA may release the table.
- Bad: treating every `codex-pat-*` filename as owned or overwriting an occupied
  canonical target without decoding its persisted discriminator and principal.
- Bad: treating a runtime timestamp alone as proof that the runtime represents
  the current credential mutation.
- Bad: checking a credential path with `Lstat` and then reopening it with
  `O_TRUNC`, or running `Lstat`/`Chmod` after atomic rename; both create a new
  attacker-raceable path resolution outside the verified file identity.

### 6. Tests Required

- Unit: ABI envelope and ownership behavior, parser isolation, refresh
  pass-through, strict credential validation, canonical filename grammar,
  sanitized optional segments, collision rejection, same-principal filename
  preservation, cross-user workspace isolation, third-party filename isolation,
  stale-watcher repair, secure
  rewrite, symlink rejection, bounded stream reads, and redaction.
- Registration unit: assert ABI 1, SDK schema 3, and independent PAT plugin
  schema 2; host schema-v2 and schema-v3 success for both register and
  reconfigure; response schema 2; exactly two capabilities; stable
  schema-v1/zero/schema-v4 rejection; and stable malformed-JSON rejection.
- Management: every authenticated route must return 401 without a key; static
  resources contain no credential data; mutations are serialized; resource HTML
  must retain every strict CSP directive while allowing only
  `frame-ancestors 'self'`.
- Integration: require each source-based CPA target to have the expected exact
  tag and a clean worktree. Build one shared library, then load/register that
  same artifact on verified CPA v7.2.103/schema-v2 and
  v7.2.129/schema-v3 hosts and complete a real host callback. Continue with the
  full target-CPA lifecycle: mock upstream validation, assert `0600`, replacement/invalidation,
  canonical naming, same-workspace cross-user isolation,
  watcher readiness, a version-pinned restart upgrade from the prior release,
  automatic cleanup of the unselected old artifact, native request headers, no
  OAuth call, plugin-disabled degradation, deletion, and secret-free logs/URLs.
- Native filesystem regression: call the final replacement helper directly with
  a symlink/reparse destination. Assert that the destination becomes a regular
  file containing the exact payload while the original link target's content
  and mode remain unchanged. Run the Windows variant on the native Windows
  release runner; cross-compilation alone does not close the proof. A local
  Windows developer may skip only when the OS reports missing symlink
  privilege, but GitHub Actions must fail rather than turn that missing
  capability into a passing skipped test.
- Artifact: inspect ELF/platform, mode, checksum, dependencies, and required
  exported symbols. Inspect the supported official CPA binary's Go build
  metadata, require the release workflow's exact toolchain pin to match it, and
  verify a `-buildvcs=false` release library contains no `vcs.*` build-info
  fields. Require exactly four archives and matching checksum entries; an extra
  unsupported platform archive is a failure.
- ABI initialization: keep a focused lazy-dispatcher regression, inspect the
  generated init symbol when this boundary changes, and require native CPA
  load/register smoke on every release runner. After registration, call the
  authenticated status route and require a real `host.auth.list` callback to
  return an explicit empty account list without creating a credential file.
  The init call graph must not contain a generated C helper or eager dispatcher
  initialization; the library must retain exactly the four required public
  plugin exports.
- Windows artifact: native-link through the safe temporary basename, rename to
  the versioned path, then run PE export inspection and package the renamed DLL
  as the single root-level `codex-pat.dll` entry.

### 7. Wrong vs Correct

#### Wrong

```go
// This couples the plugin contract to the newest SDK and silently accepts
// unknown future host schemas.
if request.SchemaVersion < pluginabi.SchemaVersion {
    return unsupportedSchema(request.SchemaVersion)
}
registration.SchemaVersion = pluginabi.SchemaVersion

// CPA reads the entire response before this plugin can enforce a limit.
response, err := host.HTTPDo(ctx, callbackID, request)

// A workspace can contain multiple ChatGPT users.
samePrincipal := existing.AccountID == incoming.AccountID

// Runtime existence can be the generic pre-watcher upsert.
ready := host.AuthGetRuntime(ctx, callbackID, authIndex) == nil

// CPA cannot embed this otherwise successful resource response.
csp := "default-src 'none'; frame-ancestors 'none'"

// A separate lookup can follow a reparse point substituted after Lstat.
file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)

// The temp file is already 0600; this adds a post-rename path race.
err = os.Chmod(path, 0o600)

// Windows reports synthetic mode bits; this is not an ACL/reparse proof.
secured := info.Mode().Perm() == 0o600
```

#### Correct

```go
const patPluginSchemaVersion uint32 = 2

switch request.SchemaVersion {
case 2, 3:
    // Both audited hosts can run the PAT plugin's schema-v2 contract.
default:
    return unsupportedSchema(request.SchemaVersion)
}
registration.SchemaVersion = patPluginSchemaVersion

response, err := host.HTTPDoLimited(ctx, callbackID, request, 1<<20)

samePrincipal := existing.AccountID == incoming.AccountID &&
    existing.ChatGPTUserID == incoming.ChatGPTUserID

// Require a post-file-write runtime update; integration also checks models.
ready := !runtime.UpdatedAt.Before(file.ModTime) &&
    time.Since(runtime.UpdatedAt) >= watcherSettleDelay

// Permit only the same-origin CPA management iframe.
csp := "default-src 'none'; frame-ancestors 'self'"

// Write through a pinned directory and replace the destination entry atomically.
root, err := os.OpenRoot(filepath.Dir(path))
temporary, err := root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
// write exact bytes, Sync, and Close
err = root.Rename(tempName, filepath.Base(path))

// On Windows, require a regular result and exercise a real symlink/reparse
// destination natively. On Unix, additionally require mode 0600.
secured := info.Mode().IsRegular()
```
