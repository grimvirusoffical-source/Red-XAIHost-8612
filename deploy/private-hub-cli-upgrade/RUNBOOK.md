# Private Hub CLI UI upgrade procedure

This is a source-only procedure for a separately reviewed future maintenance operation. The offline planner validates supplied artifact bytes and sanitized observations, then emits a reviewable JSON proposal. Its unit fragment is preview text inside that JSON: no standalone drop-in file, apply script or service-management command is emitted. It does not collect live evidence, read the live paths named by observations, install files, change routing or grant approval. No operator step below was performed by this document's author.

The existing c443 rollout remains a different, pinned operation. Do not change its manifest to accept this candidate, replace files inside c443, or describe this mixed package as all 22 files from ce44086.

## Exact candidate

| Item | Required value |
| --- | --- |
| Base source | `c443ce7eb01c0a33261c049bc7071dbc90fd46d4` |
| Overlay source | `ce4408632446225462a4f9bdce7f365b55822fe2` |
| Runtime selection | 22 regular files: 19 exact base files, plus `catalog.json`, `cli-installation.md` and `site/catalog.js` from the overlay |
| Archive | `runtime.tar.gz`, 327728 bytes, SHA-256 `56496195081d28be14b9be6d2fd79f77272de8280fcf67347f155544b94056c3` |
| Provenance | `provenance.json`, 8353 bytes, SHA-256 `5cccf128eda2bffab96d6f7b5cc2c4a261ea854033a00e29794b22a7e5e63dc0` |
| Detached checksum file | SHA-256 `0434e432fe7982fed8f5bcf6857dece283a0bcc36ea234b1b80fb86ce92b76ac` |
| Original browser asset revision | `29ae45c806fe8be49b9b5a61128ba8749b1a4e3e72b12e0f98f37c96904c2de9` |
| Candidate browser asset revision | `3035a63b01e2519806befd2a835f54f8a91924a4aa890234672d804a4be0a1f4` |
| Existing application directory | Historical expected value `/opt/redx-site-c443ce7/website/red-xai.com`; fresh identity/hash verification is required |
| Candidate application directory | `/opt/redx-site-cli-preview-3035a63b/website/red-xai.com`, a **new immutable directory**; fresh absence must be declared before staging, and created identities remain UNVERIFIED until checked |
| New unit drop-in | `/etc/systemd/system/redx-site-cms-private.service.d/zzzz-redx-cli-preview-3035a63b.conf`; fresh observed absence is required |

Base `package.json`, lockfile, backend, session, CMS store and lifecycle code remain unchanged. The ce package's additional maintenance-test script is audited but excluded. Dependencies, configuration and application data are not packaged. This does not change the RedX engine, BoxQL/formats, CLI client packages, Quick Login, Secrets or existing account authority.

The only allowed unit changes are the observed effective `WorkingDirectory` and the supported single `ExecStart` command's application script path. Preserve the observed executable and every other effective unit property, fragment, drop-in, environment source and protected data reference. A new directory that needs different hardening/permissions is outside this proposal.

The planner accepts only this manifest's `redx-site-cms-private.service`, active `Type=simple`, `redx-site` user/group, process UID 986/GID 976 and a single two-argument Node command. Its first argument must be the observed protected absolute Node executable; the second must be `server.mjs` or the exact base directory's absolute `server.mjs` path. Wrappers, prefixes, extra arguments, multiple commands and other unit forms are refused rather than generalized.

## 1. Obtain a fresh baseline

An authorized operator must collect a coherent, timestamped baseline on the actual host using a separately reviewed observation procedure. Keep unit/configuration records and credential/environment metadata references in protected recovery storage, outside Git and public assets. Any separate capture of sensitive contents belongs to its own approved private recovery workflow. Supply only the planner's sanitized evidence fields. Never include passwords, tokens, cookies, key values, environment values, user records or private TLS/recovery keys.

`observedAt` must be UTC. Evidence older than 300 seconds, or more than 30 seconds in the future, is refused. Passing that window does not establish live freshness: recheck actual identities at action time. The planner receives provided metadata only; it does not open or hash the live key/environment/unit/tunnel/executable paths named in the JSON.

Record these observations and recheck them immediately before any future maintenance action:

