// P13: plaintext left by an earlier build is sealed in place at start-up, and its old pages are gone.
import { expect, test } from "bun:test";
import { Database } from "bun:sqlite";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { SECRETS, startPanel, tempDir, waitFor } from "./helpers";

test("legacy plaintext columns are sealed at boot and no plaintext stays in the file", async () => {
  const dbFile = join(tempDir("legacy-"), "panel.db");
  let panel = await startPanel({}, dbFile);
  await waitFor(`${panel.url}/api/health`, panel);
  await panel.stop();

  // Write rows the way a pre-P13 build stored them: plain text.
  const PLAIN = {
    desc: "legacy-desc-5e6f7a internal plan",
    env: "LEGACY_TOKEN=lt_9a8b7c6d5e4f3a2b",
    email: "legacy.user@example.test",
    name: "Legacy Person Name",
    activity: "legacy-activity-0d1e2f message",
  };
  const db = new Database(dbFile);
  db.query("INSERT INTO projects (id, slug, name, description, env_vars, runtime, source_kind, status, port) VALUES ('prj_legacy','legacy','Legacy',?,?,'static','upload','draft',8199)").run(PLAIN.desc, PLAIN.env);
  db.query("INSERT INTO user (id, name, email, email_verified, created_at, updated_at) VALUES ('usr_legacy',?,?,0,0,0)").run(PLAIN.name, PLAIN.email);
  db.query("INSERT INTO activity (id, scope, level, message, created_at) VALUES ('act_legacy','project','info',?,0)").run(PLAIN.activity);
  db.close();
  expect(readFileSync(dbFile).toString("latin1") + safeRead(dbFile + "-wal")).toContain(PLAIN.env);

  panel = await startPanel({}, dbFile);
  await waitFor(`${panel.url}/api/health`, panel);
  await panel.stop();

  const raw = readFileSync(dbFile).toString("latin1") + safeRead(dbFile + "-wal");
  for (const v of Object.values(PLAIN)) expect(raw.includes(v), `file still contains ${v}`).toBe(false);

  process.env.CREDENTIAL_SECRET = SECRETS.CREDENTIAL_SECRET;
  const cs = await import("../src/api/lib/colseal");
  cs.resetColsealKeys();
  const r = new Database(dbFile, { readonly: true });
  const p = r.query("SELECT description, env_vars FROM projects WHERE id='prj_legacy'").get() as any;
  const u = r.query("SELECT name, email FROM user WHERE id='usr_legacy'").get() as any;
  const a = r.query("SELECT message FROM activity WHERE id='act_legacy'").get() as any;
  r.close();
  expect(cs.open("projects.description", "", p.description)).toBe(PLAIN.desc);
  expect(cs.open("projects.env_vars", "", p.env_vars)).toBe(PLAIN.env);
  expect(cs.open("user.name", "", u.name)).toBe(PLAIN.name);
  expect(cs.openDet("user.email", u.email)).toBe(PLAIN.email); // deterministic, so Better Auth lookups still match
  expect(cs.open("activity.message", "", a.message)).toBe(PLAIN.activity);
}, 60_000);

function safeRead(f: string) {
  try { return readFileSync(f).toString("latin1"); } catch { return ""; }
}
