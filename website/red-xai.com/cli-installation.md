# REDXAI CLI: verified manual installation

Checked November 2, 2026. The free public package is **0.3.0**. It is the same Node.js package on Windows, macOS and Linux. Platform selection can choose the instructions, but it must not label this tarball as a native graphical installer. The requested Bridge-style native CLI installers still need to be built and published.

Node.js **20 or later**, npm and a terminal are required. These examples install only under the current user. They do not install Node, change system execution policy or request administrator rights. Downloading the package does not execute it.

## Windows PowerShell

```powershell
$ErrorActionPreference = 'Stop'
$package = Join-Path (Get-Location) 'redxai-cli-0.3.0.tgz'
if (Test-Path -LiteralPath $package) { throw 'Package file already exists. Choose an empty working folder.' }
Invoke-WebRequest -UseBasicParsing 'https://github.com/grimvirusoffical-source/RedXDexAiBridge/releases/download/cli-v0.3.0/redxai-cli-0.3.0.tgz' -OutFile $package
$expected = '7e04e8171a4542a9ee8cb8c7fd5fda214313b326453cb28c054b6c4ee3efd4f9'
$actual = (Get-FileHash -LiteralPath $package -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actual -ne $expected) { throw 'CLI package checksum mismatch. Installation stopped.' }
$prefix = Join-Path $env:LOCALAPPDATA 'Red-XAI\CLI'
npm install --global --prefix $prefix --ignore-scripts $package
if ($LASTEXITCODE -ne 0) { throw 'npm installation failed.' }
$env:PATH = "$prefix;$env:PATH"
& (Join-Path $prefix 'redxai.cmd') version
& (Join-Path $prefix 'redxai.cmd') help
```

To use the command from later terminals, add the exact `$prefix` directory to your **user** PATH in Windows settings and open a new terminal. PowerShell execution policy does not need to change: use `redxai.cmd`.

## macOS and Linux

Run this block from a writable folder. The subshell stops before installation if a download or verification fails.

```sh
(
  set -eu
  package=redxai-cli-0.3.0.tgz
  test ! -e "$package"
  curl --fail --location --proto '=https' \
    'https://github.com/grimvirusoffical-source/RedXDexAiBridge/releases/download/cli-v0.3.0/redxai-cli-0.3.0.tgz' \
    --output "$package"
  node -e "const f=require('node:fs'),c=require('node:crypto');const h=c.createHash('sha256').update(f.readFileSync('redxai-cli-0.3.0.tgz')).digest('hex');if(h!=='7e04e8171a4542a9ee8cb8c7fd5fda214313b326453cb28c054b6c4ee3efd4f9')throw Error('CLI package checksum mismatch');"
  mkdir -p "$HOME/.local"
  npm install --global --prefix "$HOME/.local" --ignore-scripts "./$package"
  "$HOME/.local/bin/redxai" version
  "$HOME/.local/bin/redxai" help
)
```

For future terminal sessions, add `$HOME/.local/bin` to your PATH using your shell’s user profile. On macOS that is commonly `~/.zprofile`; on Linux it depends on your shell. Do not use `sudo` to make this per-user installation work.

## Stable 0.3.0 commands

```text
redxai version
redxai help
redxai bridge download
redxai bridge install
redxai update --download-only
redxai update
```

The Bridge resolver selects only matching published assets, checks release metadata and refuses unsupported targets. Current stable Bridge **0.10.19** has Windows/Linux downloads, without a matching macOS installer on that release. Installing the CLI on macOS does not make a missing Bridge build available.

## DB, Host and device nodes

The reviewed **0.4.0 candidate** adds the following command families. Do not present them as working in the public 0.3.0 package:

```text
redxai db open
redxai db list --key-file ./db-key.txt
redxai db create music --key-file ./db-key.txt
redxai db query music --file ./music.boxql --key-file ./db-key.txt
redxai host open
redxai host nodes
redxai host node install --token-file ./node-token.txt --accept-workloads
redxai host node status
redxai host node remove --yes
redxai host node run --token-file ./node-token.txt --accept-workloads
```

Browser DB/Host entry points use website account login. Advanced scripting requires an explicitly scoped key in a private file. No website cookie/password extraction is part of the workflow. Names, paths and BoxQL remain case sensitive even where the candidate’s command words are case insensitive.

On macOS the candidate uses a user LaunchAgent; on supported Linux systems it uses a user systemd service. Windows/Android support is foreground execution, not a promise of a continuously running system service. The package does not ship an unrestricted iOS hosting node. Actual device/service execution still needs platform validation.

## Removal

With the same prefix used above, uninstall the npm package:

- Windows: `npm uninstall --global --prefix $prefix redxai`
- macOS/Linux: `npm uninstall --global --prefix "$HOME/.local" redxai`

Remove only the CLI’s own PATH entry if no other tools use it. Removing the CLI is separate from stopping hosted projects, removing a device service or revoking a Host node token. Follow those product-specific workflows before deleting an active worker.

## Source and release references

- [Public stable CLI package release](https://github.com/grimvirusoffical-source/RedXDexAiBridge/releases/tag/cli-v0.3.0)
- [Published SHA256SUMS-cli](https://github.com/grimvirusoffical-source/RedXDexAiBridge/releases/download/cli-v0.3.0/SHA256SUMS-cli)
- [CLI source repository](https://github.com/grimvirusoffical-source/red-xai-installers) (private)
- [0.4.0 DB/Host/node candidate](https://github.com/grimvirusoffical-source/red-xai-installers/pull/5) (private source access required)

Public npm/Homebrew availability has not been established. The existing source-checkout install scripts cannot be offered as standalone installers without their required bundled files.
