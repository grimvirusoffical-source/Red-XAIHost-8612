/**
 * InfectedNation identity service — multi-user store for Vocal Lab + Community.
 * RedXAIHost control-panel auth (better-auth) is a separate private layer.
 */
import { Database } from "bun:sqlite";
import { mkdirSync, writeFileSync, existsSync, readFileSync } from "node:fs";
import { dirname, resolve, join } from "node:path";
import { rsx, ctEqStr } from "../../vendor/rsx-sdk/rsx";
import { emailVerificationEnabled, EMAIL_VERIFY_TTL_MS, newVerifyToken, parseVerifyToken, RESEND_MIN_INTERVAL_MS, RESEND_PER_HOUR, sendMail, tokenMatches, verifyMail } from "./emailverify";
import { isSealed, newTotpSecret, openSecret, otpauthUri, sealSecret, totpMasterSecret, TotpKeyMissing, verifyTotp } from "./totp";

const PORT = Number(process.env.PORT || 8787);
// Loopback by default; set HOST=0.0.0.0 explicitly to expose the service.
const HOST = (process.env.HOST || "").trim() || "127.0.0.1";
const SCHEMA_VERSION = 2;
// Age limits, checked server-side from the full date on every write of a date of birth.
const MAX_AGE = 120;
const MIN_AGE = (() => {
  const raw = process.env.INFECTEDNATION_MIN_AGE;
  if (raw == null || raw.trim() === "") return 13;
  const n = Number(raw);
  if (!Number.isInteger(n) || n < 0 || n >= MAX_AGE) {
    throw new Error(`INFECTEDNATION_MIN_AGE must be an integer from 0 to ${MAX_AGE - 1}, got "${raw}".`);
  }
  return n;
})();
const TERMS_VERSION = "2026-09-23";
const PRIVACY_VERSION = "2026-09-23";
const SESSION_TTL = 30 * 24 * 60 * 60 * 1000;
const SECURITY_RETENTION = 180 * 24 * 60 * 60 * 1000;
// Owner accounts: a configured list of emails. An address on this list is reserved: public signup can never
// create it or become owner. Owners exist only after `bun server.ts provision-owner` ran on this machine.
const DEFAULT_OWNER_EMAILS = "grimvirusoffical@gmail.com,support@infected-nation.com";
// An explicit list (INFECTEDNATION_OWNER_EMAILS / OWNER_EMAILS) replaces the defaults; the legacy single
// OWNER_EMAIL / INFECTEDNATION_OWNER_EMAIL is added to them (old .env files only carry that one).
const OWNER_EMAILS: string[] = [
  ...new Set(
    String(
      process.env.INFECTEDNATION_OWNER_EMAILS ||
        process.env.OWNER_EMAILS ||
        [DEFAULT_OWNER_EMAILS, process.env.INFECTEDNATION_OWNER_EMAIL, process.env.OWNER_EMAIL].filter(Boolean).join(","),
    )
      .split(",")
      .map((e) => e.trim().toLowerCase())
      .filter(Boolean),
  ),
];
const isOwnerEmail = (e: unknown) => OWNER_EMAILS.includes(String(e || "").trim().toLowerCase());
const STUDIO_APP = "infected-voices";
const STUDIO_TIER = "pro";
const STUDIO_WEB_URL =
  process.env.INFECTED_VOICES_STUDIO_URL || "https://app.infectedvoices.space/studio";
const STUDIO_SCHEME = "infectedvoices://";
const STRIPE_WEBHOOK_SECRET = process.env.STRIPE_WEBHOOK_SECRET || "";
const STRIPE_BASIC_PAYMENT_LINK_ID = process.env.STRIPE_BASIC_PAYMENT_LINK_ID || "";
const STRIPE_BASIC_PAYMENT_LINK_URL = process.env.STRIPE_BASIC_PAYMENT_LINK_URL || "";
const STRIPE_PRO_PAYMENT_LINK_ID = process.env.STRIPE_PRO_PAYMENT_LINK_ID || process.env.STRIPE_PAYMENT_LINK_ID || "";
const STRIPE_PRO_PAYMENT_LINK_URL = process.env.STRIPE_PRO_PAYMENT_LINK_URL || process.env.STRIPE_PAYMENT_LINK_URL || "";
const ADMIN_PANEL_TOKEN = process.env.INFECTEDNATION_ADMIN_TOKEN || "";

function resolveDataPath() {
  if (process.env.INFECTEDNATION_DATA) return resolve(process.env.INFECTEDNATION_DATA);
  if (process.env.REDX_PERSIST_ROOT) return resolve(process.env.REDX_PERSIST_ROOT + "/infectednation.sqlite");
  const candidates = [
    resolve("../../data/worker/data/infectednation/infectednation.sqlite"),
    resolve("../data/worker/data/infectednation/infectednation.sqlite"),
    resolve("./data/infectednation.sqlite"),
    resolve("../../data/infectednation.sqlite"),
  ];
  for (const c of candidates) {
    if (existsSync(c)) return c;
  }
  return resolve("./data/infectednation.sqlite");
}

const DATA = resolveDataPath();
mkdirSync(dirname(DATA), { recursive: true });
const db = new Database(DATA, { create: true, strict: true });

db.exec(`
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS accounts(
 id TEXT PRIMARY KEY,
 username TEXT NOT NULL COLLATE NOCASE UNIQUE,
 email TEXT NOT NULL COLLATE NOCASE UNIQUE,
 password_hash TEXT NOT NULL,
 dob TEXT NOT NULL,
 first_name TEXT NOT NULL,
 middle_initial TEXT NOT NULL DEFAULT '',
 last_name TEXT NOT NULL,
 use_type TEXT NOT NULL,
 company_name TEXT NOT NULL DEFAULT '',
 terms_version TEXT NOT NULL,
 privacy_version TEXT NOT NULL,
 terms_accepted_at INTEGER NOT NULL,
 status TEXT NOT NULL DEFAULT 'active',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions(
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 token_hash TEXT NOT NULL UNIQUE,
 app_id TEXT NOT NULL,
 method TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 last_seen_at INTEGER NOT NULL,
 expires_at INTEGER NOT NULL,
 ip TEXT NOT NULL DEFAULT '',
 user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS sessions_account_idx ON sessions(account_id);
CREATE TABLE IF NOT EXISTS security_events(
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 type TEXT NOT NULL,
 at INTEGER NOT NULL,
 ip TEXT NOT NULL DEFAULT '',
 user_agent TEXT NOT NULL DEFAULT '',
 method TEXT NOT NULL DEFAULT '',
 app_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS security_account_at_idx ON security_events(account_id,at);
CREATE TABLE IF NOT EXISTS connect_requests(
 id TEXT PRIMARY KEY,
 secret_hash TEXT NOT NULL,
 app_id TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 expires_at INTEGER NOT NULL,
 account_id TEXT,
 approved_at INTEGER NOT NULL DEFAULT 0,
 consumed_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS entitlements(
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 app_id TEXT NOT NULL,
 tier TEXT NOT NULL,
 source TEXT NOT NULL,
 external_id TEXT,
 status TEXT NOT NULL DEFAULT 'active',
 expires_at INTEGER,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS entitlements_external_idx ON entitlements(source,external_id) WHERE external_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS entitlements_account_app_idx ON entitlements(account_id,app_id,status);
CREATE TABLE IF NOT EXISTS app_onboarding(
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 app_id TEXT NOT NULL,
 version TEXT NOT NULL,
 progress INTEGER NOT NULL DEFAULT 0,
 complete INTEGER NOT NULL DEFAULT 0,
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(account_id,app_id)
);
CREATE TABLE IF NOT EXISTS billing_events(
 id TEXT PRIMARY KEY,
 provider TEXT NOT NULL,
 event_type TEXT NOT NULL,
 processed_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS schema_meta(
 key TEXT PRIMARY KEY,
 value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS api_keys(
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 key_hash TEXT NOT NULL UNIQUE,
 created_at INTEGER NOT NULL,
 last_used_at INTEGER NOT NULL DEFAULT 0,
 revoked_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS oauth_identities(
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 provider TEXT NOT NULL,
 provider_subject TEXT NOT NULL,
 email TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 UNIQUE(provider, provider_subject)
);
CREATE TABLE IF NOT EXISTS totp_secrets(
 account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 secret_enc TEXT NOT NULL,
 enabled INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS rate_buckets(
 key TEXT PRIMARY KEY,
 count INTEGER NOT NULL,
 reset_at INTEGER NOT NULL
);
`);

function ensureColumn(table: string, column: string, ddl: string) {
  const cols = db.prepare(`PRAGMA table_info(${table})`).all() as { name: string }[];
  if (!cols.some((c) => c.name === column)) {
    db.exec(`ALTER TABLE ${table} ADD COLUMN ${ddl}`);
  }
}

