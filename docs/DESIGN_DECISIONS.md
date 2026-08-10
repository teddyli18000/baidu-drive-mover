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

## D19. v0.6 cleanup is batch-scoped and requires durable authorization

Decision: automatic Baidu/local cleanup is authorized and recovered at the isolated staging-batch level.

A batch can enter cleanup only when every assigned file has independently reached `DRIVE_VERIFIED` (or is already recovering from `CLEANUP_PENDING`) and has a persisted Drive ID. One SQLite transaction changes eligible files to `CLEANUP_PENDING` and grants `cleanup_allowed=1` only to the exact registered local cache files and Baidu batch directory.

Rules:

- no destructive local/Baidu action may run before that authorization transaction commits;
- Drive objects never receive cleanup authorization in v0.6;
- local cache deletion must re-derive and match `cache/<task-id>/<file-id>.bin` before using the runtime containment remover;
- Baidu deletion is limited to the registered `/BaiduDriveMover/<task-id>/<batch-id>` directory;
- already-missing authorized objects are reconciled idempotently rather than recreated;
- completion is recorded separately from authorization using durable cleanup outcome fields;
- the task-level `/BaiduDriveMover/<task-id>` root is cleaned only after every file is `DONE` and every batch directory is already reconciled clean;
- `/BaiduDriveMover` itself and the Baidu recycle bin are never deleted/emptied automatically.

Reason: the isolated batch is the smallest remote unit that can be safely removed without risking another staged file in the same directory.

## D20. v0.6 starts with a cooperative bounded pipeline scheduler

Decision: SQLite remains the coordination source of truth and the first v0.6 scheduler repeatedly pumps bounded work in downstream-first priority instead of introducing unbounded goroutine queues.

Priority:

1. cleanup verified batches to release cache reservations;
2. Drive upload/verification of local-ready files;
3. local download while below the cache watermark;
4. stage another bounded Baidu batch only when downstream capacity exists.

This provides continuous end-to-end progression and backpressure while keeping each existing stage independently idempotent. Controlled parallel workers may be introduced later only if the same database invariants and byte/file watermarks remain authoritative.

Reason: v0.6 adds the first destructive stage. Proving cleanup/recovery correctness before increasing cross-stage concurrency reduces the chance that a scheduling race becomes a deletion bug.

## D21. v0.7 retries only operations with read/reconcile semantics

Decision: bounded transport retry is exposed only through explicit Baidu read helpers. Ordinary page access, share listing, and PCS staging/cleanup inspection may retry typed network failures, HTTP 429, and HTTP 5xx with capped exponential or server-directed delay.

The retry helpers reject mutation methods and unknown POST endpoints before network I/O. Transfer, mkdir, password verification, and delete continue to use one-shot HTTP calls. Their stage-specific recovery must freshly reconcile durable local state with remote observations before a later mutation attempt.

Consequences:

- a generic HTTP layer cannot replay a transfer or deletion;
- `Retry-After` and exponential delay remain bounded and cancellable;
- malformed or oversized responses fail permanently instead of consuming the transient retry budget;
- new read endpoints must opt in explicitly and add tests proving both retry bounds and mutation exclusion.

## D22. A complete manifest is a durable pipeline prerequisite

Decision: schema v5 records `tasks.scan_completed`. Only the scanner may set it, atomically with the post-scan paused state. The pipeline refuses all work while it is false.

An interrupted scan is resumed with the same task ID and may repeat idempotent manifest pages. Migration from schema v4 defaults existing tasks to incomplete rather than inferring completeness from status or row counts.

Reason: a partial manifest may look internally consistent but would silently omit source files if transferred. Completion must be explicit evidence, not inference.

## D23. v0.8 packages one executable and verifies it independently

Decision: the Windows x64 ZIP contains only `BaiduDriveMover.exe`. Version, exact commit, and commit-derived build date are injected at build time. A separate verifier checks the ZIP allowlist, SHA-256, embedded identity, minimal-runtime launch, and first-run `./temp/` boundary.

The pinned rclone helper is not bundled; it is downloaded from the fixed official HTTPS origin and hash-verified into `temp/tools/` when Drive work first needs it. Manual workflow dispatch creates an artifact only. Public GitHub releases require a matching `v0.x.y` tag.

Reason: a one-file package preserves the visible product contract while independent verification prevents build output, dependency, or version drift from masquerading as a release.

## D24. Live acceptance has a durable read-only checkpoint

Decision: `-scan-only` completes or resumes the Baidu manifest scan, prints the durable task ID and statistics, and returns before migration runners are constructed. It is mutually exclusive with other operating modes. A user must later run `-resume <task-id>` to authorize the first staging write.

An already completed scan is not silently refreshed: the command reports its persisted manifest statistics. A fresh isolated runtime folder is therefore required when the acceptance goal is a new observation of a real share.

Reason: the live-test strategy requires inspection of a real manifest before any staging, Drive, or cleanup mutation. Keeping the checkpoint in the normal executable also makes the tested artifact and the eventual migration artifact identical.

## D25. Share-relative paths are independent of Baidu enumeration paths

