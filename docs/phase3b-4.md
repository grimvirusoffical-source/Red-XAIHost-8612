# RedX phase 3b–4: media, security and scripts

Three new packages, Go standard library plus `redx/box` (and `engine/pack` / `engine/link`
in tests only for media): `engine/media` (`.red-xai-media`), `engine/sec` (`.red-xai-sec`)
and `engine/code` (`.red-xai-code`). Same pattern as phase 3: small interfaces, tests
against in-memory fakes, no existing file touched.

**Update (0.2.0): all three are now wired into the host** (`host/engineadapter`: media REST with Range, a policy built
from API-key scopes/roles, sandboxed scripts with a host-level `code_allowed_boxes`, pack encryption with a master key file,
`.red-xai-*` files loaded from the data dir). See `docs/HOSTING.md` §3b for routes and limits. The "Not done / not wired"
list below is the state at the time these packages were written; the code item there ("not wired to real engine reads/writes") is done.

---

## 1. Media (`.red-xai-media`)

An item is two things in a `pack`:

* a **data file** `data/<name>/<token>` (content-addressed 1 MiB chunks, so identical
  chunks are shared). `<token>` is 32 random hex chars, new for every version.
* a **sealed manifest** `meta/<name>.red-xai-media`:

```
{Media[]= [
    {Version[]} = 1, {Name[]} = "cat.png", {Type[]} = "image", {MIME[]} = "image/png",
    {Size[]} = 1234, {SHA256[]} = "<hex>", {Path[]} = "data/cat.png/<token>",
    {Width[]} = 64, {Height[]} = 48
];
{Seal[]} = "<sha256 of every byte above>";
```

`DurationMs` / `DurationEstimated` appear for audio and video when they can be read. Width,
height and duration are omitted when unknown. Manifests are ≤ 64 KiB and are verified
against their seal on every read.

### API (`engine/media`)

`New(files, chunks, Config{Root, MaxSize, MaxRangeRead}) (*Store, error)`, where `files`
is any `media.Files` (`*pack.Pack` fits) and `chunks` any `media.Chunks`
(`GetChunk(id)`; every `pack.ChunkStore` fits).

| call | what it does |
|------|--------------|
| `Put(name, r, PutOptions{Declared})` | sniff, check declared type, write data, then manifest. Replaces an existing item. |
| `Stat`, `List` | manifests. `List` reports items whose manifest is damaged as entries with an error rather than failing. |
| `Open(name) (*Reader, error)` | `io.Reader`, `io.Seeker`, `io.ReaderAt`, `Size()`, `Meta()`, `Close()`. Works with `http.ServeContent`. |
| `ReadRange(name, off, length)` | bytes `[off, off+length)`, clipped at EOF; at most `MaxRangeRead` per call. |
| `Verify(name)` | reads everything, checks every chunk, the file hash and the size. |
| `Fsck(deep)`, `Sweep()` | find (and with `Sweep`, delete) orphan data files / orphan manifests. Never deletes an item whose manifest exists, even a damaged one. |
| `Delete(name)` | manifest first (the item disappears), then the data. Works on a damaged item. |
| `ParseRange(header, size)`, `ContentRange(off, length, size)` | single-range HTTP `Range` parsing (`bytes=a-b`, `a-`, `-n`). Malformed → `ErrBadRange` (HTTP: ignore the header), several ranges → `ErrMultiRange`, start past EOF → `ErrUnsatisfiable`. |

Defaults: `DefaultMaxSize` 4 GiB per item, `DefaultMaxRangeRead` 64 MiB per `ReadRange`.
Names: one valid pack file name, no `/`, length-bounded (`ErrInvalidName`).

### Content sniffing (no transcoding, ever)

`Sniff(head)` needs the first 4096 bytes (`SniffLen`) and returns type, canonical MIME and extension.

| format | magic bytes | type / MIME |
|--------|-------------|-------------|
| PNG | `89 50 4E 47 0D 0A 1A 0A` | image/png |
| JPEG | `FF D8 FF` | image/jpeg |
| GIF | `GIF87a` / `GIF89a` | image/gif |
| WebP | `RIFF....WEBP` | image/webp |
| WAV | `RIFF....WAVE` | audio/wav |
| MP3 | `ID3` tag, or an MPEG audio frame sync with valid header fields | audio/mpeg |
| OGG | `OggS`, then the first packet decides: Vorbis/Opus/FLAC/Speex → audio/ogg, Theora → video/ogg, otherwise audio/ogg (also accepts a `video/ogg` declaration) | audio/ogg, video/ogg |
| MP4 | `ftyp` box at offset 4; audio-only brands (`M4A `) → audio/mp4, otherwise video/mp4 | video/mp4, audio/mp4 |

