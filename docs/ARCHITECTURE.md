# Architecture

## 1. System goal

BaiduDriveMover is a portable Windows migration pipeline:

```text
Baidu share
  -> logical manifest
  -> bounded Baidu staging
  -> bounded local cache
  -> Google Drive task root
  -> verification
  -> tool-owned cleanup
```

The user-facing goal is one executable and minimal interaction. The engineering goal is safe interruption/restart without losing directory structure or touching unrelated data.

## 2. Runtime boundary

All persistent/runtime files are confined to the executable directory's `temp/` subtree:

```text
BaiduDriveMover.exe

temp/
  state.db
  config.json
  auth/
  chrome-profile/
  cache/
  logs/
  tasks/
  tools/
```

The runtime-root manager is a foundational component. Other packages do not invent persistent paths directly.

If containment cannot be guaranteed, startup fails rather than falling back to a global/user location.

## 3. Main components

### 3.1 App / CLI

Responsibilities:

- first-run initialization;
- task selection/resume;
- share-link input;
- extraction-code prompt only when needed;
- auth prompts only when needed;
- concise progress/status;
- graceful Ctrl+C.

### 3.2 Runtime Root Manager

Responsibilities:

- resolve executable directory;
- create/validate `temp/`;
- provide typed subpaths;
- path containment checks;
- reject traversal/escape;
- centralize tool-owned deletion guards.

### 3.3 State Store

SQLite with schema versioning.

Durable entities:

- task;
- logical directory;
- logical file;
- transfer batch;
- Baidu staged object;
- local cache object;
- Drive object;
- retry/error history sufficient for recovery.

Important invariant: destructive cleanup is allowed only after the database contains durable provenance and Drive verification state.

### 3.4 Baidu Session Manager

Responsibilities:

- reusable local cookie/session state;
- dedicated Chrome profile under `temp/chrome-profile/` when interactive login is required;
- authentication refresh detection;
- no access to the user's normal Chrome profile.

### 3.5 Baidu Share Scanner

Responsibilities:

- parse arbitrary supported share URLs;
- use extraction code from URL when present;
- signal CLI prompt when password is required and absent;
- recursively enumerate all directories and individual files;
- paginate correctly;
- retain source logical paths, sizes, IDs and hashes when available;
- persist empty directories;
- never flatten root files + child directories.

Output: durable logical manifest.

### 3.6 Batch Planner

Input: manifest files not yet staged.

Rules:

- group by logical parent directory;
- split into conservative transfer batches below service limits;
- a logical directory can span unlimited batches;
- do not use one directory ID as a shortcut around per-transfer limits;
- on transfer-limit/partial failure, recursively split a batch until the failing subset/file is isolated;
- planner must be deterministic/idempotent for restart.

### 3.7 Baidu Staging Adapter

Tool-controlled remote root:

```text
/BaiduDriveMover/<task-id>/...
```

Responsibilities:

- create only tool-owned target directories;
- transfer batch files into matching logical parent paths;
- record returned/observed staged paths/IDs;
- enforce staging file/byte watermarks;
- expose only verified staged files to downloader;
- delete only registered tool-owned staging objects after destination verification.

It never clears the recycle bin automatically.

### 3.8 Local Download Engine

Responsibilities:

- locate/download staged files;
- resumable partial download;
- opaque local filenames based on internal IDs;
- bounded cache bytes;
- integrity/size checks where available;
- never materialize arbitrary Baidu filenames as local filesystem paths.

Example:

```text
temp/cache/<task-id>/<file-id>.part
temp/cache/<task-id>/<file-id>.bin
```

The original destination filename exists only as metadata.

### 3.9 Google Drive Adapter

Responsibilities:

- first-run OAuth;
- create one new task root folder;
- rebuild the logical directory tree at destination;
- resumable/retry-capable uploads;
- preserve destination names as Drive permits;
- verify uploaded object before completion;
- store destination IDs/metadata for reconciliation.

Initial implementation favors a pinned rclone helper managed entirely under `temp/`; direct Drive API remains a fallback design if runtime-path or UX constraints cannot be met.

