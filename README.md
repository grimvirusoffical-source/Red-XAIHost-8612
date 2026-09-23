# RedXAIHost

Private self-hosting control panel and worker runtime.

## Fast local start

A fresh clone no longer requires Turso, Runable Managed Auth, S3/R2, Docker, or a VPS.

```powershell
bun install
bun run setup:selfhost
bun run selfhost
```

Then open `http://127.0.0.1:4200`.

On first launch, expand **First launch only: create local owner login** and create a
12+ character password for the configured owner email. Direct Google OAuth is optional
and can be enabled later with `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET`.

By default the panel:
- stores control-plane data in local SQLite,
- starts this computer as the built-in worker,
- supports local ZIP bundle storage when S3/R2 is not configured,
- runs static/Node/Bun/Python/custom projects without Docker,
- uses Docker only for explicit Docker/database workloads or projects with a Dockerfile,
- requires a workload to actually open its configured port before deployment is marked successful,
- bootstraps the bundled **InfectedNation** service on port `8787` with persistent data outside the disposable checkout.

Turso/libSQL, S3/R2, Google OAuth, Cloudflare, registrar APIs, extra PCs and VPS nodes remain
optional upgrades rather than fresh-install requirements.


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
   On Windows the command must be pasted into **PowerShell**, not cmd.exe. The Nodes page also
   offers a cmd.exe variant. The installer needs Node.js 18+. Docker is optional unless that worker will run Docker/database workloads.
2. **Create a project** (static, Node, Bun, Python, Docker, database, or custom). Upload a zip or
   point it at a Git repo.
3. **AI setup** reads the file manifest and key files and writes the runtime, install/build/start
   commands, port and a complete Dockerfile. Non-static projects need a Dockerfile before they can
   deploy — that is what this step produces.
4. **Deploy.** The job goes to the least-loaded online node, which builds the image and runs the
   container with `--restart unless-stopped`.
5. **Connect a domain** (Domains page) — Cloudflare DNS plus a Cloudflare Tunnel, so nothing needs
   port forwarding and it works behind home internet. See below.

Nodes post hourly traffic rollups, which drive the Usage page.

## Domains: automatic, or entirely by hand

### Automatic (one API token, nothing else)

Paste a Cloudflare API token (`Zone:Edit` + `Tunnel:Edit`) into Settings → Cloudflare. That is the
whole setup — **the account ID is discovered from the token**, so you never go looking for it.

From then on the panel wires domains itself. It creates the zone, points the registrar's nameservers
at Cloudflare (when a GoDaddy or Namecheap API key is saved), creates the named tunnel, sets the
tunnel's ingress to your project's port, writes the proxied CNAME, and queues a node restart so the
running container picks up the new connector token.

It runs that flow at every point where it could possibly help:

- the moment a working Cloudflare token is saved (every waiting domain, at once),
- when a domain is added,
- when you press **Connect** or **Connect Cloudflare now** on the Domains page,
- and on a **background pass every 10 minutes** for any domain that is not live yet.

The timer matters because a nameserver change can take minutes or up to 24 hours to propagate. You
do not come back and press anything — the panel keeps retrying and flips the domain to `live` the
moment Cloudflare reports the zone active.

On the node side, `cloudflared` installs itself: if it is not already on `PATH`, the agent downloads
the official static binary from Cloudflare's GitHub releases into `~/.redxaihost/bin/` (Linux and
Windows binaries, macOS `.tgz`) and runs it directly. Only if that fails does it fall back to the
`cloudflare/cloudflared` Docker image.

### By hand (no registrar API key, no automation)

Every domain row has a **Manual setup** button that opens a six-step walkthrough, pre-filled with
that domain's real values — root domain, the exact tunnel name the panel expects, the public-hostname
fields, the CNAME target, and your project's port — with links straight to the right Cloudflare
pages. Use it when you would rather not hand the panel a registrar API key; the end state is
identical to what the Connect button produces, and the panel recognises the tunnel afterwards
because the name matches.

## Credentials (Settings → Credentials)

Every value is encrypted at rest with AES-256-GCM keyed from `CREDENTIAL_SECRET`.

| Provider | Fields | Used for |
| --- | --- | --- |
| OpenAI | `apiKey` | AI deploy planning (falls back to the bundled gateway) |
| Cloudflare | `apiToken` (Zone:Edit + Tunnel:Edit) — `accountId` is auto-discovered | DNS records, tunnels |
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
