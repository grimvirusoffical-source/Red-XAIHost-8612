import { verifyPassword as verifyUpstreamScrypt } from "better-auth/crypto";
import { rsx } from "../../../../../vendor/rsx-sdk/rsx";

/** Argon2id parameters for panel passwords (PHC string, Red-SecretsXAI). */
export const PANEL_ARGON2 = { t: 3, m: 65536, p: 1 } as const;

export async function hashPanelPassword(password: string): Promise<string> {
  return rsx.phcHashPassword(password, PANEL_ARGON2.t, PANEL_ARGON2.m, PANEL_ARGON2.p);
}

/**
 * Argon2id PHC strings are checked with rsx. Hashes written before the switch (Better Auth's
 * "salt:key" scrypt form) are still checked by Better Auth itself so local owners are not locked out;
 * the next password set (change, reset, or the Nation identity sync) stores a PHC string instead.
 */
export async function verifyPanelPassword({ hash, password }: { hash: string; password: string }): Promise<boolean> {
  if (hash.startsWith("$argon2")) {
    try {
      return rsx.phcVerifyPassword(password, hash);
    } catch {
      return false;
    }
  }
  if (/^[0-9a-f]+:[0-9a-f]+$/.test(hash)) return verifyUpstreamScrypt({ hash, password });
  return false;
}