ensureColumn("accounts", "phone", "phone TEXT NOT NULL DEFAULT ''");
ensureColumn("accounts", "stage_name", "stage_name TEXT NOT NULL DEFAULT ''");
ensureColumn("accounts", "profile_complete", "profile_complete INTEGER NOT NULL DEFAULT 1");
ensureColumn("accounts", "phone_verified", "phone_verified INTEGER NOT NULL DEFAULT 0");
ensureColumn("accounts", "totp_enabled", "totp_enabled INTEGER NOT NULL DEFAULT 0");
ensureColumn("accounts", "role", "role TEXT NOT NULL DEFAULT 'member'");
// Email verification is behind INFECTEDNATION_EMAIL_VERIFICATION (default off). With it off, nothing below
// touches the schema or any response, so the service behaves exactly as before.
const EMAIL_VERIFY = emailVerificationEnabled();
if (EMAIL_VERIFY) {
  ensureColumn("accounts", "email_verified", "email_verified INTEGER NOT NULL DEFAULT 0");
  db.exec(`CREATE TABLE IF NOT EXISTS email_verifications(
    account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    sent_at INTEGER NOT NULL
  )`);
}
ensureColumn("accounts", "owner_provisioned", "owner_provisioned INTEGER NOT NULL DEFAULT 0");
// Migration: owner used to be granted by email/username at signup, with no proof. Any owner that was not
// provisioned on this machine is demoted; real owners are restored with provision-owner.
{
  const demoted = db.prepare("UPDATE accounts SET role='member', updated_at=? WHERE role='owner' AND owner_provisioned=0").run(Date.now());
  if (demoted.changes) console.warn(`[owner] demoted ${demoted.changes} unprovisioned owner account(s); run provision-owner to restore real owners`);
}
ensureColumn("totp_secrets", "last_step", "last_step INTEGER NOT NULL DEFAULT -1");
ensureColumn("accounts", "totp_reenroll", "totp_reenroll INTEGER NOT NULL DEFAULT 0");

// Migration: older builds stored TOTP seeds in plaintext (base64url) and never verified codes, and later ones
// sealed them with AES-256-GCM ("v1."), a format that is refused, never decrypted. Remove every seed that is not
// in the current sealed form, switch that 2FA off and flag the account to enroll again.
{
  const legacy = db.prepare("SELECT account_id, secret_enc FROM totp_secrets").all() as any[];
  for (const r of legacy) {
    if (isSealed(r.secret_enc)) continue;
    db.prepare("DELETE FROM totp_secrets WHERE account_id=?").run(r.account_id);
    db.prepare("UPDATE accounts SET totp_enabled=0, totp_reenroll=1, updated_at=? WHERE id=? AND totp_enabled=1").run(Date.now(), r.account_id);
    console.warn(`[infectednation] removed a TOTP seed in a refused legacy format for account ${r.account_id}; re-enrollment required`);
  }
}

db.prepare(
  "INSERT INTO schema_meta(key,value) VALUES('version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
).run(String(SCHEMA_VERSION));

const json = (data: unknown, status = 200, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(data), {
    status,
    headers: { "content-type": "application/json; charset=utf-8", "cache-control": "no-store", ...headers },
  });
const sha = (s: string) => rsx.sha256(Buffer.from(s, "utf8")).toString("hex");
const id = (prefix: string) => prefix + "_" + rsx.random(18).toString("base64url");
const normalizeEmail = (v: string) => v.trim().toLowerCase();
const normalizeUsername = (v: string) => v.trim();
function safeEqual(a: string, b: string) {
  return ctEqStr(a, b);
}

// Account passwords: Argon2id PHC strings from Red-SecretsXAI. Hashes written by earlier builds
// (Bun's built-in hasher, also Argon2id PHC) verify unchanged; after a successful sign-in a hash with other
// parameters is rewritten with the current ones. Anything that is not an Argon2id PHC string is refused.
const PASSWORD_ARGON2 = { t: 3, m: 65536, p: 1 } as const;
const PASSWORD_PREFIX = `$argon2id$v=19$m=${PASSWORD_ARGON2.m},t=${PASSWORD_ARGON2.t},p=${PASSWORD_ARGON2.p}$`;
async function hashPassword(password: string): Promise<string> {
  return rsx.phcHashPassword(password, PASSWORD_ARGON2.t, PASSWORD_ARGON2.m, PASSWORD_ARGON2.p);
}
async function verifyPassword(password: string, hash: string): Promise<boolean> {
  if (typeof hash !== "string" || !hash.startsWith("$argon2id$")) return false;
  try { return rsx.phcVerifyPassword(password, hash); } catch { return false; }
}
function passwordNeedsRehash(hash: string): boolean {
  return !hash.startsWith(PASSWORD_PREFIX);
}
function validApp(v: unknown) {
  return typeof v === "string" && /^[a-z0-9][a-z0-9-]{2,63}$/.test(v);
}
function usernameError(v: unknown) {
  if (typeof v !== "string") return "Username is required.";
  const s = v.trim();
  if (s.length < 4 || s.length > 24) return "Username must be 4–24 characters.";
  const letters = (s.match(/[A-Za-z]/g) || []).length;
  if (letters < 3) return "Username must contain at least 3 letters.";
  if (!/^[A-Za-z0-9_.-]+$/.test(s)) return "Username may use letters, numbers, underscore, period and hyphen.";
  return "";
}
function passwordError(v: unknown) {
  if (typeof v !== "string" || v.length < 8 || v.length > 128) return "Password must be 8–128 characters.";
  if (!/[A-Z]/.test(v)) return "Password needs an uppercase letter.";
  if (!/[a-z]/.test(v)) return "Password needs a lowercase letter.";
  if (!/[0-9]/.test(v)) return "Password needs a number.";
  if (!/[^A-Za-z0-9]/.test(v)) return "Password needs a special symbol.";
  return "";
}
function dobError(v: unknown) {
  if (typeof v !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(v)) return "Enter a valid date of birth.";
  const d = new Date(v + "T00:00:00Z");
  if (!Number.isFinite(d.getTime()) || d.toISOString().slice(0, 10) !== v) return "Enter a valid date of birth.";
  const age = ageOn(v, new Date());
  if (age < 0) return "Date of birth cannot be in the future.";
  if (age < MIN_AGE) return `You must be at least ${MIN_AGE} years old to sign up.`;
  if (age > MAX_AGE) return `Enter a real date of birth (age ${MAX_AGE} or under).`;
  return "";
}
/** Whole years between an ISO date of birth and `today`, in UTC. */
function ageOn(dob: string, today: Date) {
  const [y, m, d] = dob.split("-").map(Number) as [number, number, number];
  let age = today.getUTCFullYear() - y;
  const mm = today.getUTCMonth() + 1;
  if (mm < m || (mm === m && today.getUTCDate() < d)) age -= 1;
  return age;
}
/** Latest date of birth that meets `years` of age today (inclusive), as YYYY-MM-DD. */
function latestAllowedDob(today = new Date(), years = MIN_AGE) {
  const y = today.getUTCFullYear() - years;
  const m = today.getUTCMonth() + 1;
  let d = today.getUTCDate();
  // Feb 29 has no match in a non-leap target year; the 28th is the last day that qualifies.
  if (m === 2 && d === 29 && !(y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0))) d = 28;
  return `${y}-${String(m).padStart(2, "0")}-${String(d).padStart(2, "0")}`;
}
function phoneError(v: unknown) {
  if (v == null || v === "") return "";
  const s = String(v).trim();
  if (!/^\+?[0-9][0-9\-\s().]{6,20}$/.test(s)) return "Enter a valid phone number or leave it blank.";
  return "";
}
function requestIp(req: Request, server: any) {
  if (process.env.TRUST_CLOUDFLARE === "true") {
    const cf = req.headers.get("cf-connecting-ip");
    if (cf && cf.length <= 64) return cf;
  }
  const raw = server.requestIP(req)?.address || "";
  return String(raw).slice(0, 64);
}
async function body(req: Request) {
  try {
    return (await req.json()) as any;
  } catch {
    return {};
  }
}

/** Simple fixed-window rate limit. Does not log secrets. */
function rateLimit(key: string, limit: number, windowMs: number) {
  const now = Date.now();
  const row = db.prepare("SELECT count, reset_at FROM rate_buckets WHERE key=?").get(key) as
    | { count: number; reset_at: number }
    | undefined;
  if (!row || row.reset_at <= now) {
    db.prepare(
      "INSERT INTO rate_buckets(key,count,reset_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET count=excluded.count, reset_at=excluded.reset_at",
    ).run(key, 1, now + windowMs);
    return { allowed: true, retryAfter: 0 };
  }
  if (row.count >= limit) {
    return { allowed: false, retryAfter: Math.ceil((row.reset_at - now) / 1000) };
  }
  db.prepare("UPDATE rate_buckets SET count=count+1 WHERE key=?").run(key);
  return { allowed: true, retryAfter: 0 };
}

