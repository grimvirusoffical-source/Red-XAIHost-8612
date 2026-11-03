# Private CMS maintenance evidence

This probe exercises storage as the existing scoped CMS service. It neither
calls the website owner API nor pretends to be a normal or owner account.
The audit actor is explicitly `maintenance-service:cms-persistence-probe:v1`,
with no role or owner flag. Its two preference keys begin
`maintenance_fixture_`; normal account preferences use SHA256 account keys,
so these fixture entries do not represent accounts. The only post is a clearly
labelled unpublished synthetic draft. All writes use the unchanged
`RedXCmsStore`, native Box scalar API and existing dedicated `website_cms` key.

The fixed production CLI is Linux-only, UID986/GID976. It reads the private key
through the existing store and validates the existing exact scope. It imports
only the two digest-checked c443 CMS modules. It cannot select another DB,
service credential, upstream, support path, actor or published status. It never
prints cookies, keys, account records or raw failures.

Before seed, the private gateway must be deliberately stopped and its only
local CMS writer drained. The probe also requires connection refusal on
127.0.0.1:14210 and acquires the existing writer lock normally. It never deletes
a pre-existing lock. Native CMS revision must be0 with no post, media or
preference state. A previous receipt, orphan support media or a previous probe
audit actor blocks seeding. If already populated, do not clear or replace state
to make this probe pass; use another reviewed acceptance method.

Install the exact reviewed probe outside the immutable application release in
a new root-owned public code directory (mode0755, script0644), for example
`/opt/redx-cms-maintenance-REVIEWED/cms-maintenance-probe.mjs`, after verifying its
Git source digest. Do not change permissions on the broader stage-tools or
recovery directories. No key, environment, data or cookie belongs in that code
directory. Replace `REVIEWED` below with the actual chosen code directory.

```sh
systemctl stop redx-site-cms-private.service
systemctl is-active redx-site-cms-private.service
```

The second command must report `inactive`. Separately confirm the existing
customer units stayed active; no native DB, account, Host or tunnel service is
stopped. A force-kill or leftover writer lock is a blocker for operator review,
not permission to remove a lock.

Run the seed as the already dedicated service user, with a clean environment:

```sh
runuser -u redx-site -- /usr/bin/env -i PATH=/usr/bin:/bin NODE_ENV=production /usr/bin/node /opt/redx-cms-maintenance-REVIEWED/cms-maintenance-probe.mjs seed --private-gateway-stopped
```

Success returns only evidence hashes and booleans, including
`writerLockReleased=true`. The CLI independently requires `lstat(writer.lock)`
to report `ENOENT` after close; a suppressed unlink error in the existing store
cannot be reported as successful release. It writes one actual decoded/re-encoded WebP, the
draft and two distinct synthetic preference values through four native CMS
mutations. It verifies the native sequence readbacks, reads the WebP back and
decodes it, and verifies ten audit events under the hashed service actor. A
mode0600 `maintenance-probe-v1.json` receipt in the support directory binds the
CMS state, media and audit prefix hashes. Extra legitimate appended audit
events are permitted later; changes to that preserved prefix fail verification.

On any failure, preserve all partial native/support evidence. The command does
not automatically delete fixtures, truncate logs, reset revisions, overwrite
receipts or restore old data. Never report a partial write as successful.

Restart the private application normally, observe native CMS health, then stop
only that private application again before the independent readback:

```sh
systemctl start redx-site-cms-private.service
curl --fail --silent http://127.0.0.1:14210/api/site/health
systemctl stop redx-site-cms-private.service
runuser -u redx-site -- /usr/bin/env -i PATH=/usr/bin:/bin NODE_ENV=production /usr/bin/node /opt/redx-cms-maintenance-REVIEWED/cms-maintenance-probe.mjs verify --private-gateway-stopped
```

`verify` performs no native or application data mutation. It normally acquires
and releases its own writer lock and requires the original revision4 state,
draft, two fixture preferences, media and preserved service audit prefix. A new
legitimate application mutation causes this intentionally fixed fixture proof
to fail; it must never be overwritten with the earlier snapshot. After the
readback, follow the coordinated native/support backup and restore procedure
while CMS remains quiescent, then restart the private gateway normally. The
direct CLI always targets the original approved service; testing a restored
host requires a separately reviewed isolated verifier, not changing this CLI's
fixed upstream to an arbitrary URL.

Evidence limits are explicit in every successful probe result:
`websiteOwnerApiVerified=false`, `normalAccountIsolationVerified=false` and
`backupRestoreVerified=false`. A clean restart and native sequence readback do
not establish application-consistent backup restore or power-loss behavior.
Existing owner/MFA publishing APIs, real users' preferences and A/B resource
isolation still need independent acceptance through normal website sessions.
These synthetic entries remain evidence; any later removal should use the
normal authorized application process, without purging customer data.

Local validation for this exact probe: seven refusal/readback tests passed with
the real pinned image codec, including simulated close-unlink failure; the
complete website suite passed68 tests.
The actual unchanged native851 fixture in raw-empty/effective-group mode
also passed maintenance probe seed, native daemon restart, native offline
backup/fresh restore, restored key-record authentication and copied-support
readback. That disposable fixture made no production requests and does not
establish those outcomes on Kamatera.
