# Selected Host SQLite ledger backup

This standard-library helper captures one explicitly selected SQLite ledger. It
does not start Host, read application configuration, query or print application
rows, change source
settings, checkpoint, restore, handle native RedX files, or establish full Host,
account, bundle or worker recovery. Run it in its own process, separate from all
writers. Linux root with `/proc`, Python 3.7+ and SQLite 3.22+ are required.

## Reviewed input and invocation

Supply a root-owned regular expectation JSON file, mode 0600 or stricter, without
links, maximum 16 KiB. Duplicate/unknown fields and ambiguous paths are refused.
Paths/numbers below are placeholders, not production defaults:

```json
{
  "version": 1,
  "source": {"path": "/approved/canonical/ledger.sqlite3", "device": 1, "inode": 2},
  "host": {
    "boot_id": "00000000-0000-0000-0000-000000000000",
    "pid": 123, "start_ticks": 456,
    "exe": {"path": "/approved/runtime", "device": 1, "inode": 3},
    "cwd": {"path": "/approved/deployment", "device": 1, "inode": 4},
    "ledger_fd": {"number": 10, "device": 1, "inode": 2}
  },
  "reader": {"python": "3.11.0", "sqlite": "3.40.0"}
}
```

Observe these identities freshly from the actual selected Host process. The
kernel open-FD pathname must equal the canonical source pathname, not merely its
inode. Reader versions must exactly match the reviewed runtime. Exe identity does
not identify the Bun/libSQL SQLite library; record its version separately.

```sh
python3 -I -S -B capture.py --expectations /root/reviewed-host-ledger.json \
  --output-dir /root/recovery/NEW-UNUSED-BUNDLE \
  --max-output-bytes 1073741824 --reserve-free-bytes 1073741824 \
  --deadline-seconds 60
```

Default inspection checks runtime, process/path identities, namespace and space.
Use a trusted isolated interpreter as above; this standalone helper needs no
site packages, PYTHONPATH, startup customization or fallback installer.
It does not open SQLite or create output. `inspected` is not a successful backup.
Add `--capture` only for the concrete reviewed selection. Output must be a new
absolute canonical directory under a root-owned non-writable parent. Existing
directories are refused; there is no overwrite, automatic resume or deletion.

Maximum output is 64 KiB–1 TiB, reserve 0–2^50 bytes, and deadline a finite positive
number up to 900 seconds. Preflight requires twice the maximum output, reserve,
a 4 MiB batch allowance and more than eight free inodes. Statvfs is not quota proof;
concurrent writers may consume available space. Output limits are checked after
64-page batches, so transient allocation can overshoot a checked cap. This is not
a hard disk quota. The 100 ms lock timeout and 50 ms retry sleep are not total runtime
bounds. Progress/validation deadlines are cooperative; native operations, fsync
and kernel I/O can overrun them. Use a reviewed outer watchdog targeting only this
helper for a process deadline. Uninterruptible kernel operations remain a limit.

## Capture and evidence

Capture exclusively creates a 0700 bundle and 0600 `INCOMPLETE` before SQLite opens.
The canonical, correctly encoded source URI uses `mode=ro` and private cache.
There is no `/proc/self/fd` URI, immutable/unlocked mode, permission workaround or
source PRAGMA assignment. Source SQL reads only `journal_mode`, `page_size` and
`page_count` metadata. Source/sidecars must be regular single-link files in a
trusted canonical namespace; symlink/alias, unsafe owner/write permissions and
observed process/FD/path changes are refused. The immediate source parent must
not be group/world-writable. Root-owned sticky ancestors such as `/tmp` are
permitted only above a protected trusted directory, never as the source parent.

The source raw anchor stays open until source SQLite closes. The raw exclusive
destination anchor stays open through every destination/validation connection.
Do not import this into a writer or add raw source-inode opens/closes: POSIX close
can cancel locks owned by SQLite elsewhere in the same process.

The fresh destination uses DELETE journal mode and FULL synchronization, without
an explicit open destination transaction. After copying, only the destination is
normalized to standalone DELETE mode. Backup connections close, then a fresh
read-only destination connection must return exactly `ok` from `integrity_check`
and close. Required WAL/SHM/journal sidecars are refused. Final identity, size,
private modes and stability across hashing are checked; file/directory fsync and
source/process rechecks precede completion.

Success contains `ledger.sqlite3`, `report.json` and `COMPLETE`, all 0600.
`COMPLETE` holds the report SHA256. The private report records reviewed expectation
hash, reader/source/process identities, observed copy interval, limited metadata,
artifact size/hash and iteration count. It does not contain queried rows, tokens,
configuration or raw SQLite errors. Failure retains private evidence and, where
possible, a fixed stage/reason in `failure.json`; it does not resume the copy.

**Any `INCOMPLETE` overrides `COMPLETE` or report.status.** Completion requires
successful helper exit, no INCOMPLETE, valid COMPLETE/report hash, artifact hash
and the reported standalone integrity result. Interrupted publication can leave
partial evidence. Storage failure can prevent durable failure marking too. Fsync
cannot prove hardware reliability or complete application recovery.

Final directory-descriptor release is best-effort, without retrying an ambiguous
close. After payload handles, persistence checks and markers succeed, a
directory-only close error preserves completion and sets the fixed boolean
`cleanup_warning` in stdout; process exit releases remaining descriptors. SQLite,
source/artifact close and fsync failures still refuse completion.

Stdout is one fixed JSON object with schema/status/code/cleanup_warning, without raw exceptions or
tracebacks. Exit 0 means inspected/complete; exit 1 means refused. Identity drift,
existing output, unavailable evidence, busy/deadline, space/output limit,
corruption, permissions, I/O or finalization failures cannot report success.

## Limits and tests

Checks are sampled observations, not an atomic process/path pin. A raw anchor
does not bind SQLite's separate pathname open. A trusted stable namespace and no
concurrent administrative replacement are prerequisites; use a trusted local
filesystem with working SQLite locking. The helper does not attest the VFS,
storage hardware or locking implementation. Swap-and-restore between
samples is not excluded. Normal source size/mtime/ctime changes are permitted.
Read-only WAL participation can still use SHM/read marks and locks; zero source
directory effects or contention is not promised.

Completion is one consistent committed SQLite snapshot including visible WAL
commits, excluding uncommitted changes. Copying may restart during writes and
never finish. The recorded interval is not a guaranteed start timestamp, freshness
bound, cross-database snapshot or application recovery certificate.
[SQLite backup](https://www.sqlite.org/backup.html),
[API restrictions](https://www.sqlite.org/c3ref/backup_finish.html),
[locking hazards](https://www.sqlite.org/howtocorrupt.html).

Read-only WAL requires SQLite 3.22+. The documented WAL-reset writer/checkpointer
fix is 3.51.3+, with backports 3.44.6/3.50.7; this is not a broad `>=3.44.6` safe
range. Reader pinning neither identifies nor upgrades the actual writer build.
[WAL documentation](https://www.sqlite.org/wal.html),
[Python backup](https://docs.python.org/3/library/sqlite3.html#sqlite3.Connection.backup).

Run `python3 -I -S -B test_capture.py`. Portable guards run anywhere; Linux-root fixtures
spawn separate synthetic writer/helper processes under temporary private paths.
The Linux CI fixture runs in a fresh network namespace and fails on any skipped
test; Python setup/downloads occur before that fixture isolation.
Only tests inspect fixture rows. CI never accesses production data, services,
credentials, application configuration or native RedX engine code.
