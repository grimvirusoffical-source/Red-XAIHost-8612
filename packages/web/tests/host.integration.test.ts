import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { Database } from "bun:sqlite";
import { join } from "node:path";
import {
  NATION_SIGNUP,
  postJson,
  provisionOwner,
  rpc,
  startNation,
  startPanel,
  startRedxd,
  waitFor,
} from "./helpers";

let redxd: Awaited<ReturnType<typeof startRedxd>>;
let nation: Awaited<ReturnType<typeof startNation>>;
let panel: Awaited<ReturnType<typeof startPanel>>;
let cookie = "";

const OWNER = NATION_SIGNUP();

async function signIn(email: string, password: string) {
  return postJson(
    `${panel.url}/api/auth/sign-in/email`,
    { email, password },
    { origin: panel.url },
  );
}

beforeAll(async () => {
  redxd = await startRedxd();
  nation = await startNation({ INFECTEDNATION_OWNER_EMAILS: OWNER.email });
  // Public signup with an owner address is refused; owners are provisioned on the host.
  const signup = await postJson(`${nation.url}/api/v1/signup/email`, OWNER);
  expect(signup.status).toBe(403);
  await provisionOwner(nation.dir, OWNER.email, OWNER.password, { INFECTEDNATION_OWNER_EMAILS: OWNER.email });
  panel = await startPanel({
    REDX_IDENTITY_SOURCE: "nation",
    INFECTEDNATION_URL: nation.url,
    REDX_DB_URL: redxd.url,
    REDX_DB_KEY: redxd.key,
  });
  await waitFor(`${panel.url}/api/health`, panel);
}, 60_000);

afterAll(async () => {
  await Promise.all([panel?.stop(), nation?.stop(), redxd?.stop()]);
});

describe("boot", () => {
  test("fresh database is created by versioned migrations", () => {
    const db = new Database(join(panel.dir, "panel.db"), { readonly: true });
    const tables = db.query("SELECT name FROM sqlite_master WHERE type='table'").all().map((r: any) => r.name);
    expect(tables).toContain("__drizzle_migrations");
    expect(tables).toContain("projects");
    const cols = db.query("PRAGMA table_info(app_builds)").all().map((r: any) => r.name);
    expect(cols).toEqual(expect.arrayContaining(["run_id", "head_sha", "dispatched_at"]));
    expect((db.query("SELECT count(*) AS n FROM __drizzle_migrations").get() as any).n).toBe(2);
    db.close();
  });

  test("local worker is off for REDX_AUTO_LOCAL_NODE=off", () => {
    expect(panel.out()).not.toContain("[local-worker] starting");
  });
});

describe("identity link (InfectedNation)", () => {
  test("wrong password is rejected by Nation", async () => {
    const r = await signIn(OWNER.email, "Wr0ng!Password");
    expect(r.status).toBe(401);
    expect(r.text).toContain("incorrect");
  });

  test("local sign-up is closed", async () => {
    const r = await postJson(
      `${panel.url}/api/auth/sign-up/email`,
      { email: "other@example.test", password: "Another!Passw0rd1", name: "x" },
      { origin: panel.url },
    );
    expect(r.status).toBe(403);
  });

  test("Nation credentials sign in to the panel and give an owner session", async () => {
    const r = await signIn(OWNER.email, OWNER.password);
    expect(r.status).toBe(200);
    cookie = (r.headers.getSetCookie?.() ?? [r.headers.get("set-cookie") ?? ""])
      .map((c) => c.split(";")[0])
      .join("; ");
    expect(cookie).toContain("session_token");
    const s = await fetch(`${panel.url}/api/auth/get-session`, { headers: { cookie } });
    expect((await s.json()).user.email).toBe(OWNER.email);
  });

  test("a password change in Nation is honoured (local copy follows Nation)", { timeout: 20_000 }, async () => {
    const newPassword = "N3w!Passw0rd-xyz";
    await Bun.sleep(11_000); // Better Auth allows 3 sign-ins per 10 s per IP
    // Change it in Nation directly, as an account owner would.
    const db = new Database(join(nation.dir, "nation.sqlite"));
    db.run("UPDATE accounts SET password_hash=? WHERE email=?", [await Bun.password.hash(newPassword), OWNER.email]);
    db.close();
    expect((await signIn(OWNER.email, OWNER.password)).status).toBe(401);
    const r = await signIn(OWNER.email, newPassword);
    expect(r.status, r.text).toBe(200);
  });
});


describe("RedX DB link", () => {
  test("redxdb procedures need an owner session", async () => {
    expect((await rpc(panel.url, "redxdb/status", {})).status).toBe(401);
    expect((await rpc(panel.url, "redxdb/listDatabases", {})).status).toBe(401);
  });

  test("status reaches the scratch redxd with the admin key", async () => {
    const r = await rpc(panel.url, "redxdb/status", {}, cookie);
    expect(r.status).toBe(200);
    expect(r.json.json.reachable).toBe(true);
    expect(r.json.json.keyConfigured).toBe(true);
    expect(r.json.json.health.status).toBe("ok");
    expect(r.json.json.adminError).toBeNull();
    expect(r.json.json.admin).toBeTruthy();
  });

  test("listDatabases returns the server's databases", async () => {
    const r = await rpc(panel.url, "redxdb/listDatabases", {}, cookie);
    expect(r.status).toBe(200);
    expect(JSON.stringify(r.json.json.databases)).toContain("default");
  });


  test("Database UI route is served by the SPA shell", async () => {
    const r = await fetch(panel.url + "/db");
    expect(r.status).toBe(200);
    const html = await r.text();
    expect(html).toMatch(/root|RedX|script/i);
  });

  test("a key saved through Settings is stored encrypted and used", async () => {
    const save = await rpc(panel.url, "settings/saveProvider", { provider: "redxdb", values: { apiKey: redxd.key, url: redxd.url } }, cookie);
    expect(save.status).toBe(200);
    const db = new Database(join(panel.dir, "panel.db"), { readonly: true });
    const row = db.query("SELECT secret_enc FROM credentials WHERE id='redxdb'").get() as any;
    db.close();
    expect(row.secret_enc).not.toContain(redxd.key);
    const r = await rpc(panel.url, "redxdb/listDatabases", {}, cookie);
    expect(r.status).toBe(200);
  });
});

describe("projects", () => {
  test("update of a nonexistent project is NOT_FOUND", async () => {
    const r = await rpc(panel.url, "projects/update", { id: "prj_does_not_exist", name: "x" }, cookie);
    expect(r.status).toBe(404);
    expect(r.json.json.code).toBe("NOT_FOUND");
  });
});

describe("client IP", () => {
  test("spoofed forwarding headers do not get a fresh rate-limit bucket", async () => {
    const hit = (h: Record<string, string>) =>
      fetch(`${panel.url}/api/rpc/ping`, { method: "POST", headers: { "content-type": "application/json", ...h }, body: '{"json":{}}' });
    const a = Number((await hit({ "cf-connecting-ip": "203.0.113.1" })).headers.get("x-ratelimit-remaining"));
    const b = Number((await hit({ "cf-connecting-ip": "203.0.113.2", "x-forwarded-for": "198.51.100.9" })).headers.get("x-ratelimit-remaining"));
    expect(b, `a=${a} b=${b}`).toBe(a - 1);
  });
});

describe("InfectedNation service", () => {
  test("binds loopback by default", () => {
    expect(nation.out()).toContain("listening on http://127.0.0.1:");
  });
});

