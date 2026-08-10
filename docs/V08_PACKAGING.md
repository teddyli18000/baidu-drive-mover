# v0.8 Packaged Windows Beta

Status: implementation candidate; release publication requires an explicitly authorized `v0.8.0` tag after all gates pass.

## Product contract

The user downloads one Windows x64 ZIP containing exactly:

```text
BaiduDriveMover.exe
```

No Go, Python, Node.js, Conda, WSL, installer, service, registry entry, scheduled task, or global helper installation is required. Runtime state and the verified rclone helper remain under `./temp/` beside the executable. Chrome remains the interactive Baidu-login prerequisite.

## Resume contract

Every task records whether the complete source manifest was observed. A partial scan may be safely repeated with the same task ID, but it can never enter the transfer pipeline. Normal startup resumes the most recently updated unfinished task; `-list`, `-resume <task-id>`, and `-new` provide explicit control. A process-level file lock rejects a second instance using the same folder.

Schema v5 adds `tasks.scan_completed`. Migration from v4 is deliberately fail-closed: existing records default to an incomplete scan, and every unfinished task must be reconciled by re-scanning before any further transfer. Already completed or permanently failed tasks remain terminal and are not auto-replayed.

## Build contract

`scripts/build-release.ps1` requires an explicit SemVer, full commit SHA, and commit-derived build timestamp. It cross-builds with `CGO_ENABLED=0`, `GOOS=windows`, `GOARCH=amd64`, `-trimpath`, and `-buildvcs=false`. It refuses to overwrite existing output and packages only the executable.

ZIP metadata uses the supplied commit-derived timestamp. Release CI builds the package twice from the same source and requires identical ZIP SHA-256 values.

`scripts/verify-release.ps1` independently proves:

- the checksum manifest names exactly the ZIP;
- the ZIP SHA-256 matches;
- the ZIP contains exactly `BaiduDriveMover.exe`;
- `-version` matches the requested version and exact commit;
- `-check` runs with developer runtimes removed from `PATH`;
- first run creates only `./temp/` beside the executable;
- local safety check does not provision rclone or require live credentials.

Normal PR CI runs this package smoke gate. `.github/workflows/release.yml` also uploads the verified ZIP and checksum as a workflow artifact. Only an exact `v0.x.y` tag may publish a GitHub release; manual dispatch builds an artifact without publishing.

## Release gate

v0.8 is accepted only when:

1. the complete credential-free Go suite, Windows CI, Linux race, and CodeQL pass;
2. schema v4-to-v5 migration and incomplete-scan rejection pass;
3. CLI selection proves same-task resume and explicit new-task behavior;
4. the package allowlist, identity binding, SHA-256, minimal-runtime smoke, and `./temp/` boundary pass;
5. the package works from a clean writable Windows x64 folder without developer tooling;
6. user documentation explains Chrome/accounts/network prerequisites, OAuth, Drive root behavior, sensitive `temp/` state, resume modes, and folder-removal consequences.

Real Baidu/Drive migration remains the separate v0.9 acceptance ladder. Credential-free packaging evidence must not be described as live-service proof.
