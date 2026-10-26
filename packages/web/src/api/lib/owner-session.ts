import { auth, isOwnerEmail } from "../auth";

/**
 * Opens a panel session after Red-XAI Accounts has already accepted the person.
 *
 * The accounts token is not written down. Existing local password hashes are
 * not replaced, and the encrypted credential vault is not read or written.
 * A first-time owner gets a user row with no password so offline bootstrap can
 * still create the local password itself.
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

export async function openOwnerSession(input: { email: string; name: string }): Promise<string> {
  const email = input.email.trim().toLowerCase();
  if (!isOwnerEmail(email)) {
    throw new PanelAccessError(403, "This control panel is owner-only.");
  }
  const ctx = await auth.$context;
  const existing = await ctx.internalAdapter.findUserByEmail(email);
  const user = existing?.user ?? (await ctx.internalAdapter.createUser({
    email,
    name: input.name || "Red-XAI Owner",
    emailVerified: true,
  }));
  const session = await ctx.internalAdapter.createSession(user.id);
  const cookie = ctx.authCookies.sessionToken;
  const signed = await signSessionToken(session.token, ctx.secret);
  return serializeCookie(cookie.name, signed, cookie.attributes);
}
