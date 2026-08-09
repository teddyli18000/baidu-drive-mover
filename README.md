# Baidu Drive Mover

Windows CLI tool for moving arbitrary Baidu Netdisk share links to Google Drive with resumable staged transfer.

## Product goal

Normal use should be:

1. Run `BaiduDriveMover.exe`.
2. Paste any Baidu share link.
3. If the link does not contain an extraction code and one is required, enter it in CLI.
4. Leave the program running. It automatically scans, stages, downloads, uploads, verifies, cleans temporary data, and continues.
5. Stop at any time with Ctrl+C. Restart later and continue from persisted state.

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

Current milestone: **v0.2.0 Read-only Baidu**.

This milestone adds share-link parsing, dedicated Chrome authentication, recursive paginated share scanning, and durable manifest persistence. It intentionally performs **no Baidu transfer/delete and no Google Drive write** yet.

Design and release gates:

- `docs/ARCHITECTURE.md`
- `docs/IMPLEMENTATION_PLAN.md`
- `docs/VERSIONING.md`
- `docs/DESIGN_DECISIONS.md`
- `docs/SECURITY.md`
- `docs/TESTING.md`

The public repository must never contain real account cookies/tokens, browser profiles, task databases, private share manifests, logs, or downloaded files.
