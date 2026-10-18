# RedX phase 2: links and live rules

Code: `engine/link` (`.red-xai-db-link`) and `engine/pulse` (`.red-xai-db-pulse`).
Go standard library plus the existing `redx/box` package only. Both packages
talk to storage through small interfaces (`link.Store`, `pulse.Store`) and are
tested against `link.MemStore`; wired to the real engines by `host/engineadapter` (0.1.2: links, rules; 0.2.0: standalone link files, SQL cascades; see docs/HOSTING.md).

Everything below is the syntax that is implemented and tested. Both formats
are ordinary Box documents (parsed by `box.ParseDocument`).

## Links (`.red-xai-db-link`)

A link is a Box link value:

```
"players.red-xai.db":Player[ID=1].Guild  @>   "guilds.red-xai.db":Guild[ID=10]
"players.red-xai.db":Player[ID=1]        @!>  "inventories.red-xai.db":Inv[ID=100]
```

| operator | meaning |
|----------|---------|
| `@>`     | read link. The target can be read through the source (`Engine.Resolve`, `Engine.Expand`). Deleting the source never deletes the target. |
| `@!>`    | cascade link. Deleting the source record also deletes the target record. Direction is owner `@!>` owned. |

A reference is `"file":Box[ID=n].Field.Sub[ID=m].Leaf`. The first segment (box
name and optional ID) addresses a record; the rest is a path inside it. Wildcards
(`[ID=*]`, `*`) are not valid in links. A bare reference (`"f":B[ID=1]` with no
operator) stored in a record is treated as a read link to itself.

Links live in two places:

1. **Embedded**: any field of a record whose value is a link, in nested boxes
   or arrays. The record holding the field is the owner:
   `{Guild[]} = "players.red-xai.db":Player[ID=1] @> "guilds.red-xai.db":Guild[ID=10]`
   (the From side is informational; the owner is the record that contains the field).
2. **Standalone**: entries of a `.red-xai-db-link` file, one link per top-level
   entry, parsed by `link.ParseLinkFile`. The record named by From is the owner:

```
{UserGuildLink[1]} = "players.red-xai.db":Player[ID=1] @> "guilds.red-xai.db":Guild[ID=10];
{Cleanup[2]}       = "players.red-xai.db":Player[ID=1] @!> "inventories.red-xai.db":Inv[ID=100];
```

Standalone entries must have both sides, and names must be unique. Anything
else in a link file (a plain value, a box, a bare reference) is a parse error.

### Operations (`link.Engine`)

* `Resolve(ref)` reads a record or a field across files. Missing target gives a
  `*DanglingError` (`errors.Is(err, link.ErrDangling)`) with reason
  `missing-record`, `missing-field` or `invalid-ref`.
* `Expand(addr, depth)` returns a copy of the record with embedded links replaced by
  their targets, recursively. Dangling links and links that would close a cycle
  are left in place and reported (`Expansion.Dangling`, `Expansion.Cycles`).
* `CheckDangling(files...)` scans and reports every dangling embedded link, and
  standalone links whose source or target is missing (`missing-source`).
* `Put(addr, entry, PutOptions{RequireTargets})` can refuse writes whose
  embedded links point at nothing.
* `Delete(addr, DeleteOptions)` and `PlanDelete` (dry run):
  * follows `@!>` links transitively, each record once. Cycles (including a
    self link) terminate and the closing edge is reported in `Result.Cycles`.
    Diamonds are not reported as cycles.
  * deletes leaves first, owner last, so a failure part-way leaves the owner in
    place and the same call can be repeated. It is **not atomic**: on a store
    error `DeleteResult.Deleted` lists what was already removed.
  * cascade targets that do not exist are skipped and listed in `Result.Dangling`.
    A cascade target with a field path (`@!> "f":B[ID=1].Field`) is refused
    (`ErrBadCascadeTarget`) before anything is deleted.
  * `Cascade`: `CascadeFollow` (default), `CascadeReject` (fail with
    `ErrCascadeRejected` if anything besides the named record would be deleted),
    `CascadeNone` (only the named record).
  * `Inbound`: `InboundIgnore` (default, no scan), `InboundReport` (list links from
    outside the delete set that will dangle, in `Result.Inbound`),
    `InboundRestrict` (refuse with `ErrRestricted`, listing the blockers). Links
    from records that are themselves being deleted do not block.
  * `PruneLinks` removes standalone links touching a deleted record.
  * `MaxRecords` bounds one delete (`ErrTooManyRecords`).

Link-aware `Put`/`Delete` through one Engine are serialized (a cascade is never
interleaved with another Put/Delete through that Engine). Writes that bypass the
Engine are not protected.

## Pulse rules (`.red-xai-db-pulse`)

One top-level box per rule. The key is the rule name, the `ON` attribute says
what it watches, the body holds a `When` condition and actions.

```
{PlayerDeath[ON="players.red-xai.db:Player.Health"]} = [
    {When[]}      = "<= 0",
    {SetTarget[]} = "Player.IsDead = TRUE",
    {Timer[]}     = [ {Name[]} = "Respawn", {Seconds[]} = 10,
                      {Do[]} = ;["set Health = 100", "set IsDead = FALSE", "emit player.respawned"]: ],
    {Emit[]}      = "player.died"
];
```

