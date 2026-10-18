# RedX storage engines (Phase 1)

Package `redx/engine`. One `Database` owns a directory; every file in it whose name ends in a RedX extension is a
*table* (an in-memory Box tree guarded by one `RWMutex`). The extension selects the engine.

| extension | engine | RAM | journal | restart |
|-----------|--------|-----|---------|---------|
| `.red-xai.db` / `.red-xai-db` | Cold durable | full tree | WAL + checkpoint | exact |
| `.red-xai-db-qs` | Volatile quick storage | full tree, timing wheel | none | empty (or seed file) |
| `.red-xai-db-qs-ls` | Hot persistent | full tree, timing wheel | WAL + checkpoint, coalesced | exact (bounded loss for `(N)*` keys) |

Public API: `Open(dir, Config) (Store, error)`; `Store` = `Get, Set, Delete, Query, Subscribe, Stats, Close` (+ `Begin()`
transactions on `*Database`). `Replicable` adds the cluster surface (§8). Paths: GRAMMAR.md §4.

## 1. Time and TTL model

* A deadline is an **absolute unix-millisecond timestamp** (`TTL.ExpiresAt`). It is what is journaled, checkpointed
  (`(30@1800000030000)`), replicated and compared. Relative seconds are only used to compute it at `Set` time.
* An entry is expired when `now >= expiresAt` (a read at exactly `T0+30s` of a 30 s TTL sees it gone; one
  millisecond earlier sees it live).
* `(N)` fixed: never moves. `(~N)` sliding: every successful `Get`/`Query` hit moves the deadline to `now+N`.
* A box with a TTL expires its whole subtree. A child may carry a *shorter* TTL than its parent; it never outlives it.
  Structural parent boxes without a TTL are allowed in qs files and vanish when their last child does.
* Clock jumps: forward – one sweep expires everything due (the wheel advances in bounded batches); backward – the
  sweeper detects the regression, rebuilds the wheel from the data (`rearm`), keeps absolute deadlines (keys live
  longer, never shorter) and keeps firing new TTLs on time.

### Decisions (acceptance round)

| id | decision |
|----|----------|
| E8 | A `Get` of an expired or missing entry in a **qs / qs-ls** file returns `(NELL, nil)`, every time (the first read purges, later reads answer the same). Cold files still return `ErrNotFound` for missing paths. |
| E10 | `Set` into a qs file with no TTL → `ErrTTLRequired` (unless `Config.DefaultTTLSeconds > 0`). `TTLSeconds < 0`, or `Sliding` without a positive TTL → `ErrBadOpts`. |
| E11 | Only a successful `Get` or `Query` **hit** slides a `(~N)` deadline. `Stats`, `Subscribe`, failed Gets, reads through the `link.Store`/`pulse.Store` adapters, and follower reads never slide. |
| R5 | Followers use their own clock only to *hide* entries whose absolute deadline has passed. A follower whose clock runs ahead hides data early; one that runs behind keeps showing data until the leader's delete arrives. Deletion itself is always the leader's decision. Run NTP; skew only affects visibility, never state. |
| R6 | A sliding-TTL move on the leader is journaled and replicated as a `touch` op (`Path`+`ExpiresAtMs`), emitted only when the move is ≥ 10 % of the TTL (a hot key read 1000×/s writes about 10 frames per TTL period, not 1000). Followers apply it verbatim and never slide on reads. The consequence for crash recovery is that a restarted qs-ls key may be up to 10 % of its TTL *shorter* than the last read implied. |

## 2. Cold durable (`.red-xai.db`)

* **Write path:** validate → apply to the tree under the table lock → append one WAL frame → ack according to the
  durability mode. A frame is one atomic commit (multi-op transactions are one frame).
* **Durability modes** (`Config.Durability`): `sync` (fsync every commit), `group` (default; group commit: one fsync
  covers every commit that arrived while the previous fsync ran), `async` (ack before fsync, background flush every
  `AsyncFlushMS`, default 10 ms; crash loses up to that window). The mode is a per-database setting.
* **Concurrency:** one table lock; reads share it. Transactions (`db.Begin()`) are optimistic: reads record
  `(path, version)` pairs (absent keys are recorded as version 0, so phantoms are detected), commit validates under the
  lock and either applies all ops or returns `ErrConflict`. A transaction spans one file.
* **Atomicity & isolation:** serializable per file (single writer lock + read-set validation).

