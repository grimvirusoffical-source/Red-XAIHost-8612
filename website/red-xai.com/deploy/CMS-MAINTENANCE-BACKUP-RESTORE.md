# Private CMS maintenance backup and isolated restore

Prepared 2026-11-03 from owned local source and existing receipts. **Procedure only: its author performed no server/browser action.** This does not report a completed live CMS backup, restart, restore or public cutover.

## Fixed deployment and verified contracts

- Private Hub: `redx-site-cms-private.service`, loopback `127.0.0.1:14210`.
- Native CMS: `website_cms`, prefix `site-cms.red-xai.db`, value path `site-cms.red-xai.db:SiteState[ID=1].Payload`, existing `RedXCmsStore`, one serialized CMS writer.
- Support: `/var/lib/red-xai-site`, mode0700; includes media, `audit.jsonl`, and the maintenance receipt once seeded.
- Approved service key: `/etc/red-xai/site-cms.key`, regular file, no symlink, UID986/GID976, mode0600. Do not print, copy into an artifact, export or pass its value in arguments.
- Live native daemon: recorded PID992650, port17790, binary `/opt/redx-db-website-ec6e9798/redxd-8512e17`, source revision `8512e1704e589d24535e08082c3ab9f832d7655d`, SHA-256 `dd99a44f1c41f87dc8f9571c6ebb4daec34948a70fb145b81bf0e134bf5403748`.

The clean local native checkout `work/db-console-rebuild` is exactly that revision. Selected server source files were previously verified in `/opt/redx-db-assets-8512e17`; do not assume that directory contains a complete build tree or a native CLI executable.

| Operation | Contract in pinned owned source |
| --- | --- |
| Owner download | Existing canonical HTTPS DB console → Backups → **Download a backup now** calls `GET /v1/admin/backup`, receiving a gzip attachment named `redx-backup-UTC.tgz`. Website mode retains its existing same-origin cookie; backend requires host-wide admin authorization plus `ownerOnlyOK`. |
| Online consistency | The running daemon takes its host write pause, calls `checkpointAll()`, saves TTL tracking, then creates the archive through its own registry/key store. This uses the native online recovery contract; it is not a power-loss test or a second live engine. |
| Scope | **Host-wide**, not database scoped. It walks all `dbs/`, and includes key records, TTL and optional tenant/signing material. The CLI's global `--db` option does not filter this backup implementation. |
| Offline restore | Native **`redx` CLI**: `redx restore FILE --data-dir DIR`. It obtains `DIR/redxd.lock`, refuses a running destination and nonempty databases without `--force`, uses native archive extraction, restores key records/TTL/optional tenants/signing material and resets replication history. |
| Daemon | **`redxd` has no restore command.** Use a separately verified same-revision `redx` CLI for offline restore, and the original verified `redxd` binary for the later isolated daemon drill. |

The native archive contains every regular database file below `dbs/`, `keys.json`, `ttl.json`, and, when present, `tenants.json`, `updates/signing.key`, `updates/signing.key.meta`. `keys.json` holds salted Argon2 hashes and key-record metadata, including scope, DB/prefix, expiry and revocation; it contains no plaintext API keys. Native restore recreates `keys.red-xai-keys` from those records. The signing key is private material, so protect the archive as a sensitive recovery artifact.

Native backups omit native configuration, TLS certificates/keys, cluster secret/configuration, native audit log, keystore passphrase, `.red-xai-sec` master keys, the plaintext CMS key, and **all CMS support/media/audit files**. Inspect actual optional entries rather than assuming presence: the previous five-DB archive had no tenant registry. A separate cluster replication snapshot has a different contract and is not a substitute.

The native `backup --offline --data-dir DIR` command requests a coordinated backup from a running daemon with a 30-second wait, or copies directly after obtaining the stopped daemon's lock. Its `--force` fallback can copy inconsistently. This procedure uses only the existing owner HTTPS download and never either `--force` flag.

## Prerequisites

Record fresh selected metadata: private unit/PID/listener, native PID/start time/executable hash, support/key ownership/modes and independently observed native durability for that same process. The already reviewed empty native startup value resolves to effective `group` from pinned source; do not rewrite configuration or restart the native DB to make it explicit. Do not dump environments, key stores, user records or cookies.

Arrange one CMS maintenance window. Only the private Hub or the maintenance probe may own the dedicated CMS namespace at a time. A zero-match process search does not establish exclusivity by itself. Existing customer services, Accounts, Host, native DB and cloudflared remain running.

