import { createCipheriv, createDecipheriv, createHash, randomBytes, scryptSync } from "node:crypto";

/**
 * Credential encryption. Registrar/API keys are stored as AES-256-GCM blobs keyed from CREDENTIAL_SECRET
 * (falls back to BETTER_AUTH_SECRET) so the database alone never leaks a usable key.
 * There is no built-in fallback secret: without one, encrypt/decrypt throw (fail closed) and the API
 * refuses to start (see assertCredentialSecret in index.ts).
 * Format v2: "v2.<salt>.<iv>.<ciphertext>.<tag>" with a random 16-byte scrypt salt and 12-byte IV per record.
 * Legacy v1 blobs ("<iv>.<ct>.<tag>", fixed salt) are still readable so existing rows keep working;
 * they are rewritten as v2 the next time they are saved.
 */
export class CredentialSecretMissing extends Error {
  constructor() {
    super("CREDENTIAL_SECRET (or BETTER_AUTH_SECRET) must be set to a random value of at least 16 characters.");
  }
}

export function credentialSecret(env: Record<string, string | undefined> = process.env): string {
  const s = (env.CREDENTIAL_SECRET || env.BETTER_AUTH_SECRET || "").trim();
  if (s.length < 16 || s === "redxaihost-dev") throw new CredentialSecretMissing();
  return s;
}

export function assertCredentialSecret(): void {
  credentialSecret();
}

const keyCache = new Map<string, Buffer>();
function deriveKey(secret: string, salt: Buffer | string): Buffer {
  const id = secret.length + ":" + (typeof salt === "string" ? "s:" + salt : salt.toString("hex"));
  let k = keyCache.get(id);
  if (!k) {
    k = scryptSync(secret, salt, 32);
    if (keyCache.size > 1024) keyCache.clear();
    keyCache.set(id, k);
  }
  return k;
}
const LEGACY_SALT = "redxaihost.credentials.v1"; // read-only, for decrypting pre-v2 rows

export function encryptJson(value: unknown, secret = credentialSecret()): string {
  const salt = randomBytes(16);
  const iv = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", deriveKey(secret, salt), iv);
  cipher.setAAD(Buffer.from("redxaihost.credentials.v2"));
  const enc = Buffer.concat([cipher.update(Buffer.from(JSON.stringify(value), "utf8")), cipher.final()]);
  return ["v2", salt, iv, enc, cipher.getAuthTag()].map((p) => (typeof p === "string" ? p : p.toString("base64url"))).join(".");
}

export function isLegacyBlob(blob: string): boolean {
  return !blob.startsWith("v2.");
}

export function decryptJson<T = Record<string, string>>(blob: string, secret = credentialSecret()): T | null {
  try {
    const parts = blob.split(".");
    let key: Buffer, iv: Buffer, data: Buffer, tag: Buffer, aad: Buffer | null = null;
    if (parts[0] === "v2" && parts.length === 5) {
      key = deriveKey(secret, Buffer.from(parts[1], "base64url"));
      [iv, data, tag] = parts.slice(2).map((p) => Buffer.from(p, "base64url"));
      aad = Buffer.from("redxaihost.credentials.v2");
    } else if (parts.length === 3) {
      key = deriveKey(secret, LEGACY_SALT);
      [iv, data, tag] = parts.map((p) => Buffer.from(p, "base64url"));
    } else return null;
    const decipher = createDecipheriv("aes-256-gcm", key, iv);
    if (aad) decipher.setAAD(aad);
    decipher.setAuthTag(tag);
    const dec = Buffer.concat([decipher.update(data), decipher.final()]);
    return JSON.parse(dec.toString("utf8")) as T;
  } catch {
    return null;
  }
}

/** Node agent tokens: stored hashed, shown once at creation. */
export function generateAgentToken(): string {
  return `rxh_${randomBytes(24).toString("base64url")}`;
}

export function hashToken(token: string): string {
  return createHash("sha256").update(token).digest("hex");
}

export function tokenPreview(token: string): string {
  return `${token.slice(0, 8)}…${token.slice(-4)}`;
}

export function keyHint(key: string): string {
  return key.length <= 4 ? "••••" : `••••${key.slice(-4)}`;
}
