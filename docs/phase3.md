# RedX phase 3: search indexes and file packs

Two new packages: `engine/idx` (`.red-xai-db-idx`) and `engine/pack` (`.red-xai-pack`).
Go standard library plus the existing `redx/box` package (and `redx/engine/link` for
the `Addr` type and `link.MemStore` in tests). Both talk to storage through small
interfaces and are tested against in-memory fakes; **neither is wired to the real
`.red-xai.db` engines or the host yet** (see "Not done" at the end).

---

## Search indexes (`.red-xai-db-idx`)

### Declaring indexes

A `.red-xai-db-idx` file is a Box document with one top-level box per index
(`idx.ParseDecls`, `idx.FormatDecls`, `idx.ParseManager`):

```
{PlayerByName[]} = [
    {File[]}   = "players.red-xai.db",
    {Box[]}    = "Player",
    {Fields[]} = ;["Name"]:,
    {Kind[]}   = "exact",
    {Fold[]}   = TRUE
];
{PlayerStats[]} = [
    {File[]}   = "players.red-xai.db",
    {Box[]}    = "Player",
    {Fields[]} = ;["Level", "Stats.Gold"]:,
    {Kind[]}   = "range"
];
```

| entry    | meaning |
|----------|---------|
| `File`   | required. The file whose records are indexed (matches `link.Addr.File`). |
| `Box`    | optional. Only records with this box name (`Player` in `Player[ID=1]`). Omitted = every record of the file. |
| `Fields` | required. A string or an array of strings: field paths inside the record, e.g. `Name`, `Stats.Level`, `Inventory[ID=3].Count`. No file part, no wildcards, no duplicates. |
| `Kind`   | required: `exact`, `prefix`, `range` or `text` (case-insensitive). |
| `Fold`   | optional bool. Case-insensitive `exact` and `prefix` (keys and queries are lower-cased). Not allowed on `range`; `text` always folds. |

The index name is the entry key as written (`PlayerByName`, `Stats[ID=2]`) and must be
unique. Unknown entries and anything else malformed are errors wrapping `idx.ErrBadDecl`.

A *record* is a top-level entry whose value is a box. Fields are looked up inside that box.
A field that holds an array indexes each scalar element. Boxes, links, NELL and
missing fields contribute nothing. A record with no terms is not stored in the index.

### Kinds and queries

| kind   | indexed values | query | result order |
|--------|----------------|-------|--------------|
| exact  | strings, finite numbers, bools. A number never equals a string (`7` ≠ `"7"`). | `Exact(field, value, limit)` | record address |
| prefix | strings | `Prefix(field, prefix, limit)` (empty prefix = everything) | matched value (byte order), then address; each record once |
| range  | finite numbers | `Range(field, lo, hi, limit)` with `idx.Incl(x)`, `idx.Excl(x)` or `nil` for unbounded | number, then address; each record once (at its smallest value in range) |
| text   | strings, split into word tokens | `Text(field, idx.And \| idx.Or, terms, limit)` | record address |

* `field` may be `""` only when the index has exactly one field (`ErrFieldRequired`
  otherwise); an unknown field is `ErrUnknownField`; the wrong query for the kind is
  `ErrWrongKind`; bad input (NaN bound, no tokens, unindexable value) is `ErrBadQuery`.
* Record address order is `File`, `Box`, records without an ID before records with one, then `ID`.
  Ordering is deterministic and identical for an incrementally-updated index and a rebuilt one.
* `limit <= 0` means no limit.
* Tokens are maximal runs of Unicode letters and digits, lower-cased (`Red-haired` is `red`,
  `haired`). Each string in `terms` is tokenized, so `"red dragon"` is two tokens. `And`
  needs every token, `Or` any. There is no stemming, stop-word list, phrase search or ranking.
* `Hit` carries `Addr`, `Field` and `Value` (the queried value for exact, the stored
  string for prefix (folded if `Fold`), the number for range, NELL for text).

### Keeping an index current

```go
mgr, _ := idx.ParseManager(src)                 // or idx.New(decl) for a single Index
store := idx.NewObservedStore(realStore, mgr)   // wrap a link.Store-shaped store
store.Put(addr, entry)                           // writes through, then mgr.OnPut(addr, entry)
store.Delete(addr)                               // writes through, then mgr.OnDelete(addr) if it existed
```

