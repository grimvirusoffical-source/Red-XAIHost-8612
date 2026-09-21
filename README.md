# RedXAIHost

Private self-hosting control panel. One owner (`grimvirusoffical@gmail.com`, Google sign-in only),
installable on Windows as a desktop app, with an in-app auto-updater.

The panel **never serves hosted traffic itself**. It schedules work onto worker nodes you connect
(your own PC, a VPS, or both). With no node online, hosted projects report down — that is by design.

## Commands

| Command | What it does |
| --- | --- |
| `bun run dev` | Control panel (API + UI) on port 4200 |
| `bun run dev:desktop` | Electron shell around the panel |
| `bun run build` | Builds every package |
| `bun run lint` / `bun run typecheck` | Validation |
| `bun run db:push` | Sync the Drizzle schema to the database |

Secrets live only in the root `.env`. Browser-visible values need the `VITE_` prefix.

## How hosting works

1. **Add a node** (Nodes page). The panel issues a one-time bearer token and gives you a one-line
   installer — `install.ps1` for Windows (registers a scheduled task), `install.sh` for Linux/VPS.
   The agent polls `/api/agent/heartbeat` every 20s, claims jobs, and streams build logs back.
2. **Create a project** (static, Node, Bun, Python, Docker, database, or custom). Upload a zip or
   point it at a Git repo.
3. **AI setup** reads the file manifest and key files and writes the runtime, install/build/start
   commands, port and a complete Dockerfile. Non-static projects need a Dockerfile before they can
   deploy — that is what this step produces.
4. **Deploy.** The job goes to the least-loaded online node, which builds the image and runs the
   container with `--restart unless-stopped`.
5. **Connect a domain** (Domains page) via the GoDaddy or Namecheap API, with Cloudflare DNS plus a
   Cloudflare Tunnel so nothing needs port forwarding. The agent runs `cloudflared` with the
   connector token the panel hands it.

Nodes post hourly traffic rollups, which drive the Usage page.

## Credentials (Settings → Credentials)

Every value is encrypted at rest with AES-256-GCM keyed from `CREDENTIAL_SECRET`.

| Provider | Fields | Used for |
| --- | --- | --- |
| OpenAI | `apiKey` | AI deploy planning (falls back to the bundled gateway) |
| Cloudflare | `apiToken` (Zone:Edit + Tunnel:Edit), `accountId` | DNS records, tunnels |
| GoDaddy | `apiKey`, `apiSecret` | Nameserver + DNS automation |
| Namecheap | `apiUser`, `apiKey`, `clientIp` | Nameserver + DNS automation |
| GitHub | `token` (repo + workflow), `repo` (`owner/repo`), `webhookSecret` | Build dispatch, auto-deploy |
| Expo / EAS | `token` | Mobile binary builds |

Registrar passwords are never used — official API keys only, so there is no ToS or lock-out risk.

## Auto-deploy on push

Projects with **source = git** and **auto deploy = on** redeploy when GitHub reports a push.

1. Put a random string in Settings → GitHub → *Push webhook secret*.
2. On the repo: Settings → Webhooks → Add webhook.
   - Payload URL: `https://<your-panel-host>/api/hooks/github`
   - Content type: `application/json`
   - Secret: the same string
   - Events: *Just the push event*
3. Deliveries are HMAC-verified (`x-hub-signature-256`); a bad signature is rejected and logged.
   A matching push queues a deploy on the least-loaded online node and records the commit SHA.

The project page shows the exact webhook URL for git projects.

## App builds (mobile + desktop installers)

Binaries cannot be compiled by the panel itself, so the Builds page dispatches GitHub Actions:

- `.github/workflows/build-desktop.yml` — Windows `.exe` (NSIS), macOS `.dmg`, Linux AppImage.
- `.github/workflows/build-android.yml` / `build-ios.yml` — EAS builds (needs `EXPO_TOKEN`).

For dispatch to work, push this repo to a **private** GitHub repo and set:

- Secrets: `EXPO_TOKEN` (mobile), optionally `CSC_LINK` + `CSC_KEY_PASSWORD` (code signing).
- Variables: `VITE_APPLICATION_ID`, `VITE_RUNABLE_AUTH_ISSUER`, `VITE_WEBSITE_URL`,
  `VITE_OWNER_EMAIL`.
- Settings → Credentials → GitHub: a token with `repo` + `workflow`, and `repo` as `owner/repo`.

## Desktop auto-update

`packages/desktop` packages with electron-builder (`appId host.redxai.panel`, deep-link scheme
`runable-redxaih-1wrifwx`). The update feed is a GitHub release — `electron-builder.json5` reads
`GH_OWNER`/`GH_REPO` from the environment, which CI fills in from the repo itself.

Run the desktop workflow with a `version` input and it publishes a release containing the installer
plus `latest.yml`. Installed copies check on launch and every 6 hours, download in the background,
and prompt the owner to restart.
