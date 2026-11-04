# Quick Login approval browser assets

This handoff contains two public browser assets and their synthetic UI tests from Quick Login commit `5c54aa9702af7e27cdbd44be752a3210bc428770`, reviewed in [Quick Login PR 6](https://github.com/grimvirusoffical-source/red-xai-quick-login/pull/6). The source change is based directly on production revision `2cbedc4ba32d454fe7e49d4f050e59b4ca522bd3`. It presents one explicit **Sign in** action after displaying the calling application, account, permission and comparison code.

The package includes two web files, their synthetic VM test, the integrity manifest and this document. Backend source, packages, credentials, state and account data are outside this package. The manifest pins the original backend and both original browser files as comparison metadata; it does not supply or authorize replacing that backend. The fresh-realm prototype is excluded.

Context requests are serialized because they rotate CSRF values. Returning to the page refreshes the website session automatically. Focus changes block stale approval; an explicit click checks account, request, code, scope and expiry again. Changed details require another click. Verification, MFA, consent, trust duration, cookies, Origins and client/server contracts remain authoritative.

## Local validation

From the repository root:

```sh
node --test deploy/quicklogin-ui-5c54aa9/test/approval-ui.test.cjs
```

All 25 synthetic UI cases passed. The source change also passed all 64 tests on the original production base without skips. This public handoff runs the browser tests and checks manifest integrity; it contains no backend test runtime. Its workflow uses one standard public Ubuntu runner, Node 22, read-only repository permissions and no application dependencies, secrets or service connections.

## Required live review

The original service caches all web files at startup. File replacement alone therefore does not update its responses. A restart loses pending approvals, opaque access credentials, developer CSRF contexts and session-only grants. Remembered refresh grants and registered applications survive only with the verified existing private persistent state. No supported zero-loss drain has been established.

Before an update, the sole production controller must verify all nine original runtime file identities and both serving and on-disk asset identities, inspect the current private state path and ownership/permissions, retain protected source/configuration/state backups and verify recovery without displaying credentials. Resolve any cached HTML or proxy transformation mismatch. A reviewed maintenance decision must account for clients that need to retry or sign in again. Keep existing state and service configuration; verify the owner session and real application approval afterward. Code rollback must not restore older account or grant data over current data.

This is a source-only handoff containing no installer or service commands. Deployment, restart, recovery acceptance and live browser/client behavior remain unverified and unapproved by this artifact.