| Area | Required fresh evidence |
| --- | --- |
| Host/capture | Actual host/boot identity, Linux environment, UTC capture interval and operator evidence identifier |
| Unit definition | Exact Hub unit name, active/enabled state, fragment path/hash, ordered drop-in paths/hashes, and protected original bytes/metadata for rollback |
| Effective unit | Exact current `WorkingDirectory`, supported single `ExecStart` executable/argv, service user/group and the preserved effective-property fingerprint |
| Lifecycle/hardening | Type, auxiliary start/stop commands, drain/stop timeout, kill mode, restart policy, filesystem/namespace restrictions, umask and all other protections; omitted properties are not assumed harmless |
| Process/listener | Unit cgroup, main PID/start identity, executable identity/hash, effective UID/GID, actual listener address/port and sole CMS-writer ownership |
| Current code/dependencies | `currentRuntime.directory` identity at the exact c443 base directory, `currentRuntime.runtimeFiles` equal to the complete pinned 22-file base SHA-256 map, and `currentRuntime.browserAssetRevision` equal to the original revision above; also Node executable path/version/hash, retained lockfile and dependency provenance |
| Routing/exposure | Actual active cloudflared executable/config selection and hashes, applicable hostname/path/first-match and Access/origin/TLS policies, and actual public/private exposure of port 14210 |
| Environment | Existing environment-file paths, stat identities, provided SHA-256 metadata and selection/order; no contents/values in the proposal |
| CMS key | Existing protected path, regular-file/no-link stat identity and provided SHA-256 metadata; key value withheld and scope verification retained separately |
| CMS/support | Unchanged `website_cms` and `site-cms.red-xai.db` prefix configuration, actual support/media/audit path identities, current CMS revision and writer-lock/process relationship |
| Other services | Selected account, native DB, Host, Quick Login and other customer-service PID/start/restart and protocol/health baselines |
| Website accounts | Existing owner/session/enrolled-MFA proof from the account authority and normal A/B test identities; root console access establishes none of these website roles |

Missing, stale, contradictory, unsupported or unknown observations are **UNVERIFIED** and block use. The offline planner checks supplied shapes, pins and consistency; it cannot authenticate the observer or certify live currentness. A timestamp, checksum or declaration alone is not that proof.

The JSON accepts only `schema` and the manifest's nine evidence groups: `observedAt`, `unit`, `currentRuntime`, `process`, `nodeExecutable`, `configuration`, `listener`, `routing` and `newRelease`. Keep additional host/boot, lifecycle, backup, account and acceptance records separately protected; do not add unsupported fields. File/directory stat identities contain `path`, `kind`, `device`, `inode`, `uid`, `gid`, `mode`, `size`, `mtimeNs` and `symlink:false`. The schema additionally requires provided SHA-256 metadata for unit/drop-in, Node, tunnel, key and environment files. Any collection or authorization for that metadata belongs to the separately reviewed private operator workflow; the planner neither collects it nor receives the underlying key/environment contents.

Key/environment paths must be under `/etc/red-xai/` with the protected ownership/modes required by the schema. Support may remain at the exact documented `/var/lib/red-xai-site` path or an accepted existing path under `/srv/red-xai/` or `/var/lib/red-xai/`. Acceptance still requires fresh declared identities, ownership and modes; the documented spelling is not live verification. Its `media`, `audit.jsonl` and `writer.lock` identities must remain linked to that same support directory. The special case does not accept similarly named siblings or arbitrary children as support roots. Any other real path needs separate source/evidence review. Never move or reseed current data to fit the validator.

`newRelease` requires the observed root-owned `/opt` parent and declared absence of the new release directory; `unit.newDropInAbsent` must also be true. Those declarations concern the pre-stage state. A future installer must bind its own subsequent creation to fresh identities and recognized transitions; do not falsely repeat an absence declaration after staging or treat the old proposal as live authorization.

Changing `WorkingDirectory` can change other relative-path semantics despite unchanged text. Require proof that retained commands, environment references and filesystem restrictions keep their behavior. Unsupported wrappers/prefixes, multiple start commands, release-specific auxiliary directives or additional required edits need separate review. Do not broaden the supported unit shape or patch other directives to make validation pass.

### Exposure gate

The historical name `redx-site-cms-private.service` and a loopback listener do **not** prove privacy. The Hub may already be publicly routed now; exposure is UNVERIFIED in this document until fresh inventory establishes actual routes and per-route policies. Unknown exposure is not private.

