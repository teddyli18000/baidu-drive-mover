# Baidu Drive Mover

Windows CLI tool for moving arbitrary Baidu Netdisk share links to Google Drive with resumable staged transfer.

## Product goal

Normal use should be:

1. Run `BaiduDriveMover.exe`.
2. Paste any Baidu share link.
3. If the link does not contain an extraction code and one is required, enter it in CLI.
4. Leave the program running. It scans, stages, downloads, uploads, verifies, cleans only its own temporary data, and continues from persisted state.
5. Stop at any time with Ctrl+C. Restart later and resume safely.

## Hard rules

- Single user-facing entry point: `BaiduDriveMover.exe`.
- Runtime files may only exist under `temp/` beside the executable.
- No registry writes.
- No Windows scheduled tasks.
- No AppData / LocalAppData / user-profile config files.
- No modification of the user's normal Chrome profile.
- No deletion of unrelated Baidu Netdisk files.
- Google Drive is the final destination and source of truth for completion.
- Original logical directory tree must be preserved.
- A file is not considered complete until Google Drive verification succeeds.
- Cleanup is allowed only for durably registered tool-owned local/Baidu objects after Drive verification.
- Google Drive destination objects are never cleanup targets.
- The Baidu recycle bin is never automatically emptied.
- The pipeline must be resumable and idempotent.

## Installation and normal use

The v0.8 Windows package targets Windows x64. Extract the ZIP into a folder where your account can create files, then run `BaiduDriveMover.exe`. Normal users do not need Go, Python, Node.js, Conda, or WSL. Google Chrome is required for the isolated Baidu login window; Baidu and Google accounts plus network access are required for a real migration.

The first launch creates only `temp/` beside the executable. Keep the application in the same folder while a task is active: `temp/` contains resumable state, opaque cache data, Baidu cookies, Google OAuth configuration, private share metadata, and logs. Do not upload, sync, or send this directory as a support bundle. After the last non-completed task reaches durable `COMPLETED`, the process closes its database, log, lock, browser, and rclone handles and removes the entire tool-owned `temp/`. Interrupted, blocked, failed, or scan-only work retains `temp/` for recovery. Removing the whole application folder while a task is unfinished cannot clean remote staging.

Run modes:

```text
BaiduDriveMover.exe              resume the most recently updated unfinished task, or prompt for a new link
BaiduDriveMover.exe -new         deliberately start another task
BaiduDriveMover.exe -list        list resumable tasks
BaiduDriveMover.exe -resume ID   resume one listed task
BaiduDriveMover.exe -scan-only   scan or resume the manifest, then stop before migration
BaiduDriveMover.exe -check       validate only the local temp/SQLite safety boundary
BaiduDriveMover.exe -version     print release identity
```

`-scan-only` follows the normal newest-task selection rule and performs only the Baidu share scan (including authentication and extraction-code prompts when needed). If the selected task already has a durably completed manifest, it reports those persisted statistics without scanning the share again. It prints the task ID and manifest statistics, then exits before Baidu staging, downloading, Google Drive work, or cleanup. To begin migration after reviewing the scan, run `BaiduDriveMover.exe -resume <task-id>` explicitly. It cannot be combined with `-check`, `-list`, `-resume`, or `-new`.

During first Drive use, the verified pinned rclone helper is downloaded into `temp/tools/` and opens an OAuth flow for the private `drive.file` scope. Each task creates a new `BaiduDriveMover-<task-id>` Drive folder. Do not move or rename that folder before the task completes. The tool never deletes destination Drive objects.

Successful final cleanup removes all local runtime artifacts created by the program, including its dedicated Chrome profile, stored Baidu cookies, Google OAuth configuration, managed rclone binary, caches, logs, and task database. A later migration therefore starts with fresh local authorization. If another task in the same executable folder is still non-completed, shared runtime state is retained until that task also completes.

Successful `-check` and `-list` runs also remove runtime data they created when no non-completed task exists. They never discard an unfinished, blocked, or failed task merely to make a diagnostic run residue-free.

Release downloads include `SHA256SUMS.txt`; verify the ZIP hash before extraction:

```powershell
(Get-FileHash .\BaiduDriveMover-0.8.0-windows-amd64.zip -Algorithm SHA256).Hash
```

## Development status

Current milestone: **v0.9 Real-world Beta**.

v0.6 closes the cooperative end-to-end pipeline:

```text
share scan
  -> bounded Baidu staging
  -> resumable opaque local cache
  -> Google Drive tree reconstruction/upload
  -> independent Drive ID + size + MD5 verification
  -> durable tool-owned cleanup
  -> DONE
```

The scheduler is SQLite-driven and downstream-first: verified cleanup releases cache pressure before Drive upload, download, and additional Baidu staging are pumped. If a complete pass produces no durable state change, the task stops in a blocked state rather than busy-looping.

v0.7 adds bounded typed retries only to safe Baidu read/reconcile calls, including rate-limit and server-outage handling with capped `Retry-After`. Transfer and delete mutations remain one-shot at the HTTP layer and rely on durable reconciliation before later attempts. Scanner and manifest hardening reject pagination stalls, identity rebinding, path collisions, malicious child paths, and silent filename normalization. Credential-free large-share and prolonged restart simulations exercise convergence under repeated faults and cache pressure.

v0.8 adds durable scan-completion evidence and a real CLI resume path, so restart reuses the original task rather than creating a duplicate. It also adds a process-level folder lock and a reproducible Windows package gate with a one-file allowlist, version/commit binding, SHA-256 output, minimal-`PATH` execution, and first-run write-boundary verification.

v0.9 starts controlled live acceptance. The `-scan-only` checkpoint makes the first real-share step read-only and requires an explicit `-resume <task-id>` before any Baidu staging, local download, Drive upload, or cleanup. Live evidence progresses from a tiny controlled share through intentional restarts before medium or large migrations.

Cleanup is deliberately fail-closed. A staging batch cannot be cleaned until every file in it has a persisted Drive ID and has reached `DRIVE_VERIFIED`. One SQLite transaction changes the batch to `CLEANUP_PENDING` and authorizes only the exact registered opaque local cache files and the exact `/BaiduDriveMover/<task-id>/<batch-id>` directory. Each successful deletion is recorded independently so restart can reconcile a crash between destructive steps.

The task-level `/BaiduDriveMover/<task-id>` directory is removed only after every file is `DONE`, every registered batch directory is proven cleaned, and a fresh remote listing proves the task root is empty. The global `/BaiduDriveMover` directory is never deleted.

Google Drive integration continues to use pinned rclone v1.74.4 with `drive.file` OAuth and a persisted task-root folder ID. Destination Drive objects are verified but never deleted by v0.6 cleanup.

Design and release gates:

- `docs/ARCHITECTURE.md`
- `docs/IMPLEMENTATION_PLAN.md`
- `docs/VERSIONING.md`
- `docs/DESIGN_DECISIONS.md`
- `docs/SECURITY.md`
- `docs/TESTING.md`
- `docs/V05_GOOGLE_DRIVE.md`
- `docs/V06_FULL_PIPELINE.md`
- `docs/V07_HARDENING.md`
- `docs/V08_PACKAGING.md`
- `docs/V09_LIVE_ACCEPTANCE.md`
- `docs/RCLONE_PIN.md`

The public repository must never contain real account cookies/tokens, browser profiles, task databases, private share manifests, logs, downloaded files, or rclone OAuth configuration.
