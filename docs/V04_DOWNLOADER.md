# v0.4 Downloader Design

This milestone adds the local Baidu download/cache engine. It does not upload to Google Drive or automatically delete Baidu staging yet.

## Goals

- Download only files already confirmed `BAIDU_STAGED`.
- Resume interrupted downloads with HTTP Range when the service honors it.
- Store bytes only under executable-adjacent `temp/cache/`.
- Never use original source filenames as Windows filesystem names.
- Verify size and, when available, MD5 before marking `LOCAL_READY`.
- Persist enough state that process termination/reboot can safely resume.
- Bound local cache usage so upstream work cannot consume unlimited disk.

## Cache layout

```text
temp/cache/<task-id>/<file-id>.part
temp/cache/<task-id>/<file-id>.bin
```

`task-id` and `file-id` are internal validated identifiers. The original logical name/path remains SQLite metadata only.

Database stores cache paths relative to `temp/`, not absolute paths, so moving the whole application folder does not invalidate tasks.

## Download transport

Use Baidu PCS file download semantics referenced by BaiduPCS-Go v4.0.1:

```text
GET /rest/2.0/pcs/file
  ?method=download
  &app_id=250528
  &path=<staged remote path>
```

Resume request:

```text
Range: bytes=<existing-part-size>-
```

Rules:

- offset 0 accepts a normal successful body;
- offset > 0 requires a valid partial response matching the requested start;
- if the server ignores Range, discard/restart the partial file instead of appending corrupt bytes;
- redirects are allowed through the existing authenticated HTTP client;
- signed/redirect download URLs are never logged.

## Recovery

For each candidate:

1. Resolve the opaque `.part`/`.bin` path through the runtime-root containment layer.
2. If `.bin` exists, verify expected size/hash; only then reconcile DB to `LOCAL_READY`.
3. If `.part` exists:
   - size > expected => quarantine/remove tool-owned partial and restart;
   - size == expected => verify and finalize;
   - size < expected => request Range resume.
4. Stream response to `.part`.
5. Flush/close.
6. Verify exact expected size.
7. If source MD5 is available, compute local MD5 and require equality.
8. Atomically rename `.part` -> `.bin` inside the same cache directory.
9. Persist `LOCAL_READY` and the relative `.bin` path.

No local file is uploaded in later milestones unless it is `LOCAL_READY`.

## Retry policy

- Transient network/HTTP failures: bounded retry with backoff.
- Authentication failure: bubble to app session refresh, do not spin.
- Range mismatch: restart from zero once safely.
- Size/hash mismatch: retry from zero with a small bounded budget; repeated mismatch becomes permanent for that file.
- Context cancellation: leave `.part` intact and state recoverable.

## Cache watermark

Default reserved-cache target for development: 30 GiB.

Reservation uses expected full sizes of files whose local bytes must still be retained, rather than only currently written bytes. This prevents several downloads from overcommitting the cache concurrently.

Admission rules:

- If admitting the next file would exceed the watermark and some cache is already reserved, pause upstream admission.
- A single file larger than the watermark is not silently downloaded in v0.4; it reports a clear oversized-cache blocker. A later hardening milestone may add disk-free-aware single-file override/direct streaming.
- v0.4 uses conservative one-file-at-a-time Baidu download execution.

## State transitions

```text
BAIDU_STAGED
  -> DOWNLOADING
  -> LOCAL_READY
```

On recoverable failure, state may remain `DOWNLOADING` with retry metadata so restart resumes the partial file. Repeated deterministic corruption becomes `FAILED_PERMANENT`.

## v0.4 CLI safety

Because Google Drive draining is not available yet, v0.4 is not allowed to fill the entire share into local cache unattended.

Development CLI behavior is bounded:

- stage/download only within the configured local-cache watermark;
- when the watermark prevents the next admission, stop cleanly with the task persisted;
- do not delete local or Baidu staging objects automatically.

The endless producer/consumer loop is enabled only after Drive upload/verification exists in later milestones.

## Test gates

Mock/fixture tests must cover:

- fresh download;
- Range resume;
- Range ignored -> safe restart;
- interrupted copy leaves resumable `.part`;
- exact-size completion;
- MD5 success/mismatch;
- oversized/corrupt partial recovery;
- opaque cache names for Windows-invalid source names;
- cache watermark admission;
- restart reconciliation from existing `.part` and `.bin`;
- no writes outside `temp/`;
- auth/HTTP/network error classification.

No real Baidu credentials are used by CI.