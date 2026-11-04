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
const { openOwnerSession } = await import("../src/api/lib/owner-session");
const { FRESH_ISSUER } = await import("../../../website/red-xai.com/identity-realm.mjs");
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

test("fresh realm cannot attach to a legacy same-email Host row or mint a local session", async () => {
  await sql.execute("INSERT INTO user (id,name,email,email_verified,created_at,updated_at) VALUES ('legacy-attachment-fixture','Legacy','legacy-attachment@example.invalid',1,1,1)");
  const beforeUsers=await sql.execute('SELECT count(*) AS n FROM user');
  const beforeSessions=await sql.execute('SELECT count(*) AS n FROM session');
  await expect(openOwnerSession({id:'acct-owner',email:'legacy-attachment@example.invalid',name:'same',issuer:FRESH_ISSUER})).rejects.toThrow('Fresh website identities cannot attach');
  expect((await sql.execute('SELECT count(*) AS n FROM user')).rows[0].n).toBe(beforeUsers.rows[0].n);
  expect((await sql.execute('SELECT count(*) AS n FROM session')).rows[0].n).toBe(beforeSessions.rows[0].n);
  expect((await sql.execute("SELECT redx_account_id FROM user WHERE id='legacy-attachment-fixture'")).rows[0].redx_account_id).toBeNull();
  await sql.execute("DELETE FROM user WHERE id='legacy-attachment-fixture'");
});

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

test("website-only Host identity follows live website revocation and ignores independent Host sessions", async () => {
  const originalFetch = globalThis.fetch;
  const previousMode = process.env.REDX_WEBSITE_SESSION;
  const previousOwners = process.env.REDX_WEBSITE_OWNER_ACCOUNT_IDS;
  let revoked = false;
  try {
    process.env.REDX_WEBSITE_SESSION = "true";
    process.env.REDX_WEBSITE_OWNER_ACCOUNT_IDS = "site-owner";
    globalThis.fetch = (async (url: string | URL | Request, init?: RequestInit) => {
      expect(String(url)).toBe("http://127.0.0.1:4200/api/accounts/me");
      const cookie = new Headers(init?.headers).get("cookie");
      if (revoked) return new Response(null, { status: 401 });
      const id = cookie === "redx_session=synthetic-owner" ? "site-owner" : "site-customer";
      return Response.json({ account: { id, email: "owner@example.invalid", username: id, roles: id === "site-owner" ? ["owner"] : [] },
        expiresMs: Date.now() + 60000, amr: ["pwd", "otp"] });
    }) as typeof fetch;
    const { resolveHostIdentity } = await import("../src/api/middleware/auth");
    const [owner, customer] = await Promise.all(["owner", "customer"].map(value =>
      resolveHostIdentity(new Headers({ cookie: `redx_session=synthetic-${value}`, "x-redx-account-id": "site-owner" }))));
    expect(owner.tenantId).toBe("owner");
    expect(owner.owner).toBe(true);
    expect(customer.tenantId).toBe("site-customer");
    expect(customer.owner).toBe(false);
    expect(customer.session.user.emailVerified).toBe(false);
    const local = await call("/host/api/auth/sign-in/email", { body: { email: "owner@example.invalid", password: "Synthetic-private-owner-42!" } });
    expect(local.status).toBe(200);
    const cookie = local.headers.get("set-cookie")!.split(";")[0];
    await expect(resolveHostIdentity(new Headers({ cookie }))).rejects.toThrow();
    revoked = true;
    await expect(resolveHostIdentity(new Headers({ cookie: `redx_session=synthetic-customer; ${cookie}` }))).rejects.toThrow();
  } finally {
    globalThis.fetch = originalFetch;
    if (previousMode === undefined) delete process.env.REDX_WEBSITE_SESSION;
    else process.env.REDX_WEBSITE_SESSION = previousMode;
    if (previousOwners === undefined) delete process.env.REDX_WEBSITE_OWNER_ACCOUNT_IDS;
    else process.env.REDX_WEBSITE_OWNER_ACCOUNT_IDS = previousOwners;
  }
});

test("realm-aware Host request context keeps same-subject/email fresh users outside legacy and owner tenants", async () => {
  const previous={fetch:globalThis.fetch,mode:process.env.REDX_WEBSITE_SESSION,realms:process.env.REDX_WEBSITE_SESSION_REALMS,owners:process.env.REDX_WEBSITE_OWNER_ACCOUNT_IDS};
  const {FRESH_COOKIE,FRESH_ISSUER,LEGACY_ISSUER,REALM_AUTHORITY,realmIdentity}=await import('../../../website/red-xai.com/identity-realm.mjs');
  const {HOST_TENANT_TABLES}=await import('../src/api/lib/realm-ownership');
  const {hostRealmOwnership}=await import('../src/api/lib/host-website-identity');
  for(const table of HOST_TENANT_TABLES)await sql.execute(`CREATE TABLE ${table}(id TEXT PRIMARY KEY,tenant_id TEXT NOT NULL)`);
  await hostRealmOwnership.initialize();
  try {
    process.env.REDX_WEBSITE_SESSION='true';process.env.REDX_WEBSITE_SESSION_REALMS='prototype-v1';process.env.REDX_WEBSITE_OWNER_ACCOUNT_IDS='same-subject';
    globalThis.fetch=(async(url,init)=>{
      expect(String(url)).toBe(REALM_AUTHORITY);
      const cookie=new Headers(init?.headers).get('cookie')!;
      const legacy=cookie.startsWith('redx_session='),issuer=legacy?LEGACY_ISSUER:FRESH_ISSUER,subject=cookie.endsWith('=b')?'other-subject':'same-subject';
      const p=realmIdentity(issuer,subject);
      return Response.json({identityRealm:{version:1,issuer,subject,canonicalId:p.id},account:{id:p.id,email:'owner@example.invalid',roles:legacy?['owner']:['user'],emailVerified:true,active:true},active:true,amr:legacy?['pwd','call']:['pwd'],expiresMs:Date.now()+60000});
    }) as typeof fetch;
    const {resolveHostIdentity}=await import('../src/api/middleware/auth');
    const forged={'x-redx-account-id':'same-subject','x-redx-role':'owner','x-redx-realm':LEGACY_ISSUER};
    const legacy=await resolveHostIdentity(new Headers({cookie:'redx_session=legacy',...forged}));
    const [a,b]=await Promise.all(['a','b'].map(v=>resolveHostIdentity(new Headers({cookie:`${FRESH_COOKIE}=${v}`,...forged}))));
    expect(legacy.tenantId).toBe('owner');expect(legacy.owner).toBe(true);
    expect(a.tenantId).toBe(realmIdentity(FRESH_ISSUER,'same-subject').ownershipKey);expect(a.tenantId).not.toBe('same-subject');expect(a.tenantId).not.toBe('owner');expect(a.owner).toBe(false);
    expect(b.tenantId).not.toBe(a.tenantId);expect(a.session.user.id).not.toBe(legacy.session.user.id);
  } finally {
    globalThis.fetch=previous.fetch;
    for(const [name,value] of [['REDX_WEBSITE_SESSION',previous.mode],['REDX_WEBSITE_SESSION_REALMS',previous.realms],['REDX_WEBSITE_OWNER_ACCOUNT_IDS',previous.owners]]) {if(value===undefined)delete process.env[name!];else process.env[name!]=value;}
  }
});