Prepare fresh protected capture and staging directories outside Git and public assets. Windows recovery paths must have owner/SYSTEM-only ACLs and no reparse points; Linux directories must be0700 with exclusive0600 artifact creation. Resolve absolute paths and every parent before operations. Never reuse a previous restore destination.

The new owned helper is `website/red-xai.com/cms-maintenance-probe.mjs`. Review and hash the exact file before placing it as a regular root-owned mode0644 file in a new dedicated mode0755 public-code directory, for example `/opt/redx-cms-maintenance-REVIEWED/cms-maintenance-probe.mjs`. Replace `REVIEWED` with the actual directory below. Do not change permissions on broader private stage-tools/recovery directories. Its live CLI supports exactly `seed|verify --private-gateway-stopped`, requires UID986/GID976, refuses a14210 listener, verifies the two pinned production modules, and uses fixed approved live CMS paths. The confirmation argument is an operator attestation, not an independent systemd inactive-state check.

**Seed is permitted only for a fresh native record:** envelope revision0 and empty posts, preferences and media. It refuses prior receipts, prior service-actor audit events, orphan media and existing app data. Do not weaken this rule or clear existing data to make a retry pass. Partial evidence is retained after failure. The helper creates one unpublished synthetic service draft, one real WebP and two non-account preference fixtures through the existing store. Its service actor has no owner flags and proves no real account authorization/isolation.

For the Linux restore drill, require a separately verified same-revision native `redx` CLI for that OS/architecture, with its hash recorded. A clean offline build of `./cmd/redx` from full unchanged8512e17 source and existing cached dependencies is acceptable; no source edits or GitHub credentials are needed. A prior Windows CLI is not a Linux executable. If the CLI/full build inputs are unavailable, record that blocker rather than guessing a daemon subcommand.

Bring the paired artifacts to the restore machine only through an already authorized protected path. No new public route, transfer tunnel, credential or upload destination is introduced. Missing protected transport remains a blocker.

## 1. Drain only the private Hub, seed and verify

In the existing authorized administrative terminal:

```sh
systemctl stop redx-site-cms-private.service
systemctl show redx-site-cms-private.service --property=ActiveState,SubState,MainPID,Result,ExecMainStatus
ss -ltnp 'sport = :14210'
test ! -e /var/lib/red-xai-site/writer.lock
```

Require inactive unit, mainPID0, successful graceful exit, no14210 listener and no writer lock. Existing lifecycle waits for HTTP handlers and the store queue before closing the store/releasing its lock. A timeout, forced kill or remaining lock is an inspection stop: **never delete the lock**.

After verifying the proposed helper path/hash and the actual fresh-state prerequisite, run once:

```sh
runuser -u redx-site -- /usr/bin/env -i PATH=/usr/bin:/bin NODE_ENV=production /usr/bin/node /opt/redx-cms-maintenance-REVIEWED/cms-maintenance-probe.mjs seed --private-gateway-stopped
runuser -u redx-site -- /usr/bin/env -i PATH=/usr/bin:/bin NODE_ENV=production /usr/bin/node /opt/redx-cms-maintenance-REVIEWED/cms-maintenance-probe.mjs verify --private-gateway-stopped
```

Require `ok=true`, `writerLockReleased=true`, unpublished draft, two synthetic preference keys, decoded realWebP, four native CMS mutations and ten service audit events. Capture the selected JSON privately. The output `cmsRevisionReadback:4` is the **CMS envelope revision**, not a native binary/source revision. Every store open enforces the existing six native `whoami` assertions: write, `website_cms`, exact prefix, `host_admin=false`, `owner=false`, leader. Before printing lock-release success the CLI independently requires `lstat(writer.lock)` to return `ENOENT`; it never suppresses a remaining lock or removes it manually.

For the private-Hub restart persistence check, close the probe, start only the private unit, verify its existing fixed loopback health/listener and `native-redx-single-writer` storage mode, then gracefully stop it again and rerun the same verify command. Do not restart the native daemon. The support lock must be absent before/after each stopped interval. Compare the selected unrelated service PIDs/start times/restart counts to baseline.

Probe verify checks the exact seeded state/media and the captured audit **prefix**. It intentionally permits later audit suffixes. Therefore a successful probe alone is insufficient to prove the **whole** paired audit/support snapshot; capture and compare those full hashes below.

## 2. Capture one coordinated native/support pair

