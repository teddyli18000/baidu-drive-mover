# Release scripts

## Purpose

`build-release.ps1` creates the Windows amd64 ZIP and checksum. `verify-release.ps1` independently checks its allowlist, hash, embedded version/commit, minimal-runtime execution, and first-run write boundary.

## Invariants

- The ZIP contains exactly `BaiduDriveMover.exe`.
- Release identity is injected from an exact SemVer, 40-character commit SHA, and commit-derived UTC build time.
- ZIP entry timestamps are fixed to the supplied build time; tagged release CI builds twice and requires identical ZIP SHA-256 values.
- Smoke tests run with developer runtimes removed from `PATH`; writes may occur only under `./temp/`, and a successful idle `-check` must remove that runtime before exit.
- Never overwrite an existing output directory or artifact; use a fresh task-owned output root.
- Tagged builds are the only workflow path allowed to publish a GitHub release.

## Validation

From the repository root with Go 1.26.x on `PATH`:

```powershell
$commit = (git rev-parse HEAD).Trim()
$date = (git show -s --format=%cI HEAD).Trim()
./scripts/build-release.ps1 -Version 0.8.0-dev -Commit $commit -BuildDate $date -OutputRoot build/release-smoke
./scripts/verify-release.ps1 -ZipPath build/release-smoke/BaiduDriveMover-0.8.0-dev-windows-amd64.zip -ChecksumsPath build/release-smoke/SHA256SUMS.txt -ExpectedVersion 0.8.0-dev -ExpectedCommit $commit
```
