# Hub deployment record

This is a deployment checklist for the existing Kamatera host and cloudflared
tunnel. The example files do not install a service, provision privileges, change
routing or establish that production is ready. Retain the original account,
Host, DB, Quick Login and API-domain deployments.

## Private staging

1. Record the exact release commit and source archive digest. Install it into a
   new immutable directory, without overwriting the live application. Install
   the pinned website dependency lock with the supported Node runtime. Run the
   website test suite on that runtime, including the native CMS lifecycle tests.
2. Create a dedicated service identity, private support/media directory and a
   private key file. Verify the service identity can read the release and key,
   write only its support directory and contact the existing loopback services.
   The unit and environment examples require substitution and inspection. Run
   `systemd-analyze verify` on the final unit before enabling or starting it.
3. Through the existing authenticated owner DB tools, create a dedicated system
   database. Keep its name separate from user databases and account stores. Issue
   one key with `scope=write`, the exact new database, and
   `prefix=site-cms.red-xai.db`. Store the newly issued key directly in the private
   file; never put it in shell history, terminal output, URLs, source or reports.
   The key must have neither owner nor host-admin privileges. Other applications
   and normal users must not receive this key or write the CMS namespace.
4. Use the existing owner-authenticated `GET /v1/admin/config` to inspect only
   `config.engine_durability` from the running native service. Record `sync` or
   `group`, the DB deployment revision and observation time. The declared CMS
   environment setting does not configure the native engine and is not evidence
   of its running durability. Do not publish the entire configuration response.
   An `async` or unknown value blocks enabling persistent publishing.
5. Start the single Hub service on `127.0.0.1:14210`, with no public ingress
   change. Confirm health reports `native-redx-single-writer`; an unconfigured
   CMS is sufficient only for a limited read-only shell preview. Confirm the
   scoped key's runtime identity is the exact database/prefix/write scope and
   `host_admin=false`, `owner=false`, `role=leader`.
6. Verify the independently authenticated owner's canonical immutable ID and
   completed MFA evidence. Normal-user headers, display names and emails do not
   establish owner privilege. A session contract discrepancy blocks publishing;
   never add a guessed owner ID to get past a disabled editor.

## Persistence and restart acceptance

Using private direct-origin requests with the real website session, save an
unpublished synthetic post and two synthetic users' distinct preferences.
Confirm only the verified owner can fetch drafts, no normal user can publish,
and neither user's preferences appear in the other's response. Record IDs and
hashes, without cookies or account secrets. Stop the Hub through systemd and
confirm it exits cleanly after its pending write/audit operations complete.
The native DB remains the sole engine writer; do not open its live directory
with a second engine process.

Restart the same Hub and read the saved data back. Confirm the previous
`writer.lock` was removed and the new process acquired its own lock. Then take
the existing native backup plus a coordinated support/media/audit backup,
restore both into separate protected staging paths and verify the saved post,
preferences, media and audit entries there. An HTTP sequence readback, synthetic
wire fixture or clean restart alone does not prove filesystem durability or
application restore.

SIGTERM/SIGINT drain HTTP handlers and queued CMS writes before releasing the
writer lock. The 30-second network deadline closes HTTP/upgrade connections;
handlers still finish before unlocking. If systemd must force-kill the process,
the lock remains intentionally. Before removing a stale lock, verify its
recorded process is absent, the service is stopped, and no second gateway or
client writes that namespace. Preserve the lock as incident evidence, then
recover under a protected maintenance procedure. Never unlink a live lock or
configure an automatic lock-removal command.

## Public cutover

Required account login/activation/optional-MFA tests and live normal-user A/B
Host/DB/resource isolation tests remain separate release gates. Legal drafts,
unpublished installers and central checkout must retain their honest unavailable
states until those integrations pass their own acceptance. Vortex and Cloud PC
remain ordinary links to their existing services; their separate repairs are
deferred. Keep their own authentication and browser certificate checks intact.

Preserve a protected copy of the exact current tunnel file, affected units,
private configuration, application-consistent native/support backup and legacy
asset/index. Stage the complete ingress with only the reviewed main-site rules
changed. Preserve API-domain services, both Quick Login prefixes and unknown
legacy compatibility routes. Validate the staged tunnel configuration. Install
the digest-checked legacy success-completion asset and atomic index change in
the same rollout; registration and account API bodies stay with their existing
authority. Validate direct origin and public login/Hub/Host/DB paths after the
switch. A healthy shell alone does not pass these checks.

Rollback restores the previous routing, unit/executable and legacy index. Keep
the current CMS and customer data: do not overwrite newer writes with a stale
snapshot. Record the final deployed revisions and capture the updated server
state into the encrypted private recovery process after acceptance.