Keep the private Hub stopped, probes closed and CMS namespace quiescent throughout this interval. Record UTC begin/end, native process/binary provenance and the selected verify result. Other native databases may remain active: this is a CMS recovery point, not a simultaneous full-server snapshot.

In the existing independently verified website owner HTTPS session, use Backups → **Download a backup now**. Keep the cookie in the browser. Do not export/replay it, use the CMS key for host backup, mint an admin key or add an admin write backdoor. If native backup fails, stop and retain error metadata; do not exclude an unreadable database or alter its permissions.

Retain the completed new attachment privately with filename, timestamp, size and SHA-256. Fully read gzip to EOF to check CRC, inspect tar metadata without printing contents, and reject links, devices, duplicate entries, absolute/traversal paths or unknown roots. Retain the full `dbs/<name>` inventory privately. The current deployment has **five databases**, including `fl_studio_db` and `website_cms`; all must survive. If the legitimate inventory grows, retain those too instead of filtering it back to five. The older pre-CMS archive is not this recovery point.

While still quiescent, archive the **entire** `/var/lib/red-xai-site`, including `media/`, `audit.jsonl`, the maintenance receipt and any other legitimate regular support entries. First inspect root/descendants with `lstat`: no writer lock, symlinks, hardlinked files or special files. Create the relative-path archive exclusively under umask0077, preserve metadata, flush/fsync it and its parent, and record bytes/SHA-256. Do not include `/etc/red-xai/site-cms.key`, and do not copy the running native database files directly.

Inspect the support archive's safe layout. Record every regular member's SHA-256 and verify each CMS media reference has its `<id>.webp`. Record **full audit bytes and hash**, independently of the helper's prefix hash. One protected pairing manifest must contain both artifact hashes/sizes, full DB inventory, CMS state hash/envelope revision, full support per-file hashes and capture interval. Hashes identify files; the quiescent interval plus native online backup establishes the paired CMS boundary.

Before releasing maintenance, rerun read-only verify with the Hub stopped. Require unchanged state/revision/media and independently unchanged **full** support/audit baseline. An unexplained change invalidates pairing; retain evidence and recapture after resolving exclusivity. Close the probe, require no lock, restart only `redx-site-cms-private.service`, check existing private health/listener and unrelated service metadata. No public routing is authorized.

The existing pinned age workflow may encrypt both artifacts to the existing recovery recipient. Before restore, fully decrypt/authenticate to new protected scratch and compare entire plaintext hashes, not a prefix or file size. Supply the identity only as the pinned tool's input path. No repository publication/upload is part of this procedure.

## 3. Native offline restore into an empty protected staging directory

Recheck both paired artifact hashes on the restore machine. Resolve the fresh destination and all parents; require empty protected directories distinct from every live data/support path. No daemon may have the restore destination open.

After verifying the native CLI path/hash, use its exact pinned-source contract:

```sh
# Set these only to independently reviewed absolute protected paths.
# REDX_CLI is the same-revision native redx CLI, not redxd.
# NATIVE_BACKUP is the new fully authenticated paired native .tgz.
# RESTORED_DATA is a fresh empty protected staging data directory.
env -i PATH=/usr/bin:/bin "$REDX_CLI" restore "$NATIVE_BACKUP" --data-dir "$RESTORED_DATA"
```

Do not pass `--force`, `--db`, API keys, live config or live data paths. Require successful exit and lock release before starting a restored daemon. Without a passphrase, restore creates an unencrypted-at-rest key-record store; protect the staging parent accordingly. If encrypted-at-rest staging is separately required, use an explicitly reviewed private passphrase mechanism, not a guessed live secret.

Extract support into a **separate new empty staging support directory** with an extractor that rejects links, special files, duplicates and path escapes. Never overlay current support. Set only new staging ownership/modes for its isolated verifier: directories0700, private regular files0600. Compare all restored support/media/audit bytes to the pairing manifest. Compare restored DB inventory to the complete native archive, including `fl_studio_db` and `website_cms`. Do not dump key-store contents.

## 4. Original daemon binary, isolated restored-host verification

Prepare a reviewed runner that starts the original verified `redxd` and the exported `verifyProbe()` in the **same isolated network namespace**, with fixture-only writable filesystem paths, no inherited application environment, no live native/support access and no public/tunnel route. `PrivateNetwork=yes` or equivalent can provide that namespace; a verifier outside it cannot reach its loopback. This runner is a prerequisite: the current maintenance helper's **CLI is fixed to live paths and is not a restored-host CLI**.

