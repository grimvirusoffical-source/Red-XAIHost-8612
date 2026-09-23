$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

Write-Host "RedXAIHost self-host setup" -ForegroundColor Cyan

if (-not (Get-Command bun -ErrorAction SilentlyContinue)) {
  Write-Host "Bun is not installed. Installing Bun for this user..."
  irm https://bun.sh/install.ps1 | iex
  $bunDir = Join-Path $env:USERPROFILE ".bun\bin"
  if (Test-Path (Join-Path $bunDir "bun.exe")) {
    $env:PATH = "$bunDir;$env:PATH"
  }
}
if (-not (Get-Command bun -ErrorAction SilentlyContinue)) {
  throw "Bun could not be installed automatically. Install Bun, reopen PowerShell, and run this file again."
}

if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
  if (Get-Command winget -ErrorAction SilentlyContinue) {
    Write-Host "Git is not installed. Installing Git for Windows..."
    winget install --id Git.Git -e --source winget --accept-source-agreements --accept-package-agreements
    $gitCandidates = @(
      "$env:ProgramFiles\Git\cmd",
      "$env:ProgramFiles\Git\bin",
      "$env:LOCALAPPDATA\Programs\Git\cmd"
    )
    foreach ($candidate in $gitCandidates) {
      if (Test-Path $candidate) { $env:PATH = "$candidate;$env:PATH" }
    }
  }
}
if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
  throw "Git is required for Git-backed projects and could not be installed automatically. Install Git for Windows, reopen PowerShell, then run START-REDXAIHOST.ps1 again."
}

Write-Host "Installing dependencies..."
bun install --frozen-lockfile

Write-Host "Preparing local database, secrets and production build..."
bun run setup:selfhost

Write-Host ""
Write-Host "Starting RedXAIHost at http://127.0.0.1:4200" -ForegroundColor Green
Write-Host "The built-in worker and InfectedNation start automatically."

$hostProcess = Start-Process -FilePath (Get-Command bun).Source -ArgumentList @("run","selfhost") -WorkingDirectory $PSScriptRoot -PassThru
$ready = $false
for ($i = 0; $i -lt 60; $i++) {
  try {
    $health = Invoke-RestMethod -Uri "http://127.0.0.1:4200/api/health" -TimeoutSec 2
    if ($health.status -eq "ok") { $ready = $true; break }
  } catch {}
  if ($hostProcess.HasExited) { break }
  Start-Sleep -Seconds 1
}
if (-not $ready) {
  throw "RedXAIHost did not become healthy on port 4200. Check the console output and data logs before retrying."
}

Write-Host "RedXAIHost control panel is healthy." -ForegroundColor Green
Start-Process "http://127.0.0.1:4200"

$nationReady = $false
for ($i = 0; $i -lt 120; $i++) {
  try {
    $nation = Invoke-RestMethod -Uri "http://127.0.0.1:8787/api/health" -TimeoutSec 2
    if ($nation.ok -eq $true) { $nationReady = $true; break }
  } catch {}
  if ($hostProcess.HasExited) { break }
  Start-Sleep -Seconds 1
}
if ($nationReady) {
  Write-Host "InfectedNation is hosted locally by RedXAIHost on http://127.0.0.1:8787" -ForegroundColor Green
} else {
  Write-Warning "RedXAIHost is running, but InfectedNation has not become healthy yet. Open the Projects page and inspect the latest deployment log."
}

Write-Host "Close this window only when you are ready to stop monitoring. The RedXAIHost server process is PID $($hostProcess.Id)."
Wait-Process -Id $hostProcess.Id
