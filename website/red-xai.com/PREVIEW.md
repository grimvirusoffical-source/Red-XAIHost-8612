# Local guest preview

From this directory, with Node.js 20 or newer:

```powershell
node preview.mjs
```

Open **http://127.0.0.1:14310/**. An optional second argument selects another port, for example `node preview.mjs 14311`. The server always binds to `127.0.0.1` and stops with Ctrl+C. It needs no environment variables, dependencies, account cookie or production credentials.

This is a public, signed-out visual preview. It serves only explicitly listed frontend files and public product documentation. The account session always reports signed out, even if a cookie is supplied. There is no mock logged-in user or owner, and no upstream proxy, production gateway import or server-side fetch. Host/DB frames are blocked by CSP; all account, Host, DB, media, checkout and write APIs are disabled. Registration, activation and current registration terms show an explicit preview notice instead of contacting the primary account service. Production `/signup`, `/activate` and `/tos` behavior is unchanged.

Review the Hub, stores, all software documents, CLI/manual installer availability, all 19 Secrets guides, empty news/media/brand categories, contact/legal drafts, mobile navigation and signed-out workspace recovery. Owner editing and authenticated workspace behavior require separate synthetic/backend tests and an authorized authenticated staging review; this harness does not simulate them.

Tests:

```powershell
node --test frontend.test.mjs preview.test.mjs
```

The harness tests invoke its HTTP handler directly and verify isolation responses. They do not replace a real browser check of loopback serving and desktop/mobile layout.

External documentation, social and release download links are real links. They open only after a user chooses them; the preview never follows them server-side.
