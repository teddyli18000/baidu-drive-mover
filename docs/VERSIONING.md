# Versioning and Release Plan

BaiduDriveMover uses Semantic Versioning: `MAJOR.MINOR.PATCH`.

## Version meaning before 1.0

- `0.x.0`: a new capability milestone.
- `0.x.y`: fixes/hardening within that milestone.
- Pre-1.0 releases may change internal formats, but state database migrations must be explicit and tested once real user tasks exist.

## Planned milestones

### v0.1.0 - Foundation

- executable skeleton
- strict `./temp/` runtime root
- CLI shell
- SQLite state model
- safe logging/redaction
- Windows CI/unit tests

Release gate: no runtime write outside `./temp/` in tests.

### v0.2.0 - Read-only Baidu

- arbitrary share-link parsing
- extraction code handling
- dedicated Chrome session management
- Baidu authentication
- recursive share scan
- manifest persistence

Release gate: scanner fixtures cover deep trees, root files + directories, pagination, empty directories, and invalid/expired shares.

### v0.3.0 - Baidu Staging

- tool-owned Baidu staging root
- file-level batch planner
- transfer-limit handling
- split/retry isolation

Release gate: no unrelated Baidu path mutation; single-directory >500-file planning verified.

### v0.4.0 - Download Engine

- staged-file download
- resumable partial files
- opaque cache filenames
- bounded local cache

Release gate: forced termination during download resumes without corruption or duplicate completion.

### v0.5.0 - Google Drive

- Drive OAuth
- per-task root folder
- directory reconstruction
- upload/retry
- verification

Release gate: uploaded files are not marked verified until size/hash/available destination evidence passes the defined verifier.

### v0.6.0 - End-to-End Pipeline

- concurrent staging/download/upload/verify/cleanup
- backpressure
- graceful shutdown
- restart recovery

Release gate: automated fault-injection tests across every pipeline state.

### v0.7.0 - Hardening

- quota/rate-limit/auth-expiry handling
- name/path edge cases
- duplicates
- huge shares
- service outages

Release gate: safety regression suite and prolonged mock migration pass.

### v0.8.0 - Packaged Windows Beta

- user-ready Windows package
- single visible executable entry
- clean first-run/auth experience
- release workflow and checksums

Release gate: clean Windows VM can run without Go/Python/Node/Conda/WSL.

### v0.9.0 - Real-world Beta

- large live migrations
- multi-hour/multi-restart validation
- UX simplification

Release gate: at least one large real task completes after multiple intentional interruptions with tree and verification intact.

### v1.0.0 - Stable

Release only when:

- arbitrary supported share links work end-to-end;
- directory structure is preserved;
- >500-file cases are handled correctly;
- cache/staging are bounded;
- restart recovery is proven;
- no unrelated local/Baidu/Drive files are modified;
- normal usage requires only the packaged executable and interactive account authorization when necessary.

## Branch/release policy

- `main` must stay buildable.
- Feature work may use short-lived branches/PRs when changes are non-trivial.
- CI must pass before milestone tags/releases.
- Never publish a release containing real credentials, task databases, browser profiles, logs, or local runtime data.

## Compatibility policy

After `v0.5.0`, task database migrations become compatibility-sensitive. Any schema change must include migration tests from the previous released schema.

After `v0.8.0`, package layout and first-run UX should remain stable unless a safety issue requires change.
