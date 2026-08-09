# Security and Repository Rules

This repository is public. Treat every committed byte and every CI log as public.

## Primary security objective

Protect the user's local Windows machine and accounts by minimizing write scope, credential exposure, automatic deletion, and background behavior.

## Runtime write boundary

The program may persist files only under `./temp/` beside `BaiduDriveMover.exe`.

Allowed examples:

```text
temp/state.db
temp/config.json
temp/chrome-profile/
temp/auth/
temp/cache/
temp/logs/
temp/tasks/
temp/tools/
temp/rclone-cache/
temp/rclone-tmp/
```

Forbidden persistent locations/actions:

- `%APPDATA%`
- `%LOCALAPPDATA%`
- `%USERPROFILE%` outside the application folder
- Windows registry writes
- Windows services
- Windows scheduled tasks
- startup entries
- hidden background daemons
- system-wide browser settings
- modification of the user's normal Chrome profile

If a dependency normally writes elsewhere, it must be explicitly redirected into `./temp/`. If this cannot be guaranteed, do not use that dependency in production.

## Local credentials

Local plaintext storage inside `./temp/` is acceptable for this personal tool when operationally simpler. Encryption is not a product requirement.

However:

- credentials must never be committed;
- credentials must never be printed to console/logs;
- credentials must never appear in crash diagnostics sent anywhere;
- there is no telemetry or project-owned server;
- CI must use fake credentials only.

Sensitive examples:

- Baidu cookies (`BDUSS`, `STOKEN`, etc.)
- extraction codes tied to private shares
- Google OAuth tokens
- rclone configuration containing tokens
- browser profile data
- task databases/manifests from private shares
- file lists/path names from private shares when not intentionally published

## Public repository rule

Never commit:

- `temp/`
- `.env*`
- real `rclone.conf`
- browser profiles/cookies
- SQLite runtime databases
- logs
- real downloaded files
- private share manifests
- screenshots containing account information
- copied HTTP request/response dumps containing cookies/tokens

Tests must use synthetic fixtures with invented names and IDs.

## Logging/redaction

Logs may include:

- task ID
- stage
- counts
- sizes
- retry numbers
- sanitized error categories

Logs must redact or omit:

- `Cookie` / `Set-Cookie`
- OAuth access/refresh tokens
- Authorization headers
- BDUSS/STOKEN
- browser storage contents
- extraction code where avoidable
- signed/private download URLs
- rclone configuration contents
- OAuth callback/query data

Redaction must happen before formatting a value into normal logs, not only at display time.

## Process execution

Subprocesses are allowed only when required by a documented component (for example Chrome or the pinned rclone helper).

Rules:

- no shell-string concatenation with untrusted filenames/URLs;
- use argument arrays/direct process execution;
- all dependency config/cache/temp paths must be forced under `./temp/`;
- do not alter global PATH permanently;
- child processes must be tracked and shut down cleanly when tool-owned;
- never execute content downloaded from Baidu or Drive;
- captured child stdout/stderr must be bounded before retaining it in memory or logs;
- commands capable of printing raw credentials/configuration are forbidden from normal production flows.

## Browser safety

- Use a dedicated tool-owned Chrome profile under `temp/chrome-profile/`.
- Never attach to or parse the user's primary Chrome data directory.
- Browser automation may navigate only to the required Baidu/Google authentication/service pages.
- Do not install browser extensions.
- Do not change Chrome policies or registry keys.

## Pinned helper supply-chain safety

The v0.5 Google Drive helper is pinned to the exact rclone release and archive hash documented in `V05_GOOGLE_DRIVE.md`.

Requirements:

1. download only from the documented official rclone download origin;
2. verify the complete archive SHA-256 before extraction or execution;
3. reject a mismatched hash without trying to inspect or run the contained executable;
4. reject malformed archives and zip-slip/path-traversal entries;
5. extract only the exact expected `rclone.exe` entry into `temp/tools/rclone/`;
6. verify the helper-reported version before normal use;
7. never search `%PATH%` or arbitrary machine locations for a substitute rclone binary;
8. never auto-upgrade to `latest`;
9. normal CI uses a fake process runner rather than downloading or executing an untrusted remote binary.

A failed helper verification is a hard stop, not a warning.

## Google Drive least-privilege boundary

Google Drive access in v0.5 uses the explicit OAuth scope:

```text
drive.file
```

