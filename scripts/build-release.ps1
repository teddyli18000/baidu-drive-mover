[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:(?:0|[1-9][0-9]*)|(?:[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))(?:\.(?:(?:0|[1-9][0-9]*)|(?:[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)))*)?$')]
    [string]$Version,

    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[0-9a-fA-F]{40}$')]
    [string]$Commit,

    [Parameter(Mandatory = $true)]
    [string]$BuildDate,

    [string]$OutputRoot = 'dist'
)

$ErrorActionPreference = 'Stop'
$parsedDate = [DateTimeOffset]::MinValue
if (-not [DateTimeOffset]::TryParse($BuildDate, [ref]$parsedDate)) {
    throw "BuildDate must be RFC3339-compatible: $BuildDate"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$outputRootPath = [System.IO.Path]::GetFullPath((Join-Path $repoRoot $OutputRoot))
if (-not $outputRootPath.StartsWith($repoRoot + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "OutputRoot must remain inside the repository: $outputRootPath"
}
$current = $outputRootPath
while ($current.StartsWith($repoRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
    if (Test-Path -LiteralPath $current) {
        $item = Get-Item -LiteralPath $current -Force
        if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "OutputRoot must not traverse a reparse point: $current"
        }
    }
    if ($current -eq $repoRoot) { break }
    $current = [System.IO.Path]::GetDirectoryName($current)
}

Push-Location $repoRoot
try {
    $headCommit = (& git rev-parse HEAD).Trim().ToLowerInvariant()
    if ($LASTEXITCODE -ne 0 -or $headCommit -ne $Commit.ToLowerInvariant()) {
        throw "Commit must equal repository HEAD: requested=$Commit head=$headCommit"
    }
    $headDateText = (& git show -s --format=%cI HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw "Unable to read the HEAD commit date" }
    $headDate = [DateTimeOffset]::Parse($headDateText)
    if ($headDate.ToUniversalTime() -ne $parsedDate.ToUniversalTime()) {
        throw "BuildDate must equal the HEAD commit date: requested=$BuildDate head=$headDateText"
    }

$packageName = "BaiduDriveMover-$Version-windows-amd64"
$stageRoot = Join-Path $outputRootPath $packageName
$zipPath = Join-Path $outputRootPath ($packageName + '.zip')
$checksumsPath = Join-Path $outputRootPath 'SHA256SUMS.txt'
foreach ($target in @($stageRoot, $zipPath, $checksumsPath)) {
    if (Test-Path -LiteralPath $target) {
        throw "Refusing to overwrite existing release output: $target"
    }
}

New-Item -ItemType Directory -Force $stageRoot | Out-Null
$exePath = Join-Path $stageRoot 'BaiduDriveMover.exe'
$ldflags = @(
    '-s', '-w',
    "-X github.com/teddyli18000/baidu-drive-mover/internal/version.Version=$Version",
    "-X github.com/teddyli18000/baidu-drive-mover/internal/version.Commit=$($Commit.ToLowerInvariant())",
    "-X github.com/teddyli18000/baidu-drive-mover/internal/version.BuildDate=$($parsedDate.ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ'))"
) -join ' '

    $previousCGO = $env:CGO_ENABLED
    $previousGOOS = $env:GOOS
    $previousGOARCH = $env:GOARCH
    try {
        $env:CGO_ENABLED = '0'
        $env:GOOS = 'windows'
        $env:GOARCH = 'amd64'
        & go build -trimpath -buildvcs=false -ldflags $ldflags -o $exePath ./cmd/baidu-drive-mover
        if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
    }
    finally {
        $env:CGO_ENABLED = $previousCGO
        $env:GOOS = $previousGOOS
        $env:GOARCH = $previousGOARCH
    }
}
finally {
    Pop-Location
}

$stageEntries = @(Get-ChildItem -LiteralPath $stageRoot -Force)
if ($stageEntries.Count -ne 1 -or $stageEntries[0].Name -ne 'BaiduDriveMover.exe') {
    throw "Release package allowlist violation: $($stageEntries.Name -join ', ')"
}

Add-Type -AssemblyName System.IO.Compression.FileSystem
$archive = [System.IO.Compression.ZipFile]::Open($zipPath, [System.IO.Compression.ZipArchiveMode]::Create)
try {
    $entry = $archive.CreateEntry('BaiduDriveMover.exe', [System.IO.Compression.CompressionLevel]::Optimal)
    $entry.LastWriteTime = $parsedDate.ToUniversalTime()
    $source = [System.IO.File]::OpenRead($exePath)
    try {
        $destination = $entry.Open()
        try {
            $source.CopyTo($destination)
        }
        finally {
            $destination.Dispose()
        }
    }
    finally {
        $source.Dispose()
    }
}
finally {
    $archive.Dispose()
}
$zipHash = (Get-FileHash -LiteralPath $zipPath -Algorithm SHA256).Hash.ToLowerInvariant()
[System.IO.File]::WriteAllText($checksumsPath, "$zipHash  $([System.IO.Path]::GetFileName($zipPath))`n", [System.Text.UTF8Encoding]::new($false))

[pscustomobject]@{
    Package = $zipPath
    Checksums = $checksumsPath
    SHA256 = $zipHash
    Executable = $exePath
}