If public, obtain applicable action-time approval for the concrete restart/interruption, old-tab handling and rollback after staging/review. Validate the separate current runtime/routing pins. This proposal authorizes no tunnel change and does not inherit earlier Host/PTR approvals.

## 2. Validate artifacts and review the offline proposal

Use the unchanged [artifact builder and outer pins](../private-hub-ui-artifact/README.md). Validate the archive, provenance and checksum file against the independent pins above and the separate upgrade manifest, then verify all 22 paths/types/sizes/hashes/source revisions and the candidate asset revision. Reject unknown members, links, traversal/absolute paths, duplicates or a self-consistent edited provenance/checksum pair.

The planner reads supplied sanitized evidence and artifact files only, not a live configuration collector. All flags claiming installation, application, granted approval or production verification remain false; success means only that its supplied inputs passed source checks.

From the repository root, the actual offline interface is:

```text
python -I -B deploy/private-hub-cli-upgrade/plan.py --artifact-directory PATH_TO_VERIFIED_ARTIFACT_DIRECTORY
python -I -B deploy/private-hub-cli-upgrade/plan.py --artifact-directory PATH_TO_VERIFIED_ARTIFACT_DIRECTORY --evidence PATH_TO_PRIVATE/operator-evidence.json
```

These are usage templates, not live observation data. With no `--evidence`, the result has `operatorEvidenceStatus:"UNVERIFIED"` and `unitProposal:null`. The evidence filename must be exactly `operator-evidence.json`. Accepted declarations produce a preview only, with `operatorEvidenceStatus:"FRESH_DECLARATIONS_ONLY_NOT_LIVE_VERIFIED"`, `readyForDeployment:false`, approval/live-recheck flags false and unresolved future gates. The canonical SHA-256 of the complete sanitized evidence is recorded as `observationSha256` using sorted compact ASCII JSON plus one newline. It binds the supplied declarations; it authenticates neither the observer nor the host.

Independently inspect the preview unit difference. It may replace only:

- `WorkingDirectory`: verified original application directory to the manifest-pinned new directory.
- `ExecStart`: reset the supported existing command list, preserve the exact observed executable/argument shape and substitute only the new `server.mjs` path.

Keep protected exact original unit/drop-in bytes and identities/hashes. Preserve all other definitions, environment selections, service user/group, lifecycle, writable paths and hardening. Do not change an owner account, key, CMS database/prefix, support/audit location, native service or tunnel rule to make the proposal validate.

The existing protected CMS key check must still require write scope, `website_cms`, exact prefix `site-cms.red-xai.db`, `host_admin=false`, `owner=false` and leader role. That acceptance uses the already approved restricted authenticated flow separately; the planner never reads a key value or performs this check. Its configuration declaration must preserve `cmsBoxPrefix:"site-cms.red-xai.db"`, `cmsDatabase:"website_cms"` and the existing single-writer/durability values. Expiry, revocation or a scope mismatch is a blocker for a separate authorized credential workflow, not permission to broaden or replace the key in this upgrade.

The Host repository is public. Commit only intended source, manifests, sanitized examples and tests. Do not upload runtime archives, captured unit/configuration/environment files, live evidence, backups or key material there. Approved private recovery storage/transport is separate.

## 3. Stage a new immutable release and perform isolated static checks

A separately reviewed installer may create only the manifest-pinned **new** release directory. Require canonical parent/target paths without links, fresh/exclusive creation, protected ownership/modes and exact readback. Existing c443 source, dependencies, artifacts and pins remain untouched. Never extract/overwrite a package into an existing application directory.

Require Node.js 20.9 or newer. Install dependencies only in the new release, from its retained lockfile, with `npm ci --omit=dev --ignore-scripts` under the reviewed build identity and package-source/TLS policy. Do not copy a live `.npmrc`, environment, CMS key or support tree, or share/mutate c443's `node_modules`. Validate Node/platform dependencies including usable `sharp`. Do not bypass TLS or enable install scripts to work around a failure.

The new release must run under the unchanged service identity/hardening while remaining non-writable by that service. Additional permission/unit allowlist changes require separate review. Existing protected configuration stays at its existing paths.

The existing source fixture can be run independently:

```text
python -I -B -m unittest discover -s deploy/private-hub-ui-artifact -p "test_runtime.py"
```

