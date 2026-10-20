// Email verification (feature-flagged, default OFF). Tokens: 256-bit random, only SHA-256 stored,
// single use, expiring, compared in constant time.
import { createHash, randomBytes, timingSafeEqual } from "node:crypto";

export const EMAIL_VERIFY_TTL_MS = 30 * 60 * 1000;
export const RESEND_MIN_INTERVAL_MS = 60 * 1000;
export const RESEND_PER_HOUR = 3;

export function emailVerificationEnabled(env: Record<string, string | undefined> = process.env): boolean {
  return ["on", "true", "1"].includes(String(env.INFECTEDNATION_EMAIL_VERIFICATION || "").trim().toLowerCase());
}

const h = (s: string) => createHash("sha256").update(s).digest();

/** Token handed to the user: "<accountId>.<secret>". Only sha256(secret) is persisted. */
export function newVerifyToken(accountId: string): { token: string; hash: string } {
  const secret = randomBytes(32).toString("base64url");
  return { token: `${accountId}.${secret}`, hash: h(secret).toString("hex") };
}

export function parseVerifyToken(token: unknown): { accountId: string; secret: string } | null {
  if (typeof token !== "string" || token.length > 200) return null;
  const i = token.lastIndexOf(".");
  if (i <= 0) return null;
  const accountId = token.slice(0, i), secret = token.slice(i + 1);
  if (!/^[A-Za-z0-9_-]{1,100}$/.test(accountId) || !/^[A-Za-z0-9_-]{43}$/.test(secret)) return null;
  return { accountId, secret };
}

export function tokenMatches(secret: string, storedHex: string): boolean {
  const a = h(secret), b = Buffer.from(String(storedHex), "hex");
  return b.length === a.length && timingSafeEqual(a, b);
}

export type Mail = { to: string; subject: string; text: string; link: string };

/** Delivery via an HTTPS webhook (INFECTEDNATION_MAIL_WEBHOOK). http:// only for loopback. Never logs the token. */
export async function sendMail(mail: Mail, env: Record<string, string | undefined> = process.env): Promise<boolean> {
  const url = String(env.INFECTEDNATION_MAIL_WEBHOOK || "").trim();
  if (!url) return false;
  let u: URL;
  try { u = new URL(url); } catch { return false; }
  const loopback = ["127.0.0.1", "localhost", "[::1]"].includes(u.hostname);
  if (u.protocol !== "https:" && !(u.protocol === "http:" && loopback)) return false;
  const headers: Record<string, string> = { "content-type": "application/json" };
  if (env.INFECTEDNATION_MAIL_WEBHOOK_TOKEN) headers.authorization = "Bearer " + env.INFECTEDNATION_MAIL_WEBHOOK_TOKEN;
  try {
    const r = await fetch(u, { method: "POST", headers, body: JSON.stringify(mail), redirect: "error", signal: AbortSignal.timeout(8000) });
    return r.ok;
  } catch {
    return false;
  }
}

export function verifyMail(email: string, token: string, env: Record<string, string | undefined> = process.env): Mail {
  const base = String(env.INFECTEDNATION_VERIFY_URL || "http://127.0.0.1:8787/verify-email").replace(/[?#].*$/, "");
  const link = `${base}#token=${encodeURIComponent(token)}`; // fragment: never sent to servers or logged by proxies
  return {
    to: email,
    subject: "Confirm your email",
    text: `Confirm your email address by opening this link within 30 minutes:\n\n${link}\n\nIf you didn't create an account, ignore this message.`,
    link,
  };
}
