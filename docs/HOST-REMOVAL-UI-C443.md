# Removal controls for the current Host frontend

This is a source-only frontend candidate based on
`fedf1ac3604a5db917943f1eeea403475af28a4e`. Its entire `packages/web` tree,
root package manifest and lockfile match current C443 source
`c443ce7eb01c0a33261c049bc7071dbc90fd46d4` before this change.

Only `src/web/pages/project.tsx` and `src/web/components/status.tsx` change at
runtime. They adapt the reviewed removal controls from backend source fix
`885d5bf07de48796aaf66f51707f88cbd2cecb3e`: wait for accepted removal before
navigation, retain the page and error on failure, label `removing` as **removal
pending**, explain offline nodes and offer **Retry removal**. Deploy, restart,
stop, save and AI planning are disabled while removal is pending; the removal
button is disabled during an in-flight request. Unknown failure text correctly
leaves the server's deletion state unconfirmed and suggests refreshing.

No API/type prerequisite, account/MFA/tenant behavior, authentication transport,
Quick Login script, endpoint, navigation, client/CLI contract, RedX engine or
data format is changed. The remaining Hub/CMS website and vendor removal stay
as they are at the base. This candidate does not import or enable a realm
prototype and includes no backend runtime build.

## Verification

Eight targeted/navigation tests and both web TypeScript checks pass with Bun
1.3.14. The new isolated fixture evaluates the actual component/event handlers
with inert query/hook adapters: cancel, delayed acceptance/no early navigation,
named/unknown failure, pending status/actions, in-flight disable and retry. It
also renders the actual status pill. No HTTP listener, browser, provider,
worker/process or Docker operation is exercised.

The mounted static build passes with `VITE_BASE_PATH=/host`,
`NODE_ENV=production` and a reduced environment containing only required OS
paths/temporary-directory variables plus those public flags. Root and web
directories contain no non-template `.env` file. Existing C443 dependencies are
reused through ignored local junctions; lock/package files remain unchanged.
A dedicated source-only Ubuntu workflow pins Bun 1.3.14, validates this exact
scope, installs the retained lock without lifecycle scripts, runs the UI/navigation
tests and makes the mounted static build with a reduced environment. Its actual
Linux result remains unverified until review/push. The
unchanged deferred `/redxai-connect/ux.js` build warning is expected: it stays
an external first-party classic script rather than a bundled module.

Local static output is `packages/web/dist`; full file hashes and build/source
metadata are recorded under ignored `dist/host-removal-ui-c443` after the local
commit. Generated files, dependencies and build logs are not committed or
uploaded. Source tests and a local build do not establish published or
authenticated browser behavior.

## Deployment order and acceptance gates

1. Deploy and accept backend candidate
   `152139a621d5aea4579529bb5703d54b8c8ee0ff` through its separately reviewed
   runtime/config/source pins first. The old immediate-delete backend cannot
   supply the durable removal semantics shown here. Fresh live parity, current
   complete ledger backup/isolated restore and reconciliation of already-orphaned
   legacy removals remain required. Preserve project/job/replica history and
   permanent ID/slug reservations; do not infer lost workload identifiers.
2. Independently observe the selected current frontend index and file identities,
   preserve a reversible index/metadata backup, review this candidate's static
   manifest, and validate API/session compatibility. Existing C443 artifacts,
   installer and rollout pins remain immutable; they must not accept this
   candidate by relaxing a digest or allowlist. This source contains no applier.
3. Stage new content-addressed JS before any later index switch. Keep the old
   C443 JS/CSS/public assets available for rollback and open tabs. Reuse identical
   CSS/favicon bytes only after checking identity; never overwrite a different
   file at an existing content-addressed path. Preserve the first-party Quick
   Login script and unrelated existing public files. No asset deletion or
   service restart is required or performed by this local build.
4. After an approved static rollout, verify both owner and normal users can view
   pending removal, handle an error without losing the page, retry failed work
   and see final completion. Verify mounted/embedded navigation and session
   revocation with the actual browser. Old tabs still running the prior bundle
   need explicit acceptance/reload guidance; do not claim they acquire the new
   controls automatically. Rollback is an observed-index/config operation, never
   an old project/job database restore.
5. Confirm installed worker 1.3.1 and real device/Docker/disconnect/A-B behavior
   separately. A served download or this UI cannot upgrade devices, prove
   workload destruction, attest a hostile worker or establish billing/resource
   isolation. Keep pending work visible until the backend's required owned-node
   acknowledgments complete.

Nothing here publishes assets, changes production, approves a rollout or claims
that Host teardown or the whole website is finished.
