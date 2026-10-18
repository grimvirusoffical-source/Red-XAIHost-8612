<#
 RedX host installer for Windows (run in an ELEVATED PowerShell). Installs from a LOCAL build directory.
   .\install.ps1 -From .\dist\redx-<ver>-windows-amd64
   .\install.ps1 -From .\dist -Listen 0.0.0.0:7777 -Tls auto
 Creates: C:\Program Files\RedX\{redxd,redx}.exe, C:\ProgramData\RedX\{redx.toml,data}, a Windows service "redxd".
 The first admin API key is created offline and printed once to this console.
#>
param(
  [string]$From = "",
  [string]$Listen = "127.0.0.1:7777",
  [ValidateSet("off","auto","files")][string]$Tls = "off",
  [ValidateSet("leader","follower")][string]$Role = "leader",
  [string]$Peers = "",
  [switch]$NoService
)
$ErrorActionPreference = "Stop"
$id = [Security.Principal.WindowsIdentity]::GetCurrent()
if (-not ([Security.Principal.WindowsPrincipal]$id).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  throw "Run this script from an elevated (Administrator) PowerShell."
}
$arch = switch ($env:PROCESSOR_ARCHITECTURE) { "ARM64" { "arm64" } default { "amd64" } }
if (-not $From) { $From = Split-Path -Parent $MyInvocation.MyCommand.Path }
if (-not (Test-Path (Join-Path $From "redxd.exe"))) {
  $cand = Get-ChildItem -Directory -Path $From -Filter "redx-*-windows-$arch" -ErrorAction SilentlyContinue | Select-Object -First 1
  if ($cand) { $From = $cand.FullName }
}
if (-not ((Test-Path (Join-Path $From "redxd.exe")) -and (Test-Path (Join-Path $From "redx.exe")))) {
  throw "redxd.exe / redx.exe not found in $From (build first: scripts/build.sh)"
}
$bin  = Join-Path $env:ProgramFiles "RedX"
$root = Join-Path $env:ProgramData  "RedX"
$data = Join-Path $root "data"
$conf = Join-Path $root "redx.toml"
New-Item -ItemType Directory -Force -Path $bin, $data | Out-Null
if (Get-Service redxd -ErrorAction SilentlyContinue) { Stop-Service redxd -ErrorAction SilentlyContinue }
Copy-Item (Join-Path $From "redxd.exe"), (Join-Path $From "redx.exe") -Destination $bin -Force

# Restrict the data directory (keystore!) to SYSTEM and Administrators.
icacls $root /inheritance:r /grant:r "SYSTEM:(OI)(CI)F" "Administrators:(OI)(CI)F" | Out-Null

if (-not (Test-Path $conf)) {
  $dataToml = $data -replace '\\','/'
  $lines = @("data_dir = '$dataToml'", "listen = '$Listen'", "tls = '$Tls'", "role = '$Role'")
  if ($Peers) { $lines += "peers = ['$Peers']" }
  Set-Content -Path $conf -Value $lines -Encoding ascii
} else { Write-Host "keeping existing config $conf" }

if ($Role -eq "leader" -and -not (Test-Path (Join-Path $data "keys.red-xai-keys"))) {
  Write-Host "`n==== Creating the first ADMIN API key (shown once, never stored in clear) ===="
  & (Join-Path $bin "redx.exe") key create --offline --data-dir $data --scope admin --db '*' --label bootstrap-admin
  Write-Host "================================================================================`n"
}

if (-not $NoService) {
  & (Join-Path $bin "redxd.exe") uninstall 2>$null | Out-Null
  & (Join-Path $bin "redxd.exe") install --config $conf
  if ($Role -eq "follower") {
    Write-Host "Follower: service installed but NOT started. Run 'redx node join <leader-url> --token <rxj_...> --pin <fp> --advertise <url> --data-dir `"$data`"' then 'redxd.exe start'."
  } else {
    & (Join-Path $bin "redxd.exe") start
  }
  if ($Listen -notmatch '^(127\.|localhost|\[::1\])') {
    New-NetFirewallRule -DisplayName "RedX host" -Direction Inbound -Protocol TCP -LocalPort ($Listen.Split(':')[-1]) -Action Allow -Profile Private | Out-Null
    Write-Host "Opened the Windows firewall for the Private profile on that port."
  }
}
$machinePath = [Environment]::GetEnvironmentVariable("Path","Machine")
if ($machinePath -notlike "*$bin*") { [Environment]::SetEnvironmentVariable("Path", "$machinePath;$bin", "Machine") }
Write-Host "Done. Open a NEW terminal, then:  `$env:REDX_KEY='<your admin key>'; redx doctor"
