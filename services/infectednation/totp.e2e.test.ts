// End-to-end against the real server process and a real SQLite file (no mocks).
import { afterAll, expect, test } from "bun:test";
import { Database } from "bun:sqlite";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { base32Decode, totp } from "./totp";

const dir = mkdtempSync(join(tmpdir(), "in-totp-"));
const dbFile = join(dir, "in.sqlite");
const SECRET = "e2e-master-secret-" + Math.random().toString(36).slice(2);
let proc: ReturnType<typeof Bun.spawn> | null = null;
let port = 0;

async function start(env: Record<string, string>) {
  port = 20000 + Math.floor(Math.random() * 20000);
  const e: Record<string, string> = { PATH: process.env.PATH!, HOME: dir, PORT: String(port), HOST: "127.0.0.1", INFECTEDNATION_DATA: dbFile, ...env };
  proc = Bun.spawn([process.execPath, join(import.meta.dir, "server.ts")], { env: e, stdout: "pipe", stderr: "pipe" });
  for (let i = 0; i < 100; i++) {
    try { await fetch(`http://127.0.0.1:${port}/`); return; } catch { await Bun.sleep(100); }
  }
  throw new Error("server did not start");
}
async function stop() { proc?.kill(); await proc?.exited; proc = null; }
const post = async (p: string, body: any) => {
  const r = await fetch(`http://127.0.0.1:${port}${p}`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(body) });
  return { status: r.status, body: (await r.json().catch(() => ({}))) as any };
};
afterAll(async () => { await stop(); rmSync(dir, { recursive: true, force: true }); });

const user = { appId: "redx-test", username: "totpuser" + Date.now() % 100000, email: `t${Date.now()}@example.com`, password: "Str0ng!Passw0rd#", dob: "1990-01-01", firstName: "T", lastName: "U", useType: "personal", termsAccepted: true, termsScrollCompleted: true, termsVersion: "2026-09-23", privacyVersion: "2026-09-23" };
const login = (totpCode?: string) => post("/api/v1/login/email", { appId: user.appId, email: user.email, password: user.password, ...(totpCode !== undefined ? { totpCode } : {}) });

test("enroll, enforce, reject wrong + replay, sealed at rest, migration, fail closed", async () => {
  await start({ CREDENTIAL_SECRET: SECRET });
  const su = await post("/api/v1/signup/email", user);
  expect(su.status).toBe(201);
  const tok = su.body.token;

  const setup = await post("/api/v1/2fa/totp/setup", { token: tok });
  expect(setup.status).toBe(200);
  expect(setup.body.secret).toMatch(/^[A-Z2-7]{32}$/);
  expect(setup.body.otpauthUrl).toContain(`secret=${setup.body.secret}&issuer=InfectedNation&algorithm=SHA1&digits=6&period=30`);
  const key = base32Decode(setup.body.secret);

  expect((await post("/api/v1/2fa/totp/enable", { token: tok, code: totp(key, Date.now() / 1000 - 300) })).status).toBe(401);
  const now = Date.now() / 1000;
  expect((await post("/api/v1/2fa/totp/enable", { token: tok, code: totp(key, now) })).status).toBe(200);

  const db = new Database(dbFile);
  const row = db.prepare("SELECT secret_enc FROM totp_secrets").get() as any;
  expect(row.secret_enc.startsWith("v2.")).toBe(true);
  expect(row.secret_enc).not.toContain(setup.body.secret);
  db.close();

  expect((await login()).status).toBe(401);                       // code required
  expect((await login("123456" === totp(key, Date.now() / 1000) ? "654321" : "123456")).status).toBe(401); // wrong code (was accepted before)
  expect((await login(totp(key, now))).status).toBe(401);          // replay of the code used to enable
  const nextCode = totp(key, now + 30);                             // next step, inside the skew window
  const ok = await login(nextCode);
  expect(ok.status).toBe(200);
  expect(ok.body.token).toBeTruthy();
  expect((await login(nextCode)).status).toBe(401);                // replay

  // Fail closed without the key: a 2FA account cannot sign in at all.
  await stop();
  await start({});
  expect((await login(totp(key, Date.now() / 1000 + 60))).status).toBe(503);
  expect((await post("/api/v1/2fa/totp/setup", { token: ok.body.token })).status).toBe(409);
  await stop();

  // Migration: a legacy plaintext seed and a refused AES "v1." seed are both removed at start-up.
  for (const legacy of ["legacyPlainBase64urlSeed", "v1.CQkJCQkJCQkJCQkJCQkJCQ.BwcHBwcHBwcHBwcH.TvxFjPOHKcreAhvH9Q4rtsKkoiw.JAn9kU7GIwzrOq1kP8U9cw"]) {
    const d2 = new Database(dbFile);
    if (!(d2.prepare("SELECT COUNT(*) n FROM totp_secrets").get() as any).n)
      d2.prepare("INSERT INTO totp_secrets(account_id,secret_enc,enabled,last_step,created_at,updated_at) SELECT id,'x',1,-1,0,0 FROM accounts WHERE email=?").run(user.email);
    d2.prepare("UPDATE totp_secrets SET secret_enc=?").run(legacy);
    d2.prepare("UPDATE accounts SET totp_enabled=1, totp_reenroll=0 WHERE email=?").run(user.email);
    d2.close();
    await start({ CREDENTIAL_SECRET: SECRET });
    const d3 = new Database(dbFile);
    expect((d3.prepare("SELECT COUNT(*) n FROM totp_secrets").get() as any).n).toBe(0);
    expect((d3.prepare("SELECT totp_enabled, totp_reenroll FROM accounts WHERE email=?").get(user.email) as any)).toEqual({ totp_enabled: 0, totp_reenroll: 1 });
    d3.close();
    const after = await login();
    expect(after.status).toBe(200);
    expect(after.body.totpReenrollRequired).toBe(true);
    await stop();
  }
}, 60000);
