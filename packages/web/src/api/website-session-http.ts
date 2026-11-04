import type { Hono } from "hono";
import { websiteIdentity, websiteSessionEnabled } from "./lib/website-session";

export function registerWebsiteSessionRoutes(app: Hono) {
  app.get("/api/website-session", async c => {
    c.header("Cache-Control", "no-store");
    if (!websiteSessionEnabled()) return c.json({ enabled: false, user: null });
    const identity = await websiteIdentity(c.req.raw.headers);
    return c.json({ enabled: true, user: identity ? {
      id: `website:${identity.id}`, email: identity.email, name: identity.name,
      isOwner: identity.owner,
    } : null });
  });
}