Decision: the scanner carries separate remote and logical paths for every queued directory. The initial share listing is mapped to logical `/` even when Baidu exposes each entry using its source-account absolute path. All initial entries must resolve beneath one consistent remote parent; after that anchor is established, every nested entry must exactly match its queued remote parent plus its validated filename.

The remote path is used only to enumerate descendants. Manifest and Drive paths are built only from validated filenames beneath the logical parent, so source-account prefixes never leak into the migrated tree.

Reason: Baidu's root share response can expose a provider-internal absolute path that is not the share URL's logical root. Treating the two namespaces as identical either rejects valid shares or risks reproducing unrelated source prefixes. Separating them preserves the intended tree while retaining strict nested containment checks.

## D26. The last completed task removes the entire local runtime

Decision: after the pipeline has durably marked a task `COMPLETED`, the CLI checks the same SQLite database for any task whose status is not `COMPLETED`. If one exists, shared runtime state remains available for recovery. If none exists, a deferred finalizer runs only after signal handling, SQLite, logs, the instance lock, browser sessions, and rclone processes have closed, then deletes the exact executable-adjacent `temp/` through the centralized containment layer.

This final deletion includes the dedicated Chrome profile, Baidu cookies, Google OAuth configuration, managed rclone helper, caches, logs, manifests, and task database. It never targets the executable directory, normal Chrome profile, Drive destination, unrelated Baidu data, or recycle bin. Scan-only, interruption, failure, and blocked states do not arm the finalizer. Successful `-check` and `-list` diagnostics may arm the same finalizer only when the database contains no non-completed task, so a fresh diagnostic invocation does not leave an otherwise unnecessary runtime tree.

Reason: all local runtime artifacts are private and tool-owned, but some are also required to resume safely. Treating durable completion plus the absence of every other non-completed task as the deletion boundary removes local residue without sacrificing crash recovery or another task's state.

## D27. Share bootstrap parsing follows bounded embedded JSON strings

Decision: share-page parsing checks both direct JSON objects and plausible embedded strings decoded from those objects. Embedded traversal is bounded by nesting depth, total scanned bytes, object count, object bytes, and enclosing-object depth. A valid context still requires `loginstate`, `bdstoken`, `shareid`, and `share_uk` in one decoded object; values from separate objects are never combined.

Reason: a live Baidu share page returned HTTP 200 with its complete `locals.mset(...)` bootstrap inside `locals.share[]`. Treating only direct page objects as authoritative falsely classified an authenticated session as logged out and prompted for a second QR login. Bounded recursive parsing accepts the provider's alternate serialization without turning the HTML parser into an unbounded general-purpose evaluator.

## D28. PAN and PCS requests use separate provider application identities

Decision: authenticated PAN Web requests, including share enumeration, transfer, and same-account copy, use application ID `250528`. Legacy PCS file operations for isolated staging (`mkdir`, `list`, `download`, and `delete`) use application ID `266719`. The IDs are distinct constants and tests assert the correct identity at each API boundary.

Reason: a live authenticated session returned PCS error `31030` for every read and write made with the PAN application ID. The identical bounded PCS list request made with `266719` returned the expected `31066` not-found result for an absent tool root, proving that the session and endpoint were valid but the request used the wrong provider application identity. Keeping identities endpoint-specific prevents a working share API setting from silently breaking staging and cleanup.

## D29. A share owned by the logged-in account uses bounded internal copy

Decision: when the authenticated account `uk` exactly equals the share owner `share_uk`, staging does not call `/share/transfer`. For each bounded subset, the client re-enumerates the share through the hardened read-only scanner, resolves every requested immutable `fs_id` to one validated provider path, and submits a one-shot PAN `filemanager copy` into the same isolated batch directory. More than 100 items are split before mutation. Normal reconciliation remains authoritative after the copy and before any later attempt.

The provider source path is carried only through the in-memory manifest page used for resolution; it never replaces the share-relative logical path or becomes a Drive path. Missing, duplicate, conflicting, or unsafe source identities stop before the copy request.

Reason: Baidu returns parameter error `2` when an account attempts to save its own share through `/share/transfer`, even though authentication, source listing, and the isolated destination are valid. An account-internal copy is the equivalent bounded staging operation for files the logged-in account already owns, while preserving the existing download, Drive verification, and cleanup state machine.

## D30. Only canonical provider MD5 values are source evidence

Decision: a Baidu share-list `md5` is accepted only when its trimmed value is exactly 32 hexadecimal characters; accepted values are normalized to lowercase. Any other non-empty value is treated as unavailable rather than as a checksum. Download still enforces exact size, computes the local MD5, and Drive completion still requires equality with Drive's independently returned MD5.

Schema v6 clears previously persisted non-canonical provider MD5 values. It narrowly recovers a staged file from `FAILED_PERMANENT` only when that invalid value caused a recorded cache-MD5 mismatch, retaining the cache path for fresh validation. Unrelated permanent failures remain permanent.

Reason: live share enumeration returned a 32-character provider field containing non-hexadecimal characters for every file. Comparing that opaque value to a real MD5 falsely rejected correct downloads. Treating only canonical digests as optional source evidence preserves fail-closed Drive verification without granting an undocumented provider token checksum authority.
