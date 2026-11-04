# Native-only current website DB backup

This controller uses unchanged native8512 daemon IPC and the existing pinned Linux CLI. It never stops, starts or restarts a service, opens a second live engine, restores live data, changes engine/configuration/code, or calls the older CMS capture/probe. The public Hub stays running. Native IPC backup pauses host writes while copying; background checkpoints can still run. Native8512 relies on WAL recovery for this online backup. A matching offline reconstruction is **not** proof of an application-consistent snapshot or semantic recovery of every record.

Fixed inputs still require fresh controller checks:

| Input | Path / SHA256 |
| --- | --- |
| Website native data | `/opt/redx-db-website-ec6e9798/data` |
| Native binary | `/opt/redx-db-website-ec6e9798/redxd-8512e17` |
| Native binary SHA256 | `d99a44f1c41f87dc8f9571c6ebb4daec34948a70fb145b81bf0e134bf5403748` |
| Existing CLI | `/opt/redx-cms-maintenance-ba8f91f/redx-8512` |
| CLI SHA256 | `cfe3cc75b363e741f8b4594b38ab9a040b449fe7eac233d0ba3899fe1e596c19` |
| Unchanged `cms_recovery.py` Git/LF SHA256 | `e833b99c4ad283bc78f81bf8c17478bdfdd9f4322a6b98f63c536bca6b60faa3` |

The library is read through a no-follow descriptor, checked for stable metadata and exact bytes/hash, then those same bytes are loaded. No line-ending normalization happens in production. Its subprocess runner is limited to explicit read-only unit properties, native executable-access checking, fixed native IPC backup and a fresh offline restore. The library itself remains unchanged, including its older minimum-five coverage check; this wrapper adds the stricter private expected inventory.

## Private expected inventory

The controller must maintain an independently verified current inventory **outside any source checkout**. Use an absolute root-owned, root-group, single-link regular file with exact mode0600, below root-owned directories with no group/other write permission; no symlinks. Supply at least seven unique native-valid names, and every currently known database. Extra legitimate databases in the resulting archive are retained and verified. Do not derive this expected list from the very archive being tested or publish real names in GitHub, logs or chat.

Schema (synthetic names only):

```json
{"schema":1,"databases":["synthetic_0","synthetic_1","synthetic_2","synthetic_3","synthetic_4","synthetic_5","synthetic_6"]}
```

Duplicate JSON keys/names, extra fields, fewer than seven names and invalid native names are refused. The private receipt binds the inventory's SHA256 and names; public JSON contains counts only.

## Review and run

Stage reviewed LF source under trusted root-owned directories, with the unchanged pinned library available separately. Pass absolute paths. First run tests as Linux root; **no skipped Linux-root tests are acceptable on Kamatera**. Deployed tests look for `cms_recovery.py` beside `test_capture.py`; checkout tests use the existing sibling source directory.

```sh
python3 -I -B -m unittest discover -s /ABSOLUTE/TOOLS -p test_capture.py -v
python3 -I -B /ABSOLUTE/TOOLS/capture.py --library-file /ABSOLUTE/REVIEWED/cms_recovery.py --expected-inventory /PRIVATE/expected-inventory.json
python3 -I -B /ABSOLUTE/TOOLS/capture.py --library-file /ABSOLUTE/REVIEWED/cms_recovery.py --expected-inventory /PRIVATE/expected-inventory.json --capture
```

Default inspection validates private inputs, pinned executables, native identity/held flock, all ten monitored service baselines and current read-only Hub health. It creates no recovery artifact and does not enumerate live engine data. Explicit `--capture` performs one bounded manual capture. It requires an existing root0700 `/srv/red-xai/backups`; it does not repair permissions.

Before capture, the controller must separately verify spare bytes and inodes on the native data and backup filesystems for the native export, private copy and full offline restore. The archive size limit is checked **after** the native CLI writes its export; it does not reserve capacity or cap that write. Inspection/tests do not establish available capacity.

