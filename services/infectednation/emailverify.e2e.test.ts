// Email verification, end to end against the real server process + SQLite (no mocks).
// Mail goes to a local HTTP sink standing in for the mail provider webhook.
import { afterAll, beforeAll, expect, test } from "bun:test";
import { Database } from "bun:sqlite";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { parseVerifyToken } from "./emailverify";

const dir = mkdtempSync(join(tmpdir(), "in-ev-"));
let proc: ReturnType<typeof Bun.spawn> | null = null;
let port = 0;
const mails: any[] = [];
const sink = Bun.serve({ port: 0, hostname: "127.0.0.1", async fetch(r) { mails.push(await r.json()); return new Response("ok"); } });

async function start(dbFile: string, env: Record<string, string>) {
  port = 20000 + Math.floor(Math.random() * 20000);
  proc = Bun.spawn([process.execPath, join(import.meta.dir, "server.ts")], {
    env: { PATH: process.env.PATH!, HOME: dir, PORT: String(port), HOST: "127.0.0.1", INFECTEDNATION_DATA: dbFile, CREDENTIAL_SECRET: "ev-test-secret-0123456789", ...env },
    stdout: "pipe", stderr: "pipe",
  });
  for (let i = 0; i < 100; i++) { try { await fetch(`http://127.0.0.1:${port}/`); return; } catch { await Bun.sleep(100); } }
  throw new Error("server did not start");
}
async function stop() { proc?.kill(); await proc?.exited; proc = null; }
const post = async (p: string, body: any) => {
  const r = await fetch(`http://127.0.0.1:${port}${p}`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(body) });
  return { status: r.status, body: (await r.json().catch(() => ({}))) as any, headers: r.headers };
};
let n = 0;
const newUser = () => { n++; const t = Date.now() % 1e6; return { appId: "redx-test", username: `evuser${t}${n}`, email: `ev${t}${n}@example.com`, password: "Str0ng!Passw0rd#", dob: "1990-01-01", firstName: "E", lastName: "V", useType: "personal", termsAccepted: true, termsScrollCompleted: true, termsVersion: "2026-09-23", privacyVersion: "2026-09-23" }; };
const lastToken = () => decodeURIComponent(String(mails.at(-1).link).split("#token=")[1]);

afterAll(async () => { await stop(); sink.stop(true); rmSync(dir, { recursive: true, force: true }); });

test("flag OFF (default): no verification, no schema change, no new fields, nothing restricted", async () => {
  const dbFile = join(dir, "off.sqlite");
  await start(dbFile, { INFECTEDNATION_MAIL_WEBHOOK: `http://127.0.0.1:${sink.port}/` });
  const before = mails.length;
  const su = await post("/api/v1/signup/email", newUser());
  expect(su.status).toBe(201);
  expect(Object.keys(su.body).sort()).toEqual(["account", "token"]);
  expect("emailVerified" in su.body.account).toBe(false);
  const me = await post("/api/v1/session/me", { token: su.body.token });
  expect("emailVerified" in me.body.account).toBe(false);
  expect((await post("/api/v1/2fa/totp/setup", { token: su.body.token })).status).toBe(200); // not restricted
  expect((await post("/api/v1/email/verify", { token: "x.y" })).status).toBe(404);
  expect((await post("/api/v1/email/resend", { token: su.body.token })).status).toBe(404);
  expect(mails.length).toBe(before);
  await stop();
  const db = new Database(dbFile);
  expect((db.prepare("PRAGMA table_info(accounts)").all() as any[]).some((c) => c.name === "email_verified")).toBe(false);
  expect(db.prepare("SELECT name FROM sqlite_master WHERE name='email_verifications'").get()).toBeNull();
  db.close();
}, 60000);

