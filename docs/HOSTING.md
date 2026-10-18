# Hosting RedX (`redxd`) — Windows, macOS, Linux, Raspberry Pi

**The host comes first.** An API key only works against a running `redxd`: the daemon owns the databases
(`.red-xai*` files), the key store, the REST/WebSocket API and — optionally — the cluster. This document gets a
node running, gives you your first API key, and shows how to add more nodes on other devices.

> Everything below is installed **from a local build directory** (`dist/…` produced by `scripts/build.sh`).
> Nothing is downloaded or published.

## 0. What is real, what is not (read this once)

| Area | Status |
|---|---|
| REST API (`GET/PUT/DELETE /v1/path/…`, `POST /v1/query`, `/v1/stats`, `/healthz`), multiple databases (`/v1/db/{name}/…`) | **Implemented, tested** (against the real engine and the stub) |
| WebSocket subscribe (`/v1/subscribe?path=`) | **Implemented, tested** |
| API keys: argon2id salted hashes, show-once, scopes (read/write/admin), per-db, path-prefix ACL, expiry, revoke, rotate (with grace), per-key rate limit, audit log, encrypted-at-rest option, brute-force throttle | **Implemented, tested** |
| First-run one-time admin key | **Implemented, tested** |
| TLS: off / auto self-signed (fingerprint pinning) / your own cert files | **Implemented**, pin flow tested on loopback |
| Cluster: node identity, join tokens, leader/follower, async replication with replication-id + offset resume, full-snapshot fallback, leader-forwarded writes, heartbeat/membership, manual promote/repoint | **Implemented, tested on loopback** (3-node tests + a 3-process CLI smoke test). Limits below. |
| Leader-driven TTL expiry (followers never expire anything themselves) | **Implemented, tested** |
| Backup / restore (online snapshot, offline copy) | **Implemented, tested** |
| Service install: `redxd install/uninstall/start/stop` (Windows service, systemd, launchd via `kardianos/service`), systemd unit, launchd plist, `install.sh`, `install.ps1`, Dockerfile, 3-node compose | **Written; NOT run end-to-end in the build sandbox** (no Windows/macOS machine, no systemd, no Docker there). Windows/macOS/ARM binaries were cross-compiled but not executed. Expect to fix small things on first run on those OSes. |
| Search indexes (`.red-xai-db-idx`): define/drop/rebuild/check + exact / prefix / range / text queries over REST and `redx idx` | **Implemented, tested against the real engine** (see §3a for limits; there is **no fuzzy/typo-tolerant search** in the engine: `mode=fuzzy` is an any-word text match) |
| File packs (`.red-xai-pack`): streamed upload/download, HTTP `Range`, listing, SHA-256 verification, `redx pack` | **Implemented, tested**. Contents are leader-local (not replicated) |
| Link resolution on reads (`?resolve=1`, `redx get --resolve`) | **Implemented, tested**, key path-prefix enforced on every link target |
| Pulse rules (`.red-xai-db-pulse`): define over REST, fired events over WebSocket, `redx pulse` | **Implemented, tested**. Rules run on the leader only; timers are in memory (lost on restart/failover) |
| SQL (`POST /v1/sql`, RXQL), media libraries with HTTP Range (`.red-xai-media`), access policy built from API-key scopes + roles (`.red-xai-sec`), sandboxed scripts (`.red-xai-code`) with a host-level `code_allowed_boxes`, pack encryption with a master key file | **Implemented, tested end to end through the host** (see §3b for the exact limits) |
| gRPC | **Stub only** (`docs/redx-grpc-stub.proto`); `grpc_listen` is accepted and ignored |
| **Automatic** failover / leader election / quorum writes | **NOT implemented** (designed seam: `ClusterHook`). Failover is manual (`redx node promote`). |
| Sharding, multi-key transactions across nodes | Not implemented |
| Code signing / notarization of binaries | Not done (see "first run on macOS / Windows") |

## 1. Quickstart

Build once (any machine with Go ≥ 1.22): `scripts/build.sh` (or `make dist`) → `dist/redx-<ver>-<os>-<arch>/` plus archives.
Targets: linux amd64 / arm64 / armv7, darwin amd64 / arm64, windows amd64 / arm64. Copy the matching archive to the device.

### Linux (systemd) — also Raspberry Pi OS (arm64 / armv7)
```sh
tar xzf redx-<ver>-linux-<amd64|arm64|armv7>.tar.gz && cd redx-<ver>-linux-*
sudo ./install.sh --from .                 # installs to /usr/local/bin, creates user "redx", data in /var/lib/redx
# -> prints your FIRST ADMIN API KEY once. Copy it now.
export REDX_KEY=rx_live_...                # keep keys out of shell history: use a password manager / `read -s REDX_KEY`
redx doctor
curl -s http://127.0.0.1:7777/healthz
```
No root? `./install.sh --from . --user` installs to `~/.local/bin`, data in `~/.redx`, no service;
start it with `~/.local/bin/redxd run --config ~/.redx/redx.toml`.
Quick foreground try-out without installing: `./redxd run --data-dir ./data` (the first-run admin key is printed on the console).

