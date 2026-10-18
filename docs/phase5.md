# Phase 5: query language, JS and Python SDKs

Status: tested locally. As of 0.2.0 the SQL-like engine **is wired into `redxd`**: `POST /v1/sql` and
`/v1/db/{db}/sql` (scope, path-prefix and `.red-xai-sec` policy enforced; see `docs/HOSTING.md` §3b), `redx sql`, and `sql()` in both SDKs.
The path-pattern `query()` stays beside it. The integration tests are `host/engineadapter/services_test.go` and `e2e_test.go`.

## 1. Query language (`engine/query`)

```go
db, _ := query.New(query.Config{Store: st, Auth: policy, Indexes: ix, Links: le}) // Auth is required
res, err := db.Exec(ctx, principal, "SELECT Name FROM players.Player WHERE Level >= ? ORDER BY Name LIMIT 10", 5)
```

One statement per call (a trailing `;` is fine). Values go in `?` parameters, never into the text; a parameter is never parsed as SQL.

```
SELECT items FROM file[.Box] [alias] [[LEFT|INNER] JOIN alias.field [AS] alias2]
       [WHERE expr] [ORDER BY expr [ASC|DESC], ...] [LIMIT n|?] [OFFSET n|?]
INSERT INTO file.Box (cols) VALUES (...), (...)
UPDATE file[.Box] SET path = expr, ... WHERE expr
DELETE FROM file[.Box] WHERE expr
EXPLAIN <statement>                -- shows the plan, runs nothing
```

* **Names.** Bare identifiers are `[A-Za-z_][A-Za-z0-9_]*`; anything else (hyphen, dot, reserved word) is `"quoted"`. Strings are `'single'` with `''` for a quote. No comments. A file may be abbreviated when unique (`players` for `players.red-xai.db`).
* **Expressions.** `AND OR NOT`, `= != <> < <= > >=`, `+ - * /`, `IN`, `NOT IN`, `LIKE` / `NOT LIKE`, `BETWEEN`, `IS [NOT] NELL`, `MATCH(field,'words')` (all words) and `MATCH_ANY`, `TRUE FALSE NELL`, `?`. Nested fields use dots (`Stats.Gold`). A bare boolean field is a condition.
* **Pseudo-fields.** `_id`, `_box`, `_file` (read-only; `_id` can be given in INSERT).
* **Semantics.** Two-valued logic: NELL, a missing field or a type mismatch makes an ordering comparison false. Arithmetic on non-numbers or `/0` gives NELL. `ORDER BY` uses NELL < bool < number < string < array < box < link. The default order is record address, so the chosen plan never changes the result.
* **LIKE** supports only a trailing `%` (prefix) or an exact match, case-sensitive, no escape. `query.LikePrefixArg(text)` builds a safe prefix argument and refuses text containing `%`.
* **JOIN** follows `@>` (or bare) links held in a field (single link or array) through `link.Engine`; `@!>` is not followed. Dangling or unreadable targets drop the row (INNER) or give NELL (LEFT). Max 4 joins. Needs `Config.Links`.
* **UPDATE / DELETE without WHERE are refused** (write `WHERE TRUE` if you mean it). UPDATE evaluates all SETs against the old values and counts only records that changed. DELETE follows `@!>` cascades (each cascaded record is authorized and counted against `MaxWrites` before anything is deleted); without `Config.Links` it refuses records that carry `@!>` links.

### Planner and EXPLAIN
The access path comes from conditions on the primary source only: primary-key lookup (`_id = n`/`IN`), exact index (`=`, `IN`, exact LIKE), text index (`MATCH`), prefix index (`LIKE 'x%'`), range index (comparisons/`BETWEEN`), or a union of indexed `OR` branches; otherwise a scan. Indexes are only a prefilter: the full WHERE is always re-applied, so Fold/array indexes stay correct. Strings over 256 bytes are not indexable (scan). `Result.Access` and `Result.Scanned` report what happened. The planner is a fixed ranking with no statistics.

