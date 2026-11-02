import { authTrustedOrigins } from "./auth-origins";

// This is the existing website account authority, not a caller-selected URL.
const AUTHORITY = "http://127.0.0.1:4200/api/accounts/me";
const COOKIE = "redx_session";
type Env = Record<string, string | undefined>;
type FetchLike = (url: string, init: RequestInit) => Promise<Response>;

export function websiteSessionEnabled(env: Env = process.env): boolean {
  return env.REDX_WEBSITE_SESSION === "true";
}

export interface WebsiteIdentity {
  id: string;
  email: string;
  name: string;
  owner: boolean;
  expiresAt: number;
}

/** Revalidate the original opaque website cookie on every protected request. */
export async function websiteIdentity(
  headers: Headers,
  options: { env?: Env; fetch?: FetchLike; now?: number } = {},
): Promise<WebsiteIdentity | null> {
  const env = options.env ?? process.env;
  if (!websiteSessionEnabled(env)) return null;
  const cookies = (headers.get("cookie") || "").split(";")
    .map(value => value.trim()).filter(value => value.startsWith(`${COOKIE}=`));
  // Reject ambiguous duplicate cookies; never send Host cookies or caller
  // identity/authorization headers to the website authority.
  if (cookies.length !== 1 || cookies[0].length <= COOKIE.length + 1 || cookies[0].length > 8192) return null;
  let response: Response;
  try {
    response = await (options.fetch ?? fetch)(AUTHORITY, {
      method: "GET", headers: { accept: "application/json", cookie: cookies[0] },
      redirect: "manual", cache: "no-store", signal: AbortSignal.timeout(5000),
    });
    if (response.status !== 200) return null;
    const text = await response.text();
    if (text.length > 32768) return null;
    const json = JSON.parse(text);
    const account = json?.account;
    const id = account?.id;
    const email = account?.email;
    const expiresAt = json?.expiresMs;
    if (typeof id !== "string" || !id.trim() || id.length > 256 || /[\x00-\x20\x7f]/.test(id)
      || typeof email !== "string" || !email.includes("@") || email.length > 254
      || !Number.isSafeInteger(expiresAt) || expiresAt <= (options.now ?? Date.now())
      || !Array.isArray(json.amr) || !json.amr.length
      || json.active === false || account.active === false || account.disabled === true
      || account.banned === true || account.deleted === true
      || account.emailVerified === false || account.email_verified === false
      || (account.status !== undefined && !["active", "activated"].includes(account.status))
      || (account.state !== undefined && !["active", "activated"].includes(account.state))) return null;
    const ownerIds = (env.REDX_WEBSITE_OWNER_ACCOUNT_IDS || "").split(",").map(x => x.trim()).filter(Boolean);
    const mfa = Array.isArray(json.amr) && json.amr.some((x: unknown) =>
      typeof x === "string" && ["otp", "totp", "sms", "mfa", "webauthn"].includes(x));
    const owner = ownerIds.includes(id) && Array.isArray(account.roles) && account.roles.includes("owner") && mfa;
    if (!owner && ["owner", "__unassigned__"].includes(id)) return null;
    const name = typeof account.username === "string" && account.username.trim() ? account.username : email;
    return { id, email: email.toLowerCase(), name: name.slice(0, 120), owner, expiresAt };
  } catch {
    // Network errors, malformed profiles and authority outages fail closed.
    return null;
  }
}

export function websiteRequestAllowed(headers: Headers): boolean {
  const origin = headers.get("origin");
  return Boolean(origin && authTrustedOrigins().includes(origin));
}
