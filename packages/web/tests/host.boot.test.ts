import { afterAll, describe, expect, test } from "bun:test";
import { Database } from "bun:sqlite";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { NATION_SIGNUP, freePort, postJson, rpc, startNation, startPanel, tempDir, waitFor, provisionOwner } from "./helpers";

const cleanup: (() => Promise<void>)[] = [];
afterAll(async () => {
  await Promise.all(cleanup.map((f) => f()));
});

describe("fail fast on missing secrets", () => {
  for (const name of ["BETTER_AUTH_SECRET", "CREDENTIAL_SECRET"]) {
    test(`exits 1 with a setup hint when ${name} is empty`, async () => {
      const p = await startPanel({ [name]: "  " });
      const code = await p.proc.exited;
      expect(code).toBe(1);
      expect(p.out()).toContain(name);
      expect(p.out()).toContain("bun run setup:selfhost");
    });
  }
});

describe("migrations", () => {
  test("a database created by db:push (old schema, no journal) is baselined and migrated forward", async () => {
    const dir = tempDir("pushdb-");
    const file = join(dir, "old.db");
    const db = new Database(file);
    // The 0000 schema exactly as db:push created it before run tracking existed.
    const init = readFileSync(join(import.meta.dir, "../drizzle/0000_init.sql"), "utf8");
    for (const stmt of init.split("--> statement-breakpoint")) if (stmt.trim()) db.run(stmt);
    db.run("INSERT INTO app_builds (id, platform) VALUES ('bld_old', 'linux')");
    db.close();

    const p = await startPanel({}, file);
    cleanup.push(p.stop);
    await waitFor(`${p.url}/api/health`, p);
    expect(p.out()).toContain("baselined at migration 0000");
    const after = new Database(file, { readonly: true });
    const cols = after.query("PRAGMA table_info(app_builds)").all().map((r: any) => r.name);
    expect(cols).toContain("run_id");
    expect((after.query("SELECT id FROM app_builds").get() as any).id).toBe("bld_old");
    after.close();
  });

  test("refuses to start on a half-built database without a journal", async () => {
    const dir = tempDir("halfdb-");
    const file = join(dir, "half.db");
    const db = new Database(file);
    db.run("CREATE TABLE projects (id text primary key)");
    db.close();
    const p = await startPanel({}, file);
    expect(await p.proc.exited).toBe(1);
    expect(p.out()).toContain("refusing");
  });
});

describe("trusted proxy header (opt-in)", () => {
  test("with REDX_TRUSTED_IP_HEADER set, each forwarded client gets its own bucket", async () => {
    const p = await startPanel({ REDX_TRUSTED_IP_HEADER: "x-forwarded-for" });
    cleanup.push(p.stop);
    await waitFor(`${p.url}/api/health`, p);
    const hit = (xff: string) =>
      fetch(`${p.url}/api/rpc/ping`, {
        method: "POST",
        headers: { "content-type": "application/json", "x-forwarded-for": xff },
        body: '{"json":{}}',
      }).then((r) => Number(r.headers.get("x-ratelimit-remaining")));
    const a1 = await hit("spoof, 203.0.113.1");
    const a2 = await hit("other, 203.0.113.1");
    const b1 = await hit("spoof, 203.0.113.2");
    expect(a2).toBe(a1 - 1); // right-most entry decides; the client-written left part is ignored
    expect(b1).toBe(a1);
  });
});

describe("InfectedNation date of birth (server-side)", () => {
  const iso = (d: Date) => d.toISOString().slice(0, 10);
  const yearsAgo = (n: number, dayShift = 0) => {
    const d = new Date();
    d.setUTCFullYear(d.getUTCFullYear() - n);
    d.setUTCDate(d.getUTCDate() + dayShift);
    return iso(d);
  };
  let n = 0;
  const signup = async (url: string, dob: string) =>
    postJson(`${url}/api/v1/signup/email`, NATION_SIGNUP({ dob, username: `user${++n}abc`, email: `u${n}@example.test` }));

  test("enforces the default minimum age of 13 from the full date", async () => {
    const nation = await startNation();
    cleanup.push(nation.stop);
    expect((await signup(nation.url, yearsAgo(13, 1))).status).toBe(400); // turns 13 tomorrow
    expect((await signup(nation.url, yearsAgo(13))).status).toBe(201); // 13 today
    expect((await signup(nation.url, yearsAgo(0, -10))).status).toBe(400); // newborn
    expect((await signup(nation.url, yearsAgo(-1))).json.error).toContain("future");
    expect((await signup(nation.url, yearsAgo(121))).status).toBe(400); // past the upper bound
    expect((await signup(nation.url, "1950-06-01")).status).toBe(201); // old enough, not cut off at 1980
    const cfg = await fetch(`${nation.url}/api/v1/config`).then((r) => (r.ok ? r.json() : null));
    if (cfg) expect(cfg.validation.minAge).toBe(13);
  }, 30_000);

  test("the minimum age is configurable", async () => {
    const nation = await startNation({ INFECTEDNATION_MIN_AGE: "18" });
    cleanup.push(nation.stop);
    expect((await signup(nation.url, yearsAgo(17))).json.error).toContain("at least 18");
    const ok = await signup(nation.url, yearsAgo(30));
    expect(ok.status).toBe(201);
  }, 30_000);

  test("an invalid INFECTEDNATION_MIN_AGE stops the service", async () => {
    const port = await freePort();
    const p = Bun.spawn([process.execPath, join(import.meta.dir, "../../../services/infectednation/server.ts")], {
      env: { PATH: process.env.PATH!, PORT: String(port), INFECTEDNATION_MIN_AGE: "abc", INFECTEDNATION_DATA: join(tempDir("n-"), "x.sqlite") },
      stderr: "pipe",
    });
    expect(await p.exited).not.toBe(0);
  });
});

