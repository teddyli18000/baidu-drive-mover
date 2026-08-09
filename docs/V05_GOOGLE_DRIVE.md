# v0.5 Google Drive Design

Status: **design frozen before implementation**.

This document defines the v0.5.0 Google Drive milestone. Implementation must not widen these permissions or safety boundaries without updating this document first.

## 1. Milestone goal

v0.5 takes files already proven `LOCAL_READY` by v0.4 and creates a verified copy in Google Drive while reconstructing the logical directory tree.

The v0.5 lifecycle is:

```text
LOCAL_READY
  -> DRIVE_UPLOADING
  -> DRIVE_UPLOADED
  -> DRIVE_VERIFIED
```

v0.5 does **not** delete Baidu staging objects or local cache files. Cleanup remains a v0.6 pipeline concern.

## 2. Non-goals

v0.5 does not implement:

- Baidu cleanup;
- local cache cleanup;
- Google Drive deletion, purge, sync, or trash operations;
- arbitrary writes into pre-existing user Drive folders;
- Shared Drive support;
- multiple simultaneous Drive accounts;
- user-facing concurrency tuning;
- background services or scheduled jobs;
- a self-updater.

## 3. Drive transport pin

The initial transport is a project-managed rclone helper.

Pinned release:

```text
rclone v1.74.4
Windows amd64 archive:
rclone-v1.74.4-windows-amd64.zip
SHA-256:
ef097ef9de37a57feb7d9f9c7afb34148ad3c65be8025f1d8f7f521554a701ea
```

This version is pinned because v1.74.4 is the current stable release verified during v0.5 design and includes security fixes present in the 2026-07-08 release.

Authoritative upstream references:

- https://rclone.org/changelog/#v1-74-4-2026-07-08
- https://downloads.rclone.org/v1.74.4/
- https://downloads.rclone.org/v1.74.4/SHA256SUMS

Rules:

1. Do not silently track rclone `latest`.
2. Never execute a downloaded helper before the archive hash is verified.
3. Extract only the expected `rclone.exe` entry from the archive; reject path traversal, extra executable selection, and malformed archives.
4. The helper executable lives only under `temp/tools/rclone/`.
5. The application verifies `rclone version` reports the pinned version before using it.
6. CI does not download or execute real rclone for ordinary unit tests; use a fake process runner. A separate controlled packaging/acceptance job may verify the real pinned helper.

## 4. Runtime path containment

Every rclone invocation must explicitly provide all writable rclone paths under the application runtime root.

Required layout:

```text
temp/
  auth/
    rclone.conf
  rclone-cache/
  rclone-tmp/
  tools/
    rclone/
      rclone.exe
  cache/
    <task-id>/
      <file-id>.bin
```

Every invocation supplies equivalent arguments to:

```text
--config     <absolute temp/auth/rclone.conf>
--cache-dir  <absolute temp/rclone-cache>
--temp-dir   <absolute temp/rclone-tmp>
```

The process environment must also set `TMP` and `TEMP` to the tool-owned rclone temp directory on Windows.

No rclone command is allowed to fall back to `%APPDATA%`, `%LOCALAPPDATA%`, `%USERPROFILE%`, or the system temp directory.

The application invokes `rclone.exe` directly with an argument array. It never constructs a shell command string from logical filenames or URLs.

## 5. OAuth permission model

The remote name is fixed internally, for example:

```text
bdm-drive
```

The Drive OAuth scope is explicitly:

```text
drive.file
```

`drive.file` allows rclone to read and modify files/folders it creates, without granting visibility into unrelated Drive files. This matches the v1.0 safety model because each task creates a new tool-owned Drive root and does not need to browse arbitrary existing user content.

The first OAuth flow may open the user's browser through rclone's normal local OAuth flow. The resulting token/config remains plaintext under `temp/auth/rclone.conf`; plaintext local storage is acceptable for this personal tool, but the file is never logged or committed.

