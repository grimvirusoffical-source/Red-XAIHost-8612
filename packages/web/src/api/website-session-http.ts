import type { Hono } from "hono";
import { websiteSessionEnabled } from "./lib/website-session";
import {hostWebsiteIdentity} from './lib/host-website-identity';

export function registerWebsiteSessionRoutes(app: Hono) {
  app.get("/api/website-session", async c => {
    c.header("Cache-Control", "no-store");
    if (!websiteSessionEnabled()) return c.json({ enabled: false, user: null });
    const resolved=await hostWebsiteIdentity(c.req.raw.headers),identity=resolved?.identity;
    return c.json({ enabled: true, user: identity ? {
      id: identity.sessionKey, email: identity.email, name: identity.name,
      isOwner: identity.owner,
    } : null });
  });
}