Do not widen this scope by relying on rclone defaults.

The tool creates a new Drive task root itself. Once its folder ID is observed and persisted, every subsequent task command must be scoped with that exact `--drive-root-folder-id`.

Security invariants:

- no arbitrary browsing of unrelated existing Drive files;
- no pre-existing user folder as the normal destination;
- no task path may address an object above the persisted task root;
- no Drive `delete`, `purge`, `sync`, trash-emptying, or cleanup operation exists in the v0.5 production path;
- duplicate-name reconciliation fails closed rather than deleting or guessing;
- a Drive object cannot be adopted as tool-owned without durable task provenance and matching destination evidence;
- root-folder ID scoping is defense in depth; logical path validation is still mandatory.

Forbidden diagnostic commands in production include any rclone configuration display/dump operation that can expose OAuth token material.

## rclone runtime isolation

Every rclone invocation must explicitly receive application-controlled paths for:

```text
--config
temp/auth/rclone.conf

--cache-dir
temp/rclone-cache/

--temp-dir
temp/rclone-tmp/
```

The actual arguments are absolute paths proven to be inside the runtime root. On Windows the child `TMP` and `TEMP` variables must also point to `temp/rclone-tmp/`.

There is no permissive fallback if containment cannot be proved.

The process launcher must centralize these arguments so an individual Drive operation cannot accidentally omit them.

## Drive verification and cleanup safety

An rclone upload exit code is not proof that a file is complete.

Before `DRIVE_VERIFIED`, independently observed Drive evidence must include:

- one unambiguous regular-file object;
- non-empty object ID;
- exact byte size;
- MD5 matching the hash computed from the verified local cache object.

Missing, ambiguous, or contradictory evidence leaves the file incomplete.

In v0.5, even `DRIVE_VERIFIED` does not trigger deletion. Baidu/local cleanup remains disabled until the later pipeline milestone adds its own provenance and recovery gates.

## Deletion safety

Deletion requires provenance.

Automatic deletion is allowed only when the database records that the object/path was created by the tool for the same task and the milestone explicitly enables that cleanup stage.

Baidu:

- staging root must be namespaced under a tool-controlled directory;
- never clear the recycle bin automatically;
- never delete a path outside the task's registered staging subtree.

Local:

- only delete registered objects beneath `./temp/`;
- path canonicalization must prove containment before deletion.

Drive:

- normal completion does not require deleting destination objects;
- never delete unrelated Drive objects;
- v0.5 performs no Drive destination deletion at all.

## Path traversal protection

All filesystem writes must:

1. resolve from the known application/temp root;
2. normalize the path;
3. reject traversal/absolute-path escapes;
4. verify the final path remains inside the allowed root.

Share filenames are data, never trusted local paths.

Drive logical paths are service paths, not local Windows paths. They must be canonical and remain relative to the task root before becoming subprocess arguments.

## Network scope

Expected network destinations are limited to service endpoints required for:

- Baidu Netdisk
- Google OAuth/Drive
- the pinned official rclone download origin when the managed helper must be provisioned
- GitHub only for optional application update checks if such a feature is ever explicitly added

No analytics, telemetry, paste services, or project backend.

## CI safety

CI must not need repository secrets for unit/integration-mock tests.

Live service tests:

- are never required for ordinary pull-request CI;
- must not run for untrusted fork PRs;
- should be manual/local or isolated GitHub environments only if later added;
- must use dedicated test data and least privilege.

The Drive CI model uses fake process runners and synthetic rclone JSON/output. It must test the exact argv/environment safety policy without authenticating a real Google account.

## Security regression requirements

Automated tests must cover:

- runtime path containment;
- traversal attempts (`..`, absolute paths, rooted Windows paths, volume paths, strange separators);
- log redaction;
- cleanup provenance checks;
- state recovery before deletion;
- subprocess argument handling;
- synthetic Windows-invalid source filenames not becoming unsafe local paths;
- rclone archive checksum mismatch and zip-slip rejection;
- mandatory rclone config/cache/temp containment arguments;
- mandatory `drive.file` OAuth scope;
- mandatory task-root ID scoping after Drive root creation;
- duplicate Drive object ambiguity;
- no verification without exact size and MD5 evidence.

## Incident rule

If a real credential is ever committed to this public repository, removing it in a later commit is insufficient. Revoke/rotate it immediately and treat Git history as compromised.
