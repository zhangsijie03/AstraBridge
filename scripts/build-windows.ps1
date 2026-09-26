[CmdletBinding()]
param(
    [string]$GoBin = $(if ($env:GO_BIN) { $env:GO_BIN } else { 'go' }),
    [string]$DotnetBin = $(if ($env:DOTNET_BIN) { $env:DOTNET_BIN } else { 'dotnet' }),
    [switch]$SkipSmokeTest
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$projectRoot = Split-Path -Parent $PSScriptRoot
$version = (Get-Content -LiteralPath (Join-Path $projectRoot 'VERSION') -Raw).Trim()
if ($version -notmatch '^\d+\.\d+\.\d+$') { throw 'VERSION must contain a semantic x.y.z version.' }
$dist = Join-Path $projectRoot 'dist'
$packageName = "AstraBridge-$version-Windows-x64"
$package = Join-Path $dist $packageName
$archive = Join-Path $dist "$packageName.zip"
$onWindows = [System.Environment]::OSVersion.Platform -eq [System.PlatformID]::Win32NT
if (-not $onWindows -and -not $SkipSmokeTest) { throw 'Cross compilation requires -SkipSmokeTest; run the produced package smoke test on Windows before release.' }

Push-Location $projectRoot
try {
    # 先验证来源固定文件，避免把未审查的上游代码放进发布包。
    $python = if (Get-Command python -ErrorAction SilentlyContinue) { 'python' } else { 'python3' }
    & $python scripts/check_upstream.py
    if ($LASTEXITCODE -ne 0) { throw 'Upstream source verification failed.' }
    if (Test-Path -LiteralPath $package) { Remove-Item -LiteralPath $package -Recurse -Force }
    New-Item -ItemType Directory -Path (Join-Path $package 'engine') -Force | Out-Null
    $previousGoOS = $env:GOOS; $previousGoArch = $env:GOARCH; $previousCgo = $env:CGO_ENABLED
    try {
        $env:GOOS = 'windows'; $env:GOARCH = 'amd64'; $env:CGO_ENABLED = '0'
        & $GoBin build -mod=vendor -trimpath -o (Join-Path $package 'engine/bps-local.exe') ./cmd/bps-local
        if ($LASTEXITCODE -ne 0) { throw 'Go engine build failed.' }
    }
    finally { $env:GOOS = $previousGoOS; $env:GOARCH = $previousGoArch; $env:CGO_ENABLED = $previousCgo }
    & $DotnetBin publish windows/AstraBridge/AstraBridge.csproj -c Release -r win-x64 --self-contained true "-p:Version=$version" -o $package
    if ($LASTEXITCODE -ne 0) { throw 'Windows desktop publish failed.' }
    foreach ($name in @('README.md', 'LICENSE', 'NOTICE', 'VERSION', 'upstream-manifest.json')) {
        Copy-Item -LiteralPath (Join-Path $projectRoot $name) -Destination $package
    }
    Copy-Item -LiteralPath (Join-Path $projectRoot 'licenses') -Destination $package -Recurse
    New-Item -ItemType Directory -Path (Join-Path $package 'docs') -Force | Out-Null
    foreach ($name in @('source-provenance.md', 'native-images.md')) {
        Copy-Item -LiteralPath (Join-Path $projectRoot "docs/$name") -Destination (Join-Path $package 'docs')
    }
    Copy-Item -LiteralPath (Join-Path $projectRoot 'windows/README.md') -Destination (Join-Path $package 'docs/windows.md')

    # 自包含运行时随包分发，保留 SDK 提供的 .NET 许可证与第三方告知。
    $dotnetCommand = Get-Command $DotnetBin -ErrorAction Stop
    $dotnetRoot = Split-Path -Parent $dotnetCommand.Source
    $licenseRoot = Join-Path $package 'licenses/dotnet'
    New-Item -ItemType Directory -Path $licenseRoot -Force | Out-Null
    foreach ($name in @('LICENSE.txt', 'ThirdPartyNotices.txt')) {
        $source = Join-Path $dotnetRoot $name
        if (-not (Test-Path -LiteralPath $source)) { throw "Missing .NET distribution notice: $source" }
        Copy-Item -LiteralPath $source -Destination $licenseRoot
    }
    $nugetRoot = if ($env:NUGET_PACKAGES) { $env:NUGET_PACKAGES } else { Join-Path ([Environment]::GetFolderPath('UserProfile')) '.nuget/packages' }
    foreach ($runtime in @('microsoft.netcore.app.runtime.win-x64', 'microsoft.windowsdesktop.app.runtime.win-x64')) {
        $runtimeRoot = Join-Path $nugetRoot $runtime
        if (Test-Path -LiteralPath $runtimeRoot) {
            Get-ChildItem -LiteralPath $runtimeRoot -File -Recurse | Where-Object { $_.Name -match '^(LICENSE|THIRD.?PARTY.?NOTICES).*\.txt$' } | ForEach-Object {
                $destination = Join-Path $licenseRoot "$runtime-$($_.Directory.Name)-$($_.Name)"
                Copy-Item -LiteralPath $_.FullName -Destination $destination
            }
        }
    }
    if (-not $SkipSmokeTest) {
        $buildDirectory = Join-Path $projectRoot '.build'
        New-Item -ItemType Directory -Path $buildDirectory -Force | Out-Null
        $report = Join-Path $buildDirectory 'windows-smoke.json'
        if (Test-Path -LiteralPath $report) { Remove-Item -LiteralPath $report -Force }
        $arguments = "--smoke-test --smoke-report `"$report`""
        $smoke = Start-Process -FilePath (Join-Path $package 'AstraBridge.exe') -ArgumentList $arguments -PassThru
        if (-not $smoke.WaitForExit(45000)) {
            & taskkill /PID $smoke.Id /T /F | Out-Null
            throw 'Windows offline smoke test timed out.'
        }
        if ($smoke.ExitCode -ne 0 -or -not (Test-Path -LiteralPath $report)) { throw "Windows offline smoke test failed (exit $($smoke.ExitCode))." }
        $result = Get-Content -LiteralPath $report -Raw | ConvertFrom-Json
        if (-not $result.success) { throw "Windows offline smoke report failed: $($result.error)" }
    }
    Compress-Archive -LiteralPath $package -DestinationPath $archive -Force
    Write-Output "Built: $archive"
}
finally { Pop-Location }
