# Local HTTP API and quick functions

Preview base URL: `http://127.0.0.1:46321`. Requests use JSON with
`X-Red-XAI: 1`. Authenticated calls include `Authorization: Bearer <credential>`.
Do not place credentials in URLs. The service binds only localhost; remote HTTPS
and host enrollment are explicitly not part of this release.

## Authenticate

The native app performs local owner enrollment and sign-in. Programmatic login:

```text
POST /v1/login
{"email":"your-email","password":"your-password"}
```

The result contains a session token valid for eight hours. Do not log it.
`POST /v1/register` accepts a registration object with username, password,
confirm_password, email, confirm_email. Local members require owner approval via
`POST /v1/owner/approve` with user_id. Email delivery/verification is not implemented.

## Routes

| Operation | Route | Body / result |
|---|---|---|
| List | GET /v1/databases | Owner's databases |
| Create | POST /v1/databases | name, optional project; returns a once-shown API key |
| Read | GET /v1/db/{id} | typed objects, source when permitted, revision |
| Find | POST /v1/db/{id}/find | any of kind, name, local_id, global_id |
| Save source | POST /v1/db/{id}/source | source, revision |
| Edit | POST /v1/db/{id}/edit | operation, path, revision and operation fields |
| Generate key | POST /v1/db/{id}/keys | read_only, optional box_path, days |
| Revoke | POST /v1/keys/revoke | reference |
| History | GET /v1/db/{id}/history | revision metadata, not secret values |
| Restore | POST /v1/db/{id}/restore | target_revision, revision; creates a new revision |
| Export | POST /v1/db/{id}/export | optional passphrase; base64 .Red-XAI snapshot |
| Import | POST /v1/db/{id}/import | data_base64, optional passphrase, revision |
| Soft delete | POST /v1/db/{id}/delete | revision; revokes database credentials |
| Validate | POST /v1/validate | source; no data write |
| Host metrics | GET /v1/host/stats | live local metrics; owner session required |
| Static projects | GET/POST /v1/host/projects | name and dedicated directory for POST |
| Start / stop | POST /v1/host/start or /stop | project_id |
| Cloudflare zones | POST /v1/host/cloudflare/zones | scoped api_token; read-only, not retained |

Database keys cannot create other databases, read other owners' data, or perform
owner/session-only operations. Box grants are read-only, restricted to their subtree,
and invalidated by a database revision change. API keys are never embedded in exports.

## Python

```python
import os
from redxai.client import Client

client = Client(token=os.environ['REDXAI_SESSION'])
db = client.create('Accounts.txt', project='Development')
# Save db['api_key'] securely; never print production keys.
uid = db['id']
current = client.read(uid)
current = client.edit(uid, current['revision'], 'add_box', [], name='PInfo', local_id=232, global_id=13)
current = client.edit(uid, current['revision'], 'add_packer', [0], name='PlayerName', local_id=2, global_id=2, value_source='"Player"')
found = client.find(uid, kind='packer', name='PlayerName', local_id=2)
```

`path` is the zero-based **structural path of an object**, not an array value index.
The root is `[]`; its first child is `[0]`. `find` returns exact paths. Keep the
revision used to obtain a path, and refetch after conflict errors.

## JavaScript / TypeScript (Node)

```javascript
const base = 'http://127.0.0.1:46321';
async function request(path, body) {
  const response = await fetch(base + path, {
    method: body === undefined ? 'GET' : 'POST',
    headers: {'Content-Type': 'application/json', 'X-Red-XAI': '1',
      Authorization: `Bearer ${process.env.REDXAI_SESSION}`},
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  const result = await response.json();
  if (!response.ok) throw new Error(`${response.status}: ${result.error}`);
  return result;
}
const databases = await request('/v1/databases');
```

This protocol works with any language's HTTP client; the distributed package
contains a tested Python client, not a claim that SDKs for every language are tested.
An iOS app must use a trusted HTTPS service with restricted per-user credentials;
never distribute an administrative database key inside a mobile binary.

## Edits and types

Supported operations: `add_box`, `add_packer`, `remove`, `set_value`, `array_insert`,
`array_remove`, `table_set`, `table_remove`, `set_metadata`. Inspect `Store.edit` and
the tests for exact operation contracts; a generated OpenAPI schema is a later gate.
Use Red-XAI literal text in `value_source` to avoid loss of type information.

Typed JSON values are objects with `type` and `value`. Table entries preserve typed
keys. NELL is `{ "type": "nell", "value": null }`; a missing object is an API lookup
result/error, not that value. Native JSON numbers must not be used to round-trip
arbitrary precision database numbers.

A write with a stale revision returns **409 Conflict** and commits no partial edit.
Invalid data returns 400, missing/expired auth 401, denied scope 403, missing route or
database 404, overlarge body 413, and throttled authentication 429. Never automatically
retry a conflicting edit without fetching and reconciling the current document.