* `Observer` is `OnPut(Addr, *box.Entry)` and `OnDelete(Addr)`. `Index` and `Manager` implement
  it. Observers must not keep the entry. They are idempotent (a replace removes the
  record's old terms first).
* `ObservedStore` has the exact method set of `link.Store`, so it can be handed to
  `link.NewEngine`. Writes through one wrapper are serialized, and a failed write is not
  observed. Writes that bypass the wrapper are not seen: use `Check` / `Rebuild`.
* `Rebuild(src)` scans the source into a new structure and swaps it in. Writes arriving
  during the scan are buffered and replayed, so none are lost and queries keep
  answering from the old data. Only one rebuild per index runs at a time
  (`ErrRebuilding`). A failed scan leaves the old index in place.
* `Check(src)` compares the live index with a fresh scan and returns the `Missing`,
  `Extra` and `Changed` records (`Diff.Stale()`). It is O(n) and only exact while no
  writes are in flight. Changing a field that is not indexed is *not* staleness.

### Snapshots, crashes and stale detection

`Manager.Save(blob, SaveOptions)` writes one sealed snapshot per index through a `Blob`
(`MemBlob` for tests, `DirBlob` for a directory: temp file, fsync, rename, directory sync).
A snapshot is itself a Box document:

```
{Index[]} = [ {Version[]} = 1, {Name[]} = "...", {Decl[]} = "<decl hash>", {Seq[]} = "17",
              {SourceSeq[]} = "40", {Count[]} = 2, {Digest[]} = "<hex>",
              {Rec[ID=1]} = ;["file", "Box", "<id or empty>", "field", "key", ...]:, ... ];
{Seal[]} = "<sha256 of every byte before this entry>";
```

`Manager.Load(blob, source, LoadOptions)` never returns a stale index. For each index:

| situation | result |
|-----------|--------|
| no snapshot | rebuilt, reason `missing` |
| truncated, bit-flipped, garbage, junk appended, bad rows, count/digest not matching its rows | rebuilt, reason `corrupt: ...` |
| written for a different declaration (name, file, box, kind, fold, fields) | rebuilt, `declaration changed` |
| source scan gives a different checksum | rebuilt, `stale` |
| source scan gives the same checksum | status `verified`, snapshot used |
| `LoadOptions.SourceSeq` equals the `SourceSeq` saved in the snapshot | status `loaded`, snapshot used **without scanning** the source |

* The checksum is the XOR of a SHA-256 per record over (address, sorted terms) plus the
  record count. It is maintained incrementally on every put/delete, so
  `Stats().Digest` always describes the live index.
* The sequence-number fast path is for a source with a durable write counter (a WAL
  position). The caller must save and read it consistently: `ObservedStore.Locked(fn)`
  blocks writes while `fn` runs, so `fn` can read the counter and call `Save` together.
  A wrong counter would let a stale snapshot through, which is why it is opt-in and
  both sides must supply it. Without it, `Load` costs one scan of the source (about
  the same as a rebuild), and the snapshot's benefit is detecting damage and drift.
* A crash while saving leaves either the previous snapshot or the new one (atomic rename);
  `.tmp-*` leftovers are ignored. After a crash with unsaved writes the snapshot is stale and
  is detected as such. Indexes are always rebuildable from the source.
* `Stats().Seq` counts applied writes and is restored from a snapshot. It is informational; it does not by itself prove freshness.

### Limits

* In memory: postings are Go maps, prefix/range are sorted slices. Incremental insert/delete in a
  prefix/range index is O(n) (binary search plus a slice move); rebuild is O(n log n).
  Measured about 1.8 µs per put on an exact index in a micro benchmark; not tuned.
* `MaxTermsPerRecord` = 4096 distinct terms per record per index (extra terms dropped,
  `Stats().Truncated`). Strings and tokens over `MaxTermBytes` = 256 bytes, and strings that are not
  valid UTF-8, are not indexed.
* Numbers are Box numbers (float64). Exact-match keys use the shortest round-trip decimal form, so `0.1` equals `0.1`
  but values that differ only beyond float64 precision are equal.
* One index covers one file. Multiple fields in one index are independent (they are not a
  composite key); queries name one field.
* No multi-index query planner, no `OR` across indexes, no sorting by an arbitrary field, no
  offset/cursor paging (only `limit`), no regex or substring search, no fuzzy match, no phrase search.
* The snapshot is a text Box document with one row per record: readable but large and slow to
  parse for millions of records.

---

## File packs (`.red-xai-pack`)

### Model

* File contents are cut into **fixed-size chunks** (default 1 MiB, `Config.ChunkSize` 1 byte to 64 MiB).
  Every file records its own chunk size.
* A chunk is stored once under the lower-case hex **SHA-256 of its bytes**: identical chunks
  are shared within and across files. Chunks are immutable.
* Every folder has a **manifest**, a Box document stored under `FolderKey(folder)` (SHA-256 of the
  folder path; `""` is the root):

```
{Manifest[]} = [
    {Version[]} = 1,
    {Folder[]}  = "docs",
    {File[ID=1]} = [
        {Name[]} = "a.txt", {Size[]} = 12, {SHA256[]} = "<hex of the whole file>",
        {ChunkSize[]} = 1048576, {Chunks[]} = ;["<chunk id>", ...]:
    ],
    {Dir[ID=2]} = [ {Name[]} = "images" ]
];
{Seal[]} = "<sha256 of every byte above>";
```

  A manifest is checked on every read: seal (truncation, edits), version, folder name, names,
  numbers, chunk ids and the chunk count against the size. Anything wrong is `ErrCorrupt`.
  A folder with no stored manifest is an empty folder.

### Storage interfaces

```go
type ChunkStore    interface { PutChunk; GetChunk; HasChunk; DeleteChunk; ListChunks }  // by id
type ManifestStore interface { LoadManifest; SaveManifest; DeleteManifest; ListManifests } // by key
```

`MemBackend` implements both in memory (test fake, with a `Hook` to inject a crash before any
mutating call and raw setters to damage data). `DirBackend{Root}` keeps
`Root/chunks/<2 hex>/<id>` and `Root/manifests/<key>.manifest`, written with temp file + fsync +
rename (`RemoveTemp()` deletes crash leftovers at start-up). Ids and keys must be 64 lower-case
hex characters, so a caller can never make a backend leave its root.

### API (`pack.New(chunks, manifests, Config)`)

| call | behaviour |
|------|-----------|
| `Put(path, io.Reader) (Info, error)` | streams the reader, one chunk in memory at a time; creates folders; replaces an existing file; `ErrIsDir` if the name is a folder, `ErrNotDir` if a parent is a file, `ErrTooLarge`, `ErrFolderFull`. The file appears only when its folder manifest is saved. |
| `Get(path) (io.ReadCloser, Info, error)` | streaming read, verified (below) |
| `Stat(path)`, `List(folder)` | `List` is sorted by name, files and folders together, without chunk lists. `""` is the root |
| `Walk(folder, fn)` | sorted depth-first walk |
| `Mkdir(path)` | creates the folder and parents; existing is fine |
| `Delete(path)` | removes a file (chunks stay until GC) |
| `RemoveDir(path, recursive)` | `ErrNotEmpty` unless recursive; the root cannot be removed |
| `Verify(path)` | reads the whole file through the verifying reader |
| `Fsck(deep)` | decodes every manifest and checks every file. Shallow: each chunk exists. Deep: reads every chunk, checks size and hash, then the whole-file hash. Reports `Problems`, `OrphanChunks`, `OrphanManifests`. Never changes anything |
| `GC()` | deletes chunks no manifest references and manifests of unreachable folders. Aborts without deleting anything if any manifest is unreadable or corrupt |

### Integrity on read

The reader fetches a chunk, checks its length against the manifest, checks its SHA-256 against its id,
and only then returns its bytes. Failures are `*ChunkError` (file, chunk number, id, reason):

* chunk not in the store: `errors.Is(err, pack.ErrMissing)`;
* truncated or extended chunk (wrong length) or any hash mismatch: `errors.Is(err, pack.ErrCorrupt)`.

After the last chunk the total size and the whole-file SHA-256 are verified before `io.EOF` is returned,
so a clean EOF means the file is intact. Bytes before the bad chunk are delivered, none of the bad chunk
are. Errors are sticky. `Put` also verifies an existing chunk before reusing it, and rewrites it if
it does not match (re-uploading heals a damaged shared chunk).

### Crash behaviour

* Chunks are written before the manifest. A crash or error before the manifest save leaves unreferenced
  chunks (GC removes them) and the old file, if any, untouched.
* New folders are created parent first, so a crash never leaves a file in a folder that cannot be
  reached; the worst case is an empty folder.
* `RemoveDir` removes the folder from its parent first, then deletes the sub-folder manifests; a crash in
  between leaves orphan manifests that GC removes.
* Test: a crash injected before every mutating backend call of a nested `Put` (new and overwrite) leaves a
  store that passes deep `Fsck` with no problems and no orphan manifests, and a retry succeeds.

### Path safety (`pack.CleanPath`)

Paths are relative, `/`-separated. Rejected with `ErrInvalidPath`: absolute paths, empty components
(`a//b`, trailing `/`), `.` and `..` components, backslashes, `:` anywhere (drive letters, NTFS streams),
control characters (including NUL, DEL and C1), invalid UTF-8, `<>"|?*`, bidirectional override and BOM
characters, names ending in `.` or space, Windows device names (`CON`, `NUL`, `AUX`, `PRN`, `COM1`-`9`,
`LPT1`-`9`, with any extension), names over 255 bytes, paths over 4096 bytes or 64 levels. `""` and `/`
mean the root. `%2e%2e` is just a name (nothing is URL-decoded). No Unicode normalisation or case folding: `A.txt`
and `a.txt`, or two spellings of `é`, are different names (extracting onto a case-insensitive file
system could collide them). Every public call validates before touching the backend.

### Limits

* Fixed-size chunking: inserting a byte at the start of a file changes every chunk (no content-defined chunking).
* Each folder manifest is read and rewritten whole on every change. `MaxFolderEntries` (default 50,000)
  bounds it; a file's whole chunk list lives in its manifest, so `MaxFileSize` defaults to 64 GiB
  (65,536 chunks at 1 MiB). Sizes are Box numbers, exact up to 2^53.
* Decoded manifests are cached in memory per pack (up to 512). A manifest is always re-read from the store and
  the cache is used only when the bytes are identical, so stored damage is never hidden.
* One process per backend. Operations are safe across goroutines (manifest updates are serialized; `GC` blocks
  everything while it runs and waits for running `Put`s). Two processes sharing a backend can lose manifest updates.
* A file that is deleted and garbage collected while someone is reading it fails that read with `ErrMissing`.
* No compression, encryption (that is `.red-xai-sec`), per-file permissions, modification times, symlinks,
  hard links, rename/move, partial or ranged reads (read is from the start), or resumable uploads.
* `Fsck` reports problems but has no repair (apart from re-uploading and `GC`).
* `.red-xai-media` (streaming, ranges, thumbnails) is **not** part of this change.

---

## Not done / not wired

* Neither package is connected to the real storage engines (`engine/`), the host or the CLI. `idx`
  needs an adapter exposing a `Scan` plus write hooks (or a `link.Store` adapter plus `ObservedStore`);
  `pack` needs a `ChunkStore`/`ManifestStore` over the real engine or disk layout, and a host API for upload/download.
* Index declarations and snapshots are not loaded from a real `.red-xai-db-idx` file by anything; there is no file
  watcher and no hot reload.
* No query language: queries are Go calls. The SQL-like language is phase 5.
* No replication of indexes or chunks to other nodes (phase 5).
* No access control (phase 4's `.red-xai-sec`).
* Benchmarks beyond one micro benchmark were not run, so no throughput claims are made.

## Open questions

1. Should an index declaration live in its own `.red-xai-db-idx` file per data file, or one per database?
2. Is fixed 1 MiB chunking acceptable for the first version, or do you want content-defined chunking for
   large media that changes slightly?
3. Should `Delete` free chunks immediately (needs reference counts) or stay GC-only?
4. Should composite (multi-field) exact indexes be added, such as `Guild + Rank`?
