# v0.7 Hardening Design

v0.7 does not add a new user-visible transfer stage. It hardens the v0.6 end-to-end pipeline against long-running service instability, pathological share metadata, and large synthetic migrations.

## Goals

- bounded recovery from transient Baidu/Google/network failures;
- explicit handling of rate limits and server backpressure;
- authentication expiry recovery without widening permissions;
- fail-closed handling of duplicate/ambiguous/pathological metadata;
- scanner progress guarantees for paginated/deep/large shares;
- prolonged credential-free migration simulation with repeated outages/restarts;
- no weakening of v0.6 destructive cleanup invariants.

## Retry boundary

Retries are allowed only where replay is safe or independently reconciled.

### Safe read/reconcile operations

Examples:

- share page access;
- share directory listing;
- staging directory listing;
- cleanup inspection listing;
- Drive metadata listing/probe.

These may retry bounded transient network failures, HTTP 429, and HTTP 5xx.

### Operations with remote side effects

Examples:

- Baidu staging transfer;
- Baidu staging delete;
- Drive upload;
- Drive mkdir.

Transport-layer blind retries are not introduced for these operations.

Their existing stage-specific semantics remain authoritative:

- staging retries only after remote reconciliation of which files are still missing;
- cleanup retries only after fresh remote observation on restart/pass;
- Drive upload reconciles the destination after process success or failure.

This prevents a generic HTTP retry layer from duplicating or widening a destructive mutation.

## Transient error model

Introduce a small typed transient-error contract carrying:

- operation name;
- HTTP status when known;
- optional server `Retry-After` delay;
- wrapped network error when applicable.

Retry decisions must be based on error type, not string matching.

`Retry-After` parsing accepts:

- integer seconds;
- RFC 1123 HTTP date.

The accepted delay is bounded to a conservative maximum. Invalid or extreme values fall back to exponential backoff.

Default read retry policy:

- maximum 4 attempts;
- exponential delay starting at 500 ms;
- server Retry-After wins when valid and within the configured cap;
- context cancellation interrupts sleep immediately;
- no unbounded retry loop.

## Scanner pagination progress guarantee

The recursive scanner must never rely solely on `len(page) < pageSize` for termination.

For each directory it keeps a compact page fingerprint derived from stable item metadata. If a later page repeats a previously observed full page, the scan stops with an explicit no-progress/remote-pagination error rather than looping indefinitely.

Additional invariants:

- a page may not contain duplicate logical child paths;
- a normalized child path may not escape the selected share root;
- a file entry must have positive `fs_id` and non-negative size;
- a directory/file type conflict for the same logical path is rejected by manifest persistence;
- page count per directory has a high finite safety ceiling even when fingerprints differ pathologically.

The scanner remains streaming: it persists one page at a time and does not retain all file metadata in memory.

## Huge-share tests

Credential-free tests generate synthetic shares substantially larger than ordinary unit fixtures.

Minimum target scenarios:

- 10,000 files in one directory over 100 pages;
- 10,000+ files spread across a deep tree;
- root files mixed with nested directories;
- empty directories;
- same-size files with different IDs/names;
- transient 429/503 responses on deterministic page intervals;
- authentication expiry on a read followed by successful re-auth at the app layer;
- repeated full page / pagination-stall injection;
- interrupted scan resumed through manifest upsert idempotence.

Tests assert bounded request attempts and exact final manifest counts.

## Prolonged mock migration

A deterministic fake migration repeatedly pumps the real state/staging/download/Drive/cleanup orchestration against fake adapters.

The test should exercise thousands of files and inject:

- temporary Baidu read/list failures;
- staging partial success;
- download disconnects with range resume;
- Drive upload/process failure followed by reconcile success;
- cleanup already-missing objects;
- repeated pipeline stop/restart boundaries;
- cache watermark pressure.

Release assertions:

- every logical source file ends in exactly one `DONE` state;
- no file reaches `DONE` without prior Drive verification evidence;
- no unrelated cleanup target is ever authorized;
- cache reservation never exceeds the configured watermark;
- retries remain bounded while failures persist;
- once injected failures stop, the migration converges;
- directory tree metadata remains exact.

The prolonged mock test must be deterministic and suitable for normal CI; it must not require live Baidu/Google credentials.

## Name/path hardening

Source names remain logical metadata until Drive reconstruction; Windows-invalid names must never be materialized as local filenames.

Hardening tests cover:

- leading/trailing spaces and dots;
- Unicode and multi-byte names;
- names containing Windows reserved characters that are valid remotely;
- very long logical paths;
- dot-like components (`.` / `..`) returned by a malicious/buggy service;
- slash/backslash injection;
- duplicate logical paths after normalization;
- file-vs-directory collision at the same logical path.

Unsafe/ambiguous metadata must stop the task; it must never be silently renamed because silent renaming would break exact tree preservation.

## Authentication and quota semantics

Authentication expiry is recoverable only through the existing dedicated login/OAuth flows.

- Baidu auth expiry may trigger one controlled dedicated-Chrome re-login attempt per app stage invocation.
- Google auth expiry may trigger the existing least-privilege rclone reauthorization path.
- repeated auth failure becomes `BLOCKED`; it is not retried forever.
- Baidu quota exhaustion remains `BLOCKED`; the tool does not delete unrelated data or empty the recycle bin to recover quota.
- verification/challenge requirements remain `BLOCKED` and require user interaction.

## Cleanup invariants remain unchanged

v0.7 must not make cleanup more permissive in order to improve resilience.

In particular:

- no generic Baidu delete function;
- no Drive delete/purge/sync cleanup;
- no recycle-bin clear;
- cleanup authorization remains durable and provenance-bound;
- batch/task-root fresh-list checks remain fail-closed;
- transient read retries may repeat inspection, but destructive delete itself is never blindly repeated by the transport layer.

## Release gate

v0.7 is accepted only when:

1. permanent Windows CI, Linux race tests, and CodeQL pass;
2. retry-policy tests prove bounded attempts, Retry-After handling, and cancellation;
3. pagination-stall/pathological-metadata tests pass;
4. large scanner fixtures pass;
5. prolonged deterministic mock migration passes;
6. the v0.6 destructive safety regression suite remains unchanged or stricter.
