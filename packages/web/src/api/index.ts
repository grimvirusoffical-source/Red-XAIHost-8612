import type { RouterClient } from "@orpc/server";
import { createApp } from "./__core/app";
import { auth } from "./auth";
import { registerAgentRoutes } from "./agent";
import { registerWebhookRoutes } from "./webhooks";
import { ping } from "./routes/ping";
import { overview } from "./routes/overview";
import { nodes } from "./routes/nodes";
import { projects } from "./routes/projects";
import { domains } from "./routes/domains";
import { cloudflare } from "./routes/cloudflare";
import { settings } from "./routes/settings";
import { usage } from "./routes/usage";
import { builds } from "./routes/builds";
import { startDomainReconciler } from "./lib/domain-reconciler";
import { LIMITS, clientIp, hitRateLimit } from "./lib/rate-limit";
import { registerLocalBundleRoutes } from "./lib/bundle-storage";
import { registerAccountsRoutes } from "./accounts-http";
import { framePolicy, hubOrigins } from "./lib/hub";

// API features are oRPC procedures, one file per feature in ./routes/,
// composed into this router — typed end-to-end via the clients
// (web: src/web/lib/api.ts, mobile: lib/api.ts).
export const router = {
  ping,
  overview,
  nodes,
  projects,
  domains,
  cloudflare,
  settings,
  usage,
  builds,
};

export type AppRouter = typeof router;
/** Typed client for the router — used by the web and mobile api clients. */
export type AppRouterClient = RouterClient<AppRouter>;

const app = createApp(router);

// Panel self-defence: rate limits every API path before it reaches a handler,
// so a flood or a credential-stuffing run is cheap to absorb. This runs whether
// or not Cloudflare is configured, and the edge rules sit in front of it.
app.use("/api/*", async (c, next) => {
  const path = c.req.path;
  const ip = clientIp(c.req.raw.headers);

  // Agents authenticate per node and poll on a schedule, so they get their own
  // bucket keyed by token rather than sharing the browser's.
  const isAgent = path.startsWith("/api/agent/");
  // Only the auth writes — sign-in, callback, token exchange — get the strict
  // bucket. Session reads are a GET the panel makes on every page load, and
  // counting those against the login limit would lock the owner out of their
  // own panel long before it ever stopped an attacker.
  const accountsLogin =
    c.req.method !== "GET" &&
    (path === "/api/accounts/login" ||
      path === "/api/accounts/login/2fa" ||
      path === "/api/accounts/login/call" ||
      path === "/api/accounts/quick-login/start");
  const isAuth = (path.startsWith("/api/auth/") && c.req.method !== "GET") || accountsLogin;
  const rule = isAuth ? LIMITS.auth : isAgent ? LIMITS.agent : LIMITS.api;
  const scope = isAuth ? "auth" : isAgent ? "agent" : "api";
  const identity = isAgent
    ? (c.req.header("authorization")?.slice(-24) ?? ip)
    : ip;

  const result = hitRateLimit(`${scope}:${identity}`, rule);
  c.header("X-RateLimit-Limit", String(result.limit));
  c.header("X-RateLimit-Remaining", String(result.remaining));
  if (!result.allowed) {
    c.header("Retry-After", String(result.retryAfter));
    return c.json({ error: "rate_limited", retryAfter: result.retryAfter }, 429);
  }
  await next();
});

// Baseline response headers on routes this app handles. REDX_HUB_ORIGINS names
// the Hub origins in CSP frame-ancestors; with none set, framing is denied.
// The template static server does not apply these headers to HTML.
app.use("*", async (c, next) => {
  await next();
  c.header("X-Content-Type-Options", "nosniff");
  c.header("Referrer-Policy", "strict-origin-when-cross-origin");
  const framing = framePolicy(hubOrigins());
  if (framing.frameOptions) c.header("X-Frame-Options", framing.frameOptions);
  c.header("Content-Security-Policy", framing.csp);
});

registerAccountsRoutes(app);

// Better Auth (managed Google sign-in, owner-only).
app.on(["GET", "POST"], "/api/auth/*", (c) => auth.handler(c.req.raw));

// Plain HTTP endpoints the node agents talk to (bearer token per node), plus
// the agent script and installers served as text.
registerAgentRoutes(app);
registerLocalBundleRoutes(app);

// GitHub push webhook (HMAC-signed) that powers per-project auto-deploy.
registerWebhookRoutes(app);

// Background pass that keeps retrying Cloudflare for domains that are not live
// yet (nameserver changes take hours) — so nothing waits on the owner.
startDomainReconciler();

export default app;