It verifies the pinned package, creates a fresh ignored fixture and serves an ephemeral loopback listener. It never calls production `startServer`, creates a CMS store, uses live key/support paths or contacts account/native/Host/Quick Login services. Require the candidate revision, four Hub pages, 26 asset responses, seven rejected hidden/unknown asset paths, safe dotfile fallback and zero external requests. Also run the reviewed equivalent against the staged source/dependency tree with CMS disabled. This proves neither live authorization nor a working production writer or safe old-tab transition.

Never start a second full Hub on the live CMS support directory for staging, reuse its writer lock, copy original account storage into a fixture or bind live production data to it.

## 4. Require a fresh paired backup and isolated restore proof

Use the reviewed [CMS recovery capture and isolated restore contract](../cms-recovery-tools/README.md), its exact currently reviewed tool/binary/module pins and freshly verified paths. This upgrade uses populated CMS state: **verification only, never seed**. Historical fresh-seed steps, fixed old database counts, backup timestamps or earlier restore receipts are not this operation's recovery point.

That capture drains/stops only the selected Hub, uses the native engine's existing coordinated filesystem-authorized backup IPC, captures full support/media/audit in the same CMS-quiescent interval and restores the Hub's initial state. It starts no second live engine, needs no new admin key and permits no `--force`. Do not substitute raw running-file copies, exclude unreadable databases, loosen permissions or guess a daemon subcommand.

If the Hub is public, the capture stop/resume itself needs the applicable maintenance approval and interruption budget before it happens. Another method's additional service pause needs separate review; this unit proposal authorizes none.

Require a newly created protected pair receipt binding current full native inventory, native process/binary provenance, CMS state/revision boundary, complete media/support/audit hashes, capture interval and artifact hashes/sizes. Include legitimate databases added since earlier recovery points. Keep plaintext private and use only the already approved protected encryption/transport workflow; no new credential or destination is introduced.

Require full authenticated/hash verification and a fresh restore drill into empty, distinct fixture data/support paths under the reviewed native format and binary contracts. Preserve its filesystem/PID/network isolation, exact state/media/full audit/support verification and graceful exit. No live data/support/configuration may be bound writable. A header check, listing, size match or earlier fixture alone is insufficient.

If capture/restore refuses the current runtime, inventory, policy or writer state, stop for a separately reviewed update. Do not relax c443 pins to accept a later live runtime. Keep full original-account/VPS recovery marked incomplete unless separately proven; this narrower pair establishes neither.

## 5. Decide old-tab behavior before a public transition

The retained server hashes `index.html` plus 13 browser files and keeps at most three revisions **in the current process**. Startup seeds only the current revision. Restart drops the old map: an old tab's versioned `29ae45…` asset URL can return 404 while fresh navigation serves the candidate `3035a6…` revision.

Before public use, review either an asset-retention mechanism preserving existing authorization and route-policy boundaries, or explicit reload/old-tab acceptance with applicable notice and maintenance approval. This package supplies neither an old-revision server nor seamless migration. Do not widen the c443 route allowlist/runtime pins to hide that limitation.

Test fresh navigation, retained tabs, reload, back/forward and in-progress owner editing without losing an unsaved draft. A fresh-page pass alone is insufficient. Failure to meet the selected old-tab plan blocks a public upgrade.

## 6. Transition one writer only after the required approval

These are ordered future operator actions, not commands executed or authorized by the planner. Use the exact unit/new directory in the reviewed proposal. Freshly recheck evidence identities, original hashes, exposure and unrelated service baselines immediately before mutation. Drift requires inspection rather than regeneration from unknown values.

