[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$ZipPath,
    [Parameter(Mandatory = $true)][string]$ChecksumsPath,
    [Parameter(Mandatory = $true)][string]$ExpectedVersion,
    [Parameter(Mandatory = $true)][ValidatePattern('^[0-9a-fA-F]{40}$')][string]$ExpectedCommit,
    [string]$WorkRoot = ''
)

$ErrorActionPreference = 'Stop'
$zip = (Resolve-Path -LiteralPath $ZipPath).Path
$checksums = (Resolve-Path -LiteralPath $ChecksumsPath).Path
$expectedLine = (Get-Content -LiteralPath $checksums -Raw).Trim()
$expectedParts = $expectedLine -split '\s+', 2
if ($expectedParts.Count -ne 2 -or $expectedParts[1] -ne [System.IO.Path]::GetFileName($zip)) {
    throw "Checksum manifest must contain exactly the release ZIP filename"
}
$actualHash = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actualHash -ne $expectedParts[0].ToLowerInvariant()) {
    throw "Release ZIP SHA-256 mismatch: $actualHash"
}

Add-Type -AssemblyName System.IO.Compression.FileSystem
$archive = [System.IO.Compression.ZipFile]::OpenRead($zip)
try {
    $entries = @($archive.Entries)
    if ($entries.Count -ne 1 -or $entries[0].FullName -ne 'BaiduDriveMover.exe') {
        throw "Release ZIP allowlist violation: $($entries.FullName -join ', ')"
    }
}
finally {
    $archive.Dispose()
}

if ([string]::IsNullOrWhiteSpace($WorkRoot)) {
    $WorkRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('bdm-release-smoke-' + [Guid]::NewGuid().ToString('N'))
}
$work = [System.IO.Path]::GetFullPath($WorkRoot)
if (Test-Path -LiteralPath $work) {
    throw "Smoke WorkRoot must not already exist: $work"
}
$appRoot = Join-Path $work 'app'
New-Item -ItemType Directory -Force $appRoot | Out-Null
Expand-Archive -LiteralPath $zip -DestinationPath $appRoot
$exe = Join-Path $appRoot 'BaiduDriveMover.exe'

$savedPath = $env:PATH
$savedGoRoot = $env:GOROOT
$savedGoPath = $env:GOPATH
$savedNode = $env:NODE_PATH
$savedPython = $env:PYTHONPATH
$savedAppData = $env:APPDATA
$savedLocalAppData = $env:LOCALAPPDATA
$savedUserProfile = $env:USERPROFILE
$savedTemp = $env:TEMP
$savedTmp = $env:TMP
$isolatedProfile = Join-Path $work 'isolated-profile'
$isolatedTemp = Join-Path $work 'isolated-temp'
New-Item -ItemType Directory -Force $isolatedProfile, $isolatedTemp | Out-Null
try {
    $env:PATH = "$env:SystemRoot\System32;$env:SystemRoot"
    $env:GOROOT = 'Z:\missing-go'
    $env:GOPATH = 'Z:\missing-gopath'
    $env:NODE_PATH = 'Z:\missing-node'
    $env:PYTHONPATH = 'Z:\missing-python'
    $env:USERPROFILE = $isolatedProfile
    $env:APPDATA = Join-Path $isolatedProfile 'AppData\Roaming'
    $env:LOCALAPPDATA = Join-Path $isolatedProfile 'AppData\Local'
    $env:TEMP = $isolatedTemp
    $env:TMP = $isolatedTemp

    Push-Location $appRoot
    try {
        $versionOutput = & $exe -version
        if ($LASTEXITCODE -ne 0) { throw "packaged -version failed with exit code $LASTEXITCODE" }
        $expectedPrefix = "BaiduDriveMover $ExpectedVersion (commit $($ExpectedCommit.ToLowerInvariant()), built "
        if (-not $versionOutput.StartsWith($expectedPrefix, [System.StringComparison]::Ordinal)) {
            throw "packaged version binding mismatch: $versionOutput"
        }

        $checkOutput = & $exe -check
        if ($LASTEXITCODE -ne 0) { throw "packaged -check failed with exit code $LASTEXITCODE" }
        if (($checkOutput -join "`n") -notmatch 'local safety check passed') {
            throw "packaged -check did not report success"
        }
    }
    finally {
        Pop-Location
    }
}
finally {
    $env:PATH = $savedPath
    $env:GOROOT = $savedGoRoot
    $env:GOPATH = $savedGoPath
    $env:NODE_PATH = $savedNode
    $env:PYTHONPATH = $savedPython
    $env:APPDATA = $savedAppData
    $env:LOCALAPPDATA = $savedLocalAppData
    $env:USERPROFILE = $savedUserProfile
    $env:TEMP = $savedTemp
    $env:TMP = $savedTmp
}

$rootNames = @((Get-ChildItem -LiteralPath $appRoot -Force).Name | Sort-Object)
if (($rootNames -join ',') -ne 'BaiduDriveMover.exe,temp') {
    throw "Packaged first run wrote outside the allowed ./temp boundary: $($rootNames -join ', ')"
}
if (Test-Path -LiteralPath (Join-Path $appRoot 'temp\tools\rclone\rclone.exe')) {
    throw "Local safety check unexpectedly provisioned a network helper"
}
$externalWrites = @(
    Get-ChildItem -LiteralPath $isolatedProfile, $isolatedTemp -Force -Recurse |
        Select-Object -ExpandProperty FullName
)
if ($externalWrites.Count -ne 0) {
    throw "Packaged first run wrote through a user-profile or system-temp path: $($externalWrites -join ', ')"
}

[pscustomobject]@{
    Package = $zip
    SHA256 = $actualHash
    Version = $ExpectedVersion
    Commit = $ExpectedCommit.ToLowerInvariant()
    SmokeRoot = $work
}
