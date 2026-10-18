# Fallback Windows service installer using the built-in `sc.exe` (no redxd subcommand needed).
# Prefer:  redxd.exe install --config C:\ProgramData\RedX\redx.toml ; redxd.exe start
# Run in an elevated PowerShell. Paths assume the default install.ps1 layout.
param(
  [string]$Bin  = "$env:ProgramFiles\RedX\redxd.exe",
  [string]$Conf = "$env:ProgramData\RedX\redx.toml"
)
$ErrorActionPreference = "Stop"
sc.exe create redxd binPath= "`"$Bin`" run --config `"$Conf`"" start= auto DisplayName= "RedX Database Host" | Out-Null
sc.exe description redxd "RedX database host" | Out-Null
sc.exe failure redxd reset= 86400 actions= restart/5000/restart/5000/restart/30000 | Out-Null
Write-Host "Service created. Start with: sc.exe start redxd"
Write-Host "NOTE: a plain 'sc' service cannot report status to the SCM; if it shows 'Error 1053', use 'redxd.exe install' instead (it speaks the service protocol)."