## 3. Volatile quick storage (`.red-xai-db-qs`)

* RAM only. Nothing is journaled; the file on disk stays byte-identical (a hand-written *seed* file is loaded at
  open with TTLs counted from load time, and never written back).
* Hierarchical timing wheel (`wheel.go`): O(1) insert/cancel, 8-bit levels, a level-0 occupancy bitmap so idle ticks are
  skipped. It never fires early (tests check ordering and "never early" over random schedules and clock jumps).
* **Expiry** = lazy + background. A read of an expired entry hides it and purges it (logged as a synthetic delete, event
  `expire`); the sweeper (`Config.SweepMS`, default 100 ms) drains the wheel and deletes in batches of 512 per lock hold so
  readers/writers are never stalled by a mass expiry. Because the wheel knows the next deadline exactly, no random
  *sampling* of keys is needed (Redis samples because it has no deadline index).
* Exactly one `expire` event per entry, even when the lazy path and the sweeper race (E15).

## 4. Hot persistent (`.red-xai-db-qs-ls`)

* Working set pinned in RAM; reads never touch disk.
* **Ordinary entries:** journaled like cold (immediate, per durability mode).
* **`(N)*` entries:** the write updates RAM, a *delta* is coalesced per key (later writes to the same key replace the
  pending delta; deleting or superseding drops it) and flushed to the journal at the latest N seconds after the first
  unflushed write. A crash loses at most N seconds of such writes and never leaves a stale older value ahead of a newer
  one; `Stats` exposes `writes_coalesced`, `deltas_flushed`, `pending_deltas`.
* Expiry works as in qs; expiries are journaled as synthetic deletes, so recovery never resurrects an entry the
  sweeper already removed and an entry whose deadline passed during downtime is hidden at once and purged by the
  first sweep (recovery re-arms the wheel from absolute deadlines).

### On-disk formats (cold and qs-ls)

* **Checkpoint** = the data file `<name>`: plain Box grammar text (human readable, absolute `(N@ms)` expiries) followed by
  a footer comment `<< REDX-CKPT v1 lsn=42 walgen=3 len=1234 crc32c=deadbeef >>`. It is written to `<name>.tmp`, fsynced,
  `rename`d over the data file, directory fsynced (atomic: old or new, never a mix). A file without a valid footer/CRC is
  `ErrCorrupt`, never "empty".
* **WAL** `<name>.wal.<gen>`: 16-byte header (`REDXWAL1`, generation) then frames
  `len u32 | crc32c(payload) u32 | payload` where the payload is `lsn | time | ops…` (op kinds set/delete/expire/touch).
  Checkpointing rotates to a new generation first, snapshots, then deletes older segments.
* **Recovery:** load checkpoint → replay segments with `gen >= walgen` and records with `lsn > ckpt.lsn`.
  * A **truncated or garbled tail** (short frame, bad CRC, nothing valid after it) is the signature of a crash mid-write:
    it is dropped and reported (`wal_torn_bytes_dropped`); the log is truncated to the last good frame.
  * **Damage in the middle** (a bad frame with valid frames after it) is *not* a torn tail: `Open` fails with
    `ErrCorrupt` so acknowledged commits are never discarded silently. `Config.RepairWAL` opts in to dropping everything
    from the damage on; the dropped bytes are preserved in `<seg>.torn-*`.
  * A segment shorter than its header (crash while creating it) is treated as empty. A full-length wrong header is corrupt.
  * Orphan `*.tmp` checkpoints (complete or partial) are deleted, never adopted. Stale segments older than the checkpoint
    are ignored. LSNs continue at `last+1`; they are never reused.
  * Recovery is idempotent: crashing during/after recovery and recovering again gives the same state (C9).
* **I/O errors:** a failed write/fsync latches the table into a failed state (`ErrFailed` for all later writes), the
  failing commit is *not* acknowledged, and a reopen recovers the last durable prefix (C10). We never retry fsync.

## 5. Events (`Subscribe`)

`Subscribe(prefix)` returns a buffered channel and a cancel func. Events: `set`, `delete`, `expire` (Path, Op, At). Delivery
is non-blocking: if a consumer's buffer is full the event is dropped and counted (`events_dropped`), so a slow subscriber
can never stall writers. `adapters.go` bridges events into `pulse.Bus` (topics `change.set/delete/expire`).

## 6. Stats

