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

Redaction must happen before formatting a value into normal logs, not only at display time.

## Process execution

Subprocesses are allowed only when required by a documented component (for example Chrome or a bundled helper).

Rules:

- no shell-string concatenation with untrusted filenames/URLs;
- use argument arrays/direct process execution;
- all dependency config/cache paths must be forced under `./temp/` where possible;
- do not alter global PATH permanently;
- child processes must be tracked and shut down cleanly when tool-owned;
- never execute content downloaded from Baidu or Drive.

## Browser safety

- Use a dedicated tool-owned Chrome profile under `temp/chrome-profile/`.
- Never attach to or parse the user's primary Chrome data directory.
- Browser automation may navigate only to the required Baidu/Google authentication/service pages.
- Do not install browser extensions.
- Do not change Chrome policies or registry keys.

## Deletion safety

Deletion requires provenance.

Automatic deletion is allowed only when the database records that the object/path was created by the tool for the same task.

Baidu:

- staging root must be namespaced under a tool-controlled directory;
- never clear the recycle bin automatically;
- never delete a path outside the task's registered staging subtree.

Local:

- only delete registered objects beneath `./temp/`;
- path canonicalization must prove containment before deletion.

Drive:

- normal completion does not require deleting destination objects;
- never delete unrelated Drive objects.

## Path traversal protection

All filesystem writes must:

1. resolve from the known application/temp root;
2. normalize the path;
3. reject traversal/absolute-path escapes;
4. verify the final path remains inside the allowed root.

Share filenames are data, never trusted local paths.

## Network scope

Expected network destinations are limited to service endpoints required for:

- Baidu Netdisk
- Google OAuth/Drive
- GitHub only for optional application update checks if such a feature is ever explicitly added

No analytics, telemetry, paste services, or project backend.

## CI safety

CI must not need repository secrets for unit/integration-mock tests.

Live service tests:

- are never required for ordinary pull-request CI;
- must not run for untrusted fork PRs;
- should be manual/local or isolated GitHub environments only if later added;
- must use dedicated test data and least privilege.

## Security regression requirements

Automated tests must cover:

- runtime path containment;
- traversal attempts (`..`, absolute paths, strange separators);
- log redaction;
- cleanup provenance checks;
- state recovery before deletion;
- subprocess argument handling;
- synthetic Windows-invalid source filenames not becoming unsafe local paths.

## Incident rule

If a real credential is ever committed to this public repository, removing it in a later commit is insufficient. Revoke/rotate it immediately and treat Git history as compromised.
