# Baidu Drive Mover

Windows CLI tool for moving arbitrary Baidu Netdisk share links to Google Drive with resumable staged transfer.

## Product goal

Normal use should be:

1. Run `BaiduDriveMover.exe`.
2. Paste any Baidu share link.
3. If the link does not contain an extraction code and one is required, enter it in CLI.
4. Leave the program running. It scans, stages, downloads, uploads, verifies, and continues from persisted state.
5. Stop at any time with Ctrl+C. Restart later and resume safely.

Automatic cleanup is a later milestone and is intentionally disabled in v0.5.

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
- The pipeline must be resumable and idempotent.

## Development status

Current milestone: **v0.5 Google Drive**.

The pipeline now covers recursive share discovery, deterministic Baidu staging, resumable local download, Google Drive directory reconstruction, upload, crash reconciliation, and independent Drive verification.

Google Drive integration uses a pinned rclone helper and a tool-owned `drive.file` OAuth remote. Each task gets its own persisted Drive root folder ID, and later task-scoped operations are constrained to that root. A file reaches `DRIVE_VERIFIED` only after the remote object is independently re-listed and its stable Drive ID, exact size, and MD5 are verified.

v0.5 deliberately does **not** delete Baidu staging files, local cache files, or Drive objects. Cleanup remains disabled until the v0.6 cleanup milestone has its own safety and recovery gates.

Design and release gates:

- `docs/ARCHITECTURE.md`
- `docs/IMPLEMENTATION_PLAN.md`
- `docs/VERSIONING.md`
- `docs/DESIGN_DECISIONS.md`
- `docs/SECURITY.md`
- `docs/TESTING.md`
- `docs/V05_GOOGLE_DRIVE.md`
- `docs/RCLONE_PIN.md`

The public repository must never contain real account cookies/tokens, browser profiles, task databases, private share manifests, logs, downloaded files, or rclone OAuth configuration.
