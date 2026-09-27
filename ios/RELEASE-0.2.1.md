# Red-XAI iOS 0.2.1 — local reliability beta

This is a TestFlight engineering release, not a production hosting service.

## Database
- Strict UTF-8 import rejects corrupt text instead of silently replacing bytes.
- Import and save both enforce the 2 MiB document limit.
- Clear not-checked/checking/basic-check result states; editing clears stale diagnostics.
- Native diagnostics sheet, keyboard Done button, Dynamic Type editor font, line/byte counts, and installed build label.
- The existing structural validator remains a prototype; the full language parser, ID semantics, syntax coloring, database API, access credentials, and account service are not implemented in this iOS app.

## Host
- Named Windows/macOS/Linux-VPS setup drafts now persist locally across app restarts.
- Draft storage is bounded, versioned and atomically written; unreadable saved data is not silently replaced.
- Draft deletion is confirmed and affects local plans only.
- Placeholder live CPU/traffic counters were removed. Nothing in this build enrolls a worker or starts hosting.

## Tests and release gate
15 regression tests cover UTF-8, boundary sizes, draft serialization, invalid/corrupt schemas, names, duplicate IDs and count limits. The pipeline must also run the existing XCTest suite and compile both apps before signing/upload. Test results and Apple processing status must be checked in the actual release run; this document alone is not evidence of passing tests or availability.

## iPhone retest
Use synthetic data. In Database, open/create a text document, edit, validate, save, close and reopen; edit after validating and confirm the old result clears. In Host, save a named draft, force-close/reopen, verify the draft persists, and test cancel/save/delete. Neither app should ask for developer-account secrets. Report the version/build printed in the app.

## Next major milestones
1. Formal Red-XAI lexer/parser/AST, error recovery, serialization and cross-platform conformance tests.
2. Secure shared account backend, real manual/social login, verification, recovery, optional MFA and account deletion.
3. Persistent database library and authorized database APIs/access-token lifecycle.
4. Real desktop/VPS worker enrollment, health checks, deployments, routing and Cloudflare integration.

Mobile sign-in, 2FA, owner console, subscriptions, distributed hosting and desktop installer/updater remain separate work. Source/publishing credentials stay in the existing protected GitHub environment; never in database exports or repository files.