Use a new private allowlisted fixture TOML, never production configuration:

```toml
data_dir = "/REVIEWED/STAGING/native"
listen = "127.0.0.1:17791"
advertise = "http://127.0.0.1:17791"
node_id = "cms-restore-drill"
role = "leader"
peers = []
tls = "off"
bootstrap = "off"
engine_durability = "group"
audit_log = "/REVIEWED/STAGING/native-fixture-audit.jsonl"
accounts_issuer = ""
accounts_events_poll_seconds = 0
github_repo = ""
github_repos = []
hosted_apps = []
```

Inside that reviewed sandbox, the supported original-binary launch is:

```sh
env -i PATH=/usr/bin:/bin /opt/redx-db-website-ec6e9798/redxd-8512e17 run --config /REVIEWED/STAGING/fixture.toml --no-open
```

Require17791 free before startup, exact loopback listener/PID ownership afterwards and only restored data opened. `bootstrap=off` prevents generating/printing an admin key. Do not configure Accounts, live peers/join, GitHub, mail or billing. The native live daemon remains sole writer to its live directory throughout.

The host backup's restored key hashes permit the existing plaintext CMS key to authenticate **if its matching restored record is still active**. Permit only the verifier, as UID986/GID976, read-only access to the existing approved key file, preferably at the same path through a read-only bind. Never copy it into the backup or give its value to shell arguments/environment/logs. Missing, expired, revoked, mismatched or unreadable records block this drill; do not mint a broad key or alter restored scope/expiry/revocation to make it pass.

Construct `RedXCmsStore` with the restored support directory and real `website_cms` DB. Its supported `fetchImpl` injection must require logical origin `http://127.0.0.1:17790`, remap only that path/query to `http://127.0.0.1:17791` in the isolated namespace, reject other origins and preserve redirect refusal. The owned disposable integration demonstrates this mapping. Do not edit production store code or bind another service to live17790.

Call exported `verifyProbe()` with the existing pinned `processImage`; require six native scope checks, captured state/envelope equality, unpublished synthetic draft, both synthetic preferences, realWebP hash/decode and audit-prefix match. Independently compare **full paired support/audit hashes**. No call to seed/mutate/audit is required for restored verification. Take/release only the restored support lock. This tests legitimate reuse of copied key records with their existing scoped credential, not a new authorization path.

A separate native offline open/close check may verify every restored database, but only with the restored daemon stopped. Preserve the complete inventory and do not alter the user's `fl_studio_db` records. Gracefully stop the fixture, require process/listener exit and lock release, and record unchanged production native PID/start/restart metadata. Retain protected artifacts and staging for review; no deletion, overwrite or live promotion is included.

## Completion and blockers

Record actual execution separately from source inference/prepared commands. A passing receipt needs the new pair's hashes/sizes and complete DB inventory, exact native/source/CLI provenance, graceful private-Hub stop/restart, exclusive CMS writer, unchanged full capture baseline, successful fresh offline native restore, same-key exact scope validation on an isolated restored host, draft/preferences/WebP/full-audit equality and fixture shutdown.

Missing Linux CLI/full build inputs, protected transfer, reviewed isolated runner or active matching restored key record leaves the relevant gate blocked. Earlier synthetic/Windows native restore receipts do not replace this actual CMS pair. Owner/normal-account sessions, A/B tenant isolation, publication permissions, original-account/MFA recovery, full-server recovery and public cutover remain separate gates.

## Owned evidence inspected

- Clean native8512e17 checkout: `host/backup.go`, `host/server.go`, `host/keystore.go`, `host/config.go`, `cmd/redx/main.go`, `cmd/redxd/main.go`, `host/ui/js/pages/backups.js`, `host/ui/js/api.js`.
- CMS source: `redx-cms-store.mjs`, `site-backend.mjs`, `server-lifecycle.mjs`, `native-cms-integration.mjs`, `fixtures/native-cms-host.go`, `cms-maintenance-probe.mjs` and its seven local tests.
- `outputs/CMS-LIVE-PROVISIONING-20261103.md`: key metadata and native binary/durability provenance.
- `outputs/SITE-DB-BACKUP-20261103T215951Z-RECEIPT.md`: previous host-wide five-DB native restore, preserved `fl_studio_db`, historical Windows CLI provenance and explicit pre-CMS coverage limit.
