import { createClient } from "@libsql/client";
import { describe, expect, test } from "bun:test";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const dir = mkdtempSync(join(tmpdir(), "redxaihost-accounts-"));
const dbFile = join(dir, "panel.db");
process.env.DATABASE_URL = `file:${dbFile}`;
process.env.BETTER_AUTH_SECRET = "test-better-auth-secret-32b!!";
process.env.CREDENTIAL_SECRET = "test-credential-secret-32bytes!!";
process.env.OWNER_EMAIL = "owner@example.com";
process.env.REDX_ACCOUNTS_URL = "http://127.0.0.1:9/v1/accounts";
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

const { Hono } = await import("hono");
const { registerAccountsRoutes } = await import("./accounts-http.ts");
const { auth } = await import("./auth.ts");
const { encryptJson, decryptJson } = await import("./lib/crypto.ts");

const ciphertext = encryptJson({ apiKey: "sk-test-do-not-migrate" });
await sql.execute({
  sql: "INSERT INTO credentials (id, label, secret_enc, hint, verify_status, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
  args: ["openai", "OpenAI", ciphertext, "rate", "unknown", 1],
});

type Mode = "session" | "other" | "2fa" | "quick-missing" | "quick" | "down";
let mode: Mode = "session";

const fake = Bun.serve({
  hostname: "127.0.0.1",
  port: 0,
  fetch(request) {
    if (mode === "down") return Response.json({ error: "down" }, { status: 503 });
    const url = new URL(request.url);
    const route = url.pathname.replace(/^\/v1\/accounts\/?/, "");
    if (route === "me") {
      const email = mode === "other" ? "other@example.com" : "owner@example.com";
      return Response.json({ account: { email, username: "owner", name: "Owner" } });
    }
    if (route === "login") {
      if (mode === "2fa") return Response.json({ next: "2fa", token: "flow-token", methods: { totp: true, call: false } });
      return Response.json({ token: "accounts-session-token" });
    }
    if (route === "login/2fa") {
      return Response.json({ token: "accounts-session-token" });
    }
    if (route === "quick-login/start") {
      if (mode === "quick-missing") return Response.json({ error: "not_found" }, { status: 404 });
      return Response.json({
        device_code: "device-1",
        user_code: "ABCD-EF",
        verification_url: "https://red-xai.com/quick-login?code=ABCD-EF",
        interval: 2,
        expires_in: 120,
      });
    }
    if (route === "quick-login/poll") {
      return Response.json({ token: "accounts-session-token" });
    }
    return Response.json({ error: "not_found" }, { status: 404 });
  },
});

process.env.REDX_ACCOUNTS_URL = `http://127.0.0.1:${fake.port}/v1/accounts`;

const app = new Hono();
registerAccountsRoutes(app);

function post(path: string, body: unknown, headers: Record<string, string> = {}) {
  return app.fetch(
    new Request(`http://127.0.0.1:4200${path}`, {
      method: "POST",
      headers: { "content-type": "application/json", origin: "http://127.0.0.1:4200", ...headers },
      body: JSON.stringify(body),
    }),
  );
}

async function credentialBlob(): Promise<string> {
  const row = await sql.execute("SELECT secret_enc FROM credentials WHERE id = 'openai'");
  return String(row.rows[0]?.secret_enc ?? "");
}

describe("Red-XAI Accounts on the panel", () => {
  test("manifest is public and names the hub launch points", async () => {
    const response = await app.fetch(new Request("http://127.0.0.1:4200/api/hub"));
    expect(response.status).toBe(200);
    const json = await response.json();
    expect(json.launchProtocol).toBe("redxaihost");
    expect(json.embedUrl).toBe("http://127.0.0.1:4200/embed");
    expect(json.quickLogin.poll).toContain("/api/accounts/quick-login/poll");
  });

  test("a foreign origin cannot start sign-in", async () => {
    const response = await app.fetch(
      new Request("http://127.0.0.1:4200/api/accounts/login", {
        method: "POST",
        headers: { origin: "https://evil.example", "content-type": "application/json" },
        body: JSON.stringify({ login: "owner@example.com", password: "secret-secret" }),
      }),
    );
    expect(response.status).toBe(403);
  });

  test("owner accounts sign-in opens a panel session and leaves stored credentials alone", async () => {
    mode = "session";
    const response = await post("/api/accounts/login", { login: "owner@example.com", password: "correct-password" });
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body).toEqual({ ok: true });
    expect(JSON.stringify(body)).not.toContain("accounts-session-token");
    const setCookie = response.headers.get("set-cookie") ?? "";
    expect(setCookie.toLowerCase()).toContain("httponly");
    const session = await auth.api.getSession({ headers: new Headers({ cookie: setCookie.split(";")[0] ?? "" }) });
    expect(session?.user.email).toBe("owner@example.com");

    await sql.execute({
      sql: "INSERT INTO account (id, account_id, provider_id, user_id, password, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
      args: ["local-1", "local-1", "credential", session?.user.id ?? "", "sentinel-hash", 1, 1],
    });
    const again = await post("/api/accounts/login", { login: "owner", password: "correct-password" });
    expect(again.status).toBe(200);
    const password = await sql.execute("SELECT password FROM account WHERE id = 'local-1'");
    expect(password.rows[0]?.password).toBe("sentinel-hash");
    expect(await credentialBlob()).toBe(ciphertext);
    expect(decryptJson(ciphertext)).toEqual({ apiKey: "sk-test-do-not-migrate" });
  });

  test("a non-owner Red-XAI account does not get a panel user", async () => {
    mode = "other";
    const before = await sql.execute("SELECT COUNT(*) AS n FROM user");
    const response = await post("/api/accounts/login", { login: "someone", password: "correct-password" });
    expect(response.status).toBe(403);
    const after = await sql.execute("SELECT COUNT(*) AS n FROM user");
    expect(after.rows[0]?.n).toBe(before.rows[0]?.n);
    expect(await credentialBlob()).toBe(ciphertext);
  });

  test("two-step sign-in still ends in a panel session", async () => {
    mode = "2fa";
    const started = await post("/api/accounts/login", { login: "owner@example.com", password: "correct-password" });
    const flow = await started.json();
    expect(flow.next).toBe("2fa");
    const finished = await post(
      "/api/accounts/login/2fa",
      { method: "totp", code: "123456" },
      { "x-redx-flow": String(flow.token) },
    );
    expect(finished.status).toBe(200);
    expect(await finished.json()).toEqual({ ok: true });
  });

  test("quick login uses the accounts host and can be missing", async () => {
    mode = "quick-missing";
    const missing = await post("/api/accounts/quick-login/start", {});
    expect(missing.status).toBe(404);
    mode = "quick";
    const started = await post("/api/accounts/quick-login/start", {});
    expect(started.status).toBe(200);
    const quick = await started.json();
    expect(quick.userCode).toBe("ABCD-EF");
    const polled = await post("/api/accounts/quick-login/poll", { deviceCode: quick.deviceCode });
    expect(polled.status).toBe(200);
    expect(await polled.json()).toEqual({ ok: true });
    expect(await credentialBlob()).toBe(ciphertext);
  });

  test("an unreachable accounts host leaves local sign-in as the fallback", async () => {
    process.env.REDX_ACCOUNTS_URL = "http://127.0.0.1:1/v1/accounts";
    const response = await post("/api/accounts/login", { login: "owner@example.com", password: "correct-password" });
    expect(response.status).toBe(503);
    const body = await response.json();
    expect(String(body.message)).toContain("local owner");
    process.env.REDX_ACCOUNTS_URL = `http://127.0.0.1:${fake.port}/v1/accounts`;
  });
});
