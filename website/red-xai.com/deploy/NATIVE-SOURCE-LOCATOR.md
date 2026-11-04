# Bounded native source filename locator

Source-only maintenance helper based on website commit
`eee8ae885d59fff6389282ebb2a12269420ad87f`. It has not been run on Kamatera.
It does not inspect account records, environment values, Go literals, live
services, listeners, binaries or source behavior. Finding a source path or Git
commit object does not prove it produced an installed binary or has account,
mail, protocol, persistence or engine parity.

`scripts/locate-native-source.py` performs one read-only directory traversal
over fixed `/root`, `/opt`, `/srv` and `/tmp`. There is no CLI root override.
It reports regular files matching only these exact case-sensitive suffixes:

- `host/acct/*.go`, immediately within `acct`, not arbitrary `service.go` files;
- `host/accounts_native.go`;
- `cmd/redx-mailrelay/main.go`;
- `cmd/redx-gdg-accounts/main.go`;
- `host/accounts.go` only with `--include-legacy-host-accounts`.

It prunes `.git`, `node_modules`, `data`, `dbs`, `var`, `backups`, `media`,
`uploads`, `certs`, `.ssh` and `.cloudflared` directory names without descending
into them. Symlinks are never followed. Directory descriptors, relative opens,
`O_NOFOLLOW`, `O_DIRECTORY`, device checks and opened-inode checks prevent a
replacement symlink or changed directory from steering the traversal. Mounts
on another device are pruned within each approved root. The nearest regular
`go.mod` and Git marker are recorded only within that root; neither marker's
contents are read by the locator.

The output contains matching code paths, kind, size, mtime, device/inode/link
metadata, closest marker paths and optional SHA-256. Hashing is off by default.
With `--sha256`, only exact matched regular files of at most 5 MiB and one hard
link may be opened. Raw bytes go directly to SHA-256, never text decoding,
parsing, AST inspection or output. Actual bytes read are capped independently
of advertised size; changed/truncated/replaced files never receive a verified
digest. A stable-metadata digest is not an atomic filesystem snapshot.

## Bounds and interpretation

Defaults are 100,000 total directory entries, 15 scan seconds, depth 20, 64
matches, 5 MiB per matched file, 16 MiB total hash bytes, and four Git object
probe repositories. Hard argument ceilings are 200,000 entries, 30 seconds,
depth 32, 128 matches, 5 MiB/file, 16 MiB total and eight repositories. All
encountered entries count, including pruned names. Names stream without
sorting or building an unbounded directory list. One global budget covers all
four roots. Output is capped at 2 MiB.

The worker checks the deadline between metadata operations and hash reads.
A separate supervisor waits scan seconds plus one second, then terminates
only its own worker, with at most half a second of join grace. A timeout or
worker failure discards partial worker data and emits explicit incomplete
coverage with unknown counters, rather than zero counts presented as known.
The read-only worker disables core dumps and regular-file writes. If a killed process remains stuck in a kernel operation, the CLI
reports unconfirmed termination and exits without an unbounded Python shutdown
join. This is a process deadline, not a real-time operating-system guarantee.

Every receipt explicitly says `sourceAbsenceProven:false`,
`sourceParityProven:false` and `exhaustiveFilesystemSearch:false`, even when the
filtered traversal finishes. Pruning, unreadable directories, mount boundaries,
depth/path caps, deadlines and unused roots are reported through fixed status
words/counters. Archives, deleted files, Git history, other roots, unmounted
storage and different filenames are not searched. No empty result may be
described as exhaustive absence of the original source.

## Optional Git object leads

`--vcs` is off by default. It may run only `/usr/bin/git ... cat-file -e` for
three fixed full build-reference revisions: canonical `bcc6c62a...`, mailrelay
`62516086...`, and GDG base `b3cfa4ff...`. Git stdout/stderr are discarded;
output is only locally available commit object or not confirmed. A nonzero
result is not called absence. The GDG build was modified relative to its base,
so that object's presence cannot establish the retained generated source's
revision or rebuild parity.