Authentication rules:

- create/configure the remote with `scope=drive.file` explicitly;
- never accept rclone's broader default Drive scope by omission;
- if the token expires or is revoked, pause the task and re-authenticate;
- do not print `rclone config show`, `config dump`, token JSON, or raw config contents;
- do not use service accounts in v0.5.

## 6. Per-task Drive root

Every migration task creates exactly one new Google Drive root folder owned by the tool.

Initial root name:

```text
BaiduDriveMover-<task-id>
```

The logical source root `/` maps to this folder.

Creation sequence:

1. create `BaiduDriveMover-<task-id>` with the base `bdm-drive` remote;
2. query that folder with machine-readable `lsjson --stat`;
3. require a valid Drive folder ID;
4. persist the root folder ID before any file upload;
5. after this point, every task command supplies:

```text
--drive-root-folder-id <persisted-task-root-id>
```

All subsequent remote paths are relative to that root.

This makes the task root an additional remote-side sandbox. A malformed logical path must still be rejected before process execution; root scoping is defense in depth, not a replacement for path validation.

The user should not rename, move, add duplicate names to, or otherwise edit the task root until the migration completes. After completion the folder may be moved or renamed normally.

## 7. State schema v3

v0.5 introduces the first compatibility-sensitive migration after Drive state becomes durable.

Schema v3 must add enough durable data to recover without path guessing. At minimum:

- task Drive root folder ID;
- task Drive root name;
- directory Drive IDs (including empty directories);
- file Drive ID already represented by the existing file model or migrated explicitly if needed;
- Drive upload/verification error state sufficient for restart.

Migration requirements:

- schema v2 -> v3 migration is explicit and transactional;
- existing v0.4 tasks remain readable;
- no existing Baidu/local provenance may be discarded;
- migration tests must open a real synthetic v2 database and prove v3 recovery fields are initialized safely.

## 8. Directory reconstruction

Directories are created from the durable logical manifest, not inferred from local paths.

Rules:

1. logical `/` is the task Drive root;
2. create directories in ascending depth order;
3. empty directories are created too;
4. after each create/reconcile, query machine-readable metadata and persist its Drive ID;
5. same names in different logical parents are independent;
6. a logical component is never converted to a local Windows path;
7. `..`, absolute/volume paths, NUL, or any component that would escape logical root is rejected before rclone execution.

If more than one Drive object with the same expected name exists in the same tool-owned parent, reconciliation fails closed rather than guessing which object is correct.

## 9. Upload protocol

Only `LOCAL_READY` files are eligible.

Before upload:

1. validate the cache path is a registered opaque path under `temp/cache/`;
2. verify local size again;
3. compute the local MD5 used as the destination verification anchor;
4. reconcile the destination path first in case a previous process crashed after Drive commit but before SQLite update.

If exactly one destination object already exists and its size + MD5 match the local file, adopt its Drive ID and proceed directly to verified state.

If no matching object exists, transition to `DRIVE_UPLOADING` and invoke one direct `copyto` operation from the opaque local `.bin` path to the explicit logical Drive path.

The implementation must use direct process arguments, never a shell.

Initial upload behavior is conservative:

- one file upload operation at a time in v0.5;
- bounded rclone retries;
- no `sync`, `move`, `delete`, `purge`, or cleanup commands;
- never use flags that permanently delete Drive content;
- no destination-wide traversal outside the task root.

A successful process exit transitions only to `DRIVE_UPLOADED`, not `DRIVE_VERIFIED`.

## 10. Independent Drive verifier

A file reaches `DRIVE_VERIFIED` only after an independent destination query.

Use machine-readable `lsjson --stat --hash` (or an equivalently strict rclone query) inside the task-root-scoped remote.

Required checks:

- destination object exists;
- object is a regular file, not a directory;
- exactly one expected object is selected;
- Drive object ID is non-empty;
- exact byte size equals the local verified cache file;
- Drive MD5 equals a locally computed MD5.

