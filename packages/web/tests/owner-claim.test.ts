// Owner-claim hardening, end to end: real InfectedNation + real panel processes, real SQLite.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { Database } from "bun:sqlite";
import { join } from "node:path";
import { NATION_SIGNUP, nationSealed, postJson, provisionOwner, startNation, startPanel, waitFor } from "./helpers";

const OWNERS = "owner@example.test,support@example.test";
const OWNER_ENV = { INFECTEDNATION_OWNER_EMAILS: OWNERS };
let nation: Awaited<ReturnType<typeof startNation>>;
let panel: Awaited<ReturnType<typeof startPanel>>;
const signIn = (email: string, password: string, extra: Record<string, string> = {}) =>
  postJson(`${panel.url}/api/auth/sign-in/email`, { email, password }, { origin: panel.url, ...extra });

beforeAll(async () => {
  nation = await startNation(OWNER_ENV);
  panel = await startPanel({ REDX_IDENTITY_SOURCE: "nation", INFECTEDNATION_URL: nation.url, OWNER_EMAILS: OWNERS });
  await waitFor(`${panel.url}/api/health`, panel);
}, 60_000);
afterAll(async () => { await Promise.all([panel?.stop(), nation?.stop()]); });

describe("attacker cannot claim owner", () => {
  test("public signup with either owner email is refused", async () => {
    for (const email of OWNERS.split(",")) {
      const r = await postJson(`${nation.url}/api/v1/signup/email`, NATION_SIGNUP({ email, username: "squat" + email.length }));
      expect(r.status).toBe(403);
    }
  });

  test("GRIM-VIRUS username signs up as a plain member, and the panel refuses it", async () => {
    const r = await postJson(`${nation.url}/api/v1/signup/email`, NATION_SIGNUP({ email: "grim@attacker.test", username: "GRIM-VIRUS" }));
    expect(r.status).toBe(201);
    expect(r.json.account.role).toBe("member");
    const me = await postJson(`${nation.url}/api/v1/access`, { token: r.json.token, appId: "infected-voices" });
    expect(me.json.owner).toBe(false);
    expect((await signIn("grim@attacker.test", "Str0ng!Passw0rd")).status).toBe(403);
  });

  test("a legacy self-granted owner row is demoted at boot and cannot reach the panel", async () => {
    // Simulate an old database where signup granted owner by email (no provisioning).
    const db = new Database(join(nation.dir, "nation.sqlite"));
    const hash = await Bun.password.hash("Str0ng!Passw0rd");
    db.run(
      `INSERT INTO accounts(id,username,email,password_hash,dob,first_name,last_name,use_type,terms_version,privacy_version,terms_accepted_at,status,created_at,updated_at,role)
       VALUES('nat_legacy','legacysquat','support@example.test',?,'2000-01-01','A','B','personal','2026-09-23','2026-09-23',0,'active',0,0,'owner')`,
      [hash],
    );
    db.close();
    const dir = nation.dir;
    await nation.stop();
    nation = await startNation(OWNER_ENV, dir);
    expect(nation.out()).toContain("demoted 1 unprovisioned owner");
    // The legacy plaintext row was sealed in place at boot (P13).
    {
      const d = new Database(join(nation.dir, "nation.sqlite"), { readonly: true });
      const r = d.query("SELECT username, email, first_name, password_hash, username_idx FROM accounts WHERE id='nat_legacy'").get() as any;
      d.close();
      expect(r.email).toBe((await nationSealed()).email("support@example.test"));
      for (const c of ["username", "first_name", "password_hash"]) expect(r[c]).toMatch(/^s1\./);
      expect(r.username_idx).toMatch(/^b1\./);
    }
    const login = await postJson(`${nation.url}/api/v1/login/email`, { appId: "redxai-host", email: "support@example.test", password: "Str0ng!Passw0rd" });
    expect(login.status).toBe(200);
    expect(login.json.account.role).toBe("member");
    // The panel still points at the old Nation port; restart it against the new one.
    await panel.stop();
    panel = await startPanel({ REDX_IDENTITY_SOURCE: "nation", INFECTEDNATION_URL: nation.url, OWNER_EMAILS: OWNERS });
    await waitFor(`${panel.url}/api/health`, panel);
    expect((await signIn("support@example.test", "Str0ng!Passw0rd")).status).toBe(403);
  }, 60_000);
});

