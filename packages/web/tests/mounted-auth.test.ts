import { afterAll, expect, test } from "bun:test";
import { createClient } from "@libsql/client";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Hono } from "hono";
import { internalRequest } from "../src/deployment-path";

// Run in its own Bun process: auth options are deliberately initialized for a
// mounted deployment, separate from the standalone integration test process.
const dir = mkdtempSync(join(tmpdir(), "redx-mounted-auth-"));
Object.assign(process.env, {
  DATABASE_URL: `file:${join(dir, "fixture.db")}`,
  BETTER_AUTH_SECRET: "00000000000000000000000000000000",
  CREDENTIAL_SECRET: "11111111111111111111111111111111",
  REDX_BASE_PATH: "/host", REDX_PUBLIC_URL: "https://red-xai.test/host",
  WEBSITE_URL: "https://red-xai.test/host", REDX_HUB_ORIGINS: "https://red-xai.test",
  REDX_ACCOUNTS_URL: "http://127.0.0.1:9/v1/accounts",
  OWNER_EMAIL: "owner@example.invalid", REDX_ALLOW_OWNER_BOOTSTRAP: "false", NODE_ENV: "test",
});
const sql = createClient({ url: process.env.DATABASE_URL! });
await sql.executeMultiple(`
  CREATE TABLE user (id text PRIMARY KEY,name text NOT NULL,email text NOT NULL UNIQUE,redx_account_id text UNIQUE,email_verified integer NOT NULL DEFAULT 0,image text,created_at integer NOT NULL,updated_at integer NOT NULL);
  CREATE TABLE session (id text PRIMARY KEY,expires_at integer NOT NULL,token text NOT NULL UNIQUE,created_at integer NOT NULL,updated_at integer NOT NULL,ip_address text,user_agent text,user_id text NOT NULL);
  CREATE TABLE account (id text PRIMARY KEY,account_id text NOT NULL,provider_id text NOT NULL,user_id text NOT NULL,access_token text,refresh_token text,id_token text,access_token_expires_at integer,refresh_token_expires_at integer,scope text,password text,created_at integer NOT NULL,updated_at integer NOT NULL);
  CREATE TABLE verification (id text PRIMARY KEY,identifier text NOT NULL,value text NOT NULL,expires_at integer NOT NULL,created_at integer NOT NULL,updated_at integer NOT NULL);
`);
afterAll(() => sql.close());

const { registerAccountsRoutes } = await import("../src/api/accounts-http");
const { registerAuthRoutes } = await import("../src/api/auth-http");
const app = new Hono();
registerAccountsRoutes(app, { fetch: async (input, init) => {
  if (input.endsWith("/login")) {
    const body = JSON.parse(String(init.body));
    return Response.json({ token: String(body.login).split("@")[0] });
  }
  if (input.endsWith("/me")) {
    const name = new Headers(init.headers).get("x-redx-session")!;
    return Response.json({ account: { id: `acct-${name}`, email: `${name}@example.invalid`, name } });
  }
  return Response.json({ error: "unexpected" }, { status: 404 });
} });
registerAuthRoutes(app);

async function call(path: string, options: { cookie?: string; body?: unknown } = {}) {
  const request = new Request(`https://red-xai.test${path}`, {
    method: options.body ? "POST" : "GET",
    headers: { origin: "https://red-xai.test", "content-type": "application/json", ...(options.cookie ? { cookie: options.cookie } : {}) },
    body: options.body ? JSON.stringify(options.body) : undefined,
  });
  const inner = internalRequest(request, "/host");
  return inner ? await app.fetch(inner) : new Response("Not found", { status: 404 });
}

test("mounted Accounts login creates distinct sessions without touching website cookies", async () => {
  const cookies: string[] = [];
  for (const name of ["alpha", "beta"]) {
    const result = await call("/host/api/accounts/login", { body: { login: `${name}@example.invalid`, password: "synthetic-upstream-only" } });
    expect(result.status).toBe(200);
    const set = result.headers.get("set-cookie")!;
    expect(set).toContain("redx-host.session_token=");
    expect(set).not.toContain("legacy_site_cookie");
    const cookie = set.split(";")[0];
    cookies.push(cookie);
    const session = await call("/host/api/auth/get-session", { cookie: cookie + "; legacy_site_cookie=synthetic" });
    expect(session.status).toBe(200);
    expect((await session.json()).user.email).toBe(`${name}@example.invalid`);
  }
  expect(cookies[0]).not.toBe(cookies[1]);
  const identities = await sql.execute("SELECT redx_account_id FROM user ORDER BY redx_account_id");
  expect(identities.rows.map(row => row.redx_account_id)).toEqual(["acct-alpha", "acct-beta"]);
  const signedOut = await call("/host/api/auth/sign-out", { cookie: cookies[1], body: {} });
  expect(signedOut.status).toBe(200);
  expect(signedOut.headers.get("set-cookie")).not.toContain("legacy_site_cookie");
  expect(await (await call("/host/api/auth/get-session", { cookie: cookies[1] })).json()).toBeNull();
  expect((await (await call("/host/api/auth/get-session", { cookie: cookies[0] })).json()).user.email).toBe("alpha@example.invalid");
  expect((await call("/api/auth/get-session")).status).toBe(404);
});

test("published first-run local owner registration is disabled", async () => {
  const response = await call("/host/api/auth/sign-up/email", { body: { name: "Owner", email: "owner@example.invalid", password: "Synthetic-private-owner-42!", emailVerified: true } });
  expect(response.status).toBe(403);
  expect((await response.json()).code).toBe("OWNER_BOOTSTRAP_DISABLED");
  expect((await sql.execute("SELECT count(*) AS count FROM user WHERE email='owner@example.invalid'")).rows[0].count).toBe(0);
});

test("explicit private owner setup remains available and existing password sign-in survives disabling setup", async () => {
  process.env.REDX_ALLOW_OWNER_BOOTSTRAP = "true";
  try {
    const created = await call("/host/api/auth/sign-up/email", { body: { name: "Owner", email: "owner@example.invalid", password: "Synthetic-private-owner-42!" } });
    expect(created.status).toBe(200);
  } finally { process.env.REDX_ALLOW_OWNER_BOOTSTRAP = "false"; }
  const signedIn = await call("/host/api/auth/sign-in/email", { body: { email: "owner@example.invalid", password: "Synthetic-private-owner-42!" } });
  expect(signedIn.status).toBe(200);
});
