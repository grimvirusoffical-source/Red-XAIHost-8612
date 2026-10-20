// RFC 4226 / RFC 6238 TOTP (HMAC-SHA1, 30 s step, 6 digits) plus at-rest sealing of TOTP seeds.
import { rsx } from "../../vendor/rsx-sdk/rsx";

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

/** HMAC-SHA512 (RFC 2104) over the rsx SHA-512; only for the RFC 6238 SHA-512 mode. */
function hmacSha512(key: Uint8Array, msg: Uint8Array): Buffer {
  let k = Buffer.from(key);
  if (k.length > 128) k = rsx.sha512(k);
  const pad = (x: number) => Buffer.from(Buffer.concat([k, Buffer.alloc(128 - k.length)]).map((b) => b ^ x));
  return rsx.sha512(Buffer.concat([pad(0x5c), rsx.sha512(Buffer.concat([pad(0x36), msg]))]));
}

function hmacFor(algo: string, key: Uint8Array, msg: Uint8Array): Buffer {
  if (algo === "sha1") return rsx.hmacSha1(key, msg);
  if (algo === "sha256") return rsx.hmac(key, msg);
  if (algo === "sha512") return hmacSha512(key, msg);
  throw new Error("unsupported TOTP algorithm: " + algo);
}

/** RFC 4226 HOTP with dynamic truncation. algo is "sha1" for authenticator apps. */
export function hotp(key: Uint8Array, counter: number, digits = TOTP_DIGITS, algo = "sha1"): string {
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(counter));
  const h = hmacFor(algo, key, msg);
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
    const ok = rsx.ctEq(Buffer.from(hotp(key, step)), Buffer.from(code));
    if (ok && step > lastStep && hit === null) hit = step;
  }
  return hit;
}

export function newTotpSecret(): { raw: Buffer; base32: string } {
  const raw = rsx.random(20); // 160 bits, RFC 4226 recommended length
  return { raw, base32: base32Encode(raw) };
}

export function otpauthUri(issuer: string, account: string, base32: string): string {
  const label = encodeURIComponent(issuer) + ":" + encodeURIComponent(account);
  return `otpauth://totp/${label}?secret=${base32}&issuer=${encodeURIComponent(issuer)}&algorithm=SHA1&digits=${TOTP_DIGITS}&period=${TOTP_STEP}`;
}

// ---- at-rest sealing: "v2.<salt>.<nonce>.<ciphertext>.<tag>" (base64url), Red-SecretsXAI primitives.
// master = Argon2id(secret, "infectednation.totp.v2", t=2, m=19456 KiB, p=1); per record
// key = HKDF-SHA256(master, random 16-byte salt, "infectednation totp v2"), ChaCha20-Poly1305 with a random
// 12-byte nonce and AAD "infectednation.totp.v2|<accountId>". The earlier AES-256-GCM "v1." form is refused:
// isSealed() is false for it, so the start-up sweep in server.ts removes it and asks the account to enroll again.

export class TotpKeyMissing extends Error {
  constructor() { super("TOTP encryption key is not configured (set INFECTEDNATION_TOTP_KEY or CREDENTIAL_SECRET)"); }
}

export function totpMasterSecret(env: Record<string, string | undefined> = process.env): string {
  const s = (env.INFECTEDNATION_TOTP_KEY || env.CREDENTIAL_SECRET || "").trim();
  if (s.length < 16) throw new TotpKeyMissing(); // fail closed: no fallback key
  return s;
}

const AAD = "infectednation.totp.v2|";
const INFO = Buffer.from("infectednation totp v2");
const masters = new Map<string, Buffer>();
function masterKey(master: string): Buffer {
  let k = masters.get(master);
  if (!k) {
    k = rsx.argon2Ext(Buffer.from(master, "utf8"), Buffer.from("infectednation.totp.v2"), Buffer.alloc(0), Buffer.alloc(0), 2, 19456, 1, 32);
    if (masters.size > 16) masters.clear();
    masters.set(master, k);
  }
  return k;
}
const recordKey = (master: string, salt: Uint8Array) => rsx.hkdf(masterKey(master), salt, INFO, 32);
const aadFor = (accountId: string) => Buffer.from(AAD + accountId, "utf8");

export function sealSecret(raw: Uint8Array, master: string, accountId: string): string {
  return sealSecretWith(raw, master, accountId, rsx.random(16), rsx.random(12));
}

/** Deterministic core of sealSecret; exported only for the known-answer test. */
export function sealSecretWith(raw: Uint8Array, master: string, accountId: string, salt: Buffer, nonce: Buffer): string {
  const { ct, tag } = rsx.aeadEncrypt(recordKey(master, salt), nonce, aadFor(accountId), raw);
  return ["v2", salt, nonce, ct, tag].map((p) => (typeof p === "string" ? p : Buffer.from(p).toString("base64url"))).join(".");
}

export function isSealed(v: string): boolean {
  return typeof v === "string" && v.startsWith("v2.") && v.split(".").length === 5;
}

/** Throws on tampering, wrong key, wrong account binding, or a refused (AES "v1.") blob. */
export function openSecret(blob: string, master: string, accountId: string): Buffer {
  if (!isSealed(blob)) throw new Error("not a sealed TOTP secret");
  const [s, n, ct, tag] = blob.split(".").slice(1).map((p) => Buffer.from(p, "base64url"));
  if (s.length !== 16 || n.length !== 12 || tag.length !== 16) throw new Error("not a sealed TOTP secret");
  return Buffer.from(rsx.aeadDecrypt(recordKey(master, s), n, aadFor(accountId), ct, tag));
}
