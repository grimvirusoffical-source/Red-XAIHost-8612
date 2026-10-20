import { rsx } from "../../../../../vendor/rsx-sdk/rsx";

/**
 * Credential encryption. Registrar/API keys are stored sealed with Red-SecretsXAI primitives, keyed from
 * CREDENTIAL_SECRET (falls back to BETTER_AUTH_SECRET), so the database alone never leaks a usable key.
 * There is no built-in fallback secret: without one, encrypt/decrypt throw (fail closed) and the API
 * refuses to start (see assertCredentialSecret in index.ts).
 * Format v3: "v3.<salt>.<nonce>.<ciphertext>.<tag>" (base64url parts). The master key is
 * Argon2id(secret, "redxaihost.credentials.v3", t=2, m=19456 KiB, p=1); each record has a random
 * 16-byte salt and its key is HKDF-SHA256(master, salt, "redxaihost credential v3"), sealed with
 * ChaCha20-Poly1305 (12-byte random nonce, AAD "redxaihost.credentials.v3").
 * The earlier AES-256-GCM formats (v2 and the dotted v1) are refused, never decrypted: such a row has to be
 * entered again (see getCredential).
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

const AAD = Buffer.from("redxaihost.credentials.v3");
const INFO = Buffer.from("redxaihost credential v3");
const masterCache = new Map<string, Buffer>();
function masterKey(secret: string): Buffer {
  let k = masterCache.get(secret);
  if (!k) {
    k = rsx.argon2Ext(Buffer.from(secret, "utf8"), Buffer.from("redxaihost.credentials.v3"), Buffer.alloc(0), Buffer.alloc(0), 2, 19456, 1, 32);
    if (masterCache.size > 16) masterCache.clear();
    masterCache.set(secret, k);
  }
  return k;
}
const recordKey = (secret: string, salt: Buffer) => rsx.hkdf(masterKey(secret), salt, INFO, 32);

/** Thrown for a blob in a refused (pre-v3, AES-256-GCM) format. */
export class LegacyCredentialRefused extends Error {
  constructor() {
    super("This credential was stored in the old AES format, which is no longer read. Enter it again.");
  }
}

/** v4 AD: binds the blob to its credentials row and column (P13). v3 used a constant AD. */
const adV4 = (rowId: string) => Buffer.from(`redxaihost.credentials.v4|credentials.secret_enc|${rowId}`, "utf8");

/** Seals a credential. With rowId (the provider id) it writes v4, bound to that row; without, v3. */
export function encryptJson(value: unknown, secret = credentialSecret(), rowId?: string): string {
  return sealJsonWith(value, secret, rsx.random(16), rsx.random(12), rowId);
}

/** Deterministic core of encryptJson; exported only for the known-answer test. */
export function sealJsonWith(value: unknown, secret: string, salt: Buffer, nonce: Buffer, rowId?: string): string {
  const v4 = rowId !== undefined;
  const { ct, tag } = rsx.aeadEncrypt(recordKey(secret, salt), nonce, v4 ? adV4(rowId!) : AAD, Buffer.from(JSON.stringify(value), "utf8"));
  return [v4 ? "v4" : "v3", salt, nonce, ct, tag].map((p) => (typeof p === "string" ? p : Buffer.from(p).toString("base64url"))).join(".");
}

/** True for the refused AES formats: "v2.<5 parts>" or the 3-part v1 form. */
export function isLegacyBlob(blob: string): boolean {
  const parts = blob.split(".");
  return (parts[0] === "v2" && parts.length === 5) || (parts.length === 3 && !blob.startsWith("v3.") && !blob.startsWith("v4."));
}

/**
 * Opens a v3 blob. Returns null for a wrong secret, tampering or garbage; throws LegacyCredentialRefused
 * for the old AES formats so the caller can ask for the credential again.
 */
export function decryptJson<T = Record<string, string>>(blob: string, secret = credentialSecret(), rowId?: string): T | null {
  if (isLegacyBlob(blob)) throw new LegacyCredentialRefused();
  const parts = blob.split(".");
  if ((parts[0] !== "v3" && parts[0] !== "v4") || parts.length !== 5) return null;
  // A v4 blob only opens for the row it was sealed for; there is no row-less v4.
  if (parts[0] === "v4" && rowId === undefined) return null;
  try {
    const [salt, nonce, ct, tag] = parts.slice(1).map((p) => Buffer.from(p, "base64url"));
    if (salt.length !== 16 || nonce.length !== 12 || tag.length !== 16) return null;
    const pt = rsx.aeadDecrypt(recordKey(secret, salt), nonce, parts[0] === "v4" ? adV4(rowId!) : AAD, ct, tag);
    return JSON.parse(Buffer.from(pt).toString("utf8")) as T;
  } catch {
    return null;
  }
}

/** Node agent tokens: stored hashed, shown once at creation. */
export function generateAgentToken(): string {
  return `rxh_${rsx.random(24).toString("base64url")}`;
}

export function hashToken(token: string): string {
  return rsx.sha256(Buffer.from(token, "utf8")).toString("hex");
}

export function tokenPreview(token: string): string {
  return `${token.slice(0, 8)}…${token.slice(-4)}`;
}

export function keyHint(key: string): string {
  return key.length <= 4 ? "••••" : `••••${key.slice(-4)}`;
}
