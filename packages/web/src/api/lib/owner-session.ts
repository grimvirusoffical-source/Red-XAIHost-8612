import { randomUUID } from "node:crypto";
import { and, eq, or } from "drizzle-orm";
import { auth } from "../auth";
import { db } from "../database";
import { session as authSession, user as authUser } from "../database/auth-schema";
import { normalizeSiteTrust } from "./site-trust";

/**
 * Opens a panel session after Red-XAI Accounts has already accepted the person.
 *
 * The accounts token is not written down. Existing local password hashes are
 * not replaced, and the encrypted credential vault is not read or written.
 * A first-time Red-XAI user gets a local panel identity bound to the immutable
 * account subject; provider, project and node data remain tenant-scoped.
 */
export class PanelAccessError extends Error {
  constructor(
    readonly status: 403 | 401,
    message: string,
  ) {
    super(message);
    this.name = "PanelAccessError";
  }
}

async function signSessionToken(token: string, secret: string): Promise<string> {
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const signature = new Uint8Array(await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(token)));
  let binary = "";
  for (const byte of signature) binary += String.fromCharCode(byte);
  return encodeURIComponent(`${token}.${btoa(binary)}`);
}

function serializeCookie(
  name: string,
  value: string,
  attributes: { maxAge?: number; path?: string; httpOnly?: boolean; secure?: boolean; sameSite?: string },
): string {
  let cookie = `${name}=${value}`;
  if (typeof attributes.maxAge === "number") cookie += `; Max-Age=${Math.floor(attributes.maxAge)}`;
  cookie += `; Path=${attributes.path || "/"}`;
  if (attributes.httpOnly) cookie += "; HttpOnly";
  if (attributes.secure) cookie += "; Secure";
  const sameSite = attributes.sameSite || "lax";
  cookie += `; SameSite=${sameSite.charAt(0).toUpperCase()}${sameSite.slice(1).toLowerCase()}`;
  return cookie;
}

export async function openOwnerSession(
  input: { id: string; email: string; name: string },
  trustInput?: unknown,
): Promise<string> {
  const accountId = input.id.trim();
  const email = input.email.trim().toLowerCase();
  if (!accountId || !email.includes("@")) {
    throw new PanelAccessError(401, "Red-XAI Accounts did not provide a stable verified identity.");
  }

  const trust = normalizeSiteTrust(trustInput);

  // Red-XAI Accounts is authoritative. Never let a changed email create a
  // second Host tenant or attach one local user to a different account.
  let [user] = await db
    .select()
    .from(authUser)
    .where(or(eq(authUser.redxAccountId, accountId), eq(authUser.email, email)))
    .limit(1);

  if (user?.redxAccountId && user.redxAccountId !== accountId) {
    throw new PanelAccessError(403, "That local Host identity belongs to a different Red-XAI account.");
  }

  if (!user) {
    const id = randomUUID();
    await db.insert(authUser).values({
      id,
      email,
      name: input.name || email,
      emailVerified: true,
      redxAccountId: accountId,
      createdAt: new Date(),
      updatedAt: new Date(),
    });
    [user] = await db.select().from(authUser).where(eq(authUser.id, id)).limit(1);
  } else {
    await db
      .update(authUser)
      .set({
        email,
        name: input.name || user.name,
        emailVerified: true,
        redxAccountId: accountId,
        updatedAt: new Date(),
      })
      .where(and(eq(authUser.id, user.id), or(eq(authUser.redxAccountId, accountId), eq(authUser.email, email))));
  }

  if (!user) throw new PanelAccessError(401, "Host could not establish the account workspace.");

  const ctx = await auth.$context;
  const session = await ctx.internalAdapter.createSession(user.id);
  const cookie = ctx.authCookies.sessionToken;
  const attributes = { ...cookie.attributes };

  if (trust.remember && trust.maxAgeSeconds) {
    await db
      .update(authSession)
      .set({ expiresAt: new Date(Date.now() + trust.maxAgeSeconds * 1000) })
      .where(eq(authSession.token, session.token));
    attributes.maxAge = trust.maxAgeSeconds;
  } else {
    delete attributes.maxAge;
  }

  const signed = await signSessionToken(session.token, ctx.secret);
  return serializeCookie(cookie.name, signed, attributes);
}
