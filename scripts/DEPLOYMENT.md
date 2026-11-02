# Existing Host deployments

Do not apply `0003_account_tenants.sql` directly to a legacy panel database
without the preceding authentication schema. Do not run `db:push` against the
only production copy. Names do not establish ownership.

1. Build this exact release in a separate directory. Keep the existing release,
   configuration and service running. Use a private bind address, another port,
   separate SQLite data and `REDX_AUTO_LOCAL_NODE=false` for staging.
2. Create an empty reference database with this release's Drizzle schema. The
   self-host preparation script can prepare a fresh staging directory. Verify
   the resulting database contains no users, projects or seeded resources.
3. Run the additive migration against a consistent source backup, writing a NEW
   output file. Example, with private absolute paths substituted:

   ```sh
   python3 scripts/stage-database.py --source /private/legacy-backup.db \
     --reference /private/empty-reference.db --output /private/new-stage.db \
     --ownership /private/verified-ownership.json
   ```

   The optional ownership manifest is a JSON object of table names to maps of
   immutable row IDs to verified account subjects. The reserved `owner` tenant
   requires independently verified owner provenance. Do not derive assignments
   from display names, email similarity, or possession of server-console access.
   Keep this manifest on the server, outside published artifacts and logs.

   Without a complete map, the output remains a staging copy with
   `__unassigned__` rows and exits 2. A successful exit proves the checks in the
   report; it does not authenticate the supplied ownership assertions. Re-run
   from the ORIGINAL backup into another new output after correcting a map.
4. Every original field and row is hashed and compared after the additive
   migration. Unknown legacy tables remain intact. Existing account/password
   records and immutable resource IDs are preserved. The source opens read-only;
   committed WAL content is copied through SQLite's backup API. Outputs use
   exclusive creation and mode 0600. Failed outputs must never be promoted.
5. Independently verify the existing credential encryption secret and decrypt
   compatibility without logging plaintext. Preserve node tokens, provider
   credentials, device links, sessions and callbacks. Missing credential or
   bundle namespace compatibility blocks release rather than renaming IDs.
6. Test two synthetic accounts and an authenticated owner in staging. Verify
   sign-in, sign-out, switching users, revocation, scope of each resource and
   background worker, and restoration. This tool does not migrate subscriptions,
   external billing state, native RedX/BoxQL files or Cloud PC state.
7. Before cutover, account for writes since the snapshot. Use a short,
   coordinated write pause or an independently validated synchronization step,
   take a final consistent backup and repeat migration/validation. Retain a
   code/configuration rollback. Do not overwrite intervening customer writes
   with an old database during rollback.

The new Host session and the native RedX/Cloud PC sessions have distinct trust
boundaries. Do not share an admin key, remove anti-framing protection or treat a
frontend navigation change as backend session integration.

## Mounting Host beside an existing website

Set `REDX_BASE_PATH=/host` and `VITE_BASE_PATH=/host`, and set the three public
URL settings to the full HTTPS URL including `/host`. Rebuild the frontend.
Route only `/host` and `/host/*` to the new Host service, without stripping the
prefix at the proxy. Host strips it internally for API dispatch, restores the
public prefix for authentication handlers, and uses a separate `redx-host`
cookie namespace. The original website's `/api/accounts/me`, account backend,
Store, News and `/settings` must keep their original routing. An unauthenticated
request to the new Host is still subject to Host authentication and tenant checks.

The mounted Host's Hub link returns to the original site. Standalone deployments
leave the mount settings empty and retain their existing routes and cookie names.
Account sign-in sharing requires a separately verified session integration;
mounting a service does not authenticate a website cookie by itself.

Local owner creation requires `REDX_ALLOW_OWNER_BOOTSTRAP=true`. Use this only
for an explicit private first-owner setup, then disable it before publishing.
The preparation script defaults it on for a local-only install and off when a
public URL is configured. Existing local owner passwords continue to work with
registration disabled. Red-XAI Accounts sign-in does not use local registration.