"When health reaches 0, set IsDead, start a respawn timer": on a write that takes
`Health` to 0 or below, the rule sets `IsDead`, starts a 10 second `Respawn`
timer and emits `player.died`. When the timer fires it restores health and emits
`player.respawned`. The blueprint form `ON=PlayerStatsBox.Health` (no file) is
accepted too and matches that box in any file.

### `ON`

`ON="[file:]Box[ID=n][.Field]"`. The file is optional (omitted = any file). An
`[ID=n]` pins the rule to one record. `.Field` is the field a shorthand `When`
applies to. `Box[ID=*]` means any ID.

### `When`

A string. Either a **shorthand** starting with a comparison (`"<= 0"`, applies to the
`ON` field), or a full expression.

* values: numbers, `"strings"`, `TRUE`, `FALSE`, `NELL` (or `null`), field paths
  (`Health`, `Stats.Hp`, `Inventory[id=10].Count`), cross-record references
  (`"guilds.red-xai.db":Guild[ID=1].Score`). A leading path segment equal to the
  rule's box name means the record itself (`Player.Health` == `Health`).
* operators: `==` `=` `!=` `<>` `<` `<=` `>` `>=`, `and` `or` `not` (any case),
  `+ - * /` (`+` also joins strings), parentheses.
* a missing field or missing referenced record reads as `NELL`; `NELL` only compares
  with `==` / `!=`; ordering across different types is false. Arithmetic on non-numbers
  is an error (reported, see below). Field names containing `-` cannot be used in
  expressions.

### Triggering

* default `edge`: fires when the condition becomes true, i.e. it was false (or the
  record did not exist) before the write and is true after. Taking more damage while
  already dead does not fire it again.
* `{Trigger[]} = "level"`: fires on every write where the condition is true **and**
  one of the fields the condition reads changed.
* Rules run on writes through `Engine.Put` / `Engine.Set` and on writes made by
  actions. Deletes do not run rules.
* All rules triggered by a write are decided from that write's before/after state
  first, then their actions run in rule order.

### Actions (run in the order written)

| entry | meaning |
|-------|---------|
| `SetTarget` (or `Set`) | string or array of strings `"<target> = <expr>"`. Target is a field of the record (`IsDead`, `Stats.Deaths`; missing fields and boxes are created) or `"file":Box[ID=n].Field` for another existing record. A set that does not change the value writes nothing. |
| `Emit` | string or array: publish an event with that topic and a snapshot of the record. |
| `Timer` | box with `Name`, `Seconds` (> 0, fractions allowed) and any actions (including nested timers). Starting a timer whose name is already pending for that record restarts it. Timer names are per record and shared by all rules. |
| `CancelTimer` | string or array of timer names. |
| `Do` | string or array of action strings: `set <target> = <expr>`, `emit <topic>`, `cancel <timer>`. |

Use IDs to repeat a key (`{Set[2]} = ...`). If an action fails, the remaining actions
of that firing are skipped (earlier ones are not undone) and a `pulse.error` event is
published.

### Loop protection

* A rule fires at most once per record per chain. If A sets what triggers B and B
  sets what triggers A, the second firing of A is blocked (`Blocked{Reason:"loop"}`,
  event `pulse.loop`). A level rule that triggers itself runs once.
* Chains are cut at `Config.MaxChainDepth` nested writes (default 16, reason `depth`).
* A timer's actions start a new chain (so a respawn can re-arm rules), but the number
  of timer hops is capped by `Config.MaxTimerGenerations` (default 32, reason
  `timer-generation`). This stops timer -> rule -> timer perpetual tickers; a
  deliberate periodic rule is therefore limited to that many ticks.

### Events (live clients)

`Engine.Subscribe(pattern, buffer)` returns a channel and a cancel function
(`pulse.Subscriber`; the built-in `pulse.Bus` implements `Publisher` and
`Subscriber`, or pass your own `Publisher`). Patterns: `*` (all), `prefix*`, or an
exact topic. Delivery never blocks the engine: a full subscriber buffer drops
events (counted in `Bus.Dropped()`). Topics:

* the topic of every `emit` action
* `pulse.timer.started` / `pulse.timer.fired` / `pulse.timer.cancelled`
* `pulse.loop`, `pulse.error`
* `change` for every write, only if `Config.PublishChanges` is set

Each `Event` has `Seq` (strictly increasing), `Time`, `Kind`, `Topic`, `Rule`, `Addr`
and a `Data` snapshot of the record.

### Limits and not yet done

* Timers are in memory only. They are lost on restart (no persistence or restore yet).
* Rule evaluation is serialized by one lock per Engine (correct, not tuned for throughput).
* Nothing is wired to the real `.red-xai.db` / `-qs` / `-qs-ls` engines or to the host yet.
  Both packages need an adapter implementing `link.Store`.
* Link reverse lookups scan whole files (no reverse index), so `InboundReport/Restrict`
  and `CheckDangling` are O(records).
* No foreign-key-by-ID relation declarations, no `nullify` delete policy.
* Link and pulse files are parsed from text; there is no file watcher or hot reload
  beyond calling `LoadRules` / `RemoveRule`.