describe("provisioned owners work", () => {
  test("provision-owner creates an owner; Nation and panel both accept it", async () => {
    await provisionOwner(nation.dir, "owner@example.test", "Own3r!Passw0rd-1", OWNER_ENV);
    const login = await postJson(`${nation.url}/api/v1/login/email`, { appId: "redxai-host", email: "owner@example.test", password: "Own3r!Passw0rd-1" });
    expect(login.status).toBe(200);
    expect(login.json.account.role).toBe("owner");
    await panel.stop();
    panel = await startPanel({ REDX_IDENTITY_SOURCE: "nation", INFECTEDNATION_URL: nation.url, OWNER_EMAILS: OWNERS });
    await waitFor(`${panel.url}/api/health`, panel);
    const r = await signIn("owner@example.test", "Own3r!Passw0rd-1");
    expect(r.status, r.text).toBe(200);
  }, 30_000);

  test("the second owner email works too, and re-provisioning resets the password", async () => {
    await provisionOwner(nation.dir, "support@example.test", "Supp0rt!Passw0rd-1", OWNER_ENV);
    expect((await postJson(`${nation.url}/api/v1/login/email`, { appId: "redxai-host", email: "support@example.test", password: "Supp0rt!Passw0rd-1" })).json.account.role).toBe("owner");
    await provisionOwner(nation.dir, "support@example.test", "Supp0rt!Passw0rd-2", OWNER_ENV);
    expect((await postJson(`${nation.url}/api/v1/login/email`, { appId: "redxai-host", email: "support@example.test", password: "Supp0rt!Passw0rd-1" })).status).toBe(401);
    expect((await postJson(`${nation.url}/api/v1/login/email`, { appId: "redxai-host", email: "support@example.test", password: "Supp0rt!Passw0rd-2" })).status).toBe(200);
  }, 30_000);

  test("owner status in the web client comes from the server: the second owner is an owner", async () => {
    const r = await signIn("support@example.test", "Supp0rt!Passw0rd-2");
    expect(r.status, r.text).toBe(200);
    const cookie = (r.headers.getSetCookie?.() ?? [r.headers.get("set-cookie") ?? ""]).map((c) => c.split(";")[0]).join("; ");
    const me = await postJson(`${panel.url}/api/rpc/me`, { json: {} }, { cookie });
    expect(me.status).toBe(200);
    expect(me.json.json).toEqual({ signedIn: true, email: "support@example.test", owner: true });
    const anon = await postJson(`${panel.url}/api/rpc/me`, { json: {} });
    expect(anon.json.json).toEqual({ signedIn: false, email: null, owner: false });
  }, 30_000);

  test("the web client keeps no owner allow-list of its own", async () => {
    const gate = await Bun.file(join(import.meta.dir, "../src/web/components/auth-gate.tsx")).text();
    const lib = await Bun.file(join(import.meta.dir, "../src/web/lib/auth.ts")).text();
    expect(gate).toContain("me.data?.owner");
    expect(gate).not.toMatch(/isOwner\(/);
    expect(lib).not.toMatch(/export function isOwner/);
  });

  test("provision-owner refuses an address that is not on the owner list", async () => {
    await expect(provisionOwner(nation.dir, "someone@else.test", "Str0ng!Passw0rd", OWNER_ENV)).rejects.toThrow(/owner emails/);
  });
});

describe("email verification on: owners must be verified", () => {
  test("a provisioned but unverified owner is refused by the panel until verified", async () => {
    const n2 = await startNation({ ...OWNER_ENV, INFECTEDNATION_EMAIL_VERIFICATION: "on" });
    const p2 = await startPanel({ REDX_IDENTITY_SOURCE: "nation", INFECTEDNATION_URL: n2.url, OWNER_EMAILS: OWNERS });
    try {
      await waitFor(`${p2.url}/api/health`, p2);
      await provisionOwner(n2.dir, "owner@example.test", "Own3r!Passw0rd-1", { ...OWNER_ENV, INFECTEDNATION_EMAIL_VERIFICATION: "on" });
      const s = () => postJson(`${p2.url}/api/auth/sign-in/email`, { email: "owner@example.test", password: "Own3r!Passw0rd-1" }, { origin: p2.url });
      expect((await s()).status).toBe(403);
      const db = new Database(join(n2.dir, "nation.sqlite"));
      db.run("UPDATE accounts SET email_verified=1 WHERE email=?", [(await nationSealed()).email("owner@example.test")]); // stands in for clicking the emailed link
      db.close();
      const ok = await s();
      expect(ok.status, ok.text).toBe(200);
    } finally {
      await Promise.all([p2.stop(), n2.stop()]);
    }
  }, 60_000);
});

describe("local accounts mode", () => {
  test("owner sign-up is only possible from the host itself", async () => {
    const p3 = await startPanel({ REDX_TRUSTED_IP_HEADER: "x-forwarded-for", OWNER_EMAILS: OWNERS });
    try {
      await waitFor(`${p3.url}/api/health`, p3);
      const body = { email: "owner@example.test", password: "Own3r!Passw0rd-1", name: "Owner" };
      const remote = await postJson(`${p3.url}/api/auth/sign-up/email`, body, { origin: p3.url, "x-forwarded-for": "203.0.113.9" });
      expect(remote.status).toBe(403);
      const local = await postJson(`${p3.url}/api/auth/sign-up/email`, body, { origin: p3.url });
      expect(local.status, local.text).toBe(200);
    } finally {
      await p3.stop();
    }
  }, 60_000);
});
