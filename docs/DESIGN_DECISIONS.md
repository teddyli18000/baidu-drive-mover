# Design Decisions

This file records decisions that should not drift during implementation without an explicit document update.

## D1. Primary language and toolchain: Go

Decision: build the application in Go.

Reasons:

- native Windows executable;
- strong concurrency primitives for the pipeline;
- easy static packaging;
- no Python/Node/Conda/WSL requirement for the user;
- straightforward unit/integration testing in CI.

Baseline module compatibility is Go 1.26 because the pinned browser automation layer requires it. Release/CI builds use Go 1.26.x unless deliberately changed. The user does not need Go installed for packaged releases.

## D2. User-facing shape: one executable

Decision: the user launches only `BaiduDriveMover.exe`.

Internal helper executables are allowed only when they are fully managed by the application and live under `./temp/tools/`. They are never separate required installation steps.

## D3. Runtime root is portable and fail-closed

Decision: all persistent/runtime state lives under `./temp/` next to the executable.

The application must determine its executable directory, create/validate `temp/`, and refuse to run if containment cannot be guaranteed.

No fallback to AppData, system temp, registry, services, or scheduled tasks.

## D4. Baidu integration: reuse proven behavior, own the transport/orchestration

Decision:

- use BaiduPCS-Go v4.0.1/current source behavior as the primary reference for Baidu web API semantics;
- do not use PR #520 as production logic;
- implement a small project-owned HTTP adapter for share page access, password verification and recursive listing so request limits, retries, logging and tests remain under our control;
- reuse or wrap BaiduPCS-Go exported primitives later only where that clearly lowers download/staging risk without weakening the runtime/safety boundary;
- implement our own recursive share scanner, file-level batch planner, pipeline state, and safety layer;
- pin any imported upstream revision rather than silently tracking `main`.

Reason: BaiduPCS-Go provides proven API behavior, but this tool needs stricter control over >500 batching, persistence, retries and local safety than an unmodified CLI dependency provides.

## D5. Baidu transfer unit is an individual file manifest

Decision: the durable manifest contains every logical file and directory. Batch transfer is planned from individual file IDs grouped by logical parent directory.

Never rely on transferring an oversized directory ID as the fundamental workaround for the 500-file limit.

## D6. Baidu authentication: dedicated Chrome profile

Decision: when interactive Baidu login is necessary, launch the user's installed Chrome with a tool-owned profile under `temp/chrome-profile/` using `github.com/chromedp/chromedp v0.15.1`.

Rules:

- never inspect or modify the normal Chrome profile;
- Chrome `TEMP`/`TMP`, disk cache and user-data directory are explicitly redirected under the application's `temp/` tree;
- session/cookies may be stored locally in plaintext under `temp/`;
- secrets are never logged or committed;
- Chrome processes started by the tool are tracked and closed with the login context;
- automation must not install extensions or alter browser policy/registry.

If reusable stored cookies remain valid, later runs avoid opening Chrome.

## D7. Google Drive transport: pinned rclone helper

Decision: v0.5 uses rclone as a managed Google Drive transport because it provides mature OAuth, retry/resumable upload behavior, Drive metadata queries and hash support while allowing the project to keep orchestration and state ownership in Go.

Pin for v0.5:

- `rclone v1.74.4`;
- Windows amd64 archive `rclone-v1.74.4-windows-amd64.zip`;
- SHA-256 `ef097ef9de37a57feb7d9f9c7afb34148ad3c65be8025f1d8f7f521554a701ea`.

Rules:

- never silently track `latest`;
- verify the archive SHA-256 before extraction or execution;
- reject archive traversal and extract only the expected helper executable;
- verify the helper-reported version before production use;
- helper executable lives only under `temp/tools/rclone/`;
- configuration lives only under `temp/auth/rclone.conf`;
- every rclone invocation explicitly sets `--config`, `--cache-dir`, and `--temp-dir` to paths under `temp/`;
- Windows child `TMP` and `TEMP` are redirected under `temp/`;
- invoke directly with an argument vector, never through a shell;
- ordinary CI uses a fake process runner rather than real OAuth or real Drive credentials.

If rclone cannot satisfy these runtime-path, least-privilege, or single-entry UX constraints during v0.5 acceptance, direct Google Drive API integration may replace it only after this decision is updated.

## D8. SQLite: pure-Go driver

Decision: use SQLite for durable task state via `modernc.org/sqlite`, pinned to a tagged release, to avoid CGO requirements in Windows release builds.

Initial pin: `modernc.org/sqlite v1.56.0`.

