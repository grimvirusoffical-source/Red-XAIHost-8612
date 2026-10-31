import { createClient } from "@libsql/client";
import { describe, expect, test } from "bun:test";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const dir = mkdtempSync(join(tmpdir(), "redxaihost-accounts-"));
process.env.DATABASE_URL = `file:${join(dir, "panel.db")}`;
process.env.BETTER_AUTH_SECRET = "00000000000000000000000000000000";
process.env.CREDENTIAL_SECRET = "11111111111111111111111111111111";
process.env.OWNER_EMAIL = "owner@example.invalid";
process.env.REDX_HUB_ORIGINS = "https://red-xai.com";
process.env.REDX_HUB_URL = "https://red-xai.com";
process.env.WEBSITE_URL = "http://127.0.0.1:4200";
process.env.REDX_PUBLIC_URL = "";
process.env.NODE_ENV = "test";

const sql = createClient({ url: process.env.DATABASE_URL });
await sql.executeMultiple(`
  CREATE TABLE user (
    id text PRIMARY KEY,
    name text NOT NULL,
    email text NOT NULL UNIQUE,
    redx_account_id text UNIQUE,
    email_verified integer NOT NULL DEFAULT 0,
    image text,
    created_at integer NOT NULL,
    updated_at integer NOT NULL
  );
  CREATE TABLE session (
    id text PRIMARY KEY,
    expires_at integer NOT NULL,
    token text NOT NULL UNIQUE,
    created_at integer NOT NULL,
    updated_at integer NOT NULL,
    ip_address text,
    user_agent text,
    user_id text NOT NULL
  );
  CREATE TABLE account (
    id text PRIMARY KEY,
    account_id text NOT NULL,
    provider_id text NOT NULL,
    user_id text NOT NULL,
    access_token text,
    refresh_token text,
    id_token text,
    access_token_expires_at integer,
    refresh_token_expires_at integer,
    scope text,
    password text,
    created_at integer NOT NULL,
    updated_at integer NOT NULL
  );
  CREATE TABLE credentials (
    id text PRIMARY KEY,
    label text NOT NULL,
    secret_enc text NOT NULL,
    hint text,
    verify_status text NOT NULL DEFAULT 'unknown',
    verify_message text,
    verified_at integer,
    updated_at integer NOT NULL
  );
`);
await sql.execute({
  sql: "INSERT INTO credentials (id, label, secret_enc, hint, verify_status, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
  args: ["fixture-provider", "Fixture", "sealed-fixture", "none", "unknown", 1],
});

type Mode = "owner" | "member" | "two-factor" | "quick-missing" | "quick" | "down";
let mode: Mode = "owner";

const fake = Bun.serve({
  hostname: "127.0.0.1",
  port: 0,
  fetch(request) {
    if (mode === "down") return Response.json({ error: "down" }, { status: 503 });
    const route = new URL(request.url).pathname.replace(/^\/v1\/accounts\/?/, "");
    if (route === "me") {
      const member = mode === "member";
      return Response.json({
        account: {
          id: member ? "redx-member-account" : "redx-owner-account",
          email: member ? "member@example.invalid" : "owner@example.invalid",
          username: member ? "member" : "owner",
          name: member ? "Member" : "Owner",
        },
      });
    }
    if (route === "login") {
      if (mode === "two-factor") {
        return Response.json({ next: "2fa", token: "fixture-flow", methods: { totp: true, call: false } });
      }
      return Response.json({ token: "fixture-session" });
    }
    if (route === "login/2fa") return Response.json({ token: "fixture-session" });
    if (route === "quick-login/start") {
      if (mode === "quick-missing") return Response.json({ error: "not_found" }, { status: 404 });
      return Response.json({
        device_code: "fixture-device",
        user_code: "ABCD-EF",
        verification_url: "https://red-xai.com/quick-login?code=ABCD-EF",
        interval: 2,
        expires_in: 120,
      });
    }
    if (route === "quick-login/poll") return Response.json({ token: "fixture-session" });
    return Response.json({ error: "not_found" }, { status: 404 });
  },
});

process.env.REDX_ACCOUNTS_URL = `http://127.0.0.1:${fake.port}/v1/accounts`;

const { Hono } = await import("hono");
const { registerAccountsRoutes } = await import("./accounts-http.ts");
const { auth } = await import("./auth.ts");

const app = new Hono();
registerAccountsRoutes(app);

function post(path: string, body: unknown, headers: Record<string, string> = {}) {
  return app.fetch(new Request(`http://127.0.0.1:4200${path}`, {
    method: "POST",
    headers: { "content-type": "application/json", origin: "http://127.0.0.1:4200", ...headers },
    body: JSON.stringify(body),
  }));
}

async function savedFixture(): Promise<string> {
  const row = await sql.execute("SELECT secret_enc FROM credentials WHERE id = 'fixture-provider'");
  return String(row.rows[0]?.secret_enc ?? "");
}