function accountPublic(row: any) {
  return {
    accountId: row.id,
    username: row.username,
    email: row.email,
    firstName: row.first_name,
    middleInitial: row.middle_initial,
    lastName: row.last_name,
    dob: row.dob,
    phone: row.phone || "",
    stageName: row.stage_name || "",
    useType: row.use_type,
    companyName: row.company_name,
    termsVersion: row.terms_version,
    privacyVersion: row.privacy_version,
    profileComplete: Boolean(row.profile_complete ?? 1),
    totpEnabled: Boolean(row.totp_enabled),
    phoneVerified: Boolean(row.phone_verified),
    role: row.role || "member",
    ...(EMAIL_VERIFY ? { emailVerified: Boolean(row.email_verified) } : {}),
    status: row.status,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}

function accountAdmin(row: any) {
  return { ...accountPublic(row), /* never password_hash */ };
}

function logSecurity(accountId: string, type: string, req: Request, server: any, method = "", appId = "") {
  db.prepare(
    "INSERT INTO security_events(id,account_id,type,at,ip,user_agent,method,app_id) VALUES(?,?,?,?,?,?,?,?)",
  ).run(
    id("sec"),
    accountId,
    type,
    Date.now(),
    requestIp(req, server),
    String(req.headers.get("user-agent") || "").slice(0, 300),
    method,
    appId,
  );
  db.prepare("DELETE FROM security_events WHERE account_id=? AND at<?").run(accountId, Date.now() - SECURITY_RETENTION);
  const rows = db
    .prepare("SELECT id FROM security_events WHERE account_id=? ORDER BY at DESC LIMIT -1 OFFSET 50")
    .all(accountId) as any[];
  for (const r of rows) db.prepare("DELETE FROM security_events WHERE id=?").run(r.id);
}

function createSession(accountId: string, appId: string, method: string, req: Request, server: any) {
  const token = "inat_" + accountId + "." + rsx.random(32).toString("base64url");
  const now = Date.now();
  db.prepare("DELETE FROM sessions WHERE account_id=? AND expires_at<=?").run(accountId, now);
  const old = db
    .prepare("SELECT id FROM sessions WHERE account_id=? ORDER BY last_seen_at DESC LIMIT -1 OFFSET 20")
    .all(accountId) as any[];
  for (const r of old) db.prepare("DELETE FROM sessions WHERE id=?").run(r.id);
  db.prepare(
    "INSERT INTO sessions(id,account_id,token_hash,app_id,method,created_at,last_seen_at,expires_at,ip,user_agent) VALUES(?,?,?,?,?,?,?,?,?,?)",
  ).run(
    id("ses"),
    accountId,
    sha(token),
    appId,
    method,
    now,
    now,
    now + SESSION_TTL,
    requestIp(req, server),
    String(req.headers.get("user-agent") || "").slice(0, 300),
  );
  logSecurity(accountId, "login", req, server, method, appId);
  return token;
}

/** Owner = provisioned on this machine (role alone, email or username never suffice). */
function isOwner(account: any) {
  return Boolean(account && account.role === "owner" && Number(account.owner_provisioned) === 1 && isOwnerEmail(account.email));
}

function accessFor(account: any, appId: string) {
  if (isOwner(account)) return { allowed: true, kind: "owner", tier: STUDIO_TIER, source: "owner", expiresAt: null };
  const row = db
    .prepare(
      "SELECT * FROM entitlements WHERE account_id=? AND app_id=? AND status='active' AND (expires_at IS NULL OR expires_at>?) ORDER BY updated_at DESC LIMIT 1",
    )
    .get(account.id, appId, Date.now()) as any;
  return row
    ? { allowed: true, kind: row.tier || "subscription", tier: row.tier, source: row.source, expiresAt: row.expires_at ?? null }
    : { allowed: false, kind: "none", tier: null, source: null, expiresAt: null };
}

function upsertEntitlement(
  accountId: string,
  appId: string,
  tier: string,
  source: string,
  externalId: string | null,
  status: string,
  expiresAt: number | null,
) {
  const now = Date.now();
  const existing = externalId
    ? (db.prepare("SELECT * FROM entitlements WHERE source=? AND external_id=?").get(source, externalId) as any)
    : null;
  if (existing) {
    db.prepare("UPDATE entitlements SET account_id=?,app_id=?,tier=?,status=?,expires_at=?,updated_at=? WHERE id=?")
      .run(accountId, appId, tier, status, expiresAt, now, existing.id);
    return existing.id;
  }
  const eid = id("ent");
  db.prepare(
    "INSERT INTO entitlements(id,account_id,app_id,tier,source,external_id,status,expires_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)",
  ).run(eid, accountId, appId, tier, source, externalId, status, expiresAt, now, now);
  return eid;
}

function stripeSignatureValid(raw: string, header: string) {
  if (!STRIPE_WEBHOOK_SECRET || !header) return false;
  const parts = Object.fromEntries(
    header.split(",").map((part) => {
      const i = part.indexOf("=");
      return i > 0 ? [part.slice(0, i), part.slice(i + 1)] : ["", ""];
    }),
  );
  const timestamp = Number(parts.t || 0);
  const provided = parts.v1 || "";
  if (!Number.isFinite(timestamp) || Math.abs(Date.now() / 1000 - timestamp) > 300 || !provided) return false;
  const expected = rsx.hmac(Buffer.from(STRIPE_WEBHOOK_SECRET, "utf8"), Buffer.from(String(timestamp) + "." + raw, "utf8")).toString("hex");
  return safeEqual(expected, provided);
}

function processStripeEvent(event: any) {
  if (!event?.id || !event?.type) return;
  if (db.prepare("SELECT id FROM billing_events WHERE id=?").get(event.id)) return;
  const object = event?.data?.object || {};
  if (["checkout.session.completed", "checkout.session.async_payment_succeeded"].includes(event.type)) {
    const accountId = String(object.client_reference_id || "");
    const paymentLink = typeof object.payment_link === "string" ? object.payment_link : object.payment_link?.id;
    const subscription = typeof object.subscription === "string" ? object.subscription : object.subscription?.id;
    const account = db.prepare("SELECT id FROM accounts WHERE id=? AND status='active'").get(accountId) as any;
    const metadataTier = String(object.metadata?.tier || "").toLowerCase();
    const tier = paymentLink === STRIPE_BASIC_PAYMENT_LINK_ID || metadataTier === "basic"
      ? "basic"
      : paymentLink === STRIPE_PRO_PAYMENT_LINK_ID || metadataTier === "pro"
        ? "pro"
        : "";
    if (account && subscription && tier) {
      upsertEntitlement(account.id, STUDIO_APP, tier, "stripe", subscription, "active", null);
    }
  } else if (event.type.startsWith("customer.subscription.")) {
    const subscriptionId = String(object.id || "");
    const existing = subscriptionId
      ? (db.prepare("SELECT * FROM entitlements WHERE source='stripe' AND external_id=?").get(subscriptionId) as any)
      : null;
    if (existing) {
      const active = ["active", "trialing"].includes(String(object.status || ""));
      const expires = Number(object.current_period_end || 0) > 0 ? Number(object.current_period_end) * 1000 : null;
      db.prepare("UPDATE entitlements SET status=?,expires_at=?,updated_at=? WHERE id=?")
        .run(active ? "active" : "inactive", expires, Date.now(), existing.id);
    }
  }
  db.prepare("INSERT OR IGNORE INTO billing_events(id,provider,event_type,processed_at) VALUES(?,?,?,?)").run(
    event.id,
    "stripe",
    event.type,
    Date.now(),
  );
}

function nationSession(token: unknown) {
  if (typeof token !== "string" || !token.startsWith("inat_")) return null;
  const dot = token.indexOf(".");
  if (dot < 10) return null;
  const accountId = token.slice(5, dot);
  const session = db
    .prepare("SELECT * FROM sessions WHERE account_id=? AND token_hash=? AND expires_at>?")
    .get(accountId, sha(token), Date.now()) as any;
  if (!session) return null;
  const account = db.prepare("SELECT * FROM accounts WHERE id=? AND status='active'").get(accountId) as any;
  if (!account) return null;
  if (Date.now() - session.last_seen_at > 600000)
    db.prepare("UPDATE sessions SET last_seen_at=? WHERE id=?").run(Date.now(), session.id);
  return { account, session };
}

function extractBearer(req: Request) {
  const h = req.headers.get("authorization") || "";
  if (h.toLowerCase().startsWith("bearer ")) return h.slice(7).trim();
  return (req.headers.get("x-api-key") || "").trim();
}

function verifyApiKey(req: Request) {
  const raw = extractBearer(req);
  if (!raw) return null;
  const hash = sha(raw);
  const row = db.prepare("SELECT * FROM api_keys WHERE key_hash=? AND revoked_at=0").get(hash) as any;
  if (!row) return null;
  db.prepare("UPDATE api_keys SET last_used_at=? WHERE id=?").run(Date.now(), row.id);
  return row;
}

function requireAdmin(req: Request, b: any) {
  const token = b.token || extractBearer(req);
  if (ADMIN_PANEL_TOKEN && token && safeEqual(sha(token), sha(ADMIN_PANEL_TOKEN))) {
    return { via: "panel" as const };
  }
  const s = nationSession(token);
  if (s && isOwner(s.account)) return { via: "owner" as const, account: s.account };
  return null;
}

function cors(req: Request) {
  const origin = req.headers.get("origin") || "";
  const allowed = [
    "capacitor://localhost",
    "http://localhost",
    "https://localhost",
    "http://127.0.0.1:4200",
    "http://localhost:4200",
    "https://app.infectedvoices.space",
    "https://nation.infectedvoices.space",
  ];
  if (allowed.includes(origin) || /\.infectedvoices\.space$/.test(new URL(origin || "http://x").hostname || "")) {
    if (origin) return { "access-control-allow-origin": origin, vary: "origin", "access-control-allow-credentials": "true" };
  }
  return {};
}

async function insertAccount(fields: {
  username: string;
  email: string;
  password: string;
  dob: string;
  firstName: string;
  middleInitial?: string;
  lastName: string;
  phone?: string;
  stageName?: string;
  useType?: string;
  companyName?: string;
  termsAccepted?: boolean;
  profileComplete?: boolean;
  role?: string;
}) {
  const aid = id("nat");
  const now = Date.now();
  const hash = await hashPassword(fields.password);
  const mi = String(fields.middleInitial || "")
    .trim()
    .toUpperCase();
  const useType = fields.useType === "company" ? "company" : "personal";
  db.prepare(
    `INSERT INTO accounts(
      id,username,email,password_hash,dob,first_name,middle_initial,last_name,
      use_type,company_name,terms_version,privacy_version,terms_accepted_at,status,
      created_at,updated_at,phone,stage_name,profile_complete,phone_verified,totp_enabled,role
    ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'active',?,?,?,?,?,?,?,?)`,
  ).run(
    aid,
    fields.username.trim(),
    normalizeEmail(fields.email),
    hash,
    fields.dob,
    fields.firstName.trim(),
    mi,
    fields.lastName.trim(),
    useType,
    useType === "company" ? String(fields.companyName || "").trim() : "",
    TERMS_VERSION,
    PRIVACY_VERSION,
    now,
    now,
    now,
    String(fields.phone || "").trim(),
    String(fields.stageName || "").trim(),
    fields.profileComplete === false ? 0 : 1,
    0,
    0,
    fields.role || "member",
  );
  return db.prepare("SELECT * FROM accounts WHERE id=?").get(aid) as any;
}

/** Ensure apps API key exists; write plaintext ONCE to local tmp if newly generated. */
function ensureAppsApiKey() {
  const existing = db.prepare("SELECT id FROM api_keys WHERE name='infected-apps' AND revoked_at=0").get() as any;
  const fromEnv = process.env.INFECTED_APPS_API_KEY?.trim();
  if (fromEnv) {
    const hash = sha(fromEnv);
    if (!existing) {
      db.prepare("INSERT INTO api_keys(id,name,key_hash,created_at) VALUES(?,?,?,?)").run(
        id("key"),
        "infected-apps",
        hash,
        Date.now(),
      );
    } else {
      db.prepare("UPDATE api_keys SET key_hash=? WHERE name='infected-apps' AND revoked_at=0").run(hash);
    }
    return { generated: false, path: null as string | null };
  }
  if (existing) return { generated: false, path: null as string | null };

  const raw = "inak_" + rsx.random(32).toString("base64url");
  db.prepare("INSERT INTO api_keys(id,name,key_hash,created_at) VALUES(?,?,?,?)").run(
    id("key"),
    "infected-apps",
    sha(raw),
    Date.now(),
  );

  const localAppData =
    process.env.LOCALAPPDATA ||
    (process.env.HOME ? join(process.env.HOME, "AppData", "Local") : "");
  const tmpDir = localAppData
    ? join(localAppData, "RedXAIHost", "tmp")
    : resolve("./data/tmp");
  mkdirSync(tmpDir, { recursive: true });
  const outPath = join(tmpDir, "INFECTED_APPS_API_KEY.once.txt");
  writeFileSync(
    outPath,
    [
      "# Infected Apps API key — shown once. Do not commit.",
      "# Use as: Authorization: Bearer <key>",
      `# Generated: ${new Date().toISOString()}`,
      raw,
      "",
    ].join("\n"),
    { mode: 0o600 },
  );
  // Never console.log the key value.
  console.log(`[identity] Generated INFECTED_APPS_API_KEY (written once to local tmp; not logged).`);
  return { generated: true, path: outPath };
}

const apiKeyBootstrap = ensureAppsApiKey();

/** Issue (or replace) the account's verification token and send it. Returns whether delivery succeeded. */
async function issueEmailVerification(account: any): Promise<boolean> {
  const { token, hash } = newVerifyToken(account.id);
  const now = Date.now();
  db.prepare(
    `INSERT INTO email_verifications(account_id,token_hash,expires_at,sent_at) VALUES(?,?,?,?)
     ON CONFLICT(account_id) DO UPDATE SET token_hash=excluded.token_hash, expires_at=excluded.expires_at, sent_at=excluded.sent_at`,
  ).run(account.id, hash, now + EMAIL_VERIFY_TTL_MS, now);
  return sendMail(verifyMail(account.email, token));
}

// Paths an unverified account may not use while email verification is on.
const UNVERIFIED_BLOCKED = [
  "/api/v1/2fa/", "/api/v1/access", "/api/v1/billing/stripe/checkout", "/api/v1/connect/", "/api/v1/oauth/complete-profile",
  "/api/v1/onboarding/set",
];

async function api(req: Request, server: any, url: URL) {
  const path = url.pathname;
  const ip = requestIp(req, server);

  if (req.method === "OPTIONS") {
    return new Response(null, {
      status: 204,
      headers: {
        ...cors(req),
        "access-control-allow-methods": "GET, POST, PATCH, DELETE, OPTIONS",
        "access-control-allow-headers": "content-type, authorization, x-api-key",
        "access-control-max-age": "600",
      },
    });
  }

  if (path === "/api/health") {
    const accounts = (db.prepare("SELECT COUNT(*) AS c FROM accounts").get() as any).c;
    const keys = (db.prepare("SELECT COUNT(*) AS c FROM api_keys WHERE revoked_at=0").get() as any).c;
    return json({
      ok: true,
      service: "InfectedNation",
      schemaVersion: SCHEMA_VERSION,
      accounts,
      apiKeys: keys,
      billing: { stripe: Boolean((STRIPE_PRO_PAYMENT_LINK_URL || STRIPE_BASIC_PAYMENT_LINK_URL) && STRIPE_WEBHOOK_SECRET) },
      dataPathHint: DATA.endsWith("infectednation.sqlite") ? "infectednation.sqlite" : "ok",
    });
  }

  if (path === "/api/v1/meta") {
    return json({
      service: "InfectedNation",
      termsVersion: TERMS_VERSION,
      privacyVersion: PRIVACY_VERSION,
      securityIpRetentionDays: 180,
      studioApp: STUDIO_APP,
      studioTier: STUDIO_TIER,
      studioScheme: STUDIO_SCHEME,
      studioWebUrl: STUDIO_WEB_URL,
      schemaVersion: SCHEMA_VERSION,
      validation: {
        usernameMinLetters: 3,
        passwordMin: 8,
        dobYearMin: Number(latestAllowedDob(new Date(), MAX_AGE + 1).slice(0, 4)),
        dobYearMax: Number(latestAllowedDob().slice(0, 4)),
        dobMax: latestAllowedDob(),
        minAge: MIN_AGE,
        maxAge: MAX_AGE,
      },
    });
  }

  if (path === "/api/v1/studio/open" && (req.method === "GET" || req.method === "POST")) {
    return json({
      scheme: STUDIO_SCHEME + "studio",
      intent: "android.intent.action.VIEW",
      webFallback: STUDIO_WEB_URL,
      installedHint: "Try custom URL infectedvoices://studio; if the OS cannot open it, use webFallback.",
    });
  }

  if (path === "/api/v1/billing/stripe/webhook" && req.method === "POST") {
    const raw = await req.text();
    const signature = req.headers.get("stripe-signature") || "";
    if (!stripeSignatureValid(raw, signature)) return json({ error: "Invalid Stripe signature." }, 400);
    let event: any;
    try {
      event = JSON.parse(raw);
    } catch {
      return json({ error: "Invalid Stripe payload." }, 400);
    }
    try {
      processStripeEvent(event);
      return json({ received: true });
    } catch (error) {
      console.error("[stripe-webhook] processing failed");
      return json({ error: "Webhook processing failed." }, 500);
    }
  }

  const authPaths = ["/api/v1/signup/email", "/api/v1/login/email", "/api/v1/login"];
  if (authPaths.includes(path)) {
    const rl = rateLimit(`auth:${ip}`, 20, 60_000);
    if (!rl.allowed) return json({ error: "Too many attempts. Try again shortly.", retryAfter: rl.retryAfter }, 429);
  }

  const b = await body(req);

  if (EMAIL_VERIFY) {
    if (UNVERIFIED_BLOCKED.some((p) => path === p || (p.endsWith("/") && path.startsWith(p)))) {
      const s = nationSession(b.token);
      if (s && !s.account.email_verified) {
        return json({ error: "Confirm your email address first.", code: "email_unverified" }, 403);
      }
    }
    if (path === "/api/v1/email/verify" && req.method === "POST") {
      const rl = rateLimit("emailverify:ip:" + ip, 20, 15 * 60 * 1000);
      if (!rl.allowed) return json({ error: "Too many attempts. Try again later." }, 429, { "retry-after": String(rl.retryAfter) });
      const bad = () => json({ error: "This link is invalid or has expired. Request a new one." }, 400);
      const t = parseVerifyToken(b.token);
      if (!t) return bad();
      const row = db.prepare("SELECT * FROM email_verifications WHERE account_id=?").get(t.accountId) as any;
      if (!row || !tokenMatches(t.secret, row.token_hash) || row.expires_at <= Date.now()) return bad();
      // Single use: the delete must be the one that consumes this exact token.
      const del = db.prepare("DELETE FROM email_verifications WHERE account_id=? AND token_hash=?").run(t.accountId, row.token_hash);
      if (del.changes !== 1) return bad();
      db.prepare("UPDATE accounts SET email_verified=1, updated_at=? WHERE id=?").run(Date.now(), t.accountId);
      logSecurity(t.accountId, "email_verified", req, server);
      return json({ verified: true });
    }
    if (path === "/api/v1/email/resend" && req.method === "POST") {
      const s = nationSession(b.token);
      if (!s) return json({ error: "Session expired or invalid." }, 401);
      if (s.account.email_verified) return json({ verified: true });
      const last = db.prepare("SELECT sent_at FROM email_verifications WHERE account_id=?").get(s.account.id) as any;
      if (last && Date.now() - last.sent_at < RESEND_MIN_INTERVAL_MS) {
        return json({ error: "Please wait a minute before requesting another email." }, 429, { "retry-after": String(Math.ceil((RESEND_MIN_INTERVAL_MS - (Date.now() - last.sent_at)) / 1000)) });
      }
      for (const key of ["emailresend:acct:" + s.account.id, "emailresend:ip:" + ip]) {
        const rl = rateLimit(key, key.includes(":ip:") ? 10 : RESEND_PER_HOUR, 60 * 60 * 1000);
        if (!rl.allowed) return json({ error: "Too many emails requested. Try again later." }, 429, { "retry-after": String(rl.retryAfter) });
      }
      const sent = await issueEmailVerification(s.account);
      return sent ? json({ sent: true }) : json({ error: "Email delivery is not available right now." }, 503);
    }
  }

  // ---- Apps API key routes ----
  if (path.startsWith("/api/v1/apps/")) {
    const key = verifyApiKey(req);
    if (!key) return json({ error: "Valid API key required in Authorization: Bearer header." }, 401);
    const rl = rateLimit(`apps:${key.id}:${ip}`, 120, 60_000);
    if (!rl.allowed) return json({ error: "Rate limited.", retryAfter: rl.retryAfter }, 429);

    if (path === "/api/v1/apps/users/check" && req.method === "POST") {
      const username = b.username ? normalizeUsername(String(b.username)) : "";
      const email = b.email ? normalizeEmail(String(b.email)) : "";
      const accountId = b.accountId ? String(b.accountId) : "";
      let row: any = null;
      if (accountId) row = db.prepare("SELECT * FROM accounts WHERE id=?").get(accountId);
      else if (email) row = db.prepare("SELECT * FROM accounts WHERE email=? COLLATE NOCASE").get(email);
      else if (username) row = db.prepare("SELECT * FROM accounts WHERE username=? COLLATE NOCASE").get(username);
      else return json({ error: "Provide username, email, or accountId." }, 400);
      return json({ exists: Boolean(row), account: row ? accountPublic(row) : null });
    }

    if (path === "/api/v1/apps/users/create" && req.method === "POST") {
      const ue = usernameError(b.username);
      const pe = passwordError(b.password);
      const de = dobError(b.dob);
      const phe = phoneError(b.phone);
      if (ue || pe || de || phe) return json({ error: ue || pe || de || phe }, 400);
      if (b.password !== b.confirmPassword && b.confirmPassword != null) {
        return json({ error: "Passwords do not match." }, 400);
      }
      const email = normalizeEmail(String(b.email || ""));
      if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) return json({ error: "Enter a valid email address." }, 400);
      if (!String(b.firstName || "").trim() || !String(b.lastName || "").trim()) {
        return json({ error: "First and last name are required." }, 400);
      }
      if (b.termsAccepted !== true) return json({ error: "Terms of Service must be accepted." }, 400);
      if (isOwnerEmail(email)) return json({ error: "This email address is reserved." }, 403);
      if (
        db
          .prepare("SELECT id FROM accounts WHERE username=? COLLATE NOCASE OR email=? COLLATE NOCASE")
          .get(String(b.username).trim(), email)
      ) {
        return json({ error: "Username or email is already registered." }, 409);
      }
      const row = await insertAccount({
        username: String(b.username),
        email,
        password: String(b.password),
        dob: String(b.dob),
        firstName: String(b.firstName),
        middleInitial: b.middleInitial,
        lastName: String(b.lastName),
        phone: b.phone,
        stageName: b.stageName,
        useType: b.useType || "personal",
        companyName: b.companyName,
        termsAccepted: true,
      });
      return json({ account: accountPublic(row) }, 201);
    }

    if (path === "/api/v1/apps/users/list" && req.method === "POST") {
      const q = String(b.query || "").trim();
      const limit = Math.min(100, Math.max(1, Number(b.limit || 50)));
      const offset = Math.max(0, Number(b.offset || 0));
      const rows = q
        ? (db
            .prepare(
              `SELECT * FROM accounts WHERE username LIKE ? COLLATE NOCASE OR email LIKE ? COLLATE NOCASE OR stage_name LIKE ? COLLATE NOCASE
               ORDER BY created_at DESC LIMIT ? OFFSET ?`,
            )
            .all(`%${q}%`, `%${q}%`, `%${q}%`, limit, offset) as any[])
        : (db.prepare("SELECT * FROM accounts ORDER BY created_at DESC LIMIT ? OFFSET ?").all(limit, offset) as any[]);
      return json({ users: rows.map(accountPublic), limit, offset });
    }

    return json({ error: "Not found." }, 404);
  }

  // ---- Owner admin CRUD ----
  if (path.startsWith("/api/v1/admin/")) {
    const admin = requireAdmin(req, b);
    if (!admin) return json({ error: "Owner session or panel admin token required." }, 403);

    if (path === "/api/v1/admin/tables" && req.method === "POST") {
      const tables = db
        .prepare("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
        .all() as { name: string }[];
      return json({ tables: tables.map((t) => t.name) });
    }

    if (path === "/api/v1/admin/rows" && req.method === "POST") {
      const table = String(b.table || "");
      const allowed = new Set([
        "accounts",
        "sessions",
        "security_events",
        "connect_requests",
        "entitlements",
        "app_onboarding",
        "billing_events",
        "oauth_identities",
        "totp_secrets",
        "api_keys",
        "schema_meta",
      ]);
      if (!allowed.has(table)) return json({ error: "Table not allowed." }, 400);
      const limit = Math.min(200, Math.max(1, Number(b.limit || 50)));
      const offset = Math.max(0, Number(b.offset || 0));
      const rows = db.prepare(`SELECT * FROM ${table} ORDER BY rowid DESC LIMIT ? OFFSET ?`).all(limit, offset) as any[];
      const sanitized = rows.map((row) => {
        const copy = { ...row };
        if ("password_hash" in copy) copy.password_hash = "[redacted]";
        if ("key_hash" in copy) copy.key_hash = "[redacted]";
        if ("token_hash" in copy) copy.token_hash = "[redacted]";
        if ("secret_hash" in copy) copy.secret_hash = "[redacted]";
        if ("secret_enc" in copy) copy.secret_enc = "[redacted]";
        return copy;
      });
      return json({ table, rows: sanitized, limit, offset });
    }

    if (path === "/api/v1/admin/users" && req.method === "POST") {
      const q = String(b.query || "").trim();
      const limit = Math.min(200, Math.max(1, Number(b.limit || 50)));
      const offset = Math.max(0, Number(b.offset || 0));
      const rows = q
        ? (db
            .prepare(
              `SELECT * FROM accounts WHERE username LIKE ? COLLATE NOCASE OR email LIKE ? COLLATE NOCASE
               ORDER BY created_at DESC LIMIT ? OFFSET ?`,
            )
            .all(`%${q}%`, `%${q}%`, limit, offset) as any[])
        : (db.prepare("SELECT * FROM accounts ORDER BY created_at DESC LIMIT ? OFFSET ?").all(limit, offset) as any[]);
      return json({ users: rows.map(accountAdmin) });
    }

    if (path === "/api/v1/admin/users/update" && req.method === "POST") {
      const accountId = String(b.accountId || "");
      const row = db.prepare("SELECT * FROM accounts WHERE id=?").get(accountId) as any;
      if (!row) return json({ error: "User not found." }, 404);
      const next = {
        first_name: b.firstName != null ? String(b.firstName).trim() : row.first_name,
        middle_initial: b.middleInitial != null ? String(b.middleInitial).trim().toUpperCase() : row.middle_initial,
        last_name: b.lastName != null ? String(b.lastName).trim() : row.last_name,
        phone: b.phone != null ? String(b.phone).trim() : row.phone,
        stage_name: b.stageName != null ? String(b.stageName).trim() : row.stage_name,
        status: b.status != null ? String(b.status) : row.status,
        role: b.role != null ? String(b.role) : row.role,
      };
      if (!["active", "disabled", "banned"].includes(next.status)) return json({ error: "Invalid status." }, 400);
      if (!["member", "admin", "owner"].includes(next.role)) return json({ error: "Invalid role." }, 400);
      if (next.role !== row.role && (next.role === "owner" || row.role === "owner")) {
        return json({ error: "Owner accounts are managed only by provision-owner on the host." }, 403);
      }
      db.prepare(
        "UPDATE accounts SET first_name=?, middle_initial=?, last_name=?, phone=?, stage_name=?, status=?, role=?, updated_at=? WHERE id=?",
      ).run(
        next.first_name,
        next.middle_initial,
        next.last_name,
        next.phone,
        next.stage_name,
        next.status,
        next.role,
        Date.now(),
        accountId,
      );
      if (b.password) {
        const pe = passwordError(b.password);
        if (pe) return json({ error: pe }, 400);
        const hash = await hashPassword(String(b.password));
        db.prepare("UPDATE accounts SET password_hash=?, updated_at=? WHERE id=?").run(hash, Date.now(), accountId);
      }
      return json({ account: accountAdmin(db.prepare("SELECT * FROM accounts WHERE id=?").get(accountId)) });
    }

    if (path === "/api/v1/admin/users/delete" && req.method === "POST") {
      const accountId = String(b.accountId || "");
      const row = db.prepare("SELECT * FROM accounts WHERE id=?").get(accountId) as any;
      if (!row) return json({ error: "User not found." }, 404);
      if (isOwner(row)) {
        return json({ error: "Refusing to delete a provisioned owner account." }, 400);
      }
      db.prepare("DELETE FROM accounts WHERE id=?").run(accountId);
      return json({ deleted: true, accountId });
    }

    if (path === "/api/v1/admin/rows/delete" && req.method === "POST") {
      const table = String(b.table || "");
      const rowId = String(b.id || "");
      const allowed = new Set([
        "sessions",
        "security_events",
        "connect_requests",
        "entitlements",
        "app_onboarding",
        "oauth_identities",
      ]);
      if (!allowed.has(table) || !rowId) return json({ error: "Not allowed." }, 400);
      if (table === "app_onboarding") {
        // composite key via account_id+app_id encoded as accountId|appId
        const [aid, appId] = rowId.split("|");
        db.prepare("DELETE FROM app_onboarding WHERE account_id=? AND app_id=?").run(aid, appId);
      } else {
        db.prepare(`DELETE FROM ${table} WHERE id=?`).run(rowId);
      }
      return json({ deleted: true });
    }

    return json({ error: "Not found." }, 404);
  }

  if (path === "/api/v1/username/check") {
    const e = usernameError(b.username);
    if (e) return json({ available: false, reason: e });
    const row = db.prepare("SELECT id FROM accounts WHERE username=? COLLATE NOCASE").get(normalizeUsername(b.username));
    return json({ available: !row, reason: row ? "That username is already taken." : "" });
  }

  if (path === "/api/v1/signup/email") {
    if (!validApp(b.appId)) return json({ error: "Invalid application identifier." }, 400);
    const ue = usernameError(b.username);
    const pe = passwordError(b.password);
    const de = dobError(b.dob);
    const phe = phoneError(b.phone);
    if (ue || pe || de || phe) return json({ error: ue || pe || de || phe }, 400);
    if (b.password !== b.confirmPassword && b.confirmPassword != null) {
      return json({ error: "Passwords do not match." }, 400);
    }
    const email = normalizeEmail(String(b.email || ""));
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) return json({ error: "Enter a valid email address." }, 400);
    if (isOwnerEmail(email)) return json({ error: "This email address is reserved." }, 403);
    if (!String(b.firstName || "").trim() || !String(b.lastName || "").trim()) {
      return json({ error: "First and last name are required." }, 400);
    }
    const mi = String(b.middleInitial || "")
      .trim()
      .toUpperCase();
    if (mi && !/^[A-Z]$/.test(mi)) return json({ error: "Middle initial must be one letter." }, 400);
    if (!["personal", "company"].includes(b.useType)) return json({ error: "Choose personal or company use." }, 400);
    if (b.useType === "company" && !String(b.companyName || "").trim()) {
      return json({ error: "Company name is required." }, 400);
    }
    if (
      b.termsAccepted !== true ||
      b.termsScrollCompleted !== true ||
      b.termsVersion !== TERMS_VERSION ||
      b.privacyVersion !== PRIVACY_VERSION
    ) {
      return json({ error: "Read and accept the current Terms and Privacy Policy." }, 400);
    }
    // Community requires stage_name when appId looks like community
    if ((b.appId === "infected-nation" || b.appId === "infectednation" || b.requireStageName) && !String(b.stageName || "").trim()) {
      return json({ error: "Stage name is required for community signup." }, 400);
    }
    if (
      db
        .prepare("SELECT id FROM accounts WHERE username=? COLLATE NOCASE OR email=? COLLATE NOCASE")
        .get(String(b.username).trim(), email)
    ) {
      return json({ error: "Username or email is already registered." }, 409);
    }
    const row = await insertAccount({
      username: String(b.username),
      email,
      password: String(b.password),
      dob: String(b.dob),
      firstName: String(b.firstName),
      middleInitial: mi,
      lastName: String(b.lastName),
      phone: b.phone,
      stageName: b.stageName,
      useType: b.useType,
      companyName: b.companyName,
      termsAccepted: true,
    });
    const token = createSession(row.id, b.appId, "email", req, server);
    if (EMAIL_VERIFY) {
      const sent = await issueEmailVerification(row);
      return json({ token, account: accountPublic(db.prepare("SELECT * FROM accounts WHERE id=?").get(row.id)), emailVerificationRequired: true, emailSent: sent }, 201);
    }
    return json({ token, account: accountPublic(db.prepare("SELECT * FROM accounts WHERE id=?").get(row.id)) }, 201);
  }

  if (path === "/api/v1/login/email" || path === "/api/v1/login") {
    if (!validApp(b.appId)) return json({ error: "Invalid application identifier." }, 400);
    const login = String(b.email || b.username || b.login || "").trim();
    let row: any = null;
    if (login.includes("@")) {
      row = db.prepare("SELECT * FROM accounts WHERE email=? COLLATE NOCASE AND status='active'").get(normalizeEmail(login));
    } else {
      row = db.prepare("SELECT * FROM accounts WHERE username=? COLLATE NOCASE AND status='active'").get(normalizeUsername(login));
    }
    if (!row || !(await verifyPassword(String(b.password || ""), row.password_hash))) {
      if (row) logSecurity(row.id, "password_failure", req, server, "email", String(b.appId || ""));
      return json({ error: "Email/username or password is incorrect." }, 401);
    }
    if (passwordNeedsRehash(row.password_hash)) {
      db.prepare("UPDATE accounts SET password_hash=?, updated_at=? WHERE id=? AND password_hash=?")
        .run(await hashPassword(String(b.password)), Date.now(), row.id, row.password_hash);
    }
    // TOTP: always enforced when enabled (no env switch), RFC 6238, one step of skew, no replay.
    if (row.totp_enabled) {
      const code = String(b.totpCode ?? "");
      if (!code) return json({ error: "Authenticator code required.", needsTotp: true }, 401);
      const t = db.prepare("SELECT * FROM totp_secrets WHERE account_id=? AND enabled=1").get(row.id) as any;
      let key: Buffer;
      try {
        if (!t) throw new Error("no secret");
        key = openSecret(t.secret_enc, totpMasterSecret(), row.id);
      } catch (e) {
        // Fail closed: a 2FA account is never let in without a verifiable code.
        logSecurity(row.id, "totp_unavailable", req, server, "email", String(b.appId || ""));
        return json({ error: e instanceof TotpKeyMissing ? "Two-factor sign-in is unavailable on this server." : "Authenticator check failed.", needsTotp: true }, 503);
      }
      const step = verifyTotp(key, code, Date.now() / 1000, Number(t.last_step));
      if (step === null) {
        logSecurity(row.id, "totp_failure", req, server, "email", String(b.appId || ""));
        return json({ error: "Invalid authenticator code.", needsTotp: true }, 401);
      }
      const upd = db.prepare("UPDATE totp_secrets SET last_step=? WHERE account_id=? AND last_step<?").run(step, row.id, step);
      if (upd.changes !== 1) return json({ error: "Invalid authenticator code.", needsTotp: true }, 401);
    }
    const token = createSession(row.id, b.appId, "email", req, server);
    return json({ token, account: accountPublic(row), ...(row.totp_reenroll ? { totpReenrollRequired: true } : {}) });
  }

  if (path === "/api/v1/session/me") {
    const s = nationSession(b.token);
    return s ? json({ account: accountPublic(s.account) }) : json({ error: "Session expired or invalid." }, 401);
  }
  if (path === "/api/v1/session/logout") {
    const s = nationSession(b.token);
    if (s) {
      db.prepare("DELETE FROM sessions WHERE token_hash=?").run(sha(b.token));
      logSecurity(s.account.id, "logout", req, server, s.session.method, s.session.app_id);
    }
    return json({ loggedOut: true });
  }
  if (path === "/api/v1/security/history") {
    const s = nationSession(b.token);
    if (!s) return json({ error: "Session expired or invalid." }, 401);
    return json({
      retentionDays: 180,
      items: db
        .prepare(
          "SELECT id,type,at,ip,user_agent AS userAgent,method,app_id AS appId FROM security_events WHERE account_id=? AND at>=? ORDER BY at DESC LIMIT 50",
        )
        .all(s.account.id, Date.now() - SECURITY_RETENTION),
    });
  }

  // OAuth profile completion (first OAuth signup still collects required fields)
  if (path === "/api/v1/oauth/complete-profile") {
    const s = nationSession(b.token);
    if (!s) return json({ error: "Session expired or invalid." }, 401);
    const ue = usernameError(b.username);
    const de = dobError(b.dob);
    const phe = phoneError(b.phone);
    if (ue || de || phe) return json({ error: ue || de || phe }, 400);
    if (!String(b.firstName || "").trim() || !String(b.lastName || "").trim()) {
      return json({ error: "First and last name are required." }, 400);
    }
    if (b.termsAccepted !== true) return json({ error: "Terms of Service must be accepted." }, 400);
    const taken = db
      .prepare("SELECT id FROM accounts WHERE username=? COLLATE NOCASE AND id<>?")
      .get(String(b.username).trim(), s.account.id);
    if (taken) return json({ error: "Username is already taken." }, 409);
    db.prepare(
      `UPDATE accounts SET username=?, dob=?, first_name=?, middle_initial=?, last_name=?, phone=?, stage_name=?,
       terms_version=?, privacy_version=?, terms_accepted_at=?, profile_complete=1, updated_at=? WHERE id=?`,
    ).run(
      String(b.username).trim(),
      b.dob,
      String(b.firstName).trim(),
      String(b.middleInitial || "")
        .trim()
        .toUpperCase(),
      String(b.lastName).trim(),
      String(b.phone || "").trim(),
      String(b.stageName || "").trim(),
      TERMS_VERSION,
      PRIVACY_VERSION,
      Date.now(),
      Date.now(),
      s.account.id,
    );
    return json({ account: accountPublic(db.prepare("SELECT * FROM accounts WHERE id=?").get(s.account.id)) });
  }

  // Optional 2FA setup stubs
  if (path === "/api/v1/2fa/totp/setup") {
    const s = nationSession(b.token);
    if (!s) return json({ error: "Session expired or invalid." }, 401);
    if (s.account.totp_enabled) return json({ error: "Two-factor is already on; turn it off first." }, 409);
    let master: string;
    try { master = totpMasterSecret(); } catch { return json({ error: "Two-factor setup is unavailable on this server." }, 503); }
    const { raw, base32 } = newTotpSecret();
    const now = Date.now();
    db.prepare(
      `INSERT INTO totp_secrets(account_id,secret_enc,enabled,last_step,created_at,updated_at) VALUES(?,?,0,-1,?,?)
       ON CONFLICT(account_id) DO UPDATE SET secret_enc=excluded.secret_enc, enabled=0, last_step=-1, updated_at=excluded.updated_at`,
    ).run(s.account.id, sealSecret(raw, master, s.account.id), now, now);
    return json({
      secret: base32,
      otpauthUrl: otpauthUri("InfectedNation", s.account.username, base32),
      note: "Confirm with /api/v1/2fa/totp/enable and the 6-digit code from your authenticator app.",
    });
  }
  if (path === "/api/v1/2fa/totp/enable") {
    const s = nationSession(b.token);
    if (!s) return json({ error: "Session expired or invalid." }, 401);
    const code = String(b.code || "");
    if (!/^\d{6}$/.test(code)) return json({ error: "Enter the 6-digit authenticator code." }, 400);
    const t = db.prepare("SELECT * FROM totp_secrets WHERE account_id=?").get(s.account.id) as any;
    if (!t) return json({ error: "Start two-factor setup first." }, 400);
    let key: Buffer;
    try { key = openSecret(t.secret_enc, totpMasterSecret(), s.account.id); } catch { return json({ error: "Two-factor setup is unavailable on this server." }, 503); }
    const step = verifyTotp(key, code, Date.now() / 1000, Number(t.last_step));
    if (step === null) return json({ error: "That code does not match. Check the time on your device and try again." }, 401);
    db.prepare("UPDATE totp_secrets SET enabled=1, last_step=?, updated_at=? WHERE account_id=?").run(step, Date.now(), s.account.id);
    db.prepare("UPDATE accounts SET totp_enabled=1, totp_reenroll=0, updated_at=? WHERE id=?").run(Date.now(), s.account.id);
    return json({ enabled: true });
  }
  if (path === "/api/v1/2fa/totp/disable") {
    const s = nationSession(b.token);
    if (!s) return json({ error: "Session expired or invalid." }, 401);
    if (!(await verifyPassword(String(b.password || ""), s.account.password_hash))) {
      return json({ error: "Password is incorrect." }, 401);
    }
    db.prepare("DELETE FROM totp_secrets WHERE account_id=?").run(s.account.id);
    db.prepare("UPDATE accounts SET totp_enabled=0, updated_at=? WHERE id=?").run(Date.now(), s.account.id);
    return json({ enabled: false });
  }
  if (path === "/api/v1/2fa/phone/set") {
    const s = nationSession(b.token);
    if (!s) return json({ error: "Session expired or invalid." }, 401);
    const phe = phoneError(b.phone);
    if (phe || !String(b.phone || "").trim()) return json({ error: phe || "Phone is required." }, 400);
    db.prepare("UPDATE accounts SET phone=?, phone_verified=0, updated_at=? WHERE id=?").run(
      String(b.phone).trim(),
      Date.now(),
      s.account.id,
    );
    return json({
      phone: String(b.phone).trim(),
      phoneVerified: false,
      note: "SMS verification provider not configured on this host; phone stored for future SMS 2FA.",
    });
  }

  if (path === "/api/v1/access") {
    const s = nationSession(b.token);
    if (!s) return json({ error: "Session expired or invalid." }, 401);
    const appId = validApp(b.appId) ? String(b.appId) : STUDIO_APP;
    return json({ email: s.account.email, owner: isOwner(s.account), ...accessFor(s.account, appId) });
  }
  if (path === "/api/v1/billing/stripe/checkout") {
    const s = nationSession(b.token);
    if (!s) return json({ error: "Session expired or invalid." }, 401);
    const appId = validApp(b.appId) ? String(b.appId) : STUDIO_APP;
    if (appId !== STUDIO_APP) return json({ error: "No Stripe product is configured for this app." }, 400);
    const tier = String(b.tier || "pro").toLowerCase();
    if (!["basic", "pro"].includes(tier)) return json({ error: "Choose Basic or Pro." }, 400);
    const baseUrl = tier === "basic" ? STRIPE_BASIC_PAYMENT_LINK_URL : STRIPE_PRO_PAYMENT_LINK_URL;
    if (!baseUrl) return json({ error: "Stripe checkout is not configured for this tier." }, 503);
    const separator = baseUrl.includes("?") ? "&" : "?";
    const url =
      baseUrl +
      separator +
      "client_reference_id=" +
      encodeURIComponent(s.account.id) +
      "&locked_prefilled_email=" +
      encodeURIComponent(s.account.email);
    return json({ url, tier });
  }
  if (path === "/api/v1/onboarding/get") {
    const s = nationSession(b.token);
    if (!s) return json({ error: "Session expired or invalid." }, 401);
    const appId = validApp(b.appId) ? String(b.appId) : STUDIO_APP;
    const row = db.prepare("SELECT * FROM app_onboarding WHERE account_id=? AND app_id=?").get(s.account.id, appId) as any;
    return json({ version: row?.version || "", progress: row?.progress || 0, complete: Boolean(row?.complete) });
  }
  if (path === "/api/v1/onboarding/set") {
    const s = nationSession(b.token);
    if (!s) return json({ error: "Session expired or invalid." }, 401);
    const appId = validApp(b.appId) ? String(b.appId) : STUDIO_APP;
    const version = String(b.version || "").slice(0, 80);
    const step = Math.max(0, Math.min(100, Number(b.step || 0)));
    if (!version) return json({ error: "Onboarding version is required." }, 400);
    const progress = step + 1;
    const complete = Boolean(b.complete) || Boolean(b.final) || step >= Number(b.lastStep ?? 999);
    db.prepare(
      "INSERT INTO app_onboarding(account_id,app_id,version,progress,complete,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(account_id,app_id) DO UPDATE SET version=excluded.version,progress=MAX(app_onboarding.progress,excluded.progress),complete=MAX(app_onboarding.complete,excluded.complete),updated_at=excluded.updated_at",
    ).run(s.account.id, appId, version, progress, complete ? 1 : 0, Date.now());
    const row = db.prepare("SELECT * FROM app_onboarding WHERE account_id=? AND app_id=?").get(s.account.id, appId) as any;
    return json({ version: row.version, progress: row.progress, complete: Boolean(row.complete) });
  }
  if (path === "/api/v1/connect/start") {
    if (!validApp(b.appId) || typeof b.secret !== "string" || !/^[a-f0-9]{64}$/i.test(b.secret)) {
      return json({ error: "Invalid connection request." }, 400, cors(req));
    }
    const cid = id("con");
    const now = Date.now();
    db.prepare("INSERT INTO connect_requests(id,secret_hash,app_id,created_at,expires_at) VALUES(?,?,?,?,?)").run(
      cid,
      sha(b.secret),
      b.appId,
      now,
      now + 600000,
    );
    return json({ id: cid, expires: now + 600000 }, 201, cors(req));
  }
  if (path === "/api/v1/connect/info") {
    const r = db.prepare("SELECT * FROM connect_requests WHERE id=?").get(String(b.id || "")) as any;
    return r && r.expires_at > Date.now() && !r.consumed_at
      ? json({ appId: r.app_id, expires: r.expires_at, approved: !!r.account_id })
      : json({ error: "Connection request expired or unavailable." }, 404);
  }
  if (path === "/api/v1/connect/approve") {
    const s = nationSession(b.token);
    if (!s) return json({ error: "Sign in to approve this connection." }, 401);
    const r = db.prepare("SELECT * FROM connect_requests WHERE id=?").get(String(b.id || "")) as any;
    if (!r || r.expires_at <= Date.now() || r.consumed_at) {
      return json({ error: "Connection request expired or unavailable." }, 404);
    }
    db.prepare("UPDATE connect_requests SET account_id=?,approved_at=? WHERE id=?").run(s.account.id, Date.now(), r.id);
    return json({ approved: true, appId: r.app_id });
  }
  if (path === "/api/v1/connect/session") {
    const r = db.prepare("SELECT * FROM connect_requests WHERE id=?").get(String(b.id || "")) as any;
    if (!r || r.expires_at <= Date.now() || r.consumed_at) {
      return json({ error: "Connection request expired or unavailable." }, 404, cors(req));
    }
    if (!safeEqual(r.secret_hash, sha(String(b.secret || "")))) {
      return json({ error: "Connection proof is invalid." }, 403, cors(req));
    }
    if (!r.account_id) return json({ pending: true, expires: r.expires_at }, 202, cors(req));
    const account = db.prepare("SELECT * FROM accounts WHERE id=? AND status='active'").get(r.account_id) as any;
    if (!account) return json({ error: "Account is unavailable." }, 403, cors(req));
    const token = createSession(account.id, r.app_id, "infectednation-connect", req, server);
    db.prepare("UPDATE connect_requests SET consumed_at=? WHERE id=?").run(Date.now(), r.id);
    return json({ pending: false, token, account: accountPublic(account) }, 200, cors(req));
  }


  // Discord verify bridge: after web login, POST account binding to the bot callback.
  if (path === "/api/v1/connect/discord/complete" && req.method === "POST") {
    const s = nationSession(b.token);
    if (!s) return json({ error: "Session expired or invalid." }, 401, cors(req));
    const discordId = String(b.discordId || "");
    const guildId = String(b.guildId || "");
    const state = String(b.state || "");
    const callback = String(b.callback || "");
    if (!discordId || !guildId || !state || !callback) {
      return json({ error: "Missing discordId, guildId, state, or callback." }, 400, cors(req));
    }
    let cbUrl: URL;
    try {
      cbUrl = new URL(callback);
    } catch {
      return json({ error: "Invalid callback URL." }, 400, cors(req));
    }
    const local = cbUrl.hostname === "127.0.0.1" || cbUrl.hostname === "localhost";
    if (cbUrl.protocol !== "https:" && !local) {
      return json({ error: "Callback must be HTTPS (or localhost for dev)." }, 400, cors(req));
    }
    const allow = String(process.env.DISCORD_VERIFY_CALLBACK_ALLOWLIST || "")
      .split(",")
      .map((x) => x.trim())
      .filter(Boolean);
    if (allow.length && !allow.some((prefix) => callback.startsWith(prefix))) {
      return json({ error: "Callback host is not allowlisted." }, 403, cors(req));
    }
    const account = s.account as any;
    const stageName = String(account.stage_name || account.username || "").trim();
    const payload = {
      discordId,
      guildId,
      accountId: String(account.id),
      state,
      stageName,
    };
    let botRes: Response;
    try {
      botRes = await fetch(callback, {
        method: "POST",
        headers: { "content-type": "application/json", accept: "application/json" },
        body: JSON.stringify(payload),
      });
    } catch {
      return json(
        { error: "Could not reach Discord verify callback. Is the bot running with a public HTTPS URL?" },
        502,
        cors(req),
      );
    }
    const botText = await botRes.text();
    let botJson: any = {};
    try {
      botJson = botText ? JSON.parse(botText) : {};
    } catch {
      botJson = {};
    }
    if (!botRes.ok || botJson.ok === false) {
      return json(
        { error: String(botJson.error || `Bot callback failed (${botRes.status})`) },
        502,
        cors(req),
      );
    }
    return json({ ok: true, stageName, bot: botJson }, 200, cors(req));
  }

  return json({ error: "Not found." }, 404);
}