Requirements:

- WAL where appropriate;
- explicit schema version;
- migrations tested once persisted beta tasks exist;
- transactions around state transitions that guard cleanup/destructive operations.

## D9. Local cache uses opaque filenames

Decision: downloaded bytes are stored under opaque internal IDs, not original source filenames.

Reason: Windows filename restrictions must not corrupt or rewrite the logical Baidu/Drive tree.

Logical path and destination name remain database metadata.

## D10. Drive folder ownership

Decision: every migration task creates one new Drive root folder. Until the task completes, the user should treat that folder as tool-managed and not manually rename/move its contents.

The base OAuth permission is `drive.file`, so the tool deliberately does not browse or mutate unrelated existing Drive content. The initial task root is created by the tool itself, its Drive folder ID is persisted, and all later task operations are scoped to that ID.

After completion, the user may move or rename the task root folder normally.

This avoids unnecessary mid-transfer destination reconciliation complexity in v1.0 and gives Drive operations a remote-side sandbox.

## D11. Conservative default concurrency

Decision:

- Baidu download concurrency starts conservative (normally one active download stream for free accounts unless validated otherwise);
- Baidu transfer batches are below the hard service limit, not exactly at it;
- Drive may use higher bounded upload concurrency in later milestones, but v0.5 begins with one active upload;
- all concurrency values remain configurable internally and protected by backpressure.

Correctness and account stability take priority over benchmark speed.

## D12. No automatic recycle-bin clearing

Decision: the tool may remove only its own registered Baidu staging objects after Drive verification, but never automatically empty the Baidu recycle bin.

If quota cannot be reclaimed automatically without touching unrelated user data, pause and request user action.

## D13. No self-update in v1.0

Decision: initial versions do not install background updaters or modify startup/system configuration.

Updates are explicit new releases downloaded/replaced by the user.

## D14. CI is mock-first

Decision: ordinary CI must be deterministic and credential-free. Service behavior is modeled by fixtures/fake HTTP servers and fake process runners.

Live Baidu/Drive acceptance testing is a separate controlled step and never required for untrusted pull requests.

## D15. Safety over silent success

Decision: when the tool cannot prove that a file reached Drive correctly, it remains incomplete. It must never silently skip a deterministic failure and report the task as complete.

## D16. Baidu staging is isolated by transfer batch

Decision: Baidu staging does **not** mirror the user's logical source tree. Each planned transfer batch gets a unique tool-owned directory:

```text
/BaiduDriveMover/<task-id>/<batch-id>/
```

The logical source path remains SQLite metadata and is recreated only on Google Drive.

Reasons:

- every staging directory is bounded to the conservative batch size, so reconciliation never needs to enumerate a huge directory;
- files with the same name in different source directories cannot collide during staging;
- partial transfer/restart recovery can reconcile one small batch directory independently;
- no source filename is needed to construct local filesystem paths;
- cleanup provenance is simpler because every remote staging path is an internal tool-generated identifier.

Rules:

- files in one batch still come from the same logical parent directory;
- default batch size starts at 200, well below the observed 500-file free-account ceiling;
- transfer-limit/partial failures are reconciled against the isolated batch directory, then only missing files are split/retried;
- a batch directory is registered in `owned_objects` before remote creation;
- automatic cleanup remains disabled until downstream Drive verification grants cleanup eligibility.

## D17. Google Drive uses least privilege and task-root ID scoping

Decision: Google OAuth explicitly requests `drive.file`, not rclone's broader default Drive scope.

Each task creates a new tool-owned Drive root such as `BaiduDriveMover-<task-id>`. After creation, the folder ID is observed and persisted. Every later rclone operation for that task must include the persisted `--drive-root-folder-id` so the rclone backend itself is rooted at the task folder.

Consequences:

- the tool does not need visibility into unrelated Drive files;
- user-selected pre-existing destination folders are intentionally out of scope for v0.5/v1.0 normal operation;
- task paths cannot intentionally address Drive objects above the task root;
- Drive `delete`, `purge`, and destination-wide `sync` are not part of v0.5 production behavior.

## D18. Drive verification is independent from upload success

Decision: a successful upload process exit is insufficient for completion.

For each uploaded file the application independently queries Drive metadata and requires:

- one unambiguous destination object;
- non-empty Drive object ID;
- exact size equality;
- MD5 equality against a hash computed from the verified local cache file.

Only then may the state advance to `DRIVE_VERIFIED`. If destination evidence is missing, ambiguous, or contradictory, the task remains incomplete and cleanup remains forbidden.
