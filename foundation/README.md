# Red-XAI Foundation — 0.2.0 engineering preview

**Runnable, native, local-first software. Not a finished cloud platform.**

This preview contains the Red-XAI language/parser, encrypted local document storage,
authenticated localhost API, and four native Tk desktop clients: Database, Host,
Installer, and Updator. It does not embed a browser and needs no OpenAI key to run.

## Start from source

Install Python 3.11 or newer with Tk support, create an isolated virtual environment,
then run from this directory:

```sh
python -m venv .venv
# Windows: .venv\Scripts\activate
# macOS/Linux: source .venv/bin/activate
python -m pip install -r requirements.txt
python -m redxai.desktop --product Database
```

Change `Database` to `Host`, `Installer`, or `Updator` to open another client.
Database and Host automatically pair with the same authenticated localhost service
on port 46321 under the current operating-system account. Closing a window does not
stop that service. Use the same password in both applications; do not create two
unrelated accounts. The owner setup email is `grimvirusoffical@gmail.com`.

**Use synthetic data in this preview.** First-run ownership is based on possession
of the local operating-system account and a protected bootstrap file, NOT proof of
email ownership. SMTP, social sign-in, password recovery, MFA, and remote platform
ownership are not configured.

## Native bundles

`tools/build_desktop.py` builds self-contained preview application folders on the
operating system on which it runs. Windows and macOS must be built and verified on
those operating systems; a Linux test does not validate a Windows `.exe` or Mac
`.app`. CI configuration is included. Do not describe an unexecuted CI workflow as
a successful native build.

These bundles are NOT signed production installers. `.RXAI` is the separate Red-XAI
signed-payload format; production trust roots, OS signing/notarization, MSI/PKG
bootstrap installation, file associations, app relaunch, and automatic update feeds
remain release gates.

## What works in this preview

- Real parser/validator, nested boxes/Packers, arrays, tables, non-executable metatables,
  local/global identifiers, case-insensitive booleans and NELL, and structural queries.
- First local owner setup, Argon2id passwords, owner-approved local members,
  per-database API keys, read-only box grants, and key revocation.
- SQLite transactions, optimistic revisions, AES-GCM document/history encryption,
  restoration as a new revision, and hash-linked audit events.
- Custom `.Red-XAI` snapshots, optional password-encrypted exports, import validation,
  and filename normalization. Exports do not contain secret API keys.
- A native editor and explorer, type coloring, undo, formatting, diagnostics, manual
  Packer entry, dark/light modes, and restricted CSS-token theme import/export.
- Actual single-node **loopback static-site hosting**, start/stop, live machine metrics,
  `cloudflared` detection, and a read-only Cloudflare zone-list integration.
- Ed25519-signed `.RXAI` verification, staged per-user installation, file checksums,
  a version pointer, cancellation recovery, and explicit program-version rollback.

See [delivery status](docs/DELIVERY-STATUS.md) before considering production use.

## Verify

```sh
python -m pip install -r requirements-dev.txt
python -m pytest -q
python tools/stress_test.py --output ./local-stress-report.json
# Linux with Xvfb and Pillow installed:
PYTHONPATH=. xvfb-run -a python tools/smoke_desktop.py --output ./screenshots
```

Tests use temporary directories and synthetic data. The bounded stress run does not
hit your production Railway service or spend API credits. Real test results are in
the separately provided evidence bundle; do not substitute generated agent text for
execution evidence.

## Reading guide

- [Language](docs/LANGUAGE.md)
- [HTTP API and SDK examples](docs/API.md)
- [Security boundaries](docs/SECURITY.md)
- [Architecture and remaining milestones](docs/ARCHITECTURE.md)
- [Installer/Updator and publishing](docs/DISTRIBUTION.md)
- [Verified delivery status](docs/DELIVERY-STATUS.md)
- [Agent coordination](AGENTS.md)

No existing repositories are deleted by any script in this package. No credentials
are bundled. The installation owner controls their local data; there is no hidden
upload of all self-hosted databases to a platform operator.
