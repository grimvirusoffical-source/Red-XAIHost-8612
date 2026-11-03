import type { Hono } from "hono";
import { auth } from "./auth";
import { deploymentPath, publicRequest } from "../deployment-path";
import { ownerBootstrapEnabled } from "./lib/owner-bootstrap";

export function registerAuthRoutes(app: Hono) {
  app.on(["GET", "POST"], "/api/auth/*", (c) => {
    if (c.req.path === "/api/auth/sign-up/email" && !ownerBootstrapEnabled()) {
      return c.json({ code: "OWNER_BOOTSTRAP_DISABLED", message: "Local owner setup is disabled. Sign in with Red-XAI Accounts." }, 403);
    }
    return auth.handler(publicRequest(c.req.raw, deploymentPath(process.env.REDX_BASE_PATH)));
  });
}