describe("builds.sync tracks the dispatched run", () => {
  test("picks the run for this dispatch's commit, not the workflow's latest run", async () => {
    const SHA = "a".repeat(40);
    const OTHER = "b".repeat(40);
    let runs: any[] = [];
    let seenQuery = "";
    const gh = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch(req) {
        const u = new URL(req.url);
        if (u.pathname === "/user") return Response.json({ login: "owner" });
        if (u.pathname.endsWith("/commits/main")) return new Response(SHA);
        if (u.pathname.endsWith("/dispatches")) {
          const now = new Date().toISOString();
          runs = [
            // Newest overall but for another commit: the old code would have picked this.
            { id: 3, status: "completed", conclusion: "failure", html_url: "https://gh/run/3", head_sha: OTHER, event: "workflow_dispatch", created_at: now },
            { id: 2, status: "in_progress", conclusion: null, html_url: "https://gh/run/2", head_sha: SHA, event: "workflow_dispatch", created_at: now },
            { id: 1, status: "completed", conclusion: "success", html_url: "https://gh/run/1", head_sha: SHA, event: "workflow_dispatch", created_at: "2020-01-01T00:00:00Z" },
          ];
          return new Response(null, { status: 204 });
        }
        if (u.pathname.endsWith("/runs")) {
          seenQuery = u.search;
          const sha = u.searchParams.get("head_sha");
          return Response.json({ workflow_runs: runs.filter((r) => !sha || r.head_sha === sha) });
        }
        const m = u.pathname.match(/\/actions\/runs\/(\d+)$/);
        if (m) {
          const r = runs.find((x) => x.id === Number(m[1]));
          return r ? Response.json({ ...r, status: "completed", conclusion: "success" }) : new Response("nf", { status: 404 });
        }
        return new Response("not found", { status: 404 });
      },
    });
    cleanup.push(async () => gh.stop(true));

    const nation = await startNation({ OWNER_EMAIL: "owner@example.test" });
    cleanup.push(nation.stop);
    await provisionOwner(nation.dir, "owner@example.test", NATION_SIGNUP().password as string, { OWNER_EMAIL: "owner@example.test" });
    const p = await startPanel({
      GITHUB_API_URL: `http://127.0.0.1:${gh.port}`,
      REDX_IDENTITY_SOURCE: "nation",
      INFECTEDNATION_URL: nation.url,
    });
    cleanup.push(p.stop);
    await waitFor(`${p.url}/api/health`, p);
    const login = await postJson(`${p.url}/api/auth/sign-in/email`, { email: "owner@example.test", password: NATION_SIGNUP().password }, { origin: p.url });
    expect(login.status).toBe(200);
    const cookie = login.headers.getSetCookie().map((c) => c.split(";")[0]).join("; ");

    expect((await rpc(p.url, "settings/saveProvider", { provider: "github", values: { token: "ghp_test", repo: "acme/app" } }, cookie)).status).toBe(200);
    const d = await rpc(p.url, "builds/dispatch", { platform: "linux", ref: "main" }, cookie);
    expect(d.json.json.ok).toBe(true);
    const s1 = await rpc(p.url, "builds/sync", { id: d.json.json.id }, cookie);
    expect(s1.json.json).toMatchObject({ ok: true, runId: 2, status: "building" });
    expect(seenQuery).toContain(`head_sha=${SHA}`);
    expect(seenQuery).toContain("event=workflow_dispatch");
    // Once matched, the build follows its own run id.
    const s2 = await rpc(p.url, "builds/sync", { id: d.json.json.id }, cookie);
    expect(s2.json.json).toMatchObject({ ok: true, runId: 2, status: "succeeded" });
  }, 40_000);
});