The persisted Drive ID comes from this observed destination object, not from parsing human-readable progress text.

If MD5 or size evidence is unavailable or contradictory, the file remains incomplete. v0.5 must not downgrade verification to process-exit success.

## 11. Crash/restart reconciliation

Recovery is evidence based.

### Crash before upload

`LOCAL_READY` remains eligible and is revalidated.

### Crash during upload

On restart, reconcile destination first. If no committed matching object exists, retry upload. If a matching object exists, adopt it without re-uploading.

### Crash after upload commit but before state update

Destination reconciliation finds the exact matching object by name/parent plus size/MD5 and records its Drive ID. No duplicate upload is necessary.

### `DRIVE_UPLOADED`

Never assume complete. Run the independent verifier.

### `DRIVE_VERIFIED`

v0.5 leaves local/Baidu objects untouched. v0.6 may later grant cleanup eligibility.

## 12. Duplicate and conflict policy

Google Drive permits duplicate names, so path lookup alone is not accepted as proof.

Within a tool-owned logical parent:

- zero matching names: create/upload;
- one matching name with matching evidence: adopt/verify;
- one matching name with mismatching content: controlled overwrite/re-upload is allowed only if the object ID is already registered as tool-owned for this task; otherwise block;
- more than one matching name: block and report a Drive conflict; never guess.

No unrelated Drive object may be deleted to resolve a conflict.

## 13. Helper process output and redaction

Capture only bounded stdout/stderr needed for machine results and safe diagnostics.

Never log:

- OAuth token JSON;
- Authorization headers;
- `rclone.conf` contents;
- browser callback parameters;
- raw config dump/show output.

Machine JSON used for object metadata may be parsed in memory. Normal logs contain task IDs, object IDs where safe, sizes, stage names, and sanitized rclone error categories.

## 14. Tests required before v0.5 merge

Ordinary CI remains credential-free.

Required fake/helper tests:

1. every rclone invocation includes `--config`, `--cache-dir`, and `--temp-dir` under `./temp/`;
2. no shell is invoked;
3. helper archive checksum mismatch is rejected before extraction/execution;
4. archive path traversal / zip-slip entries are rejected;
5. wrong rclone version is rejected;
6. OAuth configuration explicitly requests `drive.file`;
7. task root creation records a non-empty Drive ID;
8. every post-root command contains the persisted `--drive-root-folder-id`;
9. deep trees and empty directories are reconstructed in parent-before-child order;
10. same filename in different parents remains distinct;
11. crash after remote commit but before DB update reconciles without duplicate upload;
12. destination size mismatch does not verify;
13. destination MD5 mismatch does not verify;
14. missing hash does not silently verify;
15. duplicate same-name objects in one parent fail closed;
16. zero-byte files verify correctly;
17. Unicode/logical names are passed as process arguments without shell interpretation;
18. schema v2 -> v3 migration preserves existing task/file state;
19. Windows tests prove no helper/config/cache/temp write escapes `./temp/`;
20. `go test -race ./...`, Windows test/build, and CodeQL are green.

## 15. v0.5 release gate

v0.5.0 may merge only when all of the following hold:

- Drive OAuth is least-privilege `drive.file`;
- rclone helper is pinned and verified before execution;
- rclone runtime/config/cache/temp paths are forced under `./temp/`;
- one task root is created and its Drive ID is durably stored;
- all post-root operations are scoped by that root ID;
- logical directories, including empty ones, are reconstructed;
- upload restart cannot silently create a second copy in the normal crash path;
- every file is independently verified by Drive ID + exact size + MD5 before `DRIVE_VERIFIED`;
- no Drive delete/purge/sync behavior exists in the v0.5 production path;
- no Baidu/local cleanup is enabled yet;
- Windows CI, Linux race tests, and CodeQL pass.

Only after this document and the corresponding design decisions are committed is v0.5 implementation allowed to begin.
