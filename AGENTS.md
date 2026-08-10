# AGENTS.md

## Project mission

Build a safe, portable Windows CLI that moves arbitrary Baidu Netdisk share links to Google Drive through bounded, resumable staging.

## Priority order

1. Local machine/account safety.
2. Correctness and recoverability.
3. Preserve logical directory tree.
4. Simple user experience.
5. Transfer speed.

## Hard constraints

- User-facing entry is `BaiduDriveMover.exe`.
- Runtime writes are allowed only under `./temp/` beside the executable.
- No AppData/LocalAppData/user-profile fallback.
- No registry writes, services, scheduled tasks, startup entries, or hidden daemons.
- Never inspect or modify the user's normal Chrome profile.
- Never commit real cookies, tokens, extraction codes, browser profiles, task databases, logs, manifests, or downloaded data.
- Never delete unrelated local/Baidu/Drive data.
- Never automatically clear Baidu recycle bin.
- A file is complete only after Drive verification.
- The pipeline must survive Ctrl+C, process crash, network loss, and restart.
- Staging reconciliation must never rewind a file that already progressed downstream. It may accept a repeated staged observation only when the durable Baidu staging path is identical.
- The transfer pipeline must never consume a partial share manifest; `tasks.scan_completed` is durable authority for the scan boundary.
- Normal startup resumes the newest unfinished task. Bypassing an unfinished task to create another requires explicit `-new`; when no unfinished task exists, normal startup may prompt for a new link. Concurrent processes sharing one executable folder are rejected.
- Live acceptance starts with `-scan-only`; a successful scan-only run must return before constructing or calling staging, download, Drive, or cleanup runners. Migration begins only through an explicit later `-resume <task-id>`.

## Development discipline

- Follow `docs/IMPLEMENTATION_PLAN.md` milestone order.
- Follow `docs/VERSIONING.md` release gates.
- Update `docs/DESIGN_DECISIONS.md` before deliberately changing architecture.
- `main` should remain buildable.
- Add tests with behavior, especially for safety/recovery logic.
- Ordinary CI must not require real service credentials.
- Prefer small, reviewable commits with one concern each.
- Do not introduce large dependencies without a concrete benefit and runtime-path audit.

## Runtime path rule

All code that creates files/directories must go through a centralized runtime-root/path-containment layer. Avoid direct ad-hoc writes throughout the codebase.

All automatic deletion must go through a centralized provenance/containment guard.

## Networking/logging rule

- Never log cookies/tokens/Authorization headers/signed download URLs.
- Never send telemetry.
- Retry transient service failures with bounded backoff; do not spin aggressively.

## UI rule

Normal flow should stay simple:

1. launch executable;
2. paste share URL;
3. enter extraction code only if needed;
4. authorize accounts only when needed;
5. leave it running;
6. Ctrl+C safely stops; next launch resumes.

Technical diagnostics may exist behind explicit verbose/debug options, but secrets remain redacted.
