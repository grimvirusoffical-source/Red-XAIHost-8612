import { spawn, type Subprocess } from "bun";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { createServer } from "node:net";

export const WEB_DIR = resolve(import.meta.dir, "..");
export const REPO_DIR = resolve(WEB_DIR, "../..");
export const REDXD_BIN = process.env.REDXD_BIN || "/workspace/redx-host-scratch/bin/redxd";

export function tempDir(prefix: string) {
  return mkdtempSync(join(tmpdir(), prefix));
}

/** A free loopback port chosen by the OS (never a fixed one: the box shares ports). */
export async function freePort(): Promise<number> {
  return new Promise((ok, fail) => {
    const s = createServer();
    s.once("error", fail);
    s.listen(0, "127.0.0.1", () => {
      const port = (s.address() as { port: number }).port;
      s.close(() => ok(port));
    });
  });
}

export type Proc = { proc: Subprocess; out: () => string; stop: () => Promise<void> };

export function run(cmd: string[], opts: { cwd?: string; env?: Record<string, string> }): Proc {
  let buf = "";
  const proc = spawn(cmd, {
    cwd: opts.cwd,
    env: { PATH: process.env.PATH!, HOME: process.env.HOME!, ...opts.env },
    stdout: "pipe",
    stderr: "pipe",
  });
  const pump = async (s: ReadableStream<Uint8Array>) => {
    const dec = new TextDecoder();
    for await (const c of s) buf += dec.decode(c);
  };
  void pump(proc.stdout as ReadableStream<Uint8Array>);
  void pump(proc.stderr as ReadableStream<Uint8Array>);
  return {
    proc,
    out: () => buf,
    stop: async () => {
      proc.kill();
      await proc.exited;
    },
  };
}

export async function waitFor(url: string, p: Proc, ms = 20_000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    if (p.proc.exitCode !== null) throw new Error(`process exited early:\n${p.out()}`);
    try {
      const r = await fetch(url);
      if (r.status < 500) return;
    } catch {}
    await Bun.sleep(100);
  }
  throw new Error(`timeout waiting for ${url}\n${p.out()}`);
}

export async function startRedxd() {
  const dir = tempDir("redxd-");
  const port = await freePort();
  const cfg = join(dir, "redx.toml");
  writeFileSync(
    cfg,
    `data_dir = '${dir}'\nlisten = '127.0.0.1:${port}'\nlog_level = 'quiet'\nargon_memory_kib = 64\nargon_time = 1\nbootstrap = 'console'\ntls = 'off'\n`,
  );
  const p = run([REDXD_BIN, "run", "--config", cfg, "--no-open"], { env: { REDX_NO_OPEN: "1" } });
  const url = `http://127.0.0.1:${port}`;
  await waitFor(`${url}/healthz`, p);
  const end = Date.now() + 5000;
  let key = "";
  while (!key && Date.now() < end) {
    key = p.out().match(/rx_live_[A-Za-z0-9]+/)?.[0] ?? "";
    if (!key) await Bun.sleep(50);
  }
  if (!key) throw new Error(`no bootstrap key:\n${p.out()}`);
  return { url, key, ...p };
}

export async function startNation(env: Record<string, string> = {}, existingDir?: string) {
  const dir = existingDir ?? tempDir("nation-");
  const port = await freePort();
  const p = run(["bun", join(REPO_DIR, "services/infectednation/server.ts")], {
    cwd: REPO_DIR,
    env: { PORT: String(port), INFECTEDNATION_DATA: join(dir, "nation.sqlite"), ...env },
  });
  const url = `http://127.0.0.1:${port}`;
  await waitFor(`${url}/api/health`, p);
  return { url, port, dir, ...p };
}

export const SECRETS = {
  BETTER_AUTH_SECRET: "a".repeat(32) + "test-auth-secret-0123456789abcdef",
  CREDENTIAL_SECRET: "b".repeat(32) + "test-cred-secret-0123456789abcdef",
};

export async function startPanel(env: Record<string, string>, dbFile?: string) {
  const dir = tempDir("panel-");
  const port = await freePort();
  const p = run(["bun", join(WEB_DIR, "src/__server.ts")], {
    cwd: REPO_DIR,
    env: {
      NODE_ENV: "production",
      PORT: String(port),
      REDX_BIND_HOST: "127.0.0.1",
      WEBSITE_URL: `http://127.0.0.1:${port}`,
      DATABASE_URL: `file:${dbFile ?? join(dir, "panel.db")}`,
      REDX_AUTO_LOCAL_NODE: "off",
      OWNER_EMAIL: "owner@example.test",
      ...SECRETS,
      ...env,
    },
  });
  const url = `http://127.0.0.1:${port}`;
  return { url, port, dir, ...p };
}

export const NATION_SIGNUP = (overrides: Record<string, unknown> = {}) => ({
  appId: "redxai-host",
  username: "ownerone",
  email: "owner@example.test",
  password: "Str0ng!Passw0rd",
  confirmPassword: "Str0ng!Passw0rd",
  dob: "2000-01-15",
  firstName: "Owner",
  lastName: "One",
  useType: "personal",
  termsAccepted: true,
  termsScrollCompleted: true,
  termsVersion: "2026-09-23",
  privacyVersion: "2026-09-23",
  ...overrides,
});

export async function postJson(url: string, body: unknown, headers: Record<string, string> = {}) {
  const r = await fetch(url, {
    method: "POST",
    headers: { "content-type": "application/json", ...headers },
    body: JSON.stringify(body),
  });
  const text = await r.text();
  let json: any = null;
  try {
    json = JSON.parse(text);
  } catch {}
  return { status: r.status, json, text, headers: r.headers };
}

/** oRPC call over the RPC protocol used by the web client. */
export async function rpc(panel: string, path: string, input: unknown, cookie = "") {
  return postJson(`${panel}/api/rpc/${path}`, { json: input }, cookie ? { cookie } : {});
}

/** Provision an owner the way JF does on a host: the real provision-owner command against Nation's DB. */
export async function provisionOwner(nationDir: string, email: string, password: string, env: Record<string, string> = {}) {
  const p = Bun.spawn(["bun", join(REPO_DIR, "services/infectednation/server.ts"), "provision-owner", "--email", email], {
    cwd: REPO_DIR,
    env: {
      PATH: process.env.PATH!, HOME: process.env.HOME!, PORT: "0",
      INFECTEDNATION_DATA: join(nationDir, "nation.sqlite"),
      INFECTEDNATION_OWNER_PASSWORD: password,
      ...env,
    },
    stdout: "pipe", stderr: "pipe", stdin: "ignore",
  });
  const code = await p.exited;
  const out = (await new Response(p.stdout).text()) + (await new Response(p.stderr).text());
  if (code !== 0) throw new Error(`provision-owner failed (${code}):\n${out}`);
  return out;
}