1. Retain the protected original unit/drop-in/configuration backup and fresh paired capture/restore receipts. Confirm staged dependencies/static checks, the chosen old-tab plan and applicable action-time approval if public.
2. Drain and stop the existing Hub unit through its unchanged normal lifecycle and timeout/kill/restart policy. Do not stop account, native DB, Host, Quick Login or tunnel services as part of this unit operation.
3. Require inactive unit and `MainPID=0`, no unit-owned descendant writer, no listener on port 14210 and no CMS writer lock through the application's normal close path. Timeout, forced kill, uncertain launch/descendant or remaining lock requires investigation. Never unlink/take over a lock or infer exclusivity solely from an empty process search.
4. With the old writer stopped, install only the independently reviewed unit change at the exact protected drop-in location whose absence was declared and freshly rechecked. Require exclusive creation; an existing path is a refusal for separate review. Preserve other fragments/drop-ins. Run real host unit syntax verification, then reload definitions without changing boot enablement or other services.
5. Re-observe the effective unit. Require exactly the two allowed property changes; every preserved property/fragment/drop-in/environment/key/support/CMS/audit identity must match baseline. Confirm unchanged lifecycle/hardening semantics for the new directory. Refuse unexplained effective differences.
6. Start the same Hub unit once. Require one expected process/cgroup, unchanged service identity/listener and one normally acquired CMS writer lock owned by the new process. Do not run a manual `node server.mjs` beside systemd or remove a lock to retry.
7. Retain only redacted phase/result metadata in the proposal record. Do not dump environments, secret-bearing command lines, journals, unit environment values or key files into chat, source or public logs.

No database is copied, recreated, reseeded, migrated or restored. Existing `website_cms`, prefix, support/media/audit paths, scoped key reference and original account authority remain the same. Unchanged backend code resumes current state.

## 7. Accept actual behavior

Require the applicable checks and keep fixture/private/public evidence distinct:

- Expected health, native CMS configured/storage mode, code/dependency checks and sole process/writer/lock/listener identities.
- Candidate asset revision and exact CLI page/platform chooser/download filenames/checksums; preserved stable CLI links and intended coming-soon statuses.
- Existing website login/session, registration/Terms/email activation and enrolled/owner MFA behavior; no extra key/login navigation gate for DB or Host parent pages.
- Existing owner session, audited owner-only CMS access, draft/media round trip and reviewed publishing check. Root console is not owner proof. Preserve or reconcile actual test content through normal product actions, without erasing current CMS data.
- Normal users A/B with separate DB, Host/device/resource/subscription access, and owner overview through the audited privileged path. An owner-only page load proves no normal-user isolation.
- Current media/preferences/audit append behavior and CMS revision continuity; no missing references, unexpected empty state, silent seed or authority change.
- Host/DB embedding, native/API/Quick Login contracts, unrelated service baselines and separately reviewed actual tunnel routing/policy.
- The selected retained-tab/reload behavior, sessions and unsaved owner drafts.

This CLI UI package does not fix the separate account/email, optional-MFA rollout, billing, resource/device acceptance or full recovery work. Unsupported or untested checks stay incomplete; do not relax them or report the entire site finished.

## 8. Roll back code/configuration only if necessary

First stop/drain the candidate through the unchanged lifecycle and require the same inactive/no-descendant/no-listener/no-lock conditions. Uncertain writer ownership blocks rollback too. Preserve evidence instead of forcing another writer to start.

Restore only the protected exact original unit/drop-in bytes and metadata changed by this operation, and only across recognized baseline/proposal transitions. Preserve unknown intervening edits for review. Do not overwrite unrelated configuration or restore an old data archive, revert native sequence/replication state, reseed the CMS, change keys, delete audit/media or remove a writer lock.

The planned new drop-in was originally absent. Returning to that original state may remove or isolate only the independently identified file created by this operation, retaining its protected copy under the separately reviewed rollback procedure. Never delete a different or unrecognized drop-in, overwrite an unexpected replacement or use a generic cleanup command.

Reload definitions, verify the original effective `WorkingDirectory`/`ExecStart` and preserved properties, then start the original Hub unit once against the **latest live data/support/key references**. Verify health, sole writer, owner/session/media/current-state continuity, unrelated services and actual routing. Successful later writes survive code rollback.

Leave the new immutable release and protected capture/proposal/restore artifacts for inspection. No recursive cleanup or old-release deletion is part of this procedure. Data disaster recovery or an additional service/tunnel change requires a separate reviewed action and approval.

## Completion record

Retain artifact/provenance pins, the canonical sanitized observation hash and private evidence reference, protected originals and unit diff, paired backup plus isolated restore proof, dependency/static results, required approval reference, transition/recheck results, active runtime/asset revision, owner/normal-user/old-tab acceptance and any rollback outcome. Sensitive records stay in approved protected recovery storage, outside the public repository.

Until those actual operator actions and checks complete, the only valid source result is **proposal prepared; live deployment unverified**.