{
  const missing = OWNER_EMAILS.filter(
    (e) => !db.prepare("SELECT id FROM accounts WHERE email=? COLLATE NOCASE AND role='owner' AND owner_provisioned=1").get(e),
  );
  if (missing.length) {
    console.warn(`[owner] not provisioned yet: ${missing.join(", ")} (signup with these addresses is refused). Run: bun services/infectednation/server.ts provision-owner --email <address>`);
  }
}

// Owner provisioning (run on the host, before the service is reachable):
//   bun services/infectednation/server.ts provision-owner --email <owner email> [--username NAME]
// The password is read from INFECTEDNATION_OWNER_PASSWORD or typed at a hidden prompt. Re-running it for an
// existing address resets that account's password, makes it a provisioned owner and signs out all its sessions.
async function readSecret(promptText: string): Promise<string> {
  if (process.env.INFECTEDNATION_OWNER_PASSWORD) return process.env.INFECTEDNATION_OWNER_PASSWORD;
  const { spawnSync } = await import("node:child_process");
  const tty = process.stdin.isTTY;
  if (tty) spawnSync("stty", ["-echo"], { stdio: "inherit" });
  process.stdout.write(promptText);
  try {
    for await (const line of console) return String(line);
    return "";
  } finally {
    if (tty) { spawnSync("stty", ["echo"], { stdio: "inherit" }); process.stdout.write("\n"); }
  }
}

