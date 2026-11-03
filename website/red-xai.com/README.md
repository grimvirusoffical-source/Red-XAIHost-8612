# Red-XAI website gateway

The first-party Hub and shared Host/DB shell run on the existing Kamatera server behind the existing cloudflared tunnel. This package adds no replacement account authority and no external website hosting platform.

## Runtime and verification

Node.js 20.9.0 or newer is required. Install the exact dependency lock with `npm ci --ignore-scripts --workspaces=false`; run `npm test` from this directory. The real raster codec is tested, including supported formats, metadata removal, malformed input and pixel limits. Run the completion-hook tests separately with `node --test ../../scripts/primary-account-landing.test.cjs`.

`node server.mjs` binds only to `127.0.0.1:14210`. `/api/site/health` fails closed when required frontend files/imports are missing. It separately reports whether CMS storage is configured; a healthy shell alone does not establish fully functional publishing, accounts or billing.

The isolated signed-out preview is documented in [PREVIEW.md](PREVIEW.md). It cannot sign users in, proxy services or write production data.

## Account and protocol boundaries

- `/signup`, `/activate`, `/tos`, `/api/accounts/*`, `/api/rpc/*` and legacy assets retain the existing service on 4200. Registration fields, validation, Terms and email verification are unchanged.
- Website sessions are revalidated against the fixed private `http://127.0.0.1:4200/api/accounts/me`; browser-supplied IDs, roles and email addresses do not establish an identity.
- Normal completed password sessions are accepted. Owner publishing requires an immutable canonical ID in the private deployment allowlist, the authority's owner role and a completed MFA method together.
- Both existing `/redxai-connect` and `/redxai-connectx` prefixes retain the Quick Login service on 17774 and its required client headers/cookies. No developer-client protocol is replaced.
- `/host?embed=site` and `/host/*` reach the Host service on 14202. `/host` opens the shared shell. `/v1/*` and `/ui/*` reach the native DB service on 17790. Native API keys remain optional developer credentials.
- Embed routes require the reviewed Host build and DB console candidate. Standalone account/Quick Login interfaces keep their own security policies.
- `/vortex` and `/vortex/` open the Hub's release view through the existing fixed Host release reader; no account cookie is sent to that reader or GitHub. Existing deeper `/vortex/*` paths retain their legacy authority. `/cloud-pc` stays unavailable until its existing separate deployment, certificate, canonical-account and tenant authorization contracts have been verified; no desktop or access grant is assumed.

The digest-pinned `scripts/primary-account-landing.cjs` changes only the legacy `tg` success-completion hook from an in-place history update to a top-level navigation through its existing return-path helper. It accepts only SHA-256 `81b28895ccc85fb138bfd6a489c0aa6a5cd539c01b18c8011825ab00468f3138` and writes a separate output file exclusively. Reviewed output is `f531bead2e2fd314ea1fc5a05e19ae248695d603d57bc1a985eb04018832c327`. Every byte outside that hook, including registration and MFA components, remains unchanged. Apply it only with the new Hub cutover and retain the original asset/index for rollback. This presentation patch does not make mandatory backend MFA optional.

## Native CMS storage

Production JSON storage is forbidden. New posts and preferences use the existing RedX path API and custom Box string values in a dedicated system database, without changing engine code, BoxQL or file formats. Support media and append-only audit files remain in a private local support directory.

Private service configuration must supply:

```
REDX_SITE_STATE_DIR=/private/site-support
REDX_SITE_CMS_DATABASE=<dedicated-system-database>
REDX_SITE_CMS_KEY_FILE=/private/site-cms.key
REDX_SITE_CMS_DURABILITY=sync
REDX_SITE_CMS_SINGLE_WRITER=true
REDX_WEBSITE_OWNER_ACCOUNT_IDS=<existing-canonical-owner-id>
REDX_SITE_TRUST_CLOUDFLARED=true
```

Do not commit real values or key files. The key must have exactly `write` scope, the dedicated database and prefix `site-cms.red-xai.db`, without host-admin or owner privileges. Runtime validates scope on every read/write. A local writer lock prevents two gateway processes sharing the support directory. Acknowledged sequence readback verifies the written value; it is not an independent fsync or replication guarantee. Before deployment verify the actual native service uses `sync` or `group` engine durability. The declared environment value alone does not prove it.

