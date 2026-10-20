import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { Database } from "bun:sqlite";
import { existsSync, readFileSync } from "node:fs";
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
  nationSealed,
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
    const ns = await nationSealed();
    const { id } = db.query("SELECT id FROM accounts WHERE email=?").get(ns.email(OWNER.email)) as any;
    db.run("UPDATE accounts SET password_hash=? WHERE id=?", [ns.seal("accounts", "password_hash", id, await Bun.password.hash(newPassword)), id]);
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

  test("sign-in from a foreign Origin is refused (Origin header is never trusted)", async () => {
    const r = await postJson(
      `${panel.url}/api/auth/sign-in/email`,
      { email: OWNER.email, password: OWNER.password },
      // A cross-site browser request carries the victim's cookies and the attacker's Origin.
      { origin: "https://evil.example", cookie },
    );
    expect(r.status).toBe(403);
    expect(r.headers.get("set-cookie") ?? "").not.toContain("session");
  });

  test("a key saved through Settings is stored encrypted and used", async () => {
    const save = await rpc(panel.url, "settings/saveProvider", { provider: "redxdb", values: { apiKey: redxd.key, url: redxd.url } }, cookie);
    expect(save.status).toBe(200);
    const db = new Database(join(panel.dir, "panel.db"), { readonly: true });
    const row = db.query("SELECT secret_enc FROM credentials WHERE id='redxdb'").get() as any;
    db.close();
    expect(row.secret_enc).not.toContain(redxd.key);
    expect(row.secret_enc.startsWith("v4.")).toBe(true);
    const r = await rpc(panel.url, "redxdb/listDatabases", {}, cookie);
    expect(r.status).toBe(200);
  });

  test("an old AES credential row is refused and asks to be entered again", async () => {
    // A real AES-256-GCM v2 blob from before the switch: it must never be decrypted.
    const V2_AES = "v2.CQkJCQkJCQkJCQkJCQkJCQ.BwcHBwcHBwcHBwcH.iWBwVTb0EL2AjNTUhfo.U35moHs09A-vVm-h1wO80w";
    const db = new Database(join(panel.dir, "panel.db"));
    db.query("INSERT INTO credentials (id, label, secret_enc, hint, verify_status, updated_at) VALUES ('redxdb', 'RedX DB', ?, NULL, 'valid', 0) ON CONFLICT(id) DO UPDATE SET secret_enc = excluded.secret_enc, verify_status = 'valid'").run(V2_AES);
    db.close();
    const v = await rpc(panel.url, "settings/verifyProvider", { provider: "redxdb" }, cookie);
    expect(v.status).toBe(200);
    expect(v.json.json.ok).toBe(false);
    const list = await rpc(panel.url, "settings/providers", {}, cookie);
    const expo = (list.json.json as any[]).find((x) => x.id === "redxdb");
    expect(expo.verifyStatus).toBe("invalid");
    expect(expo.verifyMessage).toContain("old AES format");
    // Entering it again replaces the row with a v3 blob.
    expect((await rpc(panel.url, "settings/saveProvider", { provider: "redxdb", values: { apiKey: redxd.key, url: redxd.url } }, cookie)).status).toBe(200);
    const db2 = new Database(join(panel.dir, "panel.db"), { readonly: true });
    const row = db2.query("SELECT secret_enc FROM credentials WHERE id='redxdb'").get() as any;
    db2.close();
    expect(row.secret_enc.startsWith("v4.")).toBe(true);
  });

  test("a read-scope key is enough for everything the panel uses", async () => {
    const mint = await fetch(`${redxd.url}/v1/keys`, {
      method: "POST",
      headers: { authorization: `Bearer ${redxd.key}`, "content-type": "application/json", "x-redx-confirm": "createKey" },
      body: JSON.stringify({ db: "*", scope: "read", label: "panel-read" }),
    });
    expect(mint.status).toBeLessThan(300);
    const readKey = ((await mint.json()) as { key: string }).key;
    expect(readKey).toMatch(/^rx_live_/);
    const save = await rpc(panel.url, "settings/saveProvider", { provider: "redxdb", values: { apiKey: readKey, url: redxd.url } }, cookie);
    expect(save.status).toBe(200);
    const st = await rpc(panel.url, "redxdb/status", {}, cookie);
    expect(st.status).toBe(200);
    expect(st.json.json.reachable).toBe(true);
    expect(st.json.json.adminError).toBeNull();
    expect(st.json.json.admin).toBeNull();
    expect(st.json.json.adminNote).toContain("read-scope");
    const list = await rpc(panel.url, "redxdb/listDatabases", {}, cookie);
    expect(list.status).toBe(200);
    expect(JSON.stringify(list.json.json.databases)).toContain("default");
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


describe("sensitive columns at rest (P13)", () => {
  const MARK = {
    description: "desc-marker-7f3a9c Confidential roadmap",
    gitUrl: "https://git.example.test/secret-repo-91b2e4.git",
    env: "STRIPE_SECRET=sk_live_marker_c0ffee1234567890",
  };
  const rawBytes = (dir: string, name: string) =>
    ["", "-wal", "-shm", "-journal"]
      .map((s) => join(dir, name + s))
      .filter((f) => existsSync(f))
      .map((f) => readFileSync(f).toString("latin1"))
      .join("\n");

  test("after realistic use no known secret appears in the raw files, WAL or sqlite_master", async () => {
    const created = await rpc(panel.url, "projects/create", { name: "AtRest", description: MARK.description, sourceKind: "git", gitUrl: MARK.gitUrl, envVars: MARK.env }, cookie);
    expect(created.status, created.text).toBe(200);
    const got = await rpc(panel.url, "projects/get", { id: created.json.json.id }, cookie);
    expect(got.status).toBe(200);
    expect(got.json.json.project.description).toBe(MARK.description); // round trip through the sealed columns
    expect(got.json.json.project.gitUrl).toBe(MARK.gitUrl);
    expect(got.json.json.project.envVars).toBe(MARK.env);
    const nl = await postJson(`${nation.url}/api/v1/login/email`, { appId: "redxai-host", email: OWNER.email, password: "N3w!Passw0rd-xyz" });
    expect(nl.status, nl.text).toBe(200);
    const sessionToken = decodeURIComponent(/session_token=([^;]+)/.exec(cookie)![1]!).split(".")[0]!;
    const needles = [
      OWNER.email, OWNER.username, OWNER.dob, redxd.key, sessionToken, nl.json.token,
      MARK.description, MARK.gitUrl, MARK.env, "sk_live_marker", "secret-repo-91b2e4", "desc-marker-7f3a9c",
    ];
    for (const n of needles) expect(n.length).toBeGreaterThan(6);
    // Scan while both servers are live (WAL may hold recent pages) ...
    const live = rawBytes(panel.dir, "panel.db") + rawBytes(nation.dir, "nation.sqlite");
    for (const n of needles) expect(live.includes(n), `live file contains ${n}`).toBe(false);
    const p = new Database(join(panel.dir, "panel.db"), { readonly: true });
    const n2 = new Database(join(nation.dir, "nation.sqlite"), { readonly: true });
    const schema = JSON.stringify([p.query("SELECT * FROM sqlite_master").all(), n2.query("SELECT * FROM sqlite_master").all()]);
    for (const n of needles) expect(schema.includes(n), `sqlite_master contains ${n}`).toBe(false);
    // ... every sealed column really holds sealed values, and the projects row is not plaintext.
    const prj = p.query("SELECT description, git_url, env_vars FROM projects WHERE id=?").get(created.json.json.id) as any;
    for (const v of Object.values(prj)) expect(String(v)).toMatch(/^s1\./);
    const usr = p.query("SELECT email, name FROM user").get() as any;
    expect(usr.email).toMatch(/^d1\./);
    expect(usr.name).toMatch(/^s1\./);
    const ses = p.query("SELECT token FROM session").all() as any[];
    for (const s of ses) expect(s.token).toMatch(/^d1\./);
    const acc = n2.query("SELECT email, username, first_name, password_hash, username_idx FROM accounts").all() as any[];
    for (const a of acc) {
      expect(a.email).toMatch(/^d1\./);
      for (const c of ["username", "first_name", "password_hash"]) expect(a[c]).toMatch(/^s1\./);
      expect(a.username_idx).toMatch(/^b1\.[0-9a-f]{64}$/);
    }
    for (const s of n2.query("SELECT token_hash, ip, user_agent FROM sessions").all() as any[]) {
      expect(s.token_hash).toMatch(/^b1\./);
      expect(s.ip).toMatch(/^s1\./);
    }
    p.close(); n2.close();
  }, 30_000);

  test("a sealed value moved to another column (panel) or another row (Nation) is refused", async () => {
    const created = await rpc(panel.url, "projects/create", { name: "Swap", description: "swap-desc-a1b2c3", gitUrl: "https://git.example.test/swap.git", sourceKind: "git" }, cookie);
    const id = created.json.json.id;
    const pdb = new Database(join(panel.dir, "panel.db"));
    pdb.query("UPDATE projects SET git_url = description WHERE id=?").run(id); // column swap
    pdb.close();
    expect((await rpc(panel.url, "projects/get", { id }, cookie)).status).toBe(500);
    // Nation: give a second account the owner's sealed first name. Row-bound AD refuses it on read.
    const su = await postJson(`${nation.url}/api/v1/signup/email`, NATION_SIGNUP({ username: "swapuser1", email: "swap@example.test" }));
    expect(su.status, su.text).toBe(201);
    const ndb = new Database(join(nation.dir, "nation.sqlite"));
    const ns = await nationSealed();
    const ownerRow = ndb.query("SELECT first_name FROM accounts WHERE email=?").get(ns.email(OWNER.email)) as any;
    ndb.query("UPDATE accounts SET first_name=? WHERE id=?").run(ownerRow.first_name, su.json.account.accountId);
    ndb.close();
    expect((await postJson(`${nation.url}/api/v1/session/me`, { token: su.json.token })).status).toBe(500);
  }, 30_000);
});
