# Scoped Hub UI rollout candidate

This candidate publishes the already running private Hub UI from immutable website release `c443ce7eb01c0a33261c049bc7071dbc90fd46d4` on Kamatera loopback14210. It is a partial UI release. It does not replace the account authority or complete the original account/email/optional-MFA repairs. The fresh-account prototypes are excluded. No production operation has been performed by these source changes.

The source helper defaults to inspection. Staging creates protected rollback/candidate files and performs real cloudflared syntax/route checks. Apply promotes only the new public completion asset, the original account SPA's index reference and the tunnel configuration. **It never restarts, reloads, starts, stops or enables a service.** Changing the tunnel file is not evidence that the running tunnel has loaded it. The root operator must separately obtain the action-time approval for the public switch, enable Hub boot startup if needed, restart the existing tunnel, and verify/rollback real browser behavior. No native, Host, account, CMS or support data is restored during code/config rollback.

## Exact routing scope

Only hostname `red-xai.com` receives five inserted, anchored path rules. The complete original configuration remains byte-identical after the insertion. `www`/other domains are untouched. There is no new general fallback to Node.

| Request | Destination and behavior |
| --- | --- |
| `/`, `/signin`, `/verify-email`, `/settings`, `/api-keys`, contact/community/legal pages, the named Products/News/Media/Brands pages, all 11 pinned product detail pages, `/api/quick-login` documentation | Existing private Hub14210; exact allowlist from `route_catalog()` |
| Exact `/host`, exact `/db` | Hub14210 workspace parent |
| `/host?embed=site` | Hub14210's existing gateway sends only the iframe document to Host14202 |
| Every `/host/*`, including `/host/`, RPC/node/agent APIs, settings, assets, streams/upgrades | Original direct Host14202 rule remains authoritative. Node does not filter agent/protocol headers |
| `/v1`, `/v1/*`, `/ui`, `/ui/*` | Original direct native DB17790; iframe source is `/v1/console/?embed=site` |
| `/signup`, `/activate`, `/tos`, `/api/accounts/*`, `/api/rpc/*`, `/assets/*` and unknown legacy paths | Existing4200 authority/SPA and unknown fallback remain unchanged |
| Both `/redxai-connect` and `/redxai-connectx` roots/children | Existing direct17774 Quick Login protocol, including UX script |
| API domain, including native/auth/unknown routes | Exact original rules; no inserted rule can match its hostname |
| Exact approved browser files, versioned `/site-assets/<64hex>/<approved-file>` | Hub14210; no backend source/dependency files or generic asset wildcard |
| Exact existing `/api/site` operations plus constrained post/media IDs | Hub14210. Its original identity, owner/MFA, CSRF, audit, private-media and tenant-preference checks remain active. Unknown `/api/site` paths keep the original fallback |
| `/vortex` and `/cloud-pc` Hub pages | Existing c443 access links/release documentation only; `/vortex/*` and external Vortex/Cloud PC services are not rerouted or repaired |

The audit is grounded in the pinned `app.js`, `server.mjs`, `gateway-proxy.mjs`, `site/pages.js`, `site/api.js`, `site/session.js`, `index.html` and catalog. The manifest hashes all22 selected runtime files. Every enumerated UI page, browser file, versioned file and site API is checked by the actual cloudflared rule matcher during staging/application, plus negative/preserved Host, native, Quick Login, account, API-domain and unknown examples. A mismatched old route blocks application.

## Guarded account completion change

The existing helper now additionally accepts the actually live navigation-fixed bundle with SHA256 `8144e37d5067e1bd3a4b7807bc34c9963ec4eef9adb2053bb3a5ec7403052b7b` (441954 bytes). The rollout installer accepts only that current input. Only the exact `tg` success-completion hook changes: invalidate the current-account query and use `window.location.replace(Lx())` instead of SPA-only history replacement. `Lx()` remains the existing return-path helper. The hook runs only when existing account flows call their existing completion callback; it neither sets a session nor skips activation, enrolled factors or owner MFA.

