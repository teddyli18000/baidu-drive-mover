# Implementation Plan

This document is the execution contract for BaiduDriveMover. Functional code must follow this order unless a documented design change is made first.

## Product target

A Windows user runs one executable, pastes any Baidu Netdisk share link, enters an extraction code only when required, and can then leave the tool unattended. The tool stages data through the user's own Baidu account, moves it through a bounded local cache, uploads it to a newly created Google Drive task folder, verifies it, cleans only tool-owned temporary data, and resumes safely after interruption.

## Non-negotiable constraints

- Single user entry point: `BaiduDriveMover.exe`.
- All runtime artifacts must live under `./temp/` beside the executable.
- If `./temp/` cannot be created or written, fail closed. Never fall back to AppData, user profile, registry, system temp, services, or scheduled tasks.
- Never modify the user's normal Chrome profile.
- Never delete or rename unrelated Baidu/Drive/local files.
- Never consider a file complete before Drive verification succeeds.
- Original logical tree is authoritative; local cache filenames may be opaque IDs.
- Shutdown at any time must be recoverable.
- Repository and CI must never contain real credentials, cookies, extraction codes, account data, or real private share metadata.

## Runtime layout

```text
BaiduDriveMover.exe

temp/
  state.db
  config.json
  chrome-profile/
  auth/
  cache/
  logs/
  tasks/
  tools/
```

All tool-managed subprocess configuration must be redirected into this tree.

## Pipeline model

The production pipeline is bounded producer/consumer flow:

```text
share scan
  -> batch planner
  -> Baidu staging
  -> local downloader
  -> Drive uploader
  -> verifier
  -> cleanup
```

Backpressure rules:

- A stage may not produce unlimited work ahead of the next stage.
- Baidu staging has both file-count and byte-watermark limits.
- Local cache has a byte watermark.
- Drive uploader receives only fully downloaded local objects.
- Cleanup receives only verified Drive objects.

## Batch planning rules

- Enumerate individual files with full logical paths.
- Preserve empty directories separately.
- Group files by logical parent directory.
- Split transfer requests below Baidu's account limit; default target size is conservative rather than exactly 500.
- On transfer-limit or partial-batch failure, recursively split the batch until a deterministic failing file is isolated.
- A single logical directory may span many transfer batches.
- Never treat a directory `fs_id` as a substitute for individually planned files when it can exceed the transfer limit.

## State model

Task states:

```text
NEW
AUTH_REQUIRED
SCANNING
RUNNING
PAUSED
BLOCKED
COMPLETED
FAILED
```

File states:

```text
DISCOVERED
PLANNED
BAIDU_STAGING
BAIDU_STAGED
DOWNLOADING
LOCAL_READY
DRIVE_UPLOADING
DRIVE_UPLOADED
DRIVE_VERIFIED
CLEANUP_PENDING
DONE
FAILED_RETRYABLE
FAILED_PERMANENT
```

State transitions must be persisted before destructive cleanup actions.

## Recovery model

At startup:

1. Open and integrity-check `temp/state.db`.
2. Recover interrupted states conservatively.
3. Reconcile local cache entries with database records.
4. Reconcile tool-owned Baidu staging paths when required.
5. Reconcile Drive objects by stored Drive IDs/path metadata.
6. Resume only from verified facts; never infer success from a prior process exit.

## Authentication model

### Baidu

- Use a dedicated Chrome profile located only at `temp/chrome-profile/` for interactive login when needed.
- Persist reusable local session data only under `temp/`.
- Plain local storage of cookies is acceptable for this tool; do not print secrets to console or CI logs.
- If authentication expires, pause the pipeline and reopen the dedicated login flow.

### Google Drive

- OAuth state/config must live under `temp/`.
- Prefer a mature Drive implementation such as rclone for upload/retry/check semantics unless direct API integration clearly reduces complexity without reducing reliability.
- Each task creates one new Drive root folder and stores its Drive folder ID.

## Local cache model

- Cache objects use opaque names such as file IDs/task IDs, not user-visible source names.
- Logical names/paths exist in SQLite and are restored only at the destination layer.
- Partial downloads have explicit `.part` or equivalent metadata and are never uploaded as complete.
- Cache eviction is allowed only after Drive verification.

## Cleanup policy

Automatic deletion is limited to paths created and registered by this tool:

- `temp/` content owned by the current task.
- Baidu staging under a tool-specific root such as `/BaiduDriveMover/<task-id>/`.

Do not automatically clear the Baidu recycle bin. Do not delete unrelated Drive files.

## Error policy

- Network/service errors: bounded retries with exponential backoff and jitter.
- Authentication/verification requirements: pause and request user action.
- Quota/capacity blockers: pause with a clear reason.
- Deterministic file failures: isolate and record; do not silently skip.
- Repeated unexpected failures: stop the affected stage rather than spinning.

## Development order

### Phase A - design freeze

- Architecture
- Versioning
- Security/repository rules
- Test strategy
- Implementation plan

No production feature code before these documents exist.

### Phase B - v0.1 foundation

- Go module and executable skeleton
- strict runtime-root manager
- CLI shell
- logging/redaction
- SQLite schema/migrations
- task/file state model
- unit tests
- Windows CI

No live Baidu or Drive writes.

### Phase C - v0.2 read-only Baidu

- share URL parser
- extraction-code handling
- dedicated Chrome login/session flow
- Baidu auth adapter
- recursive share scanner
- manifest persistence
- scanner fixtures/mocks

No automatic transfer yet.

### Phase D - v0.3 Baidu staging

- tool-owned staging root
- directory creation
- correct individual-file batch planner
- bounded concurrent transfer requests
- split-and-retry behavior
- small live transfer acceptance test

### Phase E - v0.4 downloader

- locate/download integration
- resumable local download
- opaque cache filenames
- byte-watermark controller
- restart recovery

### Phase F - v0.5 Drive

- OAuth bootstrap
- task root folder creation
- path recreation
- upload/retry
- verification
- stored Drive IDs

### Phase G - v0.6 full pipeline

- concurrent stages
- backpressure
- recovery/reconciliation
- graceful Ctrl+C
- automatic tool-owned cleanup

### Phase H - v0.7 hardening

- large directories
- mixed root files/subdirectories
- empty directories
- duplicate names
- invalid Windows filename characters
- quota pressure
- auth expiration
- rate limiting
- partial service outages

### Phase I - v0.8 packaging

- single Windows release package
- bundled/helper tool discovery constrained to package/temp layout
- clean first-run experience
- clean uninstall-by-folder-removal behavior
- release CI

Implementation status: implemented for v0.8 development. The durable CLI resume path, reproducible package build, independent package smoke test, and release gates are part of the acceptance contract.

### Phase J - v0.9 beta / v1.0 stable

- real multi-hour migration tests
- restart tests
- interrupted-upload tests
- safety audit
- final UX simplification

## Definition of done

Version 1.0 is not reached until a real large share can be moved end-to-end with original tree preserved, process interruption/restart succeeds repeatedly, temporary storage stays bounded, no unrelated files are touched, and the final release requires no developer tooling from the user.
