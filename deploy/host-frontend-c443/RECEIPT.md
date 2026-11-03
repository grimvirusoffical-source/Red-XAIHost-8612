# Host frontend candidate c443ce7

Prepared locally on 2026-11-03. **No production/browser/backend mutation.**
The initial preparation performed no push/upload; the parent-reviewed public
candidate publication is recorded at the end of this receipt.

The isolated detached worktree `work/host-front-c443` is exactly
`c443ce7eb01c0a33261c049bc7071dbc90fd46d4`. Its tracked source and `bun.lock`
remained unchanged. The build ran with a reduced environment, no private env
files, `VITE_BASE_PATH=/host`, `NODE_ENV=production`, and official Bun **1.3.14**.
The installed default Bun was 1.4.4, so it was deliberately not used. The
official 1.3.14 Windows archive matched GitHub release digest
`0a0620930b6675d7ba440e81f4e0e00d3cfbe096c4b140d3fff02205e9e18922`.

`bun install --frozen-lockfile --ignore-scripts` succeeded. Both TypeScript
checks and the mounted Vite build passed. All seven existing Host navigation
and embed tests passed. The preserved classic `/redxai-connect/ux.js` warning
is expected: it remains a deferred first-party script, not a bundled module.

## Artifacts

- `host-frontend-c443ce7.tgz`: **138268 bytes**, SHA-256
  `00413c39f00ddca778bb53bf72b72c37676743e5b298a076f2489c02b90803bd`.
- `host-frontend-c443ce7-manifest.json`: exact per-file hashes and build/source
  metadata. The archive contains only the four runtime public files: index,
  JS, CSS and SVG favicon. It excludes source, backend, native engine, maps,
  credentials, SQLite, configuration and dependencies. Existing unreferenced
  `.well-known/redxai-host.json` and empty `.gitkeep` public placeholders are
  omitted and remain untouched on the server.
- `install-host-frontend-c443.py`: guarded inspect/apply/rollback helper,
  SHA-256 `51de0f222b9b82ed7fbe708db301a758cf1573e2e9a2694ea3183d9ff1d47e1f`.
- `test-install-host-frontend-c443.py`: pure plus actual Linux-root filesystem
  drills, SHA-256 `cc67866c1af3c5f6ea48dd7d5cc8cb7fb39eec888ec98994ae6087e370980cee`.
- `host-frontend-c443-build.log` and
  `host-frontend-c443-endpoint-comparison.json`: local evidence.

The generated archive contains **zero matches** for Runable/Runnable,
`lilstts`, badge selectors, `analytics-consent.js`, or “Made with”. Source
navigation tests prove Hub `/` and DB `/db` leave the mounted Host router;
Vortex and Cloud PC use fixed external new-tab links with `noopener noreferrer`
and no session/query forwarding. Existing local Host navigation stays local.

## Compatibility and limits

The preserved public baseline JS (`dc980958bb5f...`) and candidate have the
same ten directly quoted `/api/*` endpoint strings, including website-session,
account password/MFA/Quick Login and Hub/release paths. Both use credentialed
Host RPC under the same `/host` base. This literal/source comparison is useful
evidence, **not full backend compatibility or live user acceptance**. Reported
live backend object `bbfb77d3880e8fbed6a7ecbfa593ec4306daa11c` is unavailable
in this checkout; do not call it freshly source-verified.

The helper is intentionally frontend-only: fixed reviewed origin
`/opt/redx/staging-host-8f60582c/packages/web/dist`, no subprocess/network,
no services, no API writes, no SQLite/config. It verifies the pinned archive
and all four members, requires an independently observed current origin index
SHA, retains the existing Quick Login script, refuses symlinks/nonregular
files and conflicting asset bytes, creates and verifies a private original
index/metadata backup, installs new public assets, then atomically switches
only the index. Original UID/GID/mode/xattrs are restored to the staged index
and verified. Rollback requires the exact candidate index hash and unchanged
access metadata, then atomically restores the original index. New and old
assets remain available; there is no service restart.