Output:441942 bytes, SHA256 `773b99e3be5d4405ac043c3f11e5e0af50c4aa63aebfaa33aa593733aaf11ead`, new immutable asset `/assets/index-hub-completion-773b99e3be5d.js`. The unchanged prefix is316602 bytes and suffix125232 bytes. Whole-bundle comparison against the independently downloaded live8144 input passed without executing the bundle. Registration fields, validation, Terms, activation and MFA code and the prior native Link fix remain byte-identical. The old asset remains installed. The legacy index changes exactly one module reference; an unexpected SRI attribute is a refusal requiring review.

## Inspection, staging and application

Place the reviewed `rollout.py` and exact `manifest.json` together in a protected root-owned tools directory. Keep the manifest bytes unchanged: SHA256 `3ebaf2cf1f59304a60d347a90ba99987bd3d64658b5c825a941f17d85aa4665c`. Run the Linux tests first. Independently inspect the actual current tunnel config and the actual primary SPA origin index from the prior native-navigation repair. Record only their absolute paths and SHA256; do not print configuration, process arguments, credentials or full unit definitions. No real config/index hash is guessed or supplied by this candidate.

The repaired production SPA index is exactly `/opt/redx/lib/panel-dist/index.html`, observed by the root operator as root:root0644. That exact path is accepted in addition to the existing `/srv/red-xai` origin scope; no general `/opt` prefix is allowed. The original repair rollback remains under `/srv/red-xai/backups/native-navigation-20261103`. These path/metadata observations do not substitute for freshly verifying its actual bytes/hash before inspection and staging.

The existing application's four directories have a narrowly defined metadata exception: `/opt/redx` UID999/mode0750; `/opt/redx/lib`, `/opt/redx/lib/panel-dist` and its `assets` child UID999/mode0755. Before using this exception, the helper requires `redx-panel.service` User/Group `redx`, the OS `redx` identity UID999/GID987, and all four real/effective/saved/filesystem UID/GID fields of its active process to match. No other UID999 directory is accepted. Config, tools, immutable Hub runtime, stage/backups and other ancestors still require root ownership and no group/other writes. The selected legacy index, old JS and new JS remain root:root files, with no-follow opens and the original digest/mode checks. The helper does not chown/chmod any existing application directory.

Because the application owns those ancestors, the helper pins each directory with root-anchored `openat`/`O_DIRECTORY|O_NOFOLLOW`, retains the directory descriptors for the whole operation and compares their inode identities on later checks. Selected file reads/creation, metadata changes, atomic replacement, cleanup and directory fsync use those descriptors. File metadata changes require the exact newly created inode and use file descriptors. A directory replacement/symlink swap therefore cannot redirect a privileged read or write to a different tree; an observed path change causes refusal without rebinding. This does not prevent the existing service account from changing its own UI directory independently. Unknown intervening changes require inspection rather than blind restoration.

Use those actual four values in this inspection command; placeholders deliberately cannot pass:

```sh
python3 -I -B /srv/red-xai/stage-tools/scoped-hub-ui/rollout.py --config /ACTUAL/config.yml --config-sha256 ACTUAL_CONFIG_SHA256 --legacy-index /ACTUAL/index.html --index-sha256 ACTUAL_INDEX_SHA256
```

Inspection checks Linux/root, canonical non-symlink non-writable paths under the narrow ownership policy above, exact original hashes/assets, one module reference, all22 runtime file hashes, exact current private Hub process/listener, native CMS health/capabilities and eight live service baselines. The current config's permissions remain unchanged; staged copies/backups are owner-only. It verifies the config is explicitly selected in the running cloudflared process, and the reviewed matcher is the same installed executable as that process. Process arguments/output remain private. Relative/default-only/remote-managed configs are refused. A per-rule origin/Access/TLS policy, YAML merge or unexpected ingress structure blocks this simple insertion; bypassing an existing per-rule Access policy would be an actual security regression and requires separate source review. Global origin policy remains byte-identical and inherited.

