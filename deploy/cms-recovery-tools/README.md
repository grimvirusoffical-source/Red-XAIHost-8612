# Private CMS recovery tools — prepared for review

Source/local-fixture work only. No production command, service action, credential read, browser action or upload was performed by the tool author. The earlier owner-HTTPS procedure remains valid; root separately authorized assessing native OS backup IPC to avoid moving a browser download back to the server.

## Native IPC contract

Clean owned source revision: `8512e1704e589d24535e08082c3ab9f832d7655d`, `host/backup.go` and `cmd/redx/main.go`.

```sh
redx backup --offline --data-dir LIVE --out PRIVATE_ARCHIVE
```

While the live daemon holds `redxd.lock`, this normal command needs **no website cookie, API key, new administrator credential or keystore passphrase**. This is the existing filesystem-authorized maintenance interface. The CLI writes a random request under `LIVE/backup/`; the daemon polls every500ms and uses its normal host write pause/checkpoint to archive all databases, key hashes, TTL and optional tenant/signing material. The CLI copies/removes the IPC output. Timeout is30seconds. No second engine is started. No `--force` is used.

**Root CLI has a directory condition:** `LIVE/backup` must already exist daemon-owned0700. The daemon polls request names without reading root's0600 request contents, so that case works. If absent, root's CLI would create a root0700 directory the daemon cannot poll. `--ipc-user root` refuses that condition before any service stop.

Default `--ipc-user native` invokes the **same verified CLI as existing `redx-db-site`**, with a clean environment. It writes an exclusive0600 `.cms-capture-UUID.tgz` directly under the existing private native data directory; the normal CLI naturally creates any missing0700 IPC directory under the correct OS identity. Root copies the completed export into a new root0700 bundle, verifies the full hash, fsyncs file/parent, then removes only that task-created export. Failures retain partial evidence. No existing live permission is changed.

The public executable and its ancestors must be root-owned/non-writable by others and traversable/executable by the native user. If its build directory is private, copy the verified executable into a **new** reviewed root0755 tools directory; do not loosen the build directory, data, support or key permissions. Input hashes attest to the parent's separately controlled same-revision build, not a fabricated build stamp.

The explicit data directory must match the verified service process's open/held lock FD. PID, process-start ticks and executable hash are checked before/after native backup. A process change invalidates capture. The global CLI `--db` option does not filter this host backup. All five databases, including `fl_studio_db`, survive; additional legitimate databases are retained too.

## Files and preflight

- `cms_recovery.py`: inspection by default; guarded capture and fresh offline restore plus a unique runtime fixture unit.
- `restore-driver.mjs`: one sandboxed coordinator starts the copied original native binary and calls the existing maintenance probe's **verify only**, using c443's store and pinned image codec. It verifies complete support/audit bytes and gracefully stops the restored daemon.
- `test_cms_recovery.py`, `restore-driver.test.mjs`: synthetic archive, state-machine, GET-remap and full-audit checks.
- `native-ipc-check.mjs`: actual disposable pinned-native live-IPC/fresh-restore test. Accepts source-checkout paths only; all state/keys/listeners are disposable. Synthetic keys stay in private child pipes/temp files.

Stage reviewed helpers/driver/probe and same-revision CLI at actual root-owned paths and record independent SHA-256 values. The existing maintenance probe must already have seeded the fresh CMS namespace and completed private-Hub restart/readback; this capture tool never seeds. Key material is never an argument.

Uppercase words below represent actual reviewed paths/hashes:

```sh
python3 -B /REVIEWED/TOOLS/cms_recovery.py capture --cli /REVIEWED/TOOLS/redx-8512e17 --cli-sha256 CLI_SHA256 --probe /REVIEWED/TOOLS/cms-maintenance-probe.mjs --probe-sha256 PROBE_SHA256 --live-data-dir /ACTUAL/VERIFIED/NATIVE/DATA --ipc-user native
```

Inspection verifies fixed native binary/process, key metadata only, pinned c443 modules, private-Hub deployment, private support and existing OS identities. It does not stop a service, create a backup or open the key. Add `--apply --exclusive-writer-reviewed` only for the reviewed actual capture.

The native executable pin is the full 64-character SHA-256
`d99a44f1c41f87dc8f9571c6ebb4daec34948a70fb145b81bf0e134bf5403748`.
Pin-format tests preserve that transcription and ensure malformed hashes never
relax the exact-byte guard. Actual installed/running executable provenance must
still be verified separately on the server; hash syntax is not that proof.

Capture stops **only** `redx-site-cms-private.service` if initially active, requires graceful exit/no14210 listener/no lock, runs the reviewed probe's verify as `redx-site`, then captures native IPC and full support within one CMS-quiescent interval. Support refuses links, hardlinks, special files and writer locks; both archives are CRC/layout checked, and every support payload is hash checked. Full audit/support hashes, native inventory and probe state/receipt hashes are paired in private `pair.json`; the entire baseline is reread before acceptance.

The private Hub's **initial state** is restored: an initially active unit is restarted and checked at its existing fixed health endpoint even on capture failure; an already stopped unit stays stopped. A remaining lock blocks resume and is preserved. Seven customer services retain selected PID/start/restart metadata. No support rollback, lock deletion, native restart, permission repair or public routing occurs.

Success prints the private pair directory and `pairSha256`. Its `native.tgz`, `support.tgz`, `pair.json` are0600 under root0700. Native plaintext includes key hashes/signing private material and remains sensitive. Encryption/upload/publication is outside these tools.

## Isolated restore and unit instructions

First inspect the pair using its independently retained manifest hash:

