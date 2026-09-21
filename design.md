# RedXAIHost — Design

A private, single-owner self-hosting control panel. Ships as a **web app** (primary) and a
**Windows/desktop Electron app** (same UI, auto-updating). It is a control plane, not a host: the
panel schedules work onto connected worker nodes (the owner's PC, any VPS) and reports everything
offline when no node is online. Visual direction: dark "command deck" — graphite surfaces,
hairline borders, red signal accent, monospace telemetry. Dense but calm; status legibility beats
decoration.

## Brand & Colors

Dark-only (a control room, not a marketing site). CSS variables in
`packages/web/src/web/styles.css`; the desktop shell loads the same UI.

| Token | Value | Use |
|-------|-------|-----|
| background | `#0A0B0D` | App background |
| surface / card | `#111317` | Panels, cards, sidebar |
| surface-raised | `#171A1F` | Inputs, hovered rows, code blocks |
| border | `#23262D` | Hairlines (1px, everywhere) |
| foreground | `#F2F4F7` | Primary text |
| muted-foreground | `#8A9099` | Labels, meta, units |
| primary (red) | `#FF2B36` | Brand, primary buttons, active nav, focus ring |
| primary-dim | `#B3121B` | Gradient ends, glow washes |
| ok | `#31D07E` | online / running / live |
| warn | `#F5A524` | pending / deploying / dns_set |
| danger | `#FF4D4F` | failed / error / offline |
| info | `#4CC3FF` | queued / neutral telemetry |

Signal colors are only used on status, never as fills for layout. Red is the single accent:
brand mark, primary action, active nav item, chart line.

## Typography

- **Display / UI headings**: `Chakra Petch` (600/700) — squared technical face, uppercase for
  section labels with `0.08em` tracking.
- **Body**: `IBM Plex Sans` (400/500).
- **Telemetry / logs / tokens / commands**: `IBM Plex Mono` (400/500).

Loaded from Google Fonts at the top of `styles.css`. Numbers in stats use mono with
`tabular-nums` so counters don't jitter.

## Layout

- Fixed 248px left sidebar (brand, nav, node-capacity footer, owner chip), content scrolls.
- Content max-width 1240px, 24–32px gutters.
- Page header: uppercase eyebrow + large display title + one-line purpose + right-aligned actions.
- Cards: 1px border, `#111317` fill, 14px radius, no drop shadows — depth from border + fill only.
- Asymmetric dashboard: a wide capacity/traffic panel beside a narrow status stack; the activity
  feed runs full width beneath.
- Background: a single fixed radial red wash top-left at ~6% opacity over the near-black base,
  plus a faint 1px grid — no gradient meshes on cards.

## Motion

One orchestrated page-load: nav items and cards fade/rise in a 40ms stagger (CSS only, via
`tw-animate-css` utilities). Status dots pulse only while a job is in flight. No hover parallax.

## Pages & Screens

All web, under `packages/web/src/web/pages/` (routes registered in `app.tsx`), wrapped by
`components/shell.tsx` and gated by `components/auth-gate.tsx`:

- **Sign in** (`sign-in.tsx`) — single Google button, states the panel is owner-only; rejects any
  non-owner account with an explicit message.
- **Dashboard** (`index.tsx`) — fleet capacity banner (loud when zero nodes online), project /
  domain / 24h traffic stats, recent builds, credential health, activity feed.
- **Nodes** (`nodes.tsx`) — the fleet: add a node (PC or VPS), one-time agent token + copy-paste
  install command per OS, live heartbeat, CPU/RAM/disk, docker + cloudflared readiness, rotate
  token, remove.
- **Projects** (`projects.tsx`) — every hosted thing, status, node, domains; create via upload
  (drag-drop zip → presigned S3 PUT) or git URL.
- **Project detail** (`project.tsx`) — AI setup (OpenAI writes runtime/install/build/start/port/
  Dockerfile), editable plan, env vars, deploy / restart / stop, deployment history + logs,
  attached domains.
- **Domains** (`domains.tsx`) — add a hostname, pick registrar, run the connect flow (Cloudflare
  zone → registrar nameservers over the official API → tunnel → ingress → CNAME) with a
  step-by-step result list, recheck propagation.
- **Usage** (`usage.tsx`) — requests/visitors/errors/egress over 1–30 days, per-hour sparkline
  and per-project split.
- **Builds** (`builds.tsx`) — dispatch iOS/Android/Windows/Mac/Linux binary builds to CI, with a
  standing note that binaries cannot be compiled on the owner's PC, and a run status board.
- **Settings** (`settings.tsx`) — per-provider credential forms (OpenAI, Cloudflare, GoDaddy,
  Namecheap, GitHub, Expo), live verification result, owner identity, desktop update check.

## Key User Flows

1. **Sign in** → Google (managed auth) → non-owner emails are refused server-side at user
   creation → dashboard.
2. **Add hosting power**: Nodes → add → copy the one-line installer onto the PC/VPS → agent
   heartbeats → node goes online and capacity appears everywhere.
3. **Host something**: Projects → new → drop a zip or paste a git URL → AI writes the run plan →
   review → Deploy → job is queued to the least-loaded online node → logs stream back.
4. **Put it on a domain**: Settings → Cloudflare + registrar API keys → Domains → add hostname →
   Connect → zone, nameservers, tunnel, ingress, CNAME → recheck until live.
5. **Ship an app binary**: Settings → GitHub token + repo → Builds → pick platform → CI run is
   dispatched and tracked.

## Architecture

- **API**: typed oRPC client (`src/web/lib/api.ts`) → Hono/oRPC in `src/api`; hooks in
  `src/web/queries/` (one file per feature), consumed by pages.
- **Auth**: Better Auth + `@runablehq/managed-auth` (Google only), owner allow-list enforced in
  `api/auth.ts` (database hook) and `api/middleware/auth.ts` (`owner` procedure base).
- **Agents**: plain HTTP under `/api/agent/*` with a per-node bearer token; the panel never
  serves hosted traffic.
- **Desktop**: `packages/desktop` Electron shell loads the web UI, adds managed-auth deep links
  and `electron-updater`.
