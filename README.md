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

## Development status

Current milestone: **v0.7 Hardening**.

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
- `docs/RCLONE_PIN.md`

The public repository must never contain real account cookies/tokens, browser profiles, task databases, private share manifests, logs, downloaded files, or rclone OAuth configuration.
