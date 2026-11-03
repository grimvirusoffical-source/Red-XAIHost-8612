# Native CMS integration steps

These steps use existing authenticated native owner tools and the unchanged
RedX engine. No provisioner in this change executes writes, reads credential
files, fetches a browser cookie or creates an API key. Existing application
authority and all backend tenant checks remain in force.

## Resolve the observed default without changing the engine

An explicitly observed `config.engine_durability: ""` is a supported value in
the pinned native `redxd` revision `8512e1704e589d24535e08082c3ab9f832d7655d`.
`cmd/redxd/main.go` loads one startup config and supplies it both to the opener
and `host.New`. The opener passes `EngineDurability` through
`host/engineadapter` to `engine.OpenDatabase`; `parseDurability("")` selects
`durGroup`. The native commit path waits for the group WAL ticket; the group
flusher writes and calls `File.Sync()` before marking that ticket durable.
`GET /v1/admin/config` serializes the host's startup config. Applying a preset
only saves a next-start config and reports `restart_required`; it does not
change the current config or opener.

Thus an already trusted running instance of that exact native binary, plus a
fresh authenticated config observation for the same process, supports the
explicitly labelled **source-derived effective group mode**. It does not need
a config rewrite, engine edit or service restart. A missing/null value, a
different binary, a custom library opener or an unverified observation cannot
use this inference. This source argument and the native fixture do not prove
filesystem behavior under power loss or replace an application restore drill.

The read-only resolver accepts only four non-secret observation fields:

```json
{
  "source": "owner-authenticated-native-config",
  "observedUtc": "CURRENT_UTC_OBSERVATION_TIME",
  "dbRevision": "8512e1704e589d24535e08082c3ab9f832d7655d",
  "rawEngineDurability": ""
}
```

Run locally against a pinned Git checkout, or on a verified extracted source
tree whose eight selected files match the exact pinned Git bytes:

```sh
python3 scripts/native-durability-evidence.py --native-source /reviewed/RedX --observation-file /private/selected-durability.json
```

The resolver performs no network or database access. It verifies source
interpretation and retains `runtimeProvenanceVerified=false`,
`authenticatedObservationVerified=false` and `productionReady=false`: those
must come from the operator's separate actual deployment/session evidence.
It refuses stale observations, other revisions, extra fields, duplicate JSON
keys and unknown values without echoing input. An explicit `async` is reported
as unsupported for CMS, not silently mapped to group.

## Existing owner UI/API contract

After recording native backup/restore and owner-session prerequisites, use the
existing DB console under the verified website owner session:

1. Databases → create a dedicated database named `website_cms`. Do not use a
   user DB, a master accounts DB, `default`, `admin` or an existing conflicting
   name. The existing operation is `POST /v1/dbs` with JSON
   `{"name":"website_cms"}`. It creates native data through the normal backend;
   the owner must already have host-level database administration authority.
2. Database API keys → New key: database `website_cms`, scope `write`, a clear
   service label, an explicit reviewed expiry and Advanced limits → prefix
   `site-cms.red-xai.db`. Roles should remain empty. The native request is
   `POST /v1/keys` with `Origin: https://red-xai.com`,
   `Content-Type: application/json`, `X-RedX-Confirm: createKey` and JSON:

   ```json
   {"db":"website_cms","scope":"write","prefix":"site-cms.red-xai.db","label":"website-cms","ttl":"90d"}
   ```

   The normal website session stays in the browser. Do not export it as a
   service credential, put key material in arguments/URLs/history/reports or
   create a broad owner/admin key. The new scoped API key is shown once; place
   it directly in the reviewed private service key file through the authorized
   credential workflow. Do not record its value in a screenshot or transcript.
   The listed 90-day expiry is an example requiring an actual renewal plan.
3. Before starting CMS, let its existing `RedXCmsStore.validateKey()` verify
   `GET /v1/ui/whoami` with that key on the loopback native origin. It requires
   exactly `scope=write`, `db=website_cms`,
   `prefix=site-cms.red-xai.db`, `host_admin=false`, `owner=false`, `role=leader`.
   `KeyRecord.Owner` is a tenant subject binding, not the `owner` privilege:
   the latter requires an authenticated account identity. A key minted by the
   verified website owner uses the existing non-tenant key path; it does not
   inherit account-owner privilege. Never weaken this validation to get CMS up.
4. Set the existing CMS deployment options to the verified effective mode,
   dedicated database, absolute private key/support paths and single-writer
   mode. Start only the private candidate, then follow `CUTOVER.md` for actual
   owner/normal-user preferences, unpublished draft, media/audit restart and
   coordinated native/support backup/restore acceptance.

These API examples describe the existing contract; they are not executable
credential-bearing curl snippets and do not authorize an operator to bypass
an interactive credential/permission confirmation.

## Tenant acceptance without granting billing or resources

For two real normal accounts with existing valid website sessions, read their
own DB/Host listings first. Neither account may see the CMS system DB or the
other account's immutable resource identifiers; an owner overview is a
separate audited privilege. The current native DB implementation intentionally
requires a hosted plan for normal-account creation/key mutation. A plan error
does not establish cross-tenant read/write isolation. Use existing entitled
controlled fixtures, or separately provision reviewed test entitlements in a
private fixture authority. Do not mark production subscriptions paid, invent
resource bindings or weaken plan checks just to make acceptance succeed.

Current source tests already prove native website-cookie tenant routing, CSRF,
scoped developer keys and owner separation against controlled fixtures. Real
normal-user sessions and real resource ownership remain independent live
evidence. No normal account or device is created by this source change.