There is no distributed CAS/HA promise. Use one gateway writer and do not grant additional clients write access to this namespace. A lost CMS record or unexpected writer fails closed. Recover a stale lock only after verifying its recorded process is absent; never remove a live writer's lock. Capture native database backups plus support/media/audit files together and perform an isolated restore check.

On Linux, media files and audit entries flush their containing directory as well as their file before acknowledging the operation. This protects new directory entries; a failed flush still returns an error. Windows fixtures do not establish Linux filesystem durability.

`node native-cms-integration.mjs /absolute/pinned/RedX-checkout` runs an actual native-engine integration against the exact unchanged RedX revision `8512e1704e589d24535e08082c3ab9f832d7655d`. It compiles a disposable loopback host, provisions an isolated scoped fixture key, writes a draft and two queued preference changes, decodes/re-encodes and stores real WebP media, restarts the native host, then performs an offline native backup and fresh restore plus support/media/audit copy checks. Ephemeral keys remain in private child pipes/files and are never logged. The checked source remains unchanged and all generated data stays in a bounded temporary fixture. CI runs this on Linux. It does not contact production, prove account integration or replace production provisioning/restore acceptance; it is not a power-loss test.

SIGTERM/SIGINT now stop new connections, drain existing HTTP handlers and queued native writes, and release the local writer lock only after that work finishes. A forced kill leaves its lock for operator review. The lifecycle suite verifies a pending mutation survives a process restart; Linux CI uses a real SIGTERM, while Windows exercises the registered shutdown event because Windows termination does not deliver POSIX signals. See [the service examples and detailed deployment acceptance](deploy/CUTOVER.md) before staging.

## Features that remain explicitly unavailable

Central checkout cannot be enabled by environment URLs: the actual app-specific commerce and legal/readiness contracts require separate integration. No price or subscription grant is fabricated. Account deletion and verification resend report unavailable until the original account authority exposes a verified contract. New legal pages are review drafts and do not replace the existing accepted registration Terms. GUI CLI installer downloads stay unavailable until real platform assets are built, tested and published.

The legacy account binary currently lacks matching source in connected repositories. Optional normal-user MFA, resend/activation recovery and deletion must be implemented in that authority using its actual contracts, then tested; pending enrollment tokens must never be converted into authenticated sessions by this gateway.

## Reversible Kamatera rollout

1. Inventory existing services, exact binaries, ports, data ownership, configuration and every cloudflared ingress rule. Keep account/CLI/Quick Login/Secrets API-domain routes intact. Confirm no other operator is changing this host.
2. Take reversible protected backups of affected application data, service definitions, private environment/configuration, legacy asset/index and tunnel configuration. Native backup and isolated restore checks must pass before any migration; a live file copy is not an application-consistent backup. Keep existing customer/owner identities and data unless an explicitly reviewed migration requires otherwise.
3. Install candidate website, mounted Host and DB console into new immutable revision directories. Do not overwrite running directories or data. Provision and validate only the dedicated least-privilege CMS namespace/key, actual durability and private filesystem permissions.
4. Start candidates on private loopback ports. Test registration field/rule parity, activation/relogin, optional normal MFA, enabled MFA enforcement, normal users A/B isolation, owner audit access, drafts/media, Host/DB return navigation and existing CLI/Quick Login/Secrets clients. Required account-contract failures block the full production switch.
5. Cut over only the reviewed main-site routes in the existing cloudflared configuration. Install the completion asset with its checked hash and atomic index replacement during the same window. Validate both origin and public results, including cache behavior. Keep previous services/directories available.
6. Roll back by restoring the saved tunnel configuration and previous legacy index, returning affected service paths to their preserved revision directories. Do not restore stale application data over newer writes without a reviewed reconciliation.
7. Capture the final server state encrypted for the private `red-xai-kamatera-recovery` repository/release assets. Keep the recovery identity off-server and out of Git. Verify transfer hashes, full decryption and actual application restore. Until that happens, record the recovery archive as uncaptured/unverified.
