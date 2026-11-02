import { expect, test } from "bun:test";
import { createApp } from "./__core/app";

test("website-cookie RPC writes reject absent and untrusted origins before dispatch", async () => {
  const previous = process.env.REDX_WEBSITE_SESSION;
  try {
    process.env.REDX_WEBSITE_SESSION = "true";
    const app = createApp({});
    for (const origin of [undefined, "https://untrusted.example.invalid", "null"]) {
      const response = await app.request("/api/rpc/fixture", {
        method: "POST", headers: { cookie: "redx_session=synthetic", ...(origin ? { origin } : {}) },
      });
      expect(response.status).toBe(403);
    }
  } finally {
    if (previous === undefined) delete process.env.REDX_WEBSITE_SESSION;
    else process.env.REDX_WEBSITE_SESSION = previous;
  }
});

test("credentialed browser responses are restricted to configured origins", async () => {
  const previousPublic = process.env.REDX_PUBLIC_URL;
  const previousHub = process.env.REDX_HUB_ORIGINS;
  try {
    process.env.REDX_PUBLIC_URL = "https://panel.example.invalid";
    process.env.REDX_HUB_ORIGINS = "https://hub.example.invalid";
    const app = createApp({});
    for (const origin of ["https://panel.example.invalid", "https://hub.example.invalid"]) {
      const response = await app.request("/api/health", { headers: { origin } });
      expect(response.headers.get("access-control-allow-origin")).toBe(origin);
      expect(response.headers.get("access-control-allow-credentials")).toBe("true");
    }
    for (const origin of ["https://untrusted.example.invalid", "null", "http://localhost:4200"]) {
      const response = await app.request("/api/health", { headers: { origin } });
      expect(response.headers.get("access-control-allow-origin")).toBeNull();
    }
  } finally {
    if (previousPublic === undefined) delete process.env.REDX_PUBLIC_URL;
    else process.env.REDX_PUBLIC_URL = previousPublic;
    if (previousHub === undefined) delete process.env.REDX_HUB_ORIGINS;
    else process.env.REDX_HUB_ORIGINS = previousHub;
  }
});
