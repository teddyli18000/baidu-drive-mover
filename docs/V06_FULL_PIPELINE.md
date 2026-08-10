# v0.6 Full Pipeline Design (implemented)

v0.6 closes the production pipeline after v0.5 established independently verified Google Drive uploads. The milestone adds bounded pipeline scheduling, restart reconciliation, graceful cancellation, and automatic cleanup of **only** tool-owned local/Baidu staging data.

## Safety objective

Deletion is never inferred from process success or an in-memory decision. Before any destructive operation, SQLite must durably prove all of the following:

1. the target belongs to the same task;
2. the target path/object identity was registered by this tool;
3. all files protected by that target have independently reached `DRIVE_VERIFIED`;
4. cleanup authorization was committed before deletion starts.

Google Drive destination objects are never cleanup targets in v0.6.

## Cleanup unit: one Baidu staging batch

Baidu staging is already isolated as:

```text
/BaiduDriveMover/<task-id>/<batch-id>/
```

Therefore v0.6 cleans a batch as one recovery unit rather than deleting individual staged files.

A batch becomes cleanup-eligible only when every file assigned to it is `DRIVE_VERIFIED` or is already recovering from `CLEANUP_PENDING`.

This prevents deletion of a batch directory while another file in that directory still needs download or Drive upload.

## Durable cleanup protocol

### Phase 1: authorize

One SQLite transaction must:

- re-read every file in the batch;
- reject mixed/incomplete states;
- require a persisted non-empty Drive ID for every file;
- change `DRIVE_VERIFIED -> CLEANUP_PENDING`;
- verify the exact registered `baidu_batch_dir` provenance row;
- verify/register exact opaque local cache provenance rows;
- set `cleanup_allowed=1` for the batch directory and its local cache files;
- leave Drive provenance rows with cleanup disabled.

No filesystem or Baidu delete occurs before this transaction commits.

### Phase 2: execute idempotently

For each authorized batch:

1. delete each registered local cache `.bin` file through the runtime-root containment layer;
2. treat an already-missing registered local file as successful reconciliation;
3. delete the registered Baidu batch directory using only the validated `/BaiduDriveMover/<task-id>/<batch-id>` path;
4. reconcile an already-missing Baidu batch directory as successful cleanup when absence can be established;
5. persist cleanup completion and move all batch files to `DONE`.

If the process stops between any two steps, the batch remains `CLEANUP_PENDING` and is retried from durable provenance on restart.

`CLEANUP_PENDING` is intentionally conservative for cache accounting: bytes remain reserved until the whole batch cleanup transaction is completed.

## Task-root cleanup

The task-level Baidu root `/BaiduDriveMover/<task-id>` is authorized for deletion only after:

- every file is `DONE`;
- every registered `baidu_batch_dir` is recorded cleaned;
- no incomplete batch remains.

The tool never deletes `/BaiduDriveMover` itself and never clears the Baidu recycle bin.

Task status advances to `COMPLETED` only after all files are `DONE` and task-owned cleanup has been reconciled.

## Schema v4

v0.6 extends `owned_objects` with durable cleanup outcome fields:

```text
cleaned_at TEXT NOT NULL DEFAULT ''
last_error TEXT NOT NULL DEFAULT ''
```

`cleanup_allowed=1` means destructive cleanup has been explicitly authorized; it does not mean cleanup completed.

`cleaned_at != ''` is the durable evidence that the registered object was reconciled as removed.

The v3 -> v4 migration must preserve all existing v0.5 task/file/Drive IDs and leave every pre-existing object with cleanup disabled and no cleaned timestamp.

## Local cache provenance

New local-ready files register a tool-owned object:

```text
scope       = local_cache_file
object_id   = <file-id>
object_path = cache/<task-id>/<file-id>.bin
```

The cleanup authorizer must independently re-derive the expected opaque path and reject any database path that differs. Existing v0.5 state may be backfilled only when the deterministic path can be proven from task/file IDs.

Partial `.part` files are recovery artifacts, not cleanup evidence for a verified batch. They remain under the existing runtime containment rules and may be removed only by explicit recovery logic.

## Baidu deletion boundary

The Baidu adapter adds one narrowly scoped delete operation for staging paths.

Requirements:

- canonical path validation must pass first;
- accepted targets are only the current task root or its registered batch directories below `/BaiduDriveMover/`;
- no arbitrary caller-provided path is forwarded;
- no recycle-bin emptying operation exists;
- authentication/quota/service errors remain retryable/blocking rather than widening deletion scope.

## Scheduler and backpressure

v0.6 uses SQLite as the source of truth and repeatedly pumps bounded stage work. The safe priority is:

1. cleanup verified batches to release cache pressure;
2. upload/verify local-ready files;
3. download staged files while under the cache watermark;
4. stage additional bounded Baidu batches only when downstream capacity is available.

The first implementation may use cooperative bounded stage passes. Parallel workers may be added only after each stage is idempotent and the same DB invariants prevent overproduction.

No stage may hold an unbounded in-memory queue. Cache reservation remains the authoritative byte watermark.

## Graceful Ctrl+C

Cancellation stops creation of new work and propagates to active network/process operations. It must not rewrite an interrupted operation as success.

State already persisted before interruption is retained. On the next launch, recovery re-observes local, Baidu, and Drive facts and resumes from the last provable state.

Cleanup code must use durable `CLEANUP_PENDING` state so Ctrl+C immediately after authorization is safe.

## Fault-injection release gate

Automated tests must inject failure/cancellation at least at these boundaries:

- `DRIVE_VERIFIED` before cleanup authorization;
- authorization committed before any delete;
- some local cache files deleted, then interruption;
- all local files deleted, Baidu delete fails;
- Baidu delete succeeds, DB completion write fails;
- restart with local file already absent;
- restart with Baidu batch already absent;
- wrong/unregistered local path;
- wrong/unregistered Baidu path;
- batch containing one unverified file;
- missing Drive ID on an otherwise verified file;
- task-root cleanup attempted before all batches are done;
- cancellation during staging/download/upload/cleanup.

Every fault case must prove that no unrelated local, Baidu, or Drive object becomes eligible for deletion and that restart either resumes safely or stops with an explicit blocked/failure state.

## Explicit non-goals

v0.6 does not:

- delete any Google Drive destination object;
- clear the Baidu recycle bin;
- remove the global `/BaiduDriveMover` directory;
- introduce background services, scheduled tasks, or hidden daemons;
- widen Google OAuth beyond `drive.file`;
- treat synthetic tests as proof of real-account cleanup safety without a later controlled live acceptance step.