Probes use an inherited no-follow Git-directory descriptor, a minimal
environment, no global/system config, disabled helpers/hooks/replacements and
optional locks, no lazy fetch and no allowed transports. Each subprocess has a
remaining-deadline timeout capped at one second, CPU/address-space/file-size
limits, and Linux parent-death cleanup. Gitfiles, symlink/special Git markers,
`commondir`, symlink/non-directory object paths, and local/HTTP alternates are
refused without reading their pointer contents. Git itself may read local Git
metadata/configuration; this is not a Git configuration sandbox. Omit `--vcs`
when only the strict filename/stat/hash traversal is wanted. No found source,
Go compiler, application runtime, hook, credential helper, log/show/diff or
network command is executed.

## Reviewed invocation and tests

The CLI requires Linux and stdout redirected to a regular, singly linked file
owned by its current UID with no group/other permissions. It does not elevate
privileges or create an output pathname itself. The caller should use a new
private destination with a restrictive umask and exclusive redirect, for example:

```sh
(umask 077; set -C; python3 -I -S -B /reviewed/path/locate-native-source.py --sha256 > /private/new-source-locator.json)
```

Add `--vcs` only for the optional Git metadata probes. Exit 0 means the selected
filtered traversal finished without a reported coverage gap; exit 1 means a
bounded/incomplete receipt; exit 2 means a fixed refusal/error. Read the JSON
coverage flags for every result. Do not print the private receipt wholesale
into a chat; summarize controlled match metadata as needed.

Run the synthetic suite before any live walk:

```sh
python3 -I -S -B -m unittest discover -s scripts -p test_locate_native_source.py -v
```

Windows-local result: **14 tests, 11 passed, three Linux-only tests skipped**.
Tests exercise exact filenames, secret-bearing synthetic source and marker
files, every prune family, symlinks, directory replacement, mount boundaries,
actual hash-byte caps, truncation/replacement, aggregate budgets, unreflected
errors/arguments, private stdout and JSON path escaping. Linux-only fixtures
cover real descriptor/no-follow access, supervised timeout, and `cat-file -e`
against a manually constructed synthetic local commit object. Those Linux
fixtures and the actual server walk remain unverified until the root agent
runs them. No live source absence, recovery completeness or binary parity is
claimed by this source-only preparation.

## Kamatera verification, November 4 UTC

Root ran the exact committed Git blobs from
`932dadbff4c516f6be65bd3274c2ed856a98860c` on Kamatera. Their LF-byte SHA-256
values are `a2d0149ee3fb7c38ed49c6c6cb846bd6f8db774aef399f5fa660ccf29f8a85a9`
for the locator and
`eff7bb03036c70ea9b3bfe02cc69c8dfa180bccb819a04ebd6f2a7490cf0ecce`
for its test. Windows working-copy CRLF hashes differ; neither file was
executed until the committed-byte hashes matched on the server.

All 14 Linux tests passed in 2.041 seconds. One live scan used `--sha256` and
`--include-legacy-host-accounts`, without `--vcs`, and exited 0. Its root-only
receipt is `/srv/red-xai/stage-tools/native-source-locator-20261104.json`.
The filtered traversal finished within policy: 93,583 entries, 12,280
directories, 80 policy-pruned names, 1,248 pruned symlinks, zero metadata
errors, no depth/path/device pruning, 109,338 hash bytes and no Git probes.
It returned eight source leads: seven older `host/accounts.go` copies and the
previously identified GDG account main. It found no `host/acct/*.go`,
`host/accounts_native.go` or mailrelay main in the selected traversal.

These are metadata leads only. The receipt explicitly retains
`sourceAbsenceProven:false`, `sourceParityProven:false` and
`exhaustiveFilesystemSearch:false`. Archives, pruned trees and other roots
were not searched, and no live service, account data or source behavior was
changed or inspected by this walk. The original account authority and mail
relay source parity therefore remain unproven.
