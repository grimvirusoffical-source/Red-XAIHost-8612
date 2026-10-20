// RFC 4226 / RFC 6238 TOTP (HMAC-SHA1, 30 s step, 6 digits) plus at-rest sealing of TOTP seeds.
import { createCipheriv, createDecipheriv, createHmac, randomBytes, scryptSync, timingSafeEqual } from "node:crypto";

const B32 = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

export function base32Encode(buf: Uint8Array): string {
  let bits = 0, value = 0, out = "";
  for (const byte of buf) {
    value = (value << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      out += B32[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  if (bits > 0) out += B32[(value << (5 - bits)) & 31];
  return out;
}

export function base32Decode(s: string): Buffer {
  const clean = s.toUpperCase().replace(/[\s=-]/g, "");
  let bits = 0, value = 0;
  const out: number[] = [];
  for (const ch of clean) {
    const i = B32.indexOf(ch);
    if (i < 0) throw new Error("invalid base32");
    value = (value << 5) | i;
    bits += 5;
    if (bits >= 8) {
      out.push((value >>> (bits - 8)) & 255);
      bits -= 8;
    }
  }
  return Buffer.from(out);
}

export const TOTP_STEP = 30;
export const TOTP_DIGITS = 6;
export const TOTP_SKEW = 1; // accept one step either side

/** RFC 4226 HOTP with dynamic truncation. algo is "sha1" for authenticator apps. */
export function hotp(key: Uint8Array, counter: number, digits = TOTP_DIGITS, algo = "sha1"): string {
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(counter));
  const h = createHmac(algo, key).update(msg).digest();
  const off = h[h.length - 1] & 0x0f;
  const bin = ((h[off] & 0x7f) << 24) | (h[off + 1] << 16) | (h[off + 2] << 8) | h[off + 3];
  return String(bin % 10 ** digits).padStart(digits, "0");
}

export function totp(key: Uint8Array, unixSeconds: number, digits = TOTP_DIGITS, algo = "sha1"): string {
  return hotp(key, Math.floor(unixSeconds / TOTP_STEP), digits, algo);
}

/**
 * Verify a code. Returns the matched time step, or null. A step <= lastStep is refused (replay protection):
 * callers must persist the returned step and pass it back as lastStep next time.
 */
export function verifyTotp(key: Uint8Array, code: string, unixSeconds: number, lastStep = -1): number | null {
  if (!/^\d{6}$/.test(code)) return null;
  const now = Math.floor(unixSeconds / TOTP_STEP);
  let hit: number | null = null;
  for (let d = -TOTP_SKEW; d <= TOTP_SKEW; d++) {
    const step = now + d;
    if (step < 0) continue;
    const ok = timingSafeEqual(Buffer.from(hotp(key, step)), Buffer.from(code));
    if (ok && step > lastStep && hit === null) hit = step;
  }
  return hit;
}

export function newTotpSecret(): { raw: Buffer; base32: string } {
  const raw = randomBytes(20); // 160 bits, RFC 4226 recommended length
  return { raw, base32: base32Encode(raw) };
}

export function otpauthUri(issuer: string, account: string, base32: string): string {
  const label = encodeURIComponent(issuer) + ":" + encodeURIComponent(account);
  return `otpauth://totp/${label}?secret=${base32}&issuer=${encodeURIComponent(issuer)}&algorithm=SHA1&digits=${TOTP_DIGITS}&period=${TOTP_STEP}`;
}

// ---- at-rest sealing: "v1.<salt>.<iv>.<ciphertext>.<tag>" (base64url), AES-256-GCM, key = scrypt(secret, random salt)

export class TotpKeyMissing extends Error {
  constructor() { super("TOTP encryption key is not configured (set INFECTEDNATION_TOTP_KEY or CREDENTIAL_SECRET)"); }
}

export function totpMasterSecret(env: Record<string, string | undefined> = process.env): string {
  const s = (env.INFECTEDNATION_TOTP_KEY || env.CREDENTIAL_SECRET || "").trim();
  if (s.length < 16) throw new TotpKeyMissing(); // fail closed: no fallback key
  return s;
}

const AAD = Buffer.from("infectednation.totp.v1");

export function sealSecret(raw: Uint8Array, master: string, accountId: string): string {
  const salt = randomBytes(16), iv = randomBytes(12);
  const key = scryptSync(master, salt, 32);
  const c = createCipheriv("aes-256-gcm", key, iv);
  c.setAAD(Buffer.concat([AAD, Buffer.from("|" + accountId)]));
  const ct = Buffer.concat([c.update(raw), c.final()]);
  return ["v1", salt, iv, ct, c.getAuthTag()].map((p) => (typeof p === "string" ? p : p.toString("base64url"))).join(".");
}

export function isSealed(v: string): boolean {
  return typeof v === "string" && v.startsWith("v1.") && v.split(".").length === 5;
}

/** Throws on tampering, wrong key or wrong account binding. */
export function openSecret(blob: string, master: string, accountId: string): Buffer {
  if (!isSealed(blob)) throw new Error("not a sealed TOTP secret");
  const [, s, i, ct, tag] = blob.split(".").map((p, n) => (n === 0 ? Buffer.alloc(0) : Buffer.from(p, "base64url")));
  const key = scryptSync(master, s, 32);
  const d = createDecipheriv("aes-256-gcm", key, i);
  d.setAAD(Buffer.concat([AAD, Buffer.from("|" + accountId)]));
  d.setAuthTag(tag);
  return Buffer.concat([d.update(ct), d.final()]);
}
