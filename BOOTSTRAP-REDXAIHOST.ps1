$ErrorActionPreference = "Stop"

$repoZip = "https://github.com/grimvirusoffical-source/Red-XAIHost-8612/archive/refs/heads/main.zip"
$baseDir = Join-Path $env:USERPROFILE "RedXAIHost"
$staging = Join-Path $env:TEMP "redxaihost-bootstrap"
$zipPath = Join-Path $staging "redxaihost-main.zip"
$extractDir = Join-Path $staging "extract"

Write-Host "RedXAIHost bootstrap" -ForegroundColor Cyan

if (Test-Path $staging) {
  Remove-Item $staging -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $extractDir | Out-Null

Write-Host "Downloading repaired RedXAIHost main..."
Invoke-WebRequest -Uri $repoZip -OutFile $zipPath -UseBasicParsing

Write-Host "Extracting..."
Expand-Archive -Path $zipPath -DestinationPath $extractDir -Force

$source = Join-Path $extractDir "Red-XAIHost-8612-main"
if (-not (Test-Path $source)) {
  throw "Downloaded archive did not contain the expected RedXAIHost directory."
}

if (Test-Path $baseDir) {
  $backup = "$baseDir-backup-$(Get-Date -Format 'yyyyMMdd-HHmmss')"
  Write-Host "Backing up existing checkout to $backup"
  Move-Item $baseDir $backup
}

Move-Item $source $baseDir
Remove-Item $staging -Recurse -Force

$launcher = Join-Path $baseDir "START-REDXAIHOST.ps1"
if (-not (Test-Path $launcher)) {
  throw "RedXAIHost launcher is missing after extraction."
}

Write-Host "Starting repaired RedXAIHost..." -ForegroundColor Green
Set-Location $baseDir
& powershell.exe -NoProfile -ExecutionPolicy Bypass -File $launcher
