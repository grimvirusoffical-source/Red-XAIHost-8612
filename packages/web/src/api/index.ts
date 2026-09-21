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
import { settings } from "./routes/settings";
import { usage } from "./routes/usage";
import { builds } from "./routes/builds";

// API features are oRPC procedures, one file per feature in ./routes/,
// composed into this router — typed end-to-end via the clients
// (web: src/web/lib/api.ts, mobile: lib/api.ts).
export const router = {
  ping,
  overview,
  nodes,
  projects,
  domains,
  settings,
  usage,
  builds,
};

export type AppRouter = typeof router;
/** Typed client for the router — used by the web and mobile api clients. */
export type AppRouterClient = RouterClient<AppRouter>;

const app = createApp(router);

// Better Auth (managed Google sign-in, owner-only).
app.on(["GET", "POST"], "/api/auth/*", (c) => auth.handler(c.req.raw));

// Plain HTTP endpoints the node agents talk to (bearer token per node), plus
// the agent script and installers served as text.
registerAgentRoutes(app);

// GitHub push webhook (HMAC-signed) that powers per-project auto-deploy.
registerWebhookRoutes(app);

export default app;
