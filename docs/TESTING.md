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
- after verification before local cleanup
- after local cleanup before Baidu cleanup
- during Baidu cleanup

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
- retries do not create duplicate logical completions.

### 6. Windows CI

Windows is the primary platform and must be a required CI job.

At minimum:

- build
- formatting check
- static analysis (`go vet`)
- unit/contract tests
- race-sensitive tests where supported/appropriate
- path-safety tests on Windows semantics

A lightweight Linux job may be used for fast generic Go checks, but it never replaces Windows validation.

### 7. Packaging smoke test

For release candidates:

- build packaged Windows artifact;
- launch from a clean temporary directory;
- verify only `./temp/` is created by first-run initialization;
- verify no AppData/registry/scheduled-task behavior is introduced;
- run fake end-to-end task with bundled/package layout.

### 8. Live acceptance tests

Live tests are separate from normal CI and use the user's real environment only when needed.

Progression:

1. read-only real share scan;
2. tiny controlled Baidu staging transfer;
3. tiny download;
4. tiny Drive upload + verification;
5. intentional restart during each stage;
6. medium task;
7. large real migration.

Never jump directly from mocks to destructive/large live tests.

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
