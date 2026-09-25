# Red-XAI Installer and Updator preview

`.RXAI` is a signed ZIP format distinct from `.Red-XAI` data snapshots. It contains
canonical JSON installation metadata, an Ed25519 signature, and only declared
payload files. The installer must receive trusted public keys independently; do
not trust the key merely because it arrives beside an untrusted download.

Use `tools/publisher.py keygen` once for an isolated development signing identity.
Private-key files are not included in bundles. Production signing requires a
separate protected signing service and managed key rotation.

```sh
python tools/publisher.py --help
python tools/publisher.py keygen --private ./dev-signing.pem --trust ./dev-trust.json --key-id dev-local
python tools/publisher.py package --source ./my-payload --output ./my-app.RXAI --app-id my-app --version 0.2.0 --private ./dev-signing.pem --key-id dev-local
```

Open Installer, select the package and your independently trusted keys JSON,
inspect the manifest, and choose the per-user install directory. Recommended mode
never executes a package script, launches an app, enables autorun, or deletes data.
Custom mode currently exposes the supported destination choice only.

Installed files live below `<destination>/<app_id>/releases/<version>-<unique>/`.
`current.json` points to the active program version. The preview does not register
system file associations or forcibly restart applications. You can inspect the
result path; launching installed code is a separate explicit action.

Updator accepts a final HTTPS package URL (redirects are rejected), then follows
the same independent signature and checksum verification. The previous version
button restores the previous program pointer, never reverse-migrates a database.
Replay protection remembers the highest version even after an intentional rollback.

The ring around the red X indicates ongoing work; the progress bar reaches 100 only
when installation returns success. A full resumable download UI, per-file progress,
live channels/feeds and automatic updates are not yet implemented.

Cancelled or failed extraction preserves the old version pointer and clears normal
staging/lock files. A hard-killed process may leave a stale lock and staging directory;
inspect them before recovery. Do not silently remove a possibly active install lock.

Native application-bundle CI uses PyInstaller on each OS. Those preview binaries
are unsigned/unnotarized and are not the promised final universal bootstrappers.
No script recommends disabling Gatekeeper, antivirus, or other OS protections.

The preview .RXAI writer rejects symbolic links. Some macOS .app bundles contain
framework symlinks, so such bundles require the separate native ZIP distribution
until a confined, audited app-bundle link policy is implemented. Do not strip or
rewrite a signed Mac bundle silently to force it into this preview package format.