### macOS (Intel and Apple silicon)
```sh
tar xzf redx-<ver>-darwin-<amd64|arm64>.tar.gz && cd redx-<ver>-darwin-*
xattr -d com.apple.quarantine redxd redx 2>/dev/null   # binaries are unsigned; needed if they came via browser/AirDrop
sudo ./install.sh --from .                # /usr/local/bin, data /usr/local/var/redx, launchd daemon com.redx.redxd (runs as root)
export REDX_KEY=rx_live_...               # the key printed by the installer
redx doctor
```
Without installing: `./redxd run --data-dir ./data`.

### Windows 10/11, Server (amd64 or arm64) — elevated PowerShell
```powershell
Expand-Archive redx-<ver>-windows-amd64.zip .; cd redx-<ver>-windows-amd64
Set-ExecutionPolicy -Scope Process Bypass
.\install.ps1 -From .                     # C:\Program Files\RedX, data C:\ProgramData\RedX\data, Windows service "redxd"
# -> prints your FIRST ADMIN API KEY once.
$env:REDX_KEY = "rx_live_..."             # in a NEW terminal (PATH was updated)
redx doctor
```
SmartScreen/Defender may warn because the binaries are unsigned: "More info → Run anyway", or build them yourself.
Without installing: `.\redxd.exe run --data-dir .\data`. Manual service control: `redxd.exe install|start|stop|uninstall`
(fallback: `deploy\windows\sc-install.ps1`).

### Docker (untested here)
```sh
cd deploy/docker && cp env.example .env     # edit REDX_CLUSTER_SECRET
docker compose up --build -d                 # 3 nodes: 127.0.0.1:7777 (leader), :7778, :7779 (followers)
docker compose logs node1 | grep -A8 "FIRST RUN"   # the one-time admin key
```

## 2. Your first API key

* **Installers** create the first admin key *offline* and print it once. It is stored only as a salted argon2id hash.
* **Foreground first run** (`redxd run`, empty data dir) prints a one-time admin key on the console. It is **not** written to any log file.
  Under a service manager you would not see it, which is why the installers create it up front and set `REDX_NO_BOOTSTRAP=1`.
