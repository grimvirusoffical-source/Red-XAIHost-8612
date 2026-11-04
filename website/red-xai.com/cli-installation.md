# REDXAI CLI: verified manual installation

Checked November 3, 2026. The default stable package remains **0.3.0**, with the same Node.js package on Windows, macOS and Linux. An optional **0.4.0 manual preview** is now publicly available for DB, Host and node commands. Its separate `redxai-candidate` installation preserves the stable CLI. Neither download is a native graphical installer; GUI installers, signing/notarization and real-device installation acceptance remain unfinished.

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

The Bridge resolver selects only matching published assets, checks release metadata and refuses unsupported targets. Bridge installation requires a compatible published asset for the chosen operating system; installing this CLI does not supply a missing Bridge build.

## Optional 0.4.0 manual preview

The [public manual preview release](https://github.com/grimvirusoffical-source/RedXDexAiBridge/releases/tag/redxai-cli-v0.4.0-candidate.1) contains separate Windows, macOS and Linux toolkits. Node.js **20 or later** must already be installed. Download the toolkit for the computer on which you intend to install it, verify its SHA-256, extract the whole archive into a new folder, and read its bundled `INSTALL.md` before running setup. Downloading or extracting it does not install the CLI or enroll a Host node.

- [Windows manual toolkit (.zip)](https://github.com/grimvirusoffical-source/RedXDexAiBridge/releases/download/redxai-cli-v0.4.0-candidate.1/Red-XAI-CLI-0.4.0-candidate-windows-manual.zip) — SHA-256 `772e5e8c94e2d2e533d206535dac71886bb717c8821cf041662d1d8f86e507e8`.
- [macOS manual toolkit (.tar.gz)](https://github.com/grimvirusoffical-source/RedXDexAiBridge/releases/download/redxai-cli-v0.4.0-candidate.1/Red-XAI-CLI-0.4.0-candidate-macos-manual.tar.gz) — SHA-256 `1621056a116911cf087bca5a183723cb5aaf3983baa2b4c48ed683a6c662af9c`.
- [Linux manual toolkit (.tar.gz)](https://github.com/grimvirusoffical-source/RedXDexAiBridge/releases/download/redxai-cli-v0.4.0-candidate.1/Red-XAI-CLI-0.4.0-candidate-linux-manual.tar.gz) — SHA-256 `69c6c0ba707fc177ca74b7f941c5327dd1caff564e004ef5858c3cea9827fefa`.
- [Preview checksums](https://github.com/grimvirusoffical-source/RedXDexAiBridge/releases/download/redxai-cli-v0.4.0-candidate.1/SHA256SUMS).

Run setup from inside the extracted toolkit folder. These per-user commands explicitly opt into the separate `redxai-candidate` command; omit `-AddCommand` or `--add-command` to leave PATH/command links unchanged and use the full command path reported by setup instead. No administrator rights, `sudo`, execution-policy bypass or automatic prerequisite installation is part of this workflow.

### Windows preview setup

```powershell
.\scripts\install-windows.ps1 -AddCommand
```

### macOS preview setup

```sh
sh scripts/install-posix.sh macos --add-command
```

### Linux preview setup

```sh
sh scripts/install-posix.sh linux --add-command
```

The toolkits install separately from `REDXAI` / `redxai` and do not change Bridge's stable updater. Automated package/source, lifecycle-fixture and three-OS checks passed. Actual user-profile installation, Host workloads and physical-device lifecycle acceptance remain unfinished; this preview does not include GUI installers or claim signing/notarization.

## Preview DB, Host and device nodes

After opting into preview command setup, use the following command families. They are **not included in stable 0.3.0**:

```text
redxai-candidate db open
redxai-candidate DB OPEN
redxai-candidate db list --key-file ./db-key.txt
redxai-candidate db create music --key-file ./db-key.txt
redxai-candidate db query music --file ./music.boxql --key-file ./db-key.txt
redxai-candidate host open
redxai-candidate host nodes
redxai-candidate host node install --token-file ./node-token.txt --accept-workloads
redxai-candidate host node status
redxai-candidate host node remove --yes
redxai-candidate host node run --token-file ./node-token.txt --accept-workloads
```

Browser DB/Host entry points use website account login. Advanced scripting requires an explicitly scoped key in a private file. No website cookie/password extraction is part of the workflow. Names, paths and BoxQL remain case sensitive; command words are case insensitive. Existing production account activation, permissions, subscription and storage limits still apply. Installing the preview does not repair those separate backend issues or activate the pending replacement account services.

On macOS the candidate uses a user LaunchAgent; on supported Linux systems it uses a user systemd service. Windows/Android support is foreground execution, not a promise of a continuously running system service. The package does not ship an unrestricted iOS hosting node. Actual device/service execution still needs platform validation.

## Stable 0.3.0 removal

With the same prefix used above, uninstall the npm package:

- Windows: `npm uninstall --global --prefix $prefix redxai`
- macOS/Linux: `npm uninstall --global --prefix "$HOME/.local" redxai`

Remove only the CLI’s own PATH entry if no other tools use it. Removing the CLI is separate from stopping hosted projects, removing a device service or revoking a Host node token. Follow those product-specific workflows before deleting an active worker.

## Preview removal

From the same extracted manual toolkit, follow `INSTALL.md` and use the matching removal script:

- Windows: `.\scripts\uninstall-windows.ps1`
- macOS: `sh scripts/uninstall-posix.sh macos`
- Linux: `sh scripts/uninstall-posix.sh linux`

Removal checks the candidate's ownership receipt and leaves modified or unrelated files for review. It does not remove the stable CLI or application configuration. Node-service removal, workload shutdown and Host token revocation remain separate operations.

## Source and release references

- [Public stable CLI package release](https://github.com/grimvirusoffical-source/RedXDexAiBridge/releases/tag/cli-v0.3.0)
- [Published SHA256SUMS-cli](https://github.com/grimvirusoffical-source/RedXDexAiBridge/releases/download/cli-v0.3.0/SHA256SUMS-cli)
- [CLI source repository](https://github.com/grimvirusoffical-source/red-xai-installers) (private)
- [Public 0.4.0 manual preview](https://github.com/grimvirusoffical-source/RedXDexAiBridge/releases/tag/redxai-cli-v0.4.0-candidate.1)
- [Preview artifact provenance](https://github.com/grimvirusoffical-source/RedXDexAiBridge/releases/download/redxai-cli-v0.4.0-candidate.1/manual-build-manifest.json)

Public npm/Homebrew availability has not been established. The existing source-checkout install scripts cannot be offered as standalone installers without their required bundled files.