```sh
python3 -B /REVIEWED/TOOLS/cms_recovery.py restore --cli /REVIEWED/TOOLS/redx-8512e17 --cli-sha256 CLI_SHA256 --probe /REVIEWED/TOOLS/cms-maintenance-probe.mjs --pair /srv/red-xai/backups/ACTUAL_PAIR_DIRECTORY --pair-sha256 PAIR_SHA256 --driver-sha256 RESTORE_DRIVER_SHA256
```

Add `--apply` after reviewing these sources and actual paths/hashes. It creates an exclusive root0700 `cms-restore-UUID` under `/srv/red-xai/cms-restore-fixtures`, never an existing destination. Native restore uses the same CLI's `restore FILE --data-dir NEW`; no force, API credentials or daemon against that destination. Support extracts through strict entry/type/hash checks into another new directory. Ownership changes apply only to newly created, resolved/bounds-checked staging copies.

The tool writes the exact unit to the private fixture, then calls the server's `systemd-analyze verify` before installing its unique runtime unit. Inspect that generated file if the parser refuses; unsupported options/setup are blockers, not permission to remove isolation. Its properties are:

- `RootDirectory` with only explicit readonly runtime binds (`/usr`, actual `/lib*`, loader cache), c443 modules/dependencies, reviewed copied tools and the existing key bound readonly at `/credential`.
- Restored data/support/config alone writable under `/work`. No live native data, live support, server root, production env/config, Accounts secrets or GitHub credential is bound.
- A private PID namespace, with `/sys` inaccessible. Before any credential read or native child, the driver requires PID1, only that PID visible, and a PID-namespace inode different from the host reference recorded by the coordinator. Unsupported or ignored `PrivatePIDs` therefore fails closed. Core dumps are disabled. Node20 aborts during startup when `/proc` is entirely masked; host process visibility is still prohibited.
- `PrivateNetwork=yes`: Node and its copied original native child share one isolated loopback namespace on17791. No host public listener, external route or transfer tunnel; additional IP filtering allows localhost only.
- No new privileges/capabilities, private devices/temp, strict filesystem protection, no stdout/stderr, bounded startup/stop and fixture-only cgroup cleanup.

Fresh TOML has restored data, loopback17791, leader/no peers or join, bootstrapoff, group durability, separate native fixture audit, empty Accounts issuer and empty hosted/GitHub configuration. Environment is clean. Original native binary copy must match the pinned executable hash. There is no administrator bootstrap credential. `.red-xai-sec` master keys are not copied: CMS requires none, while any unrelated encrypted customer-content reads remain outside this evidence.

Only the verifier reads the existing approved key through the readonly bind; no copied key, environment/argument value or log is used. Restored native key hashes accept the same plaintext key only if its matching record remains active. Missing, expired, revoked or mismatched credentials fail the existing six physical `whoami` assertions. No restored key record/scope/expiry is changed to force success.

The store's supported `fetchImpl` maps only logical native17790 GETs to isolated17791, refusing other origins, writes and redirects. The driver imports **exported `verifyProbe()`**, never the helper's live CLI. No seed/mutate/audit operation occurs. Exact captured CMS revision/state/receipt/media and **every full support/audit file** must match, beyond the probe's audit-prefix check.

SIGTERM must gracefully stop the restored daemon; needing SIGKILL fails the drill. The unique fixture unit is stopped, removed from `/run/systemd/system` and daemon-reloaded. Its reviewed unit text and all private staging artifacts remain retained. Seven live customer service baselines must remain unchanged. There is no live promotion, overwrite or cleanup deletion of recovery artifacts.

RootDirectory path restrictions explicitly use the systemd `+` prefix. The private PID namespace follows the [official systemd execution contract](https://www.freedesktop.org/software/systemd/man/257/systemd.exec.html); runtime checks remain required. Bounded exclusive0600 `restore-status.jsonl` contains only allowlisted phase/status words. It never contains exceptions, credentials, environment values, subprocess output or restored contents. A failing phase is diagnostic evidence, not restored-host acceptance.

## Validation

```sh
python -B -m unittest -v test_cms_recovery.py
node --test restore-driver.test.mjs
node native-ipc-check.mjs /ABSOLUTE/CLEAN/NATIVE_8512_SOURCE
```

Run tests from this directory. The native fixture requires an explicit clean native checkout at the exact8512e17 revision and cached Go dependencies; it builds offline. Site modules default to this repository's `website/red-xai.com`, which needs its pinned dependencies already installed. An optional second absolute argument selects another reviewed site source checkout with the same modules/dependencies, allowing a local fixture run to reuse installed dependencies without changing either source checkout. The script rejects linked source roots. No database directory, key path or production endpoint is an input.

At preparation: Python9 passed/1 POSIX-mode test skipped on Windows; Node driver3 passed. Actual disposable native test passed a live native IPC backup while its daemon remained up, fresh same-revision CLI restore, all five databases including `fl_studio_db`, same scoped key/six scope assertions, draft/two preferences/WebP and **full support/audit** equality. Native source stayed clean; fixture processes/files were stopped and bounds-checked before removal. The portable fixture entrypoint passed the same checks before commit, with synthetic archive SHA256 `5b87d71346f65ea0ede325a7d06f772eda3b0d6f410c2c65fb7d28a9bcfa3c98`. It was deleted with its disposable fixture and is not a production recovery artifact; archive hashes vary with synthetic timestamps and random media/key records.

Independent source review found no material blocker and independently reproduced Python9/1skip and Node3 passing tests. Windows cannot validate actual Linux root ownership, chroot dynamic libraries, systemd parser/startup or network namespaces. The real server must pass its parser and full isolated run before restored-host acceptance is claimed. The fixture's successful inventory/readback does not prove every user's record/pack opens, real website-owner/session authorization, A/B account isolation, original-account/MFA recovery or full-server recovery.