The native process's PID/start/executable and current lock inode are checked before and after. Its open FD and an exact, non-blocked `/proc/locks` FLOCK write owner must match that inode/PID. Every service's PID/start/restart counter and Hub read-only health must remain stable. No API credential or keystore passphrase is supplied. Every permitted subprocess is bounded by a timeout; private child output/errors are withheld from public JSON.

Capture creates one exclusive root0700 `native-seven-TIMESTAMP-ID` bundle. The existing `redx-db-site` identity writes one exclusive native-owned0600 `.native-seven-ID.tgz` in its private data directory. Its inode/metadata, bounded copy, hash readback, file and parent fsync establish root0600 `native.tgz` in the bundle. Strict single-gzip CRC/size and complete tar layout/zero EOF padding checks reject links, duplicates, unsafe paths and hidden trailing payloads. The expected databases plus every additional archive database are required.

The same pinned CLI restores only into the newly created, empty root0700 `restored` directory. No restored daemon or engine starts. Verification requires exact database and full restored-tree inventories, every DB/TTL/tenant/signing payload byte hash, and all full key-record JSON fields keyed by unique ID. Key serialization/order can differ, so `keys.json` is compared by canonical, sorted, compact records rather than file bytes. All fields and nested JSON types are preserved; booleans and numbers differ, and nonfinite numbers are refused. Generated lock files must be empty. All restored files/directories are checked private, hashed and fsynced before the private receipt is fsynced. The receipt retains detailed inventories and file hashes. Missing or dropped optional metadata fails validation, even if the CLI returned success.

**The private native-owned temporary export is deliberately retained on success and failure.** Checking an inode and then unlinking a pathname in the daemon-writable directory cannot prove the same entry is removed. Its identity/hash/path are recorded only in the private successful receipt; public JSON reports `temporaryExportRetained:true`. Later cleanup needs a separately reviewed exclusive controller procedure. Do not repeat captures unattended or let retained archives fill the filesystem. Unknown/failed/partial evidence is preserved; no automatic cleanup, overwrite, `--force`, permission repair or reused restore destination is offered.

## Scope and evidence

Success proves a durable native archive and exact offline file reconstruction plus full key-record equality. It does not prove record-level/WAL acceptance, a clean snapshot, live key-client acceptance, full CMS/support/media/audit recovery, off-server recovery or full-VPS recovery. The old synthetic revision4 CMS verifier and transport receipt must not be reused or reset for this operation. Native archives/restores contain private key-record hashes and potentially signing material; retain root0700/0600 protection and use separately reviewed encryption/transport.

Tests use synthetic names, temporary files and mocked service commands. Portable tests check private schema, pins/command limits, archive attacks, metadata/key corruption, inventory preservation, identity changes, failure evidence retention and public redaction. They do not attest POSIX protections. Four Linux-root tests exercise actual private ownership/modes, links, inode replacement, copy/fsync, restored content and a synthetic child-held FLOCK; they never call systemctl, touch native data or start an engine. The pinned library's historical real-name membership requirement remains enforced in production; synthetic archive tests reuse its generic CRC/path checks and supply a synthetic inventory adapter.

## Focused CI

`.github/workflows/native-seven-backup.yml` runs only `test_capture.py` on an ephemeral Ubuntu24.04 runner as root, using the runner's `/usr/bin/python3` after requiring Python>=3.11. It fails on any skipped test, zero discovered tests, failure or error. Checkout has read-only contents permission and does not retain credentials. The root test process receives a clean environment, uses isolated Python and rejects socket audit events. Checkout accesses GitHub; the synthetic tests use no network, native daemon, real services, private inventory, deployment or credentials. Existing broader CI is unchanged.

The job runs for helper, pinned-library or workflow changes on pushes to `feature/native-seven-backup`, pull requests targeting `main` or `feature/unified-hub-rebuild`, and manual dispatch. A passing job verifies synthetic Linux-root syscall/fixture behavior on that runner. It does not establish Kamatera permissions, executable identity, live flock/service/Hub baselines, available capacity, native restore behavior or record/WAL acceptance. The controller must still run the Linux-root suite without skips on Kamatera, verify capacity and fresh inspection there, and review actual capture/restore evidence before making any production recovery claim.
