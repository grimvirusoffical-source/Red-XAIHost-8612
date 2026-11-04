# Host provider and teardown fixes on the production-source baseline

This source candidate starts at exact commit
`bbfb77d3880e8fbed6a7ecbfa593ec4306daa11c`, recovered from the public Host Git
repository. That is the last recorded deployed backend revision, historically
at `/opt/redx/staging-host-8f60582c`; current deployment identity and source/config
parity must be observed again by the production controller. This document does
not authorize a rollout or establish live acceptance.

It adapts the reviewed source changes from:

- Provider guard PR12, `bfec689bc3fe0a410679c498926b4a6e8898286f`.
- Project teardown PR13, `d86acc19a6917e4e94155e5381d79fde18ce29d2`.

Both originally target prototype base
`5db08985714c7a84fa03d8865a32439a29c91e80`. All eleven existing teardown
runtime/UI files have identical pre-fix Git blobs at that base and `bbfb77d`.
The provider runtime files also match. The required extra prerequisite is an
export of the existing libSQL client for atomic ledger batches; no new database
connection or table/column migration is introduced. The project status type adds
`removing`, stored in the existing text column.

The backport keeps the `bbfb77d` authentication, fixed website authority,
`redx_session` validation, immutable owner ID/role/MFA requirements (including
phone-call MFA), registration, account routes and client contracts unchanged.
It does not import a realm registry, activate the fresh realm or add an identity
mapping migration. The two new procedure fixtures instead use synthetic
legacy website A/B accounts and a verified owner, with isolated libSQL and
stubbed authority/provider responses. No RedX engine, BoxQL, format, credential,
billing or subscription implementation changes are included.

## Resulting behavior

Deployment-wide AI gateway and repository fallbacks are available only in the
existing verified-owner context. A normal tenant supplies its own provider key,
and its own repository when dispatching builds. Personal provider configuration
continues to work.

Project removal persists a pending intent and node jobs until every required
owned node acknowledges teardown. Offline nodes remain pending. Terminal
history, retry identity and permanent minimal project-ID/slug reservations are
retained. Late callbacks cannot revive a removing project or reuse a completed
slug. Removal payloads omit source/command/environment/connector secrets. See
[the lifecycle and limits](HOST-PROJECT-REMOVAL.md) for exact semantics.

## Verification completed locally

- All 50 baseline-plus-targeted API tests passed, including actual in-process
  provider RPC and teardown RPC/HTTP fixture handlers, restart/rollback/tenant
  cases and seven inert served-worker checks.
- Four mounted-auth tests passed. Existing website-session tests still cover
  immutable owner ID, authoritative role and MFA, including phone-call MFA.
- The unchanged baseline web typecheck passed; whitespace checks passed.

Local Bun is 1.4.4. Dependencies were reused through ignored junctions to
existing dependency directories. Package manifests and the exact baseline lock
remain unchanged. A clean Linux install from that lock under the repository's
pinned Bun 1.3.14 and its CI checks remains required after review/push; these
local results do not substitute for it. No worker command, container, live
listener, provider or production endpoint was exercised by these tests.

## Required before production use

1. **Observe the actual deployment.** Verify selected backend executable/source
   revision, runtime path, unit/config identity, schema and ownership mappings,
   current worker versions and frontend selection. Record sanitized metadata;
   do not export credentials or environment contents. A historical path or
   revision is not fresh parity proof. Stage a new reversible release through
   the reviewed deployment process; do not mutate an immutable old release.
2. **Back up the current complete ledger consistently.** Establish the actual
   supported writer/quiescence and backup method first. Capture and verify the
   same current database state for projects, nodes, replicas, deployments,
   activity reservations and associated configuration identities. Preserve
   tenant ownership, job history, tombstones and their relationships through an
   isolated restore proof. Never restore an older project table alone or prune
   `activity.scope = project_removal` safety reservations during maintenance,
   account cleanup or rollback.
3. **Reconcile existing legacy removals.** The old remove path could delete the
   project before a worker fetched its payload. Inventory current removal jobs
   and correlate them to still-known project IDs/slugs, owned nodes, replicas
   and actual workloads. Preserve uncertain records; missing payload/slug cannot
   safely be inferred from a display name, email or guessed identifier. Do not
   fabricate a tombstone, silently mark an unknown removal successful, remove
   an unknown workload or introduce a second live database writer.
4. **Preserve the current frontend.** The public frontend has a separately
   delivered C443 asset release; this backend baseline predates that delivery.
   Do not publish a full `bbfb77d` frontend build over those assets or reintroduce
   its old vendor UI. The source includes the reviewed pending/removal-retry UI
   changes, but their integration with the currently selected frontend needs a
   separate reviewed build/asset manifest and acceptance. Backend source parity
   and local typecheck do not establish that frontend integration.
5. **Verify installed workers, not only the served download.** Source serves
   worker 1.3.1. Existing remote/self-hosted workers are not upgraded by this
   commit or by the tests. Confirm an approved upgrade and recorded 1.3.1
   enrollment/heartbeat on actual supported Windows/macOS/Linux devices before
   relying on strict Docker failure handling and busy-heartbeat delivery.
6. **Run controlled real acceptance.** Use two normal users and a separately
   authenticated owner. Verify node/device/VPS, provider credential, project,
   bundle, job, usage and callback isolation through actual HTTP and device
   behavior. Confirm native PID/child identity, disconnect/reconnect, offline
   removal, retry, stale callbacks and exact Docker-name/error handling. A
   Docker permission/daemon/list/remove failure must leave removal pending or
   failed. A worker's authenticated success report is not independent hostile
   device attestation. No real Docker/process/device acceptance is claimed here.
7. **Validate contracts and rollback.** Check existing website login/session
   revocation, owner MFA, Quick Login and CLI/API paths. Recheck owner personal
   providers and normal-user own-key behavior. Retain a code/config rollback
   with a version that understands pending removal and reservation safety;
   blindly returning to immediate-delete behavior can violate the new ledger.
   Never roll back by restoring old account/project/job data over new state.

Subscription/billing correctness, cloud allocation/entitlements, provider
resource deletion, cached checkout/bundle/log cleanup and secure erasure remain
separate gates. This source candidate neither enables billing nor proves that
customers can use only their own production resources.