async function provisionOwnerCli(args: string[]) {
  const arg = (name: string) => { const i = args.indexOf("--" + name); return i >= 0 ? String(args[i + 1] || "") : ""; };
  const email = normalizeEmail(arg("email"));
  if (!isOwnerEmail(email)) {
    console.error(`--email must be one of the configured owner emails: ${OWNER_EMAILS.join(", ")}`);
    process.exit(2);
  }
  const password = await readSecret(`Password for ${email}: `);
  const pe = passwordError(password);
  if (pe) { console.error(pe); process.exit(2); }
  if (!process.env.INFECTEDNATION_OWNER_PASSWORD && (await readSecret("Repeat password: ")) !== password) {
    console.error("Passwords do not match."); process.exit(2);
  }
  let row = db.prepare("SELECT * FROM accounts WHERE email=? COLLATE NOCASE").get(email) as any;
  if (!row) {
    const username = arg("username") || email.split("@")[0].replace(/[^A-Za-z0-9_-]/g, "").slice(0, 20) + "-owner";
    const ue = usernameError(username);
    if (ue) { console.error(`--username: ${ue}`); process.exit(2); }
    row = await insertAccount({ username, email, password, dob: arg("dob") || "1970-01-01", firstName: arg("first") || "Owner", lastName: arg("last") || "Account", useType: "personal", termsAccepted: true });
  } else {
    const hash = await hashPassword(password);
    db.prepare("UPDATE accounts SET password_hash=? WHERE id=?").run(hash, row.id);
    db.prepare("DELETE FROM sessions WHERE account_id=?").run(row.id);
  }
  db.prepare("UPDATE accounts SET role='owner', owner_provisioned=1, status='active', updated_at=? WHERE id=?").run(Date.now(), row.id);
  console.log(`[owner] provisioned ${email} (account ${row.id})`);
  process.exit(0);
}
if (process.argv[2] === "provision-owner") await provisionOwnerCli(process.argv.slice(3));

const index = await Bun.file(new URL("./public/index.html", import.meta.url)).text();
const connectDiscord = await Bun.file(new URL("./public/connect-discord.html", import.meta.url)).text();
const server = Bun.serve({
  hostname: HOST,
  port: PORT,
  fetch(req, server) {
    const u = new URL(req.url);
    if (u.pathname.startsWith("/api/")) return api(req, server, u);
    if (u.pathname === "/connect/discord") {
      return new Response(connectDiscord, { headers: { "content-type": "text/html; charset=utf-8" } });
    }
    return new Response(index, { headers: { "content-type": "text/html; charset=utf-8" } });
  },
});

console.log(`InfectedNation listening on http://${HOST}:${server.port}`);
if (apiKeyBootstrap.generated && apiKeyBootstrap.path) {
  console.log(`[identity] API key file written (path only): ${apiKeyBootstrap.path}`);
}
