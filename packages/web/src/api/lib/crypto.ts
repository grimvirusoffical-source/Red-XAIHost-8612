import { createCipheriv, createDecipheriv, createHash, randomBytes, scryptSync } from "node:crypto";

/**
 * Credential encryption. Registrar/API keys are stored as AES-256-GCM blobs
 * keyed from CREDENTIAL_SECRET (falls back to BETTER_AUTH_SECRET) so the
 * database alone never leaks a usable key.
 */
const SECRET = process.env.CREDENTIAL_SECRET ?? process.env.BETTER_AUTH_SECRET ?? "redxaihost-dev";
const KEY = scryptSync(SECRET, "redxaihost.credentials.v1", 32);

export function encryptJson(value: unknown): string {
  const iv = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", KEY, iv);
  const plaintext = Buffer.from(JSON.stringify(value), "utf8");
  const enc = Buffer.concat([cipher.update(plaintext), cipher.final()]);
  const tag = cipher.getAuthTag();
  return [iv.toString("base64url"), enc.toString("base64url"), tag.toString("base64url")].join(".");
}

export function decryptJson<T = Record<string, string>>(blob: string): T | null {
  try {
    const [ivB64, dataB64, tagB64] = blob.split(".");
    if (!ivB64 || !dataB64 || !tagB64) return null;
    const decipher = createDecipheriv("aes-256-gcm", KEY, Buffer.from(ivB64, "base64url"));
    decipher.setAuthTag(Buffer.from(tagB64, "base64url"));
    const dec = Buffer.concat([
      decipher.update(Buffer.from(dataB64, "base64url")),
      decipher.final(),
    ]);
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
