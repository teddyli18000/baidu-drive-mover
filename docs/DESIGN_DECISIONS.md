# Design Decisions

This file records decisions that should not drift during implementation without an explicit document update.

## D1. Primary language: Go

Decision: build the application in Go.

Reasons:

- native Windows executable;
- strong concurrency primitives for the pipeline;
- easy static packaging;
- no Python/Node/Conda/WSL requirement for the user;
- straightforward unit/integration testing in CI.

Target toolchain starts at Go 1.23 and may be raised deliberately later.

## D2. User-facing shape: one executable

Decision: the user launches only `BaiduDriveMover.exe`.

Internal helper executables are allowed only when they are fully managed by the application and live under `./temp/tools/`. They are never separate required installation steps.

## D3. Runtime root is portable and fail-closed

Decision: all persistent/runtime state lives under `./temp/` next to the executable.

The application must determine its executable directory, create/validate `temp/`, and refuse to run if containment cannot be guaranteed.

No fallback to AppData, system temp, registry, services, or scheduled tasks.

## D4. Baidu integration: reuse proven BaiduPCS-Go behavior, own the orchestration

Decision:

- use BaiduPCS-Go v4.0.1/current proven API behavior as the primary technical reference and, where practical, a pinned Go dependency for exported primitives;
- do not use PR #520 as production logic;
- implement our own recursive share scanner, file-level batch planner, pipeline state, and safety layer;
- pin exact upstream revisions rather than silently tracking `main`.

Reason: BaiduPCS-Go already has mature cookie/session, file, mkdir/delete, quota, and download-location behavior, while the >500 recursive batching logic needed here requires stricter correctness than the unmerged PR provides.

## D5. Baidu transfer unit is an individual file manifest

Decision: the durable manifest contains every logical file and directory. Batch transfer is planned from individual file IDs grouped by logical parent directory.

Never rely on transferring an oversized directory ID as the fundamental workaround for the 500-file limit.

## D6. Baidu authentication: dedicated Chrome profile

Decision: when interactive Baidu login is necessary, launch the user's installed Chrome with a tool-owned profile under `temp/chrome-profile/`.

Rules:

- never inspect or modify the normal Chrome profile;
- session/cookies may be stored locally in plaintext under `temp/`;
- secrets are never logged or committed;
- Chrome processes started by the tool are tracked;
- automation uses a dedicated user-data directory and must not install extensions or alter browser policy.

If reliable direct cookie/session reuse can avoid launching Chrome on later runs, use it.

## D7. Google Drive transport: rclone first, direct Drive API only if clearly superior

Decision: begin with rclone as the Drive transport because it already provides mature OAuth, retry, resumable upload, Drive naming behavior, and verification primitives.

Packaging target:

- pin an rclone version;
- release CI obtains/verifies the pinned binary;
- the user-facing package remains one entry executable;
- helper/config/cache material is placed only under `temp/tools/` and `temp/auth/`;
- every rclone invocation explicitly supplies config/cache paths under `temp/`.

If rclone cannot meet the runtime-write boundary or single-entry UX after testing, replace it with direct Google Drive API integration before v0.5.0. This change requires updating this decision first.

## D8. SQLite: pure-Go driver preferred

Decision: use SQLite for durable task state, preferably via a pure-Go driver to avoid CGO requirements in Windows release builds.

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

After completion, the user may move the task root folder anywhere in Drive.

This avoids unnecessary mid-transfer destination reconciliation complexity in v1.0.

## D11. Conservative default concurrency

Decision:

- Baidu download concurrency starts conservative (normally one active download stream for free accounts unless validated otherwise);
- Baidu transfer batches are below the hard service limit, not exactly at it;
- Drive may use higher bounded upload concurrency;
- all concurrency values remain configurable internally and protected by backpressure.

Correctness and account stability take priority over benchmark speed.

## D12. No automatic recycle-bin clearing

Decision: the tool may remove only its own registered Baidu staging objects after Drive verification, but never automatically empty the Baidu recycle bin.

If quota cannot be reclaimed automatically without touching unrelated user data, pause and request user action.

## D13. No self-update in v1.0

Decision: initial versions do not install background updaters or modify startup/system configuration.

Updates are explicit new releases downloaded/replaced by the user.

## D14. CI is mock-first

Decision: ordinary CI must be deterministic and credential-free. Service behavior is modeled by fixtures/fake HTTP servers.

Live Baidu/Drive acceptance testing is a separate controlled step and never required for untrusted pull requests.

## D15. Safety over silent success

Decision: when the tool cannot prove that a file reached Drive correctly, it remains incomplete. It must never silently skip a deterministic failure and report the task as complete.