### 3.10 Pipeline Orchestrator

Coordinates independent bounded stages:

```text
scanner -> planner -> Baidu staging -> download -> Drive upload -> verify -> cleanup
```

Queues are durable or reconstructable from SQLite state rather than process memory alone.

## 4. Backpressure

Pipeline concurrency must never let upstream stages consume unlimited space.

Controls:

- maximum outstanding Baidu staged bytes;
- maximum outstanding Baidu staged file count;
- maximum local cache bytes;
- bounded active transfer batches;
- conservative Baidu download workers;
- bounded Drive upload workers.

Example behavior:

- Drive slow -> local cache reaches high-water mark -> downloader pauses -> Baidu staging eventually pauses.
- Baidu staging quota pressure -> planner waits while downstream drains verified batches.

Configuration values are initially internal defaults; user-facing tuning is not required for v1.0 normal use.

## 5. Durable file lifecycle

```text
DISCOVERED
  -> PLANNED
  -> BAIDU_STAGING
  -> BAIDU_STAGED
  -> DOWNLOADING
  -> LOCAL_READY
  -> DRIVE_UPLOADING
  -> DRIVE_UPLOADED
  -> DRIVE_VERIFIED
  -> CLEANUP_PENDING
  -> DONE
```

Failures branch into retryable/permanent states with explicit reason.

Key invariant:

```text
No DRIVE_VERIFIED -> no automatic source/cache cleanup.
```

## 6. Recovery

Startup recovery is reconciliation, not blind continuation.

Examples:

- `DOWNLOADING`: inspect partial local object and resume/restart safely.
- `LOCAL_READY`: validate local object before upload.
- `DRIVE_UPLOADING`: query/reconcile destination rather than assume success/failure.
- `DRIVE_UPLOADED`: run verifier.
- `DRIVE_VERIFIED`: cleanup may continue.
- cleanup interruption: only registered task-owned objects may be retried.

A completed task requires every logical file to end in `DONE` and every required logical directory to exist at the destination.

## 7. Path model

Three path namespaces are kept separate:

1. `logical_path` — source/destination tree using service path semantics.
2. `baidu_staging_path` — tool-owned temporary remote path.
3. `local_cache_path` — opaque safe path under `temp/cache/`.

Never convert an untrusted logical filename directly into a local Windows path.

## 8. Safety boundaries

### Local

- persistent writes only under `temp/`;
- canonical containment check before create/delete;
- no execution of downloaded user content.

### Baidu

- writes/deletes only inside tool-owned staging root;
- no unrelated file mutation;
- no recycle-bin clearing.

### Drive

- create/write within task destination tree;
- no unrelated deletion;
- destination is source of truth for final completion only after verification.

## 9. Dependency strategy

- Go standard library first.
- SQLite via a pure-Go implementation when practical.
- BaiduPCS-Go behavior/exported APIs reused at a pinned upstream revision where they reduce risk.
- Custom scanner/batching/orchestration owned by this project.
- rclone considered a managed/pinned helper for Drive.
- dependencies that cannot keep runtime files under `temp/` are rejected or replaced.

## 10. CI architecture

Normal CI is credential-free:

- synthetic share trees;
- fake Baidu HTTP behavior;
- fake Drive behavior;
- state/recovery fault injection;
- Windows path-safety tests;
- build/vet/test checks.

Live service tests are separate acceptance tests and never run on untrusted PRs.

## 11. Release architecture

Normal user installation is folder-based. No installer is required for v1.0.

Target release experience:

```text
BaiduDriveMover.exe
```

On first run it creates only:

```text
temp/
```

Removing the application folder removes all local tool state. No system cleanup procedure should be necessary.

## 12. Source-of-truth documents

- `IMPLEMENTATION_PLAN.md` — execution order
- `VERSIONING.md` — milestone/release gates
- `DESIGN_DECISIONS.md` — fixed technical choices
- `SECURITY.md` — security/repository boundaries
- `TESTING.md` — validation strategy
- root `AGENTS.md` — development discipline
