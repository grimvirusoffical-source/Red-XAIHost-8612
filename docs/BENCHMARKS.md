# RedX engine benchmarks

Honest numbers, measured separately per path. Command:

```
cd redx/engine && go test -run xxx -bench . -benchtime 1s -benchmem .
```

Machine: 8 vCPU Intel Xeon (virtualised), Linux, Go 1.24, data directory on a virtio **ext4** disk (real `fsync`).
Single process, embedded API (no network). Numbers are one run; expect ±15% noise. Re-run on your hardware.

**How to read them.** RAM-path numbers include the whole `Set`/`Get` call: path parse, lock, tree mutation, TTL
bookkeeping, timing-wheel insert and event fan-out check. They do *not* include network, TLS, or serialization of a
client protocol. "Durable" numbers include a real `fsync`. We deliberately do **not** merge them into one headline.

## 1. RAM path (qs, qs-ls with `(N)*`, async journal)

| benchmark | ns/op | ops/s | allocs/op |
|-----------|------:|------:|----------:|
| `.red-xai-db-qs` Set (fixed TTL), 1 goroutine | 1,184 | **845 k** | 18 |
| qs Set sliding TTL | 1,184 | 845 k | 18 |
| qs Set, 8 goroutines | 1,442 | 693 k | 18 |
| qs Get, 1 goroutine | 338 | **2.96 M** | 4 |
| qs Get, 8 goroutines | 176 | **5.69 M** | 4 |
| qs-ls Set with `(5)*` checkpoint marker, 1 goroutine | 1,132 | 883 k | 17 |
| qs-ls Set with `(5)*`, 8 goroutines | 1,391 | 719 k | 17 |
| qs-ls Get | 343 | 2.92 M | 4 |
| timing wheel add+advance (per item) | 2,740 | – | 0 |

Write throughput on the RAM path does not scale with goroutines: each file is guarded by one lock, by design
(one lock per file keeps ordering and the journal trivially correct; shard across files for write parallelism).
Reads scale because they share an `RWMutex`.

Blueprint target "500k–2M simple ops/s, p99 < 0.5 ms": **met** for Get (3–6 M/s) and for Set within the band
(0.7–0.9 M/s). Latency: measured p99 of a `Get` while a 100,000-key sweep runs on another goroutine is ≈2 µs, max
≈33 ms (`TestE14SweepDoesNotStallReaders`, single run, `-race` off).

## 2. Durable path (`fsync` per commit or per batch)

| benchmark | ns/op | ops/s | notes |
|-----------|------:|------:|-------|
| `.red-xai.db` Set, **sync** (fsync each commit), 1 writer | 72,765 | **13.7 k** | the honest single-writer durable number on this disk |
| cold Set, **group commit**, 1 writer | 74,861 | 13.4 k | nothing to batch with one writer |
| cold Set, **group commit**, 64 writers | 4,027 | **248 k** | one fsync covers all concurrent commits |
| qs-ls Set, immediate journal (group), 1 writer | 73,132 | 13.7 k | |
| qs-ls Set, immediate journal (group), 64 writers | 3,734 | 268 k | |
| cold Set, **async** (ack before fsync), 1 writer | 1,355 | 738 k | loses up to `AsyncFlushMS` (10 ms) of acked writes on crash |
| cold Set, async, 8 writers | 1,686 | 593 k | |
| qs-ls Set immediate, async | 1,457 | 686 k | |

Blueprint target "50k–200k durable writes/s": **met only with group commit and ≥ ~16 concurrent writers**
(248 k/s at 64). A *single* synchronous writer is bound by fsync latency (~72 µs here, i.e. ~14 k/s); on
consumer SSDs with slower flush it will be lower, on NVMe with power-loss protection higher. Async numbers are
shown for completeness and must never be quoted as durable throughput.

## 3. Reads (cold)

| benchmark | ns/op | ops/s |
|-----------|------:|------:|
| cold Get, 1 goroutine | 335 | 2.99 M |
| cold Get, 8 goroutines | 166 | 6.03 M |

## 4. Expiry

* 100,000 keys sharing one deadline are swept in ≈78 ms in bounded batches of 512 deletions per lock hold; one
  `Get` never waits for more than one batch.
* 10,000 keys with scattered deadlines and a one-hour clock jump: a single sweep expires all of them.

## Methodology caveats

* Not compared against Redis/Postgres here: no network layer exists in the embedded engine, so a head-to-head
  would not be apples to apples. The host (`redxd`) adds a protocol cost not included above.
* Allocation counts include the Box value construction done by the benchmark harness.
* Benchmarks use the real clock but `SweepMS` is raised so the sweeper does not interfere; sweeper cost is
  reported in §4.
* Recovery time (replay of a 32 MiB WAL, the default checkpoint threshold) has not been benchmarked yet.
