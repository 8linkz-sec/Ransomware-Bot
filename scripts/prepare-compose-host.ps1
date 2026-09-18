param(
    [int]$BotUID = $(if ($env:BOT_UID) { [int]$env:BOT_UID } else { 1000 }),
    [int]$BotGID = $(if ($env:BOT_GID) { [int]$env:BOT_GID } else { 1000 }),
    [string]$LogsDir = "logs",
    [string]$DataDir = "data"
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$targets = @($LogsDir, $DataDir)

foreach ($target in $targets) {
    $path = Join-Path $repoRoot $target
    New-Item -ItemType Directory -Force -Path $path | Out-Null
}

$isWindows = $PSVersionTable.Platform -eq "Win32NT" -or $env:OS -eq "Windows_NT"
if ($isWindows) {
    Write-Host "Created logs and data directories. Windows bind mounts use the Docker Desktop file-sharing permissions."
    exit 0
}

$chown = Get-Command chown -ErrorAction SilentlyContinue
if (-not $chown) {
    throw "chown is required on non-Windows hosts to set bind-mount ownership."
}

foreach ($target in $targets) {
    $path = Join-Path $repoRoot $target
    & $chown.Source -R "${BotUID}:${BotGID}" $path
}

Write-Host "Prepared logs and data directories for UID/GID ${BotUID}:${BotGID}."