describe("Red-XAI website account sessions", () => {
  test("Hub manifest is public and exposes same-site launch points", async () => {
    const response = await app.fetch(new Request("http://127.0.0.1:4200/api/hub"));
    expect(response.status).toBe(200);
    const json = await response.json();
    expect(json.launchProtocol).toBe("redxaihost");
    expect(json.quickLogin.poll).toContain("/api/accounts/quick-login/poll");
  });

  test("foreign origins cannot start sign-in", async () => {
    const response = await app.fetch(new Request("http://127.0.0.1:4200/api/accounts/login", {
      method: "POST",
      headers: { origin: "https://example.invalid", "content-type": "application/json" },
      body: JSON.stringify({ login: "owner@example.invalid", password: "fixture-value" }),
    }));
    expect(response.status).toBe(403);
  });

  test("owner account opens a persistent 30-day website session", async () => {
    mode = "owner";
    const response = await post("/api/accounts/login", {
      login: "owner@example.invalid",
      password: "fixture-value",
      trust: { mode: "30days" },
    });
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ ok: true });
    const cookie = response.headers.get("set-cookie") ?? "";
    expect(cookie.toLowerCase()).toContain("httponly");
    expect(cookie.toLowerCase()).toContain("max-age=2592000");
    const session = await auth.api.getSession({ headers: new Headers({ cookie: cookie.split(";")[0] ?? "" }) });
    expect(session?.user.email).toBe("owner@example.invalid");
  });

  test("member account gets a separate tenant identity and does not inherit saved provider data", async () => {
    mode = "member";
    const before = await sql.execute("SELECT COUNT(*) AS n FROM user");
    const response = await post("/api/accounts/login", {
      login: "member",
      password: "fixture-value",
      trust: { mode: "5days" },
    });
    expect(response.status).toBe(200);
    expect((response.headers.get("set-cookie") ?? "").toLowerCase()).toContain("max-age=432000");
    const after = await sql.execute("SELECT COUNT(*) AS n FROM user");
    expect(Number(after.rows[0]?.n)).toBe(Number(before.rows[0]?.n) + 1);
    const identity = await sql.execute("SELECT redx_account_id FROM user WHERE email = 'member@example.invalid'");
    expect(identity.rows[0]?.redx_account_id).toBe("redx-member-account");
    expect(await savedFixture()).toBe("sealed-fixture");
  });

  test("session-only login is non-persistent and invalid custom duration is rejected", async () => {
    mode = "owner";
    const transient = await post("/api/accounts/login", {
      login: "owner@example.invalid",
      password: "fixture-value",
      trust: { mode: "no" },
    });
    expect(transient.status).toBe(200);
    expect((transient.headers.get("set-cookie") ?? "").toLowerCase()).not.toContain("max-age=");

    const invalid = await post("/api/accounts/login", {
      login: "owner@example.invalid",
      password: "fixture-value",
      trust: { mode: "custom", days: 3651 },
    });
    expect(invalid.status).toBe(400);
  });

  test("two-factor completion keeps the selected website duration", async () => {
    mode = "two-factor";
    const started = await post("/api/accounts/login", {
      login: "owner@example.invalid",
      password: "fixture-value",
      trust: { mode: "20days" },
    });
    const flow = await started.json();
    expect(flow.next).toBe("2fa");
    const finished = await post("/api/accounts/login/2fa", {
      method: "totp",
      code: "123456",
      trust: { mode: "20days" },
    }, { "x-redx-flow": String(flow.token) });
    expect(finished.status).toBe(200);
    expect((finished.headers.get("set-cookie") ?? "").toLowerCase()).toContain("max-age=1728000");
  });

  test("Quick Login can be unavailable or finish a website session", async () => {
    mode = "quick-missing";
    expect((await post("/api/accounts/quick-login/start", {})).status).toBe(404);
    mode = "quick";
    const started = await post("/api/accounts/quick-login/start", {});
    const quick = await started.json();
    const polled = await post("/api/accounts/quick-login/poll", {
      deviceCode: quick.deviceCode,
      trust: { mode: "30days" },
    });
    expect(polled.status).toBe(200);
    expect(await polled.json()).toEqual({ ok: true });
  });

  test("accounts outage leaves local owner sign-in available as fallback", async () => {
    process.env.REDX_ACCOUNTS_URL = "http://127.0.0.1:1/v1/accounts";
    const response = await post("/api/accounts/login", {
      login: "owner@example.invalid",
      password: "fixture-value",
      trust: { mode: "30days" },
    });
    expect(response.status).toBe(503);
    const body = await response.json();
    expect(String(body.message)).toContain("local owner");
    process.env.REDX_ACCOUNTS_URL = `http://127.0.0.1:${fake.port}/v1/accounts`;
  });
});
