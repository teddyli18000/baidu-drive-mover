# Test Strategy

Testing is part of the product definition. CI should catch logic/safety regressions without using real account credentials.

## Test layers

### 1. Unit tests

Fast, deterministic, offline.

Required areas:

- share URL parsing and extraction-code parsing
- logical path normalization
- batch grouping/splitting
- backpressure/watermarks
- retry classification and backoff
- state-machine transitions
- runtime-root containment
- cleanup provenance
- log redaction
- restart recovery decisions

### 2. Adapter contract tests

Use local HTTP test servers/fake transports to simulate Baidu and Drive behaviors.

Baidu fixtures must cover:

- valid share
- password-required share
- wrong password
- expired share
- paginated listing
- deep tree
- root files mixed with subdirectories
- empty directories
- a single directory containing >500 files
- service rate limiting
- transfer limit response
- partial transfer failures
- duplicate-name response
- authentication expiration
- staging-only delete path validation
- explicit already-missing cleanup codes
- task-root listing before final cleanup

Drive fixtures must cover:

- folder creation
- upload success
- resumable upload interruption
- retryable errors
- auth expiration
- verification mismatch
- duplicate/restart reconciliation

### 3. State/recovery tests

For every durable state, simulate process death immediately after persistence and ensure restart converges safely.

Important cut points:

- before/after Baidu transfer
- mid-download
- after local download before DB transition
- mid-Drive upload
- after Drive upload before verification
- after verification before cleanup authorization
- after cleanup authorization before any deletion
- after one local cache deletion
- after all local cache deletions before Baidu deletion
- after Baidu deletion before its cleanup evidence is persisted
- after every cleanup object is reconciled before the batch completion transaction
- before/after task-root cleanup

### 4. Filesystem safety tests

Run against temporary test directories and prove:

- no writes escape configured runtime root;
- `..` and absolute-path source names cannot escape;
- Windows-invalid logical filenames are kept as metadata, not materialized unsafely;
- cleanup refuses unregistered paths;
- symlink/junction escape attempts are rejected where applicable.

### 5. Pipeline simulation tests

Use fully fake Baidu and Drive adapters with thousands of synthetic files.

Scenarios:

- 10,000+ files
- many tiny files
- few huge files
- deep directory trees
- random retryable failures
- random process-stop/restart events
- cache watermark saturation
- Baidu staging watermark saturation
- slow Drive uploader / fast downloader
- slow downloader / fast uploader

Assertions:

- every source logical file maps to exactly one verified destination object;
- no file is cleaned before verification;
- bounded cache limits are respected;
- task eventually converges when injected failures stop;
- retries do not create duplicate logical completions;
- a no-progress durable pass stops instead of spinning indefinitely;
- one failing stage prevents later stages in the same pass from running.

### 6. Windows CI

Windows is the primary platform and must be a required CI job.

At minimum:

- build
- formatting check
- static analysis (`go vet`)
- unit/contract tests
- path-safety tests on Windows semantics

Linux CI runs formatting plus the Go race detector for generic race-sensitive coverage. It complements rather than replaces Windows validation.

### 7. Packaging smoke test

For release candidates:

- build packaged Windows artifact;
- launch from a clean temporary directory;
- verify only `./temp/` is created by first-run initialization;
- verify no AppData/registry/scheduled-task behavior is introduced;
- run fake end-to-end task with bundled/package layout.

### 8. v0.6 destructive fault-injection gate

Automatic cleanup is not accepted merely because the happy path works. Credential-free tests must inject failures around destructive boundaries and prove restart behavior.

Required assertions include:

- migration from v0.5 never grants cleanup authority;
- one unverified file blocks the whole staging batch from cleanup authorization;
- a missing Drive ID blocks cleanup authorization;
- changed/unregistered local or Baidu paths are rejected before deletion;
- cleanup authorization is committed before any destructive action starts;
- cancellation immediately after authorization starts no deletion;
- cancellation after one local deletion stops before later destructive work and restart completes exactly;
- an already-missing authorized local file reconciles successfully;
- Baidu explicit not-found codes reconcile successfully, while other delete failures remain failures;
- a successful Baidu delete followed by a DB persistence failure resumes safely;
- a batch-completion transaction failure resumes without re-deleting already-cleaned objects;
- `CompleteBatchCleanup` independently checks provenance cardinality in its own transaction;
- the task-level Baidu root cannot be authorized unless every batch has one cleaned provenance row;
- the task root is never deleted if a fresh listing shows any unexpected object;
- an already-missing task root can be reconciled without an extra delete;
- Drive destination objects never become cleanup-authorized;
- `/BaiduDriveMover` and the Baidu recycle bin have no automatic delete/clear path;
- scheduler failures at cleanup/Drive/download/staging stop later stages;
- Ctrl+C pauses rather than converting interrupted work into success;
- permanent file failure prevents task completion.

### 9. Live acceptance tests

Live tests are separate from normal CI and use the user's real environment only when needed.

Progression:

1. read-only real share scan with `BaiduDriveMover.exe -scan-only`; review the printed task ID and manifest statistics, then explicitly use `-resume <task-id>` to leave the read-only phase;
2. tiny controlled Baidu staging transfer;
3. tiny download;
4. tiny Drive upload + verification;
5. intentional restart during each stage;
6. controlled cleanup of only the tool-owned tiny staging batch;
7. verify the task root is removed only when empty;
8. medium task;
9. large real migration.

Never jump directly from mocks to destructive/large live tests.

`-scan-only` must not be combined with `-check`, `-list`, `-resume`, or `-new`. Its successful exit is a hard boundary: it must not construct or call staging, download, Drive, or cleanup runners. A scan-only run may still perform the read-only share scan and the required interactive Baidu authentication/extraction-code steps.

## CI policy

Normal pushes/PRs:

- no real secrets;
- no live Baidu/Drive calls;
- deterministic synthetic data only.

Milestone/release:

- all normal CI green;
- packaging smoke green;
- milestone-specific release gates from `VERSIONING.md` satisfied.

## Failure policy

Flaky tests are treated as bugs, not ignored permanently. A test may be quarantined only with a tracked reason and a replacement validation path.