test("flag ON: hashed, single-use, expiring tokens; resend limits; unverified restricted", async () => {
  const dbFile = join(dir, "on.sqlite");
  await start(dbFile, { INFECTEDNATION_EMAIL_VERIFICATION: "on", INFECTEDNATION_MAIL_WEBHOOK: `http://127.0.0.1:${sink.port}/` });
  const su = await post("/api/v1/signup/email", newUser());
  expect(su.status).toBe(201);
  expect(su.body.emailVerificationRequired).toBe(true);
  expect(su.body.emailSent).toBe(true);
  expect(su.body.account.emailVerified).toBe(false);
  const tok = lastToken();
  expect(parseVerifyToken(tok)).not.toBeNull();
  expect(mails.at(-1).to).toBe(su.body.account.email);

  // stored hashed, not the token
  const db = new Database(dbFile);
  const row = db.prepare("SELECT token_hash FROM email_verifications").get() as any;
  expect(row.token_hash).toMatch(/^b1\.[0-9a-f]{64}$/); // keyed blind index (P13)
  expect(tok).not.toContain(row.token_hash);
  expect(row.token_hash).not.toBe(tok.split(".")[1]);

  // restricted until verified; login and /me still work
  const blocked = await post("/api/v1/2fa/totp/setup", { token: su.body.token });
  expect(blocked.status).toBe(403);
  expect(blocked.body.code).toBe("email_unverified");
  expect((await post("/api/v1/session/me", { token: su.body.token })).status).toBe(200);

  // wrong secret for the right account, malformed token
  const [aid, secret] = [tok.slice(0, tok.lastIndexOf(".")), tok.slice(tok.lastIndexOf(".") + 1)];
  const flipped = aid + "." + (secret[0] === "A" ? "B" : "A") + secret.slice(1);
  expect((await post("/api/v1/email/verify", { token: flipped })).status).toBe(400);
  expect((await post("/api/v1/email/verify", { token: "garbage" })).status).toBe(400);

  // resend too soon -> 429; old token still the live one
  expect((await post("/api/v1/email/resend", { token: su.body.token })).status).toBe(429);

  // verify, then single use
  expect((await post("/api/v1/email/verify", { token: tok })).body).toEqual({ verified: true });
  expect((await post("/api/v1/email/verify", { token: tok })).status).toBe(400);
  expect((await post("/api/v1/session/me", { token: su.body.token })).body.account.emailVerified).toBe(true);
  expect((await post("/api/v1/2fa/totp/setup", { token: su.body.token })).status).toBe(200);

  // expiry: second user, token aged past its TTL in the DB
  const su2 = await post("/api/v1/signup/email", newUser());
  const tok2 = lastToken();
  db.prepare("UPDATE email_verifications SET expires_at=? WHERE account_id=?").run(Date.now() - 1, su2.body.account.accountId);
  expect((await post("/api/v1/email/verify", { token: tok2 })).status).toBe(400);

  // resend replaces the token (old one dies) and is capped per hour
  for (let i = 0; i < 3; i++) {
    db.prepare("UPDATE email_verifications SET sent_at=? WHERE account_id=?").run(Date.now() - 120000, su2.body.account.accountId);
    const r = await post("/api/v1/email/resend", { token: su2.body.token });
    if (i < 3) expect(r.status).toBe(200);
  }
  const tok3 = lastToken();
  db.prepare("UPDATE email_verifications SET sent_at=? WHERE account_id=?").run(Date.now() - 120000, su2.body.account.accountId);
  expect((await post("/api/v1/email/resend", { token: su2.body.token })).status).toBe(429); // 4th in the hour
  expect((await post("/api/v1/email/verify", { token: tok2 })).status).toBe(400);           // replaced
  expect((await post("/api/v1/email/verify", { token: tok3 })).status).toBe(200);
  db.close();
}, 60000);

test("flag ON without a mail transport: signup still works, resend fails closed (503)", async () => {
  await stop();
  const f = join(dir, "nomail.sqlite");
  await start(f, { INFECTEDNATION_EMAIL_VERIFICATION: "on" });
  const su = await post("/api/v1/signup/email", newUser());
  expect(su.status).toBe(201);
  expect(su.body.emailSent).toBe(false);
  expect((await post("/api/v1/2fa/totp/setup", { token: su.body.token })).status).toBe(403);
  const d = new Database(f);
  d.prepare("UPDATE email_verifications SET sent_at=?").run(Date.now() - 120000);
  d.close();
  expect((await post("/api/v1/email/resend", { token: su.body.token })).status).toBe(503);
}, 60000);