* Lost the admin key? Stop nothing — create another one directly on the data dir (it needs filesystem access, so only the machine's owner can do it):
  `redx key create --offline --data-dir <dir> --scope admin --db '*' --label recovery`

Create an application key (requires an admin key in `REDX_KEY`):
```sh
redx key create --db default --scope write --ttl 30d --label my-game-server   # shown ONCE: rx_live_<43 chars>
redx key create --db game --scope read --prefix 'public.red-xai.db:Box[ID=1]' # path-prefix ACL
redx key list
redx key rotate <id> --grace 10m       # new key now, old one dies in 10 minutes
redx key revoke <id>                   # effective immediately on this node
```
Scopes: `read` < `write` < `admin`. A key is bound to one database (or `*`). `admin` on `*` is the *host admin*
(create databases/keys, backups, cluster). `admin` on one db manages keys of that db only. Prefix-restricted keys cannot use `/v1/query`.
Each key may carry its own `--rate-rps/--rate-burst` (default 100 rps, burst 200; `-1` = unlimited).
Key format: `rx_live_` + 43 base62 characters (32 random bytes). The first 8 characters are a non-secret lookup id; the rest is only ever compared via argon2id + constant-time compare.

## 3. Using the API

```sh
KEY=$REDX_KEY; H="Authorization: Bearer $KEY"; U=http://127.0.0.1:7777
# paths look like  file.red-xai.db:Box[ID=1].Key   (escape [ ] as %5B %5D if your client needs it)
curl -X PUT    -H "$H" --data '"Ann"' "$U/v1/path/players.red-xai.db:Box%5BID=1%5D.Name"      # raw Box text
curl           -H "$H" "$U/v1/path/players.red-xai.db:Box%5BID=1%5D.Name"                      # {"path":…,"value":"\"Ann\""}
curl -H "$H" -H 'Accept: text/x-redx-box' "$U/v1/path/…"                                       # raw Box text back
curl -X PUT -H "$H" -H 'Content-Type: application/json' --data '{"value":{"hp":10,"tags":["a"]}}' "$U/v1/path/…"   # JSON -> Box (best effort)
curl -X PUT -H "$H" --data '"1234"' "$U/v1/path/sessions.red-xai-db-qs:Q.Otp?ttl=30"           # TTL (needed for -qs files); &sliding=true
curl -X PUT -H "$H" --data '12'      "$U/v1/path/world.red-xai-db-qs-ls:Hot.Counter?checkpoint=5"   # (5)* marker
curl -X DELETE -H "$H" "$U/v1/path/…"
curl -X POST -H "$H" -H 'Content-Type: application/json' --data '{"q":"accounts.red-xai.db:AccountVault[ID=*].Name"}' "$U/v1/query"
curl -H "$H" "$U/v1/stats"
curl -X POST -H "$H" --data '{"name":"game"}' "$U/v1/dbs"      # new database; then use /v1/db/game/path/… , /v1/db/game/query …
```
`redx get|set|delete|query|stats|subscribe` do the same from the CLI (`redx set PATH VALUE --ttl 30`).
WebSocket: `GET /v1/subscribe?path=<prefix>` with the same `Authorization` header (browsers, which cannot set headers, may use `?access_token=`
— URLs get logged by proxies, prefer the header). Events: `{"path","op":"set|delete|expire","value","at"}`.

Errors: `401` bad/missing/revoked/expired key (deliberately identical), `403` key lacks scope/db/prefix, `404`, `400` (bad path, bad Box text, engine validation),
`429` rate limit (`Retry-After`) or too many failed auths from your IP (20/min), `503` follower with no leader.

## 3a. Indexes, packs, links and rules (REST + CLI)

These features need the real engine (the default build). With the in-memory stub backend (`-tags redx_stub`) every
route below answers **501**. All routes live under `/v1/db/{db}/…`; the CLI uses `--db` / `REDX_DB` (default `default`).

### Access control (what each key may do)

| Route | Scope needed | Path-prefix keys |
|---|---|---|
| `GET idx` (list sets), `GET idx/{set}/check`, `GET pack` (list packs), `GET pulse` (rules + timers) | read | **403** (they reveal definitions/names) |
| `GET idx/{set}/{index}/query` | read | allowed; **hits are filtered** to records whose path (`file:Box[ID=n]`) starts with the key's prefix |
| `PUT/DELETE idx/{set}`, `POST idx/{set}/rebuild`, `PUT/DELETE pulse`, `POST pack/{pack}/gc`, `GET pack/{pack}/fsck` | **admin** on the db | **403** (even for an admin key that has a prefix) |
| `GET pack/{pack}/ls|stat|file` (and `HEAD`) | read | prefix is matched against `"<pack>.red-xai-pack:<path>"`, boundary-aware: `media.red-xai-pack:public` allows `public/a.png`, not `publicity/` and not the pack root |
| `PUT/DELETE pack/{pack}/file/{path}` | write | same prefix rule |
| `GET path/{p}?resolve=1` | read | the path itself must match the prefix, **and every link target must too**: a target outside the prefix is left as an unresolved link and reported only as "dangling" (indistinguishable from a missing record) |
| `GET pulse/subscribe` (WebSocket) | read | only events about records matching the prefix are delivered; events without a record are dropped |

A key with prefix `players.red-xai.db:Player[ID=1]` therefore sees that one record in index results and rule events, and
nothing reachable only through other records. No new scope names were added: the existing `read < write < admin` +
`db` + `prefix` triple is what is enforced (data prefixes use `file:Box…`, pack prefixes use `<pack>.red-xai-pack:path`, so
one key cannot be accidentally valid for both). Unknown/wrong/revoked keys get **401**; wrong scope/db/prefix gets **403**.

### Indexes

```sh
redx idx define players players.red-xai-db-idx            # admin; declaration is Box text, see docs/phase3.md
redx idx list
redx idx query players PlayerByName --field Name --value ann                    # exact (folded if the index says Fold)
redx idx query players PlayerByPrefix --mode prefix --field Name --value an
redx idx query players PlayerLevel --field Level --lo 10 --hi 50 [--lo-excl]     # range (numbers)
redx idx query players PlayerBio --field Bio --q "red dragon" [--op or]          # text: word tokens, and/or
redx idx query players PlayerBio --mode fuzzy --field Bio --q "red mage"         # = text with op=or (NOT typo-tolerant)
redx idx check players | rebuild players | drop players
```

REST: `GET /v1/db/default/idx/players/PlayerLevel/query?mode=range&field=Level&lo=10&hi=50&limit=100&record=1`.
`limit` defaults to 100 (max 10,000). The response is `{"hits":[{"path":"players.red-xai.db:Player[ID=2]","field":"Level","value":"12"}],"count":1,"mode":"range"}`.

How it stays correct:
* Indexes are in memory and restored from a verified snapshot, or rebuilt from a scan, when first used after a start. They are
  never stale at open time.
* Every write that goes through the host API (`PUT/DELETE /v1/path/…`, rule actions, link-engine writes) updates them synchronously
  after the commit. Writes that bypass the host (e.g. opening the data files with another tool) do not: run `idx check` / `idx rebuild`.
* Records that expire (TTL files) are filtered out of results at query time and removed from the index lazily.
* A wildcard write/delete path triggers a full rebuild of the open sets (slow on big data).
* Limits are the engine's: one index covers one file; no multi-index planner, no sorting, no paging beyond `limit`, 4096 terms per record per index,
  no regex/substring/phrase/fuzzy search.

### Packs (file storage)

```sh
redx pack put media ./cat.png images/cat.png      # streams, sends the SHA-256 so the server verifies; creates the pack on first use
redx pack ls media images
redx pack get media images/cat.png --out cat.png                      # whole file: SHA-256 verified against the ETag
redx pack get media images/cat.png --out head.bin --range 0-1023       # ranged (also --offset N --length N)
redx pack stat media images/cat.png | redx pack rm media images/cat.png | redx pack list
redx pack gc media            # admin: free chunks of deleted/replaced files (deletes only leave them on disk until you run this)
redx pack fsck media --deep   # admin: read + hash every chunk, report problems (changes nothing)
```

REST: `PUT /v1/db/default/pack/media/file/images/cat.png` (raw body; optional `X-Redx-SHA256` header, mismatch → **422** and nothing is stored),
`GET …/file/…` with `Range: bytes=a-b` / `a-` / `-n` (single range → **206**; multi-range is ignored and the whole file is returned; unsatisfiable → **416**),
`HEAD`, `ETag: "sha256-<hex>"` + `If-None-Match` → 304, `GET …/ls[/folder]`, `GET …/stat/path`, `DELETE …/file/path`, admin-only `POST …/gc` and `GET …/fsck[?deep=1]`.

* Chunks are 1 MiB, content-addressed by SHA-256 (identical chunks are stored once). Uploads are streamed one chunk at a time; the largest upload
  is `max_upload_mb` (default 1024, **413** beyond). Folder entries, names and path rules are the engine's (`..`, `//`, `\`, `:`, control characters, Windows device names are rejected with **400**).
* A **whole-file** download is verified chunk by chunk and by the whole-file hash. If stored data is damaged the server aborts the connection
  (clients see a failed/short transfer, never a clean-looking wrong file); `redx pack get` also checks the hash and keeps nothing on failure.
  A **ranged** read verifies the length and SHA-256 of every chunk it touches (so damage in those chunks is a **500**) but cannot check the whole-file hash.
* Files are served inline only for a short allow-list of media types (png, jpeg, gif, webp, mp3, ogg, wav, mp4, webm, pdf, txt, json, csv); everything else, including `.html`, is
  `application/octet-stream` + `Content-Disposition: attachment`, with `nosniff` and `Content-Security-Policy: sandbox`.
* Not implemented: rename/move, folder delete (`rmdir`), mkdir without a file, resumable uploads, multi-range responses; automatic garbage collection (deleted/replaced files leave their chunks on disk until `redx pack gc`), thumbnails/streaming (`.red-xai-media`).

### Links

`GET /v1/path/players.red-xai.db:Player[ID=1].Guild?resolve=1[&depth=N]` (CLI: `redx get PATH --resolve [--depth N]`) returns the value with embedded `@>` links replaced by
their targets, recursively (depth 0 = engine default 8, max 32). Dangling links and cycles are left in place and listed under `dangling` / `cycles` in the JSON response. The path must be concrete
(a file and a record, no wildcards). Resolution reads local data, so it also works on followers.

### Pulse rules

```sh
redx pulse put rules.red-xai-db-pulse        # admin; replaces the whole rule set (validated first; all-or-nothing)
redx pulse get                               # source, rule names, pending timers
redx pulse subscribe --topic 'player.*'      # WebSocket: one JSON event per line; --changes adds raw change.* events
redx pulse rm
```

REST: `PUT/GET/DELETE /v1/db/{db}/pulse`, WebSocket `GET /v1/db/{db}/pulse/subscribe?topic=<pattern>&changes=1` (`Authorization: Bearer` header, or `?access_token=` for browsers).
Event: `{"seq":3,"time":"…","kind":"emit|timer|loop|error|change","topic":"player.died","rule":"PlayerDeath","path":"players.red-xai.db:Player[ID=1]","data":"<record as Box text>"}`.
Topic patterns are `""`/`*` (all), `prefix*`, or an exact topic.

* When rules exist, `PUT /v1/path/…` writes made through the host are routed through the rule engine so rules see before/after state. Whole-record writes (with or without TTL) and field writes to an existing
  record are covered; field writes **with a TTL**, wildcard paths, and field writes to a record that does not exist yet are written directly and **do not run rules**. Deletes never run rules. TTL expiry does not run rules.
* Rules and timers run **only on the leader**. Timers live in memory: a restart or a failover loses pending timers (the rule definitions survive).
* Event delivery is best effort: a subscriber that cannot keep up misses events (the engine's bus drops rather than blocks).

### Cluster behaviour of these features (honest version)

| What | Replicated to followers? |
|---|---|
| Index and pulse **definitions** (small text files) | **Yes**, as log entries (and inside snapshots) |
| Index **contents** | No. Rebuilt from replicated data on a node when it is first queried as leader (so a promoted node answers correctly, after a rebuild pause) |
| **Pack contents** | **No.** They exist only on the node that received the upload (and in a full snapshot taken at that moment). After a failover the new leader lacks packs uploaded after its last snapshot. Back packs up (`redx backup`) if you cannot lose them |
| Pulse timers | No (in memory on the leader) |
| Link resolution | Works on any node (reads replicated data) |

A follower **forwards** index/pack/pulse requests (reads too, and WebSocket subscriptions) to the leader, because that is where this state lives; resolution is answered locally.

## 3b. SQL, media, access policy, scripts, encryption (0.2.0)

Real engine only (the `redx_stub` build answers **501**). Everything is under `/v1/db/{db}/…`; `POST /v1/sql` is the
shorthand for the default database. The older path query `POST /v1/query` and the SDK `query()` are **kept** next to SQL.

| Route | Scope | Path-prefix keys |
|---|---|---|
| `POST sql` body `{"q":"SELECT … WHERE Level > ?","params":[10],"explain":false}` (or the statement as plain text) | read (SELECT/EXPLAIN); **write** for INSERT/UPDATE/DELETE | prefix must be `file`, `file:Box` or `file:Box.Field` (becomes a policy ceiling); **403** for record-id or wildcard prefixes |
| `GET media`, `GET media/{lib}`, `GET media/{lib}/stat/{name}`, `GET\|HEAD media/{lib}/file/{name}` (HTTP `Range`, `If-Range`, `If-None-Match`, 304, 416) | read | prefix matched against `<lib>.red-xai-media:<name>`; list is filtered |
| `PUT media/{lib}/file/{name}[?type=image/png]`, `DELETE …` | write | same prefix rule |
| `GET media/{lib}/fsck[?deep=1]`, `POST media/{lib}/sweep` | admin | 403 |
| `GET sec` (summary; source only for admins) | read | 403 |
| `PUT\|DELETE sec` | admin | 403 |
| `GET code` | read | 403 |
| `PUT\|DELETE code/file/{file}` | admin | 403 |
| `POST code/run/{script}` body `{"args":[…]}` | write | needs a `file`/`file:Box[.Field]` prefix (403 otherwise) |

CLI: `redx sql 'SELECT …' [--param V]… [--sparam S]… [--explain] [--json]`, `redx media put|get|ls|stat|rm|fsck`,
`redx code ls|define|rm|run`, `redx sec show|put|rm`, `redx key create … --roles a,b`, and `redxd keygen`.

**SQL (RXQL, `engine/query`).** `SELECT/INSERT/UPDATE/DELETE/EXPLAIN`, `?` parameters only for values (never parsed as
query text), `FROM file.Box` (the file name is without `.red-xai.db`; `_id` is the record id), `JOIN` over `@>` links,
`DELETE` cascades along `@!>` links (standalone `.red-xai-db-link` files in the db folder and embedded links), a `DELETE` without
`WHERE` is refused (tested), the engine has limits on rows/scan/statement size (`docs/phase5.md`). Errors: 400 (syntax, with position), 403, 404, 409
(duplicate `_id`). The result is `{kind, columns, rows, count, affected, paths, access, plan, cascaded}`; `access` is
`index:<name>` when a `.red-xai-db-idx` index served the `WHERE`, else `scan`. Indexes defined through the API are
kept current by SQL writes **and** by REST writes. Explicitly naming a field the caller may not read is **403**;
`SELECT *` silently drops denied fields (the engine's rule, tested).
On a follower SQL **reads** run locally (scan, no planner indexes); SQL **writes** are forwarded to the leader.

**Access policy (`.red-xai-sec`) and API keys.** The host builds the policy the engines enforce from **two** sources:
(1) a generated grant per API key from its scope/db/prefix, which is a **hard ceiling** (a policy file can narrow a key but never widen
it; a read key stays read-only whatever the file says), and (2) your policy: hand-placed `*.red-xai-sec` files in the db folder
and/or the API-managed one (`PUT sec`). Keys carry **roles** (`redx key create --roles guest`); the policy refers to them as `role:guest`.
Default is deny for everything a key's grant does not cover; deny beats allow at equal specificity. The policy is checked
**inside the engines** (SQL: `query.Authorizer`, scripts: `code` host functions) and **again at the host** for REST reads/writes/deletes, resolve, index hits,
packs, media, pulse events. Behaviour you should know:

* Record reads over REST are **redacted** (denied fields removed, also inside nested boxes and resolved links); a denied whole file/box is 403; a write that contains a denied field is 403.
* Once any policy exists, the raw path query `POST /v1/query` is **admin only** (it returns whole records and cannot be field-filtered). Use `/v1/sql`.
* Pulse event `data` is dropped for non-admin keys when a policy exists.
* Policy changes via the API apply immediately and replicate as definitions; **hand-placed `.red-xai-sec` files are read when the db opens** (no hot reload; restart or use `PUT sec`). A policy file that fails to parse stops the database from opening: it never falls back to "no policy".
* `Encrypt` rules (`{Encrypt[1]} = [ {File[]} = "vault*", {Key[]} = "main" ]`) apply to **packs only** (see below). They are *not* enforced for `.red-xai.db` / `-qs` / `-qs-ls` data files (use disk encryption), and a media library matched by an Encrypt rule is refused (501), because Range reads over ciphertext are not implemented.
* Keys with a record-id or wildcard prefix (e.g. `file:Box[ID=1]`) keep working on the old REST routes but get **403** on SQL, scripts and the other policy-checked operations, because those prefixes cannot be expressed as policy resources.

**Master key from a key file (never env/command line).** `redxd keygen [--out FILE] [--id ID] [--data-dir DIR]` writes 32 random bytes
as `<id> <64 hex chars>` lines (`#` comments; mode 0600; refuses to overwrite; prints nothing secret). `redxd` loads `master_key_file`
(`--master-key-file`, `REDX_MASTER_KEY_FILE` = a *path*) or, if present, `<data_dir>/master.key`. On POSIX a file readable by group/others is
**refused** and the daemon does not start; a configured but missing file is an error. Several lines = key rotation (the first line is the current key; older ids still decrypt).
Error messages never echo key material. **Back the key file up separately from the data: without it encrypted packs are unreadable.** Windows: ACL
permissions are not checked by the daemon (the installer restricts the folder).

**Encrypted packs.** A pack whose name matches an `Encrypt` rule is stored as AES-256-GCM chunks (the key id is in every ciphertext).
Verified: no plaintext at rest; a byte flipped on disk is detected and the download fails; a plaintext file found in a pack that is now under a rule fails closed (500);
with no master key loaded the write is **503** and no pack folder is created. Limits: encrypted packs have **no HTTP `Range`** and no `Content-Length`
(`Accept-Ranges: none`, `X-Redx-Encrypted: 1`; a `Range` request gets the whole file), and the size/ETag the API reports are for the stored (ciphertext) form.

**Scripts (`.red-xai-code`).** A document of `{Script[n]} = [Name, Params, Uses, Source, Limits]`. Host functions must be declared in `Uses`
(an undeclared one is rejected when the file is defined). The interpreter has step/time/allocation limits (a runaway loop returns 400 `limit`). Every
`db_get`/`db_set`/… call is checked three times: against the host-level **`code_allowed_boxes`** (`file:Box` globs; **empty = scripts may touch no record**),
against the running key's grant, and against the policy. `AllowedBoxes` did not exist in the engine, so it is defined in the host
(`code_allowed_boxes` in `redx.toml`, `--code-allowed-boxes`, `REDX_CODE_ALLOWED_BOXES`). Scripts run **on the leader** and need a write key; defining/removing code is admin.
There is no persistent state between runs. Hand-placed `*.red-xai-code` files load when the db opens.

**Quirks fixed in 0.2.0.** `GET /v1/path/…` of an absent or expired path is now **always 404** (before: sometimes 200 with `NELL` in `.red-xai-db-qs*`);
a value that was *stored* as NELL is a real value and still returns 200. `GET /v1/subscribe` with a bare file name (or no path) is **400**
(it used to upgrade and close silently); `file.red-xai.db:` (every box of a file) works. The engine's own `Subscribe` is unchanged; the host validates before it.

**Hand-placed files and the cluster.** On start `redxd` opens every `.red-xai*` file found in `<data>/dbs/<name>/` (cold/qs/qs-ls data, `.red-xai-db-link`, `.red-xai-db-pulse`,
`.red-xai-db-idx`, `.red-xai-sec`, `.red-xai-code`; packs and media libraries are folders created on first use). Definitions made through the API
(idx, pulse, sec, code) replicate to followers as one log op; **pack, media and index contents do not** (leader-local, as for packs in 0.1.2); SQL writes
and script runs are forwarded to / run on the leader.

## 4. Adding nodes on other devices

Any device that can run the static binary can be a node (PC, Mac, Linux box, Raspberry Pi; Android via Termux with the linux/arm64 or
armv7 build; neither was executed on real devices, only cross-compiled). A cluster is **one leader + any number of followers** (async replication).

1. **Leader must be reachable and use TLS** if the nodes talk over anything but a trusted LAN:
   in `redx.toml`: `listen = "0.0.0.0:7777"`, `tls = "auto"`, `advertise = "https://<leader-ip>:7777"`. On start it prints the certificate fingerprint
   (`redx doctor` / the log show it; also `openssl x509 -in <data>/tls/cert.pem -noout -fingerprint -sha256`). Open the firewall for that port only on your private network.
2. **On the leader** create a short-lived, single-use join token (admin key required, default 15 min):
   ```sh
   export REDX_ADDR=https://<leader-ip>:7777 REDX_PIN=<leader cert sha256 hex>
   redx node token            # prints rxj_...
   ```
3. **On the new device** install with the follower role (`sudo ./install.sh --from . --role follower` / `.\install.ps1 -From . -Role follower`;
   this installs the service but does not start it and creates no admin key), then:
   ```sh
   redx node join https://<leader-ip>:7777 --token rxj_... --pin <fingerprint> \
        --advertise https://<this-device-ip>:7777 --node-id pi-garage --data-dir /var/lib/redx
   sudo chown -R redx:redx /var/lib/redx && sudo systemctl start redxd   # Windows: redxd.exe start; macOS: sudo launchctl load -w /Library/LaunchDaemons/com.redx.redxd.plist
   ```
   `join` redeems the token, stores a per-node secret in `<data>/cluster/cluster.json` (0600) and the node starts replicating. State in `cluster.json`
   wins over `role/peers` in the config file.
   Already-running node? `redx node join <leader> --token T --local http://127.0.0.1:7777 --key <local admin key>` repoints it live.
4. Check: `redx node status` (on the leader: members, ack offsets, lag, health).
5. Static clusters (docker-compose): set the same `cluster_secret` / `REDX_CLUSTER_SECRET` on every node and `role=follower`, `peers=[leader url]`; no token needed.

Clients may talk to **any** node: reads are served locally (eventually consistent), writes/key changes/db creation are transparently forwarded to the leader.
For read-your-writes send the `X-Redx-Seq` you got from the write as `X-Redx-Wait-Seq` on the read (waits up to 3 s for that offset).
For a stronger write ack send `X-Redx-Wait-Replicas: N` on the write: the leader waits up to 2 s for N followers to confirm and returns `X-Redx-Replicas-Acked`
(it does **not** fail the write if fewer ack — it is a measurement, not a quorum).

### Consistency model — what you actually get
* **Asynchronous** replication of an ordered log (one offset space per leader "replication id"). The leader acknowledges a write after applying it locally, **before** followers have it.
* A follower **resumes by (replication id, offset)** after a restart or disconnect. If the leader no longer has that offset (log truncated — default retains 1,000,000 entries,
  `repl_log_max`) or the replication id is unknown/diverged, the follower is told to do a **full snapshot resync** (leader pauses writes while it copies; follower swaps in the files and continues from the snapshot offset).
* **Followers never expire keys.** The leader's engine decides expiry and replicates an explicit `expire` delete; followers apply it as a plain delete. (Absolute deadlines, not relative TTLs, are shipped.)
  Consequence: a follower can serve a key for a short moment after its TTL passed, until the leader's delete arrives. Sliding-TTL extensions caused by reads on the leader are leader-local.
* **Failover is manual and can lose acknowledged writes.** `redx node promote` (on a follower, admin key) bumps the replication id (the old id + offset are remembered), re-arms expiry and makes it a leader.
  Any write the old leader acknowledged but had not yet replicated is **lost**. The other followers are re-attached with `redx node repoint <new-leader> --token <token from new leader>`;
  a follower whose history is a prefix of the new leader's resumes by offset (partial resync), otherwise it takes a snapshot and its extra writes are discarded.
* **Split brain is possible**: nothing stops the old leader from continuing to accept writes if it is merely partitioned. Make sure it is stopped (or demote it with `redx node repoint`, which discards its un-replicated writes via snapshot) before promoting.
* **Key revocation is eventually consistent across nodes** (keys replicate through the same log). Revoking on the leader is instant there; followers apply it within the replication lag. Revoke on all nodes if urgency matters.
* **Volatile data** (`.red-xai-db-qs`) is RAM-only by definition: a restart loses it; a follower that needs a full snapshot does not receive volatile contents, only later changes.
* The snapshot is write-paused file copy after an engine checkpoint; engine-initiated expiry commits during the copy may be replayed on the follower (idempotent).
* Not implemented: automatic election, quorum acks, multi-leader, partial/sharded replication, bounded-staleness reads.

## 5. Backup & restore

```sh
redx backup --out nightly.tgz                      # ONLINE: admin key; writes paused briefly (snapshot)
redx backup --offline --data-dir /var/lib/redx --out cold.tgz   # OFFLINE: refuses while redxd runs (cleanest)
redx restore nightly.tgz --data-dir /var/lib/redx [--force]     # stop redxd first; --force moves existing dbs aside, never deletes
```
Backups contain databases + key records (hashes only) + TTL deadlines. They do **not** contain TLS keys, node secrets, the audit log or `cluster.json`.
A restore starts a new replication history: followers will full-resync from the restored leader. Protect backups like the key store — they include key hashes and all your data.

## 6. Security notes

* **Keys are secrets.** They are shown once, stored only as salted argon2id hashes (m=19 MiB, t=2 by default), compared in constant time, and never logged (the audit log
  records only the 8-character key id, the source IP, route class and reason; request data paths are not logged). Put keys in env vars or a secret manager, not in command lines
  (`--key` shows up in process lists), source control or client-side code. Use the narrowest scope/db/prefix and a TTL; rotate with `redx key rotate`.
* **TLS.** The default listen address is loopback. If you listen on anything else without TLS, `redxd` prints a warning: keys and data would be readable on the network.
  Use `tls = "auto"` (self-signed; clients pin the printed SHA-256 with `--pin`/`REDX_PIN`) or `tls = "files"` with a real certificate, or terminate TLS in a reverse proxy.
  Node-to-node traffic (replication, snapshots, join) also needs TLS across untrusted networks; followers pin the leader's certificate fingerprint.
* **Firewall.** Expose the port only to the networks that need it (private LAN/VPN such as WireGuard/Tailscale). Do not port-forward a bare redxd to the internet.
* **Brute force.** 20 failed authentications per minute per source IP → `429`. Behind a reverse proxy all clients share the proxy's IP (X-Forwarded-For is deliberately not trusted); throttle at the proxy too.
* **At rest.** `keystore_encrypt = true` + env `REDX_KEYSTORE_PASSPHRASE` encrypts `keys.red-xai-keys` (AES-256-GCM, argon2id-derived key). Database files (`.red-xai.db`, `-qs`, `-qs-ls`) are **not** encrypted by the host (use disk encryption). **Packs** are encrypted when a `.red-xai-sec` `Encrypt` rule names them (master key from a key file, §3b); media libraries and indexes are not.
  The data directory is created 0700 and the keystore 0600 (engine data files rely on the 0700 directory; Windows: the installer restricts `C:\ProgramData\RedX` to SYSTEM and Administrators).
* **Join tokens** are single-use and short-lived; **node secrets** and the optional `cluster_secret` grant replication access (read everything, including key hashes). Treat them like admin keys.
* **Offline commands** (`redx key … --offline`, `restore`) need filesystem access to the data dir — by design that equals root of the host. Offline key changes made while a leader runs are noticed within ~1 s and replicated.
* **Updating:** replace the binaries and restart the service. Keep followers on the same or a newer version than the leader's *log format* (v0.1 only has one).

## 7. Configuration reference

File `redx.toml` (all optional; see `deploy/redx.example.toml`), env `REDX_*`, flags. Precedence: defaults < file < env < flags.

| Key | Env | Default |
|---|---|---|
| `data_dir` | `REDX_DATA_DIR` | `/var/lib/redx` (root) · `~/.redx/data` · `C:\ProgramData\RedX\data` · `/usr/local/var/redx` (macOS) |
| `listen` | `REDX_LISTEN` | `127.0.0.1:7777` |
| `tls` (`off`/`auto`/`files`), `tls_cert`, `tls_key`, `tls_hosts` | `REDX_TLS…` | `off` |
| `node_id` | `REDX_NODE_ID` | generated once → `<data>/node.id` |
| `role` (`leader`/`follower`), `peers`, `advertise`, `cluster_secret` | `REDX_ROLE/PEERS/ADVERTISE/CLUSTER_SECRET` | `leader` |
| `default_db` | `REDX_DEFAULT_DB` | `default` |
| `rate_limit_rps`, `rate_limit_burst` | `REDX_RATE_LIMIT_RPS` | 100 / 200 |
| `keystore_encrypt` | `REDX_KEYSTORE_ENCRYPT` + `REDX_KEYSTORE_PASSPHRASE` | off |
| `audit_log`, `audit_success` | `REDX_AUDIT_LOG` | `<data>/audit.log` (50 MB rotate to `.1`), successes off |
| `engine_durability` (`sync`/`group`/`async`), `engine_default_ttl` | `REDX_ENGINE_DURABILITY` | `group`, 0 |
| `repl_log_max`, `heartbeat_seconds`, `max_body_mb` | | 1,000,000 · 3 · 16 |
| `max_upload_mb` (largest single pack upload) | | 1024 |
| `master_key_file` | `REDX_MASTER_KEY_FILE` (a path) | `<data>/master.key` if present; must be mode 0600 (POSIX) |
| `code_allowed_boxes` (list of `file:Box` globs) | `REDX_CODE_ALLOWED_BOXES` | empty = scripts touch no record |
| `grpc_listen` | `REDX_GRPC_LISTEN` | stub, ignored |
| — | `REDX_NO_BOOTSTRAP=1` | disable first-run admin key |

Data layout: `<data>/dbs/<name>/*.red-xai*` (engine files; also hand-placed `.red-xai-sec`, `.red-xai-code`, `.red-xai-db-link`), `master.key` (optional), `keys.red-xai-keys` (+`.lock`), `audit.log`, `node.id`, `tls/`, `cluster/{cluster.json,repl.json,oplog.jsonl,members.json,ttl.json}`, `redxd.lock` (single-instance lock).

## 8. Troubleshooting

`redx doctor [--config FILE]` checks: platform, config validity, data dir writability and free space, keystore permissions/readability, TLS certificate expiry,
whether the daemon answers, whether your key works, cluster health, and "non-loopback listen without TLS". Common issues:
* `data dir … is in use by another redxd process` — a second instance (or a stale service) holds `redxd.lock`.
* Follower log shows `leader answered 401` — wrong token/secret, or the node id given to `redx node join --node-id` differs from the daemon's (the CLI writes `node.id` for you; don't override `node_id` in the config afterwards).
* `429` while testing wrong keys — you tripped the per-IP failed-auth throttle; wait a minute.
* Termux/Android: Go binaries built for linux cannot read Android's DNS config; use **IP addresses** for `peers`/`advertise`, run with `./install.sh --user`, and keep the app awake (Android may kill background processes). Untested here.

## 9. Building, packaging, releases

```sh
go vet ./... && go test -race -count=1 ./...        # whole module
scripts/build.sh                # all 7 targets -> dist/ (tar.gz, zip for Windows, SHA256SUMS); VERSION=1.2.3 scripts/build.sh
scripts/build.sh --only linux/amd64      # one target;   --tags redx_stub  builds the in-memory stub store (NOT for production)
scripts/smoke.sh bin            # curl-based smoke test of a native build (keys, set/get, idx, pack+range, SQL, media range, sec, encrypted pack, code, 404, subscribe); scripts/cluster-smoke.sh bin  for 3 local nodes
```
`deploy/release.yml.example` is a GitHub Actions workflow kept **outside** `.github/` so nothing triggers; copy it to `.github/workflows/` when you decide to publish.
The `Makefile` wraps the same commands (`make dist`, `make test`); it was not exercised in the build sandbox (no `make` installed) — `scripts/build.sh` was.