`Database.Stats()` returns a flat `map[string]float64`: per-engine counters (`cold.*`, `qs.*`, `qsls.*`: gets, sets,
deletes, expired, lazy_expired, checkpoints, txn_commits, txn_conflicts, writes_coalesced, deltas_flushed,
recovered_records, wal_* …), `wheel_items`, `entries_top_level`, and `events_sent/dropped`.

## 7. Adapters

`Database.Records()` returns a `*RecordStore` implementing `link.Store` (and the `pulse.Store` surface), and `BridgeChanges` forwards `Subscribe` events into `pulse.Bus`
(see `adapters.go`, `integration_test.go`). Dangling `@>` links, `@!>` cascades and pulse rules/timers are exercised over
the real engines.

## 8. Replication surface (stub; Phase 5 owns the transport)

* `ClusterHook{OnCommit, Apply}`: `OnCommit` sees every commit **including synthetic expire deletes and touch ops**, in LSN
  order per file; `Apply` may veto the ack (the local commit is not rolled back).
* `Replicable`: `ApplyEntry` (leader's delete is applied as a plain delete, no local expiry), `ExportSnapshot` /
  `InstallSnapshot` (resync), `HistoryID`, `SetFollower(on)`.
* **Follower:** client writes → `ErrFollower`; no local expiry, no sweep deletes, no lazy purge, no sliding; idle follower
  writes nothing (LSN and WAL size constant). Entries journal under the **leader's LSN**; a duplicate LSN is a silent no-op;
  `last+1` is applied; anything else (gap, or entry from another history) returns `ErrNeedResync` and changes nothing.
* **Promotion** (`SetFollower(false)`): the wheel is re-armed from absolute deadlines, a new `HistoryID` is minted
  (persisted in `<dir>/REPLID`) so a deposed leader's late entries are refused, and the LSN sequence continues.

## 9. Acceptance matrix (from `/workspace/redx-extra-acceptance-tests.md`)

All implemented in `engine/acceptance_*_test.go` with a fake clock unless noted; all pass under `go test -race` and
(`GOARCH=386`).

| case | test | | case | test |
|------|------|-|------|------|
| E1 | `TestE1FixedBoundary` | | C1 | `TestTornTailEveryByteOffset` (existing) |
| E2/E3 | `TestE2*`, `TestE2ExactTimeline` | | C2 | `TestC2TruncatedSegmentHeader` |
| E4 | `TestE4ExpiredGetNeverSlides` | | C3 | `TestC3MidLogCorruption` |
| E5 | `TestE5SubBoxTTL` | | C4 | `TestCheckpointRecoveryPaths` (existing) |
| E6 | `TestE6ChildShorterThanParent` | | C5 | `TestC5CompleteTmpCheckpointIgnored` |
| E7 | `TestE7StructuralParentVanishes` | | C6 | `TestC6StaleWALNotReplayed` |
| E8 | `TestE8ExpiredGetConsistentNELL` | | C7 | `TestC7…`, `TestCorruptCheckpointDetected` |
| E9 | `TestE9ReSetReplacesDeadlineAndKind` | | C8 | `TestC8KillNine` (real `kill -9` of a child process, sync+group) |
| E10 | `TestE10NoTTLAndNonPositive` | | C9 | `TestC9RecoveryIdempotent` |
| E11 | `TestE11OnlySuccessfulGetAndQuerySlide` | | C10 | `TestC10IOErrors` (torn write, ENOSPC, fsync fail, group) |
| E12 | `TestE12ClockJumpForwardManyKeys` | | C11 | `TestC11CrashAroundExpiry` |
| E13 | `TestE13ClockJumpBackward` | | C12 | `TestC12StrayFiles` |
| E14 | `TestE14SweepDoesNotStallReaders` | | R1/R4 | `TestR1FollowerNeverExpires`, `TestR5ClockSkew` |
| E15 | `TestE15ExactlyOneExpireEventUnderRace` | | R2 | `TestR2IdleFollowerWritesNothing` |
| E16 | `TestE16QSRestartIsEmptyAndSeedNeverWritten` | | R3 | `TestR3FollowerEventExactlyOnce` |
| E17 | `TestE17QSLSAbsoluteDeadlineAcrossCleanClose` | | R5 | `TestR5ClockSkew` |
| E18 | `TestE18CheckpointMarkerBoundsLoss` | | R6 | `TestR6SlidingReplicatesAsTouch` |
| | | | R7 | `TestR7TTLReplicatedVerbatim` |
| | | | R8 | `TestR8SubtreeExpiryReplicates` |
| | | | R9 | `TestR9DuplicateAndGap` |
| | | | R10 | `TestR10ExpireThenResetConverges` |
| | | | R11 | `TestR11Promotion` |
| | | | R12 | `TestR12SnapshotResync` |
| | | | R13 | `TestR13FollowerRestart` |

**Skipped / weakened (with reasons):**
* E17 covers only a *clean* close; a hard-kill variant is E18/C11 (the absolute deadline is journaled, so the same code path).
* E18 asserts bounds ("absent or 1 at +4.9 s, 2 at +5.1 s") by calling the flusher directly instead of waiting for the 20 ms
  background tick, because the fake clock does not drive goroutine timers.
* C8 drives the real process but not a real power cut; fsync-to-media durability is the OS/disk's contract.
* C10 injects errors through the engine's `ioHook`; it does not exercise a real full disk.
* E14's 50 ms p99 bound is a regression guard (measured ≈2 µs p99, 33 ms max); it can be flaky on a heavily loaded CI box.
* R-series tests run against two in-process databases wired with `ClusterHook` → `ApplyEntry`; no network transport exists yet.

## 10. Phase 2/3 wiring (`link`, `pulse`, `idx`, `pack`)

| package | how it is wired | entry point |
|---------|-----------------|-------------|
| `engine/link` | `RecordStore` implements `link.Store` (Get/Put/Delete/Scan/Files) over all open files; TTLs on entries are honoured, expired records are hidden; returned entries are clones | `db.Records()`, `db.NewLinkEngine(link.Options{})` |
| `engine/pulse` | `db.NewPulseEngine(pulse.Config{...})` runs rules/timers whose writes land in the real engines; `db.BridgeChanges(prefix, bus)` forwards `Subscribe` events (set/delete/expire) as `change.*` events on a `pulse.Bus` (best effort: events may be dropped under back-pressure, rule-driven events are exact) | see `integration_test.go` |
| `engine/idx` | `db.OpenIndexes(name)` reads `<dir>/<name>.red-xai-db-idx`, restores each index from `<name>.red-xai-db-idx.snap/` when the snapshot verifies against a scan of the engine (else rebuilds; never stale), and returns an `IndexSet`. `IndexSet.Store` is a `link.Store` that keeps the indexes current: use it (instead of `db.Records()`) for `NewLinkEngine`/`NewPulseEngine` and for writes you want indexed. `Save/Close` writes snapshots, `Check` detects drift, `Rebuild` repairs it | `engine/extras.go` |
| `engine/pack` | `db.OpenPack(name, cfg)` returns a `*pack.Pack` over `<dir>/<name>.red-xai-pack/{chunks,manifests}` (`pack.DirBackend`); crashed temp files are cleaned on open | `engine/extras.go` |

Limits (by design, not bugs): direct `db.Set/Delete` calls do **not** feed indexes (use `IndexSet.Store`, or `Check`/`Rebuild`);
snapshots are always verified by a scan, never trusted by sequence number; indexes and packs are local (not replicated);
there is no hot reload of `.red-xai-db-idx` files.

**Status (0.2.0): all of the above is exposed through `redxd`/`redx`.** `host/engineadapter` keeps, per database, an `*engine.IndexSet`
(`db.OpenIndexes`, one per `*.red-xai-db-idx` file; a union manager for SQL), a pulse engine whose rule writes land in the real engines,
`pack.New(DirBackend…)` per pack folder (encrypted through `sec.Encrypt`/`NewReader` when a policy `Encrypt` rule names the pack),
`media.New(…)` per `*.red-xai-media` folder, the `link.Engine` (embedded + `.red-xai-db-link` standalone links), an `engine/query`
executor (`POST /v1/sql`) and an `engine/code` sandbox. Writes made through the host (REST, SQL, scripts) go through
`IndexSet.Store`/the rule engine, so indexes and rules stay current. Routes, scopes and limits are in `docs/HOSTING.md` §3a/§3b.
Remaining gaps: no hot reload of hand-placed `.red-xai-sec`/`.red-xai-code`/`.red-xai-db-idx` files; pack, media and index *contents* are not replicated;
`Encrypt` rules cover packs only; `engine.Database.Subscribe` still ends silently for a bare name (the host rejects it before calling the engine).
