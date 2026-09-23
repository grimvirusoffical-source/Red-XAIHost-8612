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

if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
  Write-Warning "Node.js is required by the built-in worker. Install Node.js LTS before deploying projects."
}
if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
  Write-Warning "Git is not on PATH. Git-backed projects (including the bundled InfectedNation bootstrap) need Git. Install Git for Windows and rerun if InfectedNation cannot clone."
}

Write-Host "Installing dependencies..."
bun install --frozen-lockfile

Write-Host "Preparing local database, secrets and production build..."
bun run setup:selfhost

Write-Host ""
Write-Host "Starting RedXAIHost at http://127.0.0.1:4200" -ForegroundColor Green
Write-Host "Keep this window open. The built-in worker and InfectedNation start automatically."
bun run selfhost