### Authorization (fail closed)
`Config.Auth` is mandatory (`nil` is `ErrNoAuth`; `query.AllowAll{}` is the explicit opt-out; a nil `*sec.Policy` denies everything). Checks run before any storage read when the box is fixed: box-level read, then fields named in the statement. Rows are gated at runtime (with `OwnerOf` if given) and every returned field is masked (denied field = NELL, also inside nested boxes and for `*`). INSERT needs box write plus per-column write; UPDATE needs the read gate plus per-field write; DELETE needs delete on the box and on every cascaded record. Field-only grants do not open a box for querying. An unknown file is authorized first, so "forbidden" and "missing" look alike to an unauthorized caller. EXPLAIN is authorized like the statement.

### Limits (`query.Limits`, zero = default)
| Limit | Default | Limit | Default |
|---|---|---|---|
| MaxQueryBytes | 64 KiB | MaxRows | 10,000 |
| MaxTokens | 8192 | MaxScan | 1,000,000 records |
| MaxDepth | 64 | MaxWrites | 10,000 (incl. cascades) |
| MaxInList | 1000 | MaxResultBytes | 16 MiB |
| MaxJoins | 4 | MaxColumns | 1024 |
| MaxInsertRows | 1000 | MaxParamBytes | 1 MiB |
| MaxPathDepth | 16 | | |

A result over `MaxRows` without a LIMIT is `ErrLimit`; so is `LIMIT` above `MaxRows`. The context is checked every 256 rows. Errors wrap sentinels (`ErrSyntax`, `ErrLimit`, `ErrParams`, `ErrNoFile`, `ErrAmbiguous`, `ErrExists`, `ErrType`, `ErrNoLinks`, `ErrNoAuth`, `ErrTooLarge`, `ErrBadStatement`) plus `sec.ErrDenied`; syntax errors have line/column and `Context(src)` prints a caret.

### Known limits
* Writes are serialized inside one `DB` but **not atomic across records**: a failure part-way returns the partial `Result` plus the error. Writers that bypass this package can still race with read-modify-write.
* No aggregates, `GROUP BY`, array indexing, or removing a field with `SET`.
* `LIMIT` without an index-ordered `ORDER BY` still reads every matching record. `MaxScan`/`MaxRows` bound the memory.
* A whole-file query (no box) silently skips boxes the caller may not read.
* Only the primary source uses indexes; joined sources are resolved by link.

## 2. SDKs (`sdk/js`, `sdk/python`)
Dependency-free (Node >= 18 with global `fetch`; Python >= 3.9 stdlib). Neither is published. Both implement the WebSocket protocol (RFC 6455) themselves, because neither runtime ships a client that can send an `Authorization` header.

```js
const { RedX } = require('./sdk/js/redx');
const db = new RedX({ url: 'http://127.0.0.1:7777', key: process.env.REDX_KEY, db: 'default' });
await db.set('players.red-xai.db:Player[ID=1]', { Name: 'Ann', Level: 5 });
await db.get('players.red-xai.db:Player[ID=1].Name');            // 'Ann'  (getBox() = raw Box text)
await db.set('sess.red-xai-db-qs:Q[ID=1].Otp', '1234', { ttl: 30, sliding: true });
(await db.query('players.red-xai.db:Player[ID=*].Name')).values;  // ['Ann']
const sub = await db.subscribe('players.red-xai.db:*', (ev) => console.log(ev.op, ev.path, ev.data));
sub.close();
```
```python
import os
from redx import RedX
db = RedX("http://127.0.0.1:7777", os.environ["REDX_KEY"])
db.set("players.red-xai.db:Player[ID=1]", {"Name": "Ann", "Level": 5})
db.get("players.red-xai.db:Player[ID=1].Name")                    # 'Ann'
with db.subscribe("players.red-xai.db:*") as sub:                 # or subscribe(path, handler)
    for ev in sub: print(ev["op"], ev["path"], ev["data"]); break
```