Anything else (including formats that merely look similar, e.g. SVG, AVI, MKV) is `ErrUnsupported`.
The declared type can be a MIME type (parameters and common aliases such as `image/jpg`,
`audio/mp3`, `audio/x-wav` are normalised) or just `image` / `audio` / `video`. If it
disagrees with the bytes: `ErrTypeMismatch`, and nothing is stored. An empty declaration accepts the sniffed type.

### Probing

`Probe` reads only headers, with bounded reads: PNG/GIF/JPEG/WebP dimensions, WAV duration
(exact, from the `fmt` and `data` chunks), MP3 duration (estimated from the first frame's
bitrate, so `DurationEstimated = true`), OGG duration (from the last page's granule position), MP4 duration (from `mvhd`) and
dimensions (from `tkhd`). A malformed header just means "unknown", never an error.

### Reads and verification

`pack` has no ranged read, so the Reader uses `pack.Info` (chunk ids, chunk size, size)
and fetches only the chunks a range touches, each verified (SHA-256 of the chunk against its id) before use. A small chunk cache
is kept per Reader. `Open` first checks that the data file still matches the manifest (size, chunk count, hash).
A ranged read **cannot** check the whole-file hash (it never sees the whole file); `Verify` does.

### Crash behaviour

Order on `Put`: data under a **fresh** path, then manifest (the commit point), then best-effort delete of the old data.
A crash before the manifest leaves the old version fully intact plus an orphan data file that `Fsck` reports and `Sweep` deletes.
A crash after the manifest leaves the new version plus an orphan old data file. Tested with `pack.MemBackend`'s crash hook at every step.

Limits: roughly 50k items per `Root` (a pack folder holds one sealed manifest, see phase 3).

---

## 2. Security (`.red-xai-sec`)

### Policy document

A Box document. Unknown entries or unknown keys inside an entry are errors (a typo must not silently open or close access).

```
{Role[1]}    = [ {Name[]} = "mod", {Scopes[]} = ;["chat.write"]:, {Inherits[]} = ;["player"]: ];
{Scope[1]}   = [ {Name[]} = "chat.write", {Actions[]} = ;["read","write"]:, {Resource[]} = "chat.red-xai-db-qs" ];
{Rule[1]}    = [ {Effect[]} = "deny", {Who[]} = ;["role:guest"]:, {Actions[]} = "*",
                 {Resource[]} = "players.red-xai.db:Player.Email", {Priority[]} = 0, {Owner[]} = FALSE ];
{Encrypt[1]} = [ {File[]} = "vault*", {Key[]} = "main-2026" ];
```

* **Role**: `Name`, optional `Scopes` (names), optional `Inherits` (other roles; cycles and unknown names are errors).
* **Scope**: `Name`, `Actions`, `Resource`. Holding the scope (directly, or through a role) grants those actions on that resource (an allow with priority 0).
* **Rule**: `Effect` (`allow` / `deny`), `Who` (`*`, `role:NAME`, `scope:NAME`, `user:ID`; one string or an array), `Actions` (`read`, `write`, `delete`, `exec`, or `*`), `Resource`, optional `Priority` (integer, `-1000..1000`, default 0), optional `Owner` (TRUE = only when `Resource.Owner` equals the principal id).
* **Encrypt**: `File` glob and master key `Key` id; `Policy.EncryptionFor(file)` returns the first match.
* Resource patterns are `file`, `file:Box` or `file:Box.Field`, with `*` and `?` wildcards per segment. A pattern covers everything below the level it names (`players.red-xai.db` covers every box and field in it), but a pattern naming a deeper level does **not** match a request for the coarser level.
* Limits: 4 MiB document, 10 000 entries, 256-byte patterns, 64-byte names.

### `Authorize(Principal{ID, Roles, Scopes}, Action, Resource{File, Box, Field, Owner}) Decision`

**Default deny.** Candidates are the scope grants the principal holds plus all matching rules. The winner is, in order:

1. highest `Priority` (scope grants count as 0);
2. most specific resource pattern (more segments named, then more segments without wildcards);
3. `deny` beats `allow`.

No candidate → deny. Invalid input (unknown action, empty file, control characters, field without box) → deny. Unknown roles grant nothing. The `Decision` names the deciding rule (`Rule[ID=3]`) for audit. `Check` returns an error wrapping `ErrDenied`. `FilterFields` helps with whole-box reads: the caller must authorize per field.

### Encryption (`Encrypt`, `Decrypt`, `Rewrap`, plus `*Bytes` forms)

* AES-256-GCM, standard library only. Each file gets a fresh random 32-byte data key, wrapped by a caller-supplied 32-byte **master key** (`NewMasterKey(id, key)`, held in a `Keyring`).
* The master key is never written to disk, logged or printed: it exists only in memory; `%v %+v %#v %s %x %q`, JSON and text marshalling all print or return only the id (tested, including through reflection-based printers); `Destroy()` zeroes it.
* Streaming: the plaintext is split into segments (default 64 KiB, `Options.SegmentSize` 4 KiB..1 MiB), each with its own random 96-bit nonce (tested for uniqueness over many segments and files).
* Layout (big-endian): `"RXSEC" | 1 | flags | keyIDLen | keyID | fileID(16) | segSize(4) | salt(16) | wrapNonce(12) | wrappedKey(48)`, then per segment `ctLen(4) | nonce(12) | ciphertext+tag`.
* Authenticated data: the wrap covers key id, file id, segment size and the caller's `Options.Context`; each segment covers file id, segment size, index, a final-segment flag and the context. So tampering, reordering, dropping, duplicating or appending segments, truncation, and moving ciphertext to another file or context (pass the file name or record address as `Context`) all fail with `ErrAuth` / `ErrTruncated`. A wrong master key fails with `ErrAuth`; an unknown key id with `ErrUnknownKey`; garbage with `ErrFormat`. On any error `DecryptBytes` returns no plaintext; the streaming `Decrypt` may already have written earlier, verified segments to `dst`, so write to a temp location and commit on success.
* The wrap key is derived per wrap (HKDF-SHA256 from master key, random salt), so one master key never encrypts under the same key twice.
* **Rotation**: `Rewrap(dst, src, from KeySource, to *MasterKey, opts)` unwraps the data key with the old key and re-wraps it under the new key, rewriting only the header. The segments are unchanged, so rotation is cheap and does not read the file's data. Add the new key to the keyring, `SetCurrent`, rewrap each file, then `Remove` the old key.

---

## 3. Scripts (`.red-xai-code`)

A small tree-walking interpreter written for this project. It is not Lua, JS or anything embedded: there are no imports, reflection, first-class functions, globals, goroutines or `eval`, and nothing that names a file, socket, process or the clock. The only way out is a **host function**.

### File format

```
{Script[1]} = [
    {Name[]}   = "heal",
    {Params[]} = ;["target", "amount"]:,
    {Uses[]}   = ;["db_get", "db_set"]:,
    {Limits[]} = [ {MaxSteps[]} = 5000, {TimeoutMs[]} = 50 ],
    {Source[]} = "let hp = db_get(target, \"Health\"); db_set(target, \"Health\", hp + amount); return hp + amount;"
];
```

`Name` and `Source` are required. `Params`, `Uses`, `Limits` (`MaxSteps`, `TimeoutMs`, `MaxStringLen`, `MaxArrayLen`, `MaxAlloc`, `MaxDepth`, `MaxHostCalls`) are optional. Unknown keys are errors. Every script is compiled at load; the first error is returned with the script name. ≤ 4 MiB per file, ≤ 1000 scripts.

### Language

Values: number (finite float64), string (UTF-8), bool, nil, array. Comments: `// to end of line`. Strings: `"..."` with `\n \t \" \\` escapes. Statements end in `;`.

```
fn name(a, b) { ... return x; }            // top level only, may recurse (depth-limited)
let x = expr;                              // declares (block scoped)
x = expr;      a[i] = expr;                // assigns
if cond { ... } else if cond { ... } else { ... }
while cond { ... }                         // break; continue;
for i in 0..n { ... }                      // i = 0 .. n-1
for e in array { ... }
return expr;                               // or return;
expr;                                      // call for effect
```

Operators, loosest first: `or` `||`; `and` `&&`; `==` `!=`; `<` `<=` `>` `>=`; `+` `-`; `*` `/` `%`; unary `-` `not` `!`; postfix `a[i]`, `f(x)`. `+` adds numbers or concatenates strings; the rest are numeric (comparisons also work on two strings). **Conditions must be booleans** (no truthiness) and operands must have the right type, otherwise a runtime error with line and column. Overflow to Inf/NaN is an error, not a value. Division or modulo by zero is an error. Arrays are by reference; index must be an integer in range.

Built-ins (pure): `len str num type push pop abs floor ceil round sqrt min max substr find contains upper lower`. `substr(s, start[, count])` counts runes; `find` returns an index or -1. Host function names may not shadow these.

### Host functions and authorization

The embedder builds a `Host` from `HostFunc{Name, Action, Resource, Fn}`. A call goes through only if **all three** hold:

1. the script listed the name in `Uses` (checked at compile time: calling anything else is a compile error);
2. the `Host` registered it;
3. `RunOptions.Auth.Authorize(Principal, Action, Resource)` (a `*sec.Policy`) allows it. `Resource` is computed from the arguments by the host function (default `{File: "host", Box: <name>}`) and may refuse the call. No `Auth` means deny everything.

Arguments are deep-copied into plain Go values (`nil, bool, float64, string, []any`); results are converted back, with a node/depth bound so cyclic or shared arrays cannot blow up. Host functions get a `context.Context` that expires with the run's time limit and **must honour it**. Every attempted call is listed in `Result.Calls` (allowed or denied, with the reason) for audit.

### Limits

| limit | default | meaning |
|-------|---------|---------|
| `MaxSteps` | 100 000 | every expression node, statement and loop iteration costs 1; walking built-ins cost extra |
| `MaxDuration` | 1 s | wall clock, checked every 64 steps and at each host call; the context deadline also applies |
| `MaxStringLen` | 64 KiB | longest string a script can create |
| `MaxArrayLen` | 10 000 | longest array |
| `MaxAlloc` | 8 MiB | **cumulative** allocation budget (string bytes, 16 per array element), not live memory |
| `MaxDepth` | 32 | nested user-function calls |
| `MaxHostCalls` | 100 | per run |
| `MaxSourceBytes` | 64 KiB | compile time |
| `MaxNesting` | 64 | block/expression nesting, compile time |
| `MaxConvertNodes` | 100 000 | values converted when crossing the host boundary (constant) |

`RunOptions.Limits` is the host's ceiling; a script's own `Limits` can only lower it (`Effective`). Limit errors wrap `ErrLimit` (`ErrStepLimit`, `ErrTimeout`, …). Errors: `*SyntaxError` (line, col), `*RuntimeError` (line, col, wrapped cause), `ErrNotGranted`, `sec.ErrDenied`, `ErrHostFunc`. An internal panic is recovered and returned as `ErrInternal` (never expected; the fuzz tests treat it as a failure).

---

## Tests and results

All run with `go vet`, `go build ./...` and `go test -race`:

* `engine/sec`: default deny, rule precedence (priority, specificity, deny-wins), roles/inheritance, owner rules, wrong key, tampered/reordered/truncated/spliced ciphertext, nonce uniqueness, rotation via `Rewrap`, key redaction in every fmt verb and JSON.
* `engine/media`: sniff table for every format (plus lookalikes), declared-type mismatch, probes, ranges at chunk boundaries, `http.ServeContent` Range, `Seek`, crash injection at every pack operation of `Put`, `Fsck`/`Sweep`, concurrent reads.
* `engine/code`: language semantics, every limit, escape attempts (undefined names, no ambient access, forged host names, `Uses` bypass, huge/cyclic values, internal panic containment), a source scan test that the package imports no `os`, `net`, `os/exec`, `unsafe`, `reflect`, `syscall` and starts no goroutines, 3000 generated well-typed programs plus 20 000 token-soup inputs (no `ErrInternal`, no hang), and a native `FuzzCompileRun` (40 s locally: ~5 M executions, no failure).

## Not done / not wired

* None of the three packages is used by `host/`, `cmd/` or the `.red-xai.db` engines. Nothing loads `.red-xai-sec`, `.red-xai-code` or `.red-xai-media` from disk, and there is no hot reload. Manifests are only stored through `pack`.
* **Media**: no HTTP handler (only `ParseRange`/`Reader` that plug into `ServeContent`); no transcoding, thumbnails or streaming-format (HLS/DASH) support; MP3 duration is an estimate; about 50k items per pack folder; `pack` has no ranged reads, so media does its own chunk reads (and a pack with other chunk sizes would need a code change).
* **Sec**: no persistent rotation driver (the caller loops over files), no audit log (the `Decision` carries the rule name only), no encrypted-pack integration (nothing yet calls `Encrypt` on write), and `Rewrap` does not change the data key: someone who held the old master key and a copy of the old header can still decrypt old data, so a key *compromise* needs a full re-encrypt, not a rewrap.
* **Code**: not wired to real engine reads/writes (the tests use fake `db_get` / `db_set`); no persistent state between runs; no first-class functions, modules or floats formatting options; the interpreter cannot preempt a host function that ignores its context; `MaxAlloc` is cumulative, not live memory.
* The master key is supplied by the caller. Where it comes from (env var, key file, KMS, passphrase) is not decided and nothing here stores it.

## Open questions

1. Should sec be enforced **inside** each engine, or only in the host in front of them? (The code sandbox already enforces it per host call; the engines do not yet.)
2. How should the master key be provided in production: environment, a key file with strict permissions, passphrase at start-up, or something else? What do you want for backup and recovery of it?
3. Whole-box reads: callers have to authorize per field (`FilterFields`). Do you want that done automatically by the engine?
4. Script language: are bool-only conditions and no first-class functions the right trade-off? Defaults (100k steps, 1 s, 8 MiB) are guesses; what do you expect scripts to do?
5. Media: should the host serve ranges directly (HTTP), and do you want thumbnails or only raw blobs? Should `pack` itself gain ranged reads or content-defined chunking?