If inspection succeeds, repeat the same command with `--stage`. It creates a unique root0700 directory under `/srv/red-xai/backups/scoped-hub-ui-UTC-UUID`; originals, candidates, selected unit definitions and receipt are exclusive0600/fsynced files. It runs `cloudflared tunnel --config <candidate> ingress validate` and the complete rule matrix before declaring the stage valid. This follows Cloudflare's [configuration validation and route-testing commands](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/do-more-with-tunnels/local-management/configuration-file/). No active file or service changes during staging.

The staging result identifies whether the Hub unit still needs boot enablement. Publishing a disabled unit would leave Hub routes down after reboot. After action-time review/approval, the root operator can enable only `redx-site-cms-private.service` if required; it is already running. Then apply the selected actual stage directory:

```sh
python3 -I -B /srv/red-xai/stage-tools/scoped-hub-ui/rollout.py --apply-from /srv/red-xai/backups/ACTUAL_SELECTED_STAGE_DIRECTORY
```

Application requires unchanged service process baselines, exact original file identities/bytes/xattrs and a boot-enabled Hub. It recomputes the staged transforms instead of trusting self-consistent edited receipts, reruns the real cloudflared validation/matrix and acquires an exclusive `/run/redx-scoped-hub-ui.lock`. It installs the immutable asset first, then atomically promotes the index and tunnel file with their original owner/group/mode/xattrs, file and directory fsync, fresh readback and per-file rechecks. Mutation tracking is armed before replacement, including post-replacement fsync errors. Failure restores only recognized unchanged/candidate files; an intervening unknown edit is preserved and reported for manual review. A killed process leaves a lock for inspection; never remove an unverified live/stale lock automatically.

Successful application reports `tunnelRestartRequired:true`, `tunnelRestarted:false`, `publicBehaviorVerified:false`. Root must separately perform the approved tunnel restart and real signed-in/anonymous Hub, Host/DB parent/iframe, native/API/Quick Login and registration/MFA-completion checks. A restart can interrupt long-lived connections. Confirm real owner CMS draft/media/publishing permissions using the original owner session; the synthetic maintenance fixture did not prove this HTTP/browser path. Keep normal-user A/B and owner/resource verification marked outstanding until actually tested.

## Rollback

```sh
python3 -I -B /srv/red-xai/stage-tools/scoped-hub-ui/rollout.py --rollback-from /srv/red-xai/backups/ACTUAL_SELECTED_STAGE_DIRECTORY
```

Rollback validates the original config, verifies the live selected config path and recognized file transitions immediately before each promotion, restores the original config/index metadata and bytes, and reports that root must restart the tunnel separately. It is resumable after an interrupted recognized rollback and refuses unknown intervening changes. The unused new immutable JS asset remains. It never restores an older database, media, audit/support capture or environment. It does not disable the running Hub or undo boot enablement; those are separate reviewed service actions if wanted.

This code/config candidate is acceptable as a scoped UI release only after the actual server's checks and action-time approval. It cannot make the whole site complete: email activation/delivery, optional normal MFA, account deletion, checkout, GUI installers, original-account recovery/client parity, normal-user A/B isolation/billing/resource acceptance and real live CMS publishing remain separate unfinished work. Existing production account4200 and enrolled/owner MFA remain authoritative throughout.

Local Windows validation: ten pure route/transform/path/identity tests passed; eight Linux file/xattr/fault/tamper/directory-descriptor tests await Linux for this correction. Coverage includes exact UID999 directory policy, refusal of unrelated paths and nonroot selected files, mismatched service/process IDs, real ancestor symlink replacement, a check-to-open assets swap and inode replacement. Linux race fixtures use a temporary tree under the test user's UID with metadata-policy mocks; the exact production UID/mode policy is separately tested and the real main/preflight enforces it. Six account-completion tests previously passed including the real8144 whole-bundle comparison. The root operator ran the prior13 tests successfully on Kamatera before this correction; that does not establish the new18-test result. No real tunnel configuration was read/validated and no installer was run on Kamatera by this source task. The22-file runtime manifest and its SHA remain unchanged.