Methods (same in both, JS camelCase / Python snake_case): `health stats databases get getBox getOr set setBox delete query sql subscribe createKey revokeKey withDb`.
* **Values.** `set` takes JSON-compatible data (objects become boxes, `null` becomes NELL); `setBox` takes raw Box text. `get` decodes the host's Box text into plain data (links become `{"$link": "<text>"}`, ids in keys as `"Name[ID=2]"`); `getBox`/`get_box` returns it untouched. TTL options: `ttl` seconds, `sliding`, `checkpoint`. A `.red-xai-db-qs` file needs a `ttl`.
* **Errors.** `RedXError` with `status` (0 = network/timeout/decode), `retryAfter`/`retry_after` for 429, and helpers (`unauthorized`, `forbidden`, `notFound`, `rateLimited`). A bad, missing, revoked and expired key all give the same 401. The key is never in a message, `repr`, `JSON.stringify` or a pickle, and error text is scrubbed of `rx_live_...` strings. Redirects are not followed and Python ignores environment proxies, so the key is only sent to the URL you gave.
* **Paths** are percent-encoded for you (`[ ] : =`). Database names are lower-case `[a-z0-9_-]`.
* **Subscribe** paths are *path patterns* (`file.red-xai.db:*`, `file:Box[ID=*]`, `file:Box[ID=1]`), not string prefixes: a bare file name or `""` is rejected with 400 (host >= 0.2.0; it used to close silently); `file:` means every box of the file. Events are `{path, op: set|delete|expire, value (Box text), at, data (decoded)}`. The SDK does **not** reconnect; `onClose`/`on_close` reports the code (1006 = connection lost) so the caller can resubscribe. Pings are answered; the host pings every 30 s. Max message 16 MiB.
* **Not provided:** TLS client options (system trust store only), retries, connection pooling beyond the runtime's, browser support (subscribe needs Node sockets), TypeScript types, the pack/idx/pulse/`?resolve=1` endpoints.

Tests (`sdk/js/test`, `sdk/python/tests`) build `cmd/redxd` into a temp dir (or use `REDXD_BIN`), start it on a free 127.0.0.1 port with a throwaway data dir and minimal environment, take the first-run admin key from its stdout (never printed), create read/write/prefix/rate-limited keys with `POST /v1/keys`, and kill it and delete the dir afterwards. Run:
`cd sdk/js && npm test` (= `node --test test/sdk.test.js test/ws.test.js`), `cd sdk/python && python3 -m unittest discover -s tests -t .`.

Host behaviour (>= 0.2.0): reading an absent or expired path is always 404 (a stored NELL is a real value and returns 200 `NELL`), so `get` raises `notFound` and `getOr` returns the default. Keys restricted to a path prefix cannot use `/v1/query` (403); they can use `sql()` when the prefix is `file`, `file:Box` or `file:Box.Field`. `sql()` is covered by the host tests (`host/engineadapter/services_test.go`) and the live SDK tests.

## 3. Open questions for Josh
1. **Wire the SQL engine into the host?** *Done in 0.2.0 (`POST /v1/sql`, see docs/HOSTING.md).* Original question: Needs an adapter (`engine.Database` records as a `link.Store`, `OpenIndexes` for `idx.Manager`, `sec.Policy` from the key scopes) and a route such as `POST /v1/sql`, in `host/engineadapter/adapter.go`, `host/server.go`, `cmd/redxd/main.go`. All off limits for this phase. Until then `engine/query` has only been tested against `link.MemStore`, not against `engine.Database` (integration test not written).
2. **Host `/v1/query` vs SQL**: *decided: keep both* (`query()` and `sql()`).
3. **GET of an absent path**: *done (always 404).*
4. **Subscribe**: *done at the host (bare name = 400, `file:` works); `engine/db.go` `Subscribe` itself is unchanged.*
5. **SDK language set** (`sdk/`): JS and Python only. Go already has `host/client`. More (TypeScript types, browser WebSocket, Rust)?
6. **Query semantics:** `_id` as the pseudo-field name (`engine/query/plan.go`); LIKE with an escape / infix `%` (`engine/query/eval.go`); should UPDATE count unchanged rows (`engine/query/write.go`); atomic multi-record writes (needs a transaction hook in `link.Store`, `engine/link/store.go`); the record-owner model for `Owner = TRUE` rules (`Config.OwnerOf`, `engine/query/exec.go`).
7. **Aggregates / GROUP BY** are absent; add `COUNT` first?