**Retaining original assets preserves rollback and already-open clients, but
does not remove every old vendor byte from the public origin.** The generated
candidate itself is clean. Any retirement of legacy assets must be a separate
reviewed operation after live acceptance, with pinned file hashes and private
reversible backup; this helper does not claim it has done that.

Three pure tests passed locally; five tests were explicitly skipped because
this Windows host lacks Linux-root/xattr support and symlink permission.
WSL and Docker are absent. **Run the actual Linux-root temporary-directory
drills before authorizing application.** They make no network/service calls
and touch only their fresh temporary fixture. They cover atomic install and
rollback with UID999/GID987/mode0600/user-xattr, wrong origin pins, public asset
collisions, backup collisions and concurrent index change before rollback.

## Next operator steps

1. Parent reviews the helper and manifests. Nothing has been uploaded.
2. If accepted, publish the public artifact, helper, manifest and test under a
   clearly named non-latest candidate prerelease in the existing Host repo.
   No secrets, environment files or production index/metadata backup belong
   in that release. Fresh-download and verify all hashes.
3. In the already authorized Kamatera console, verify downloaded helper/test
   hashes, run `python3 -B test-install-host-frontend-c443.py` in the downloaded
   candidate directory, and require all eight tests pass with no skips.
4. Independently inspect the actual origin index SHA/metadata and run the
   helper without `--apply`; require the expected old module/Quick Login and
   non-conflicting assets. Do not use the public HTTP HTML digest as the origin
   digest: Cloudflare injects/normalizes public HTML.
5. Apply with that exact origin SHA and a fresh private backup name; validate
   owner and real normal-user Host, local tools, DB/Hub external navigation,
   Quick Login and same-site embed. Roll back the index if acceptance fails.

Example after downloads and independent origin review:

```sh
python3 -B install-host-frontend-c443.py host-frontend-c443ce7.tgz --expected-origin-sha256 REVIEWED_CURRENT_ORIGIN_SHA
python3 -B install-host-frontend-c443.py host-frontend-c443ce7.tgz --expected-origin-sha256 REVIEWED_CURRENT_ORIGIN_SHA --apply
```

Rollback uses the candidate index hash
`f339b0a65e9d15541c97213f48334bc30f5e5991b04d9ed6bab80abdf58f1503`
and the same backup name:

```sh
python3 -B install-host-frontend-c443.py host-frontend-c443ce7.tgz --expected-origin-sha256 f339b0a65e9d15541c97213f48334bc30f5e5991b04d9ed6bab80abdf58f1503 --rollback
```

No real normal-user isolation, device/VPS ownership or billing acceptance was
performed by this local artifact task. Existing backend tenant authorization
remains the authority and is outside this frontend-only change.

## Parent-reviewed publication follow-up

The parent reviewed the artifact/helper and authorized publishing exactly the
four reviewed public files. They were uploaded first to a draft, then all four
were freshly downloaded and compared for exact size and SHA-256. Only after
those comparisons passed was the release published as a **prerelease**, with
`--latest=false` explicitly set:

https://github.com/grimvirusoffical-source/Red-XAIHost-8612/releases/tag/host-frontend-c443ce7-candidate.1

Published release ID: `433272457`. Its tag resolves exactly to source revision
`c443ce7eb01c0a33261c049bc7071dbc90fd46d4`. The helper, tests, manifest and
initial review receipt are separately committed at
`307a06d1135cc6cb4bf0db5f09b39784729bef4a` on
`feature/host-frontend-c443-delivery`; generated archive bytes are not committed.

All four published public download URLs were freshly downloaded again without
credentials, with matching size and SHA-256. The public latest-release endpoint
remained absent (404), consistent with this repository having no stable latest
release before or after candidate publication. Exactly four assets are present.

The standalone manifest is 1055 bytes, SHA-256
`493491e72f0e358e6be0f233970fefd91d6b5a912bcd6443d9e02217dc085fb6`.
The other three file sizes/digests above remain unchanged. No configuration,
private production index/metadata backup, credentials, env files, backend or
SQLite was uploaded. No release/tag-triggered workflow exists in the reviewed
source tree. **Linux-root fixture tests and live application remain pending.**
