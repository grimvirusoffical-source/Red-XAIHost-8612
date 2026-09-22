/**
 * Cloudflare token plumbing, kept free of any database import so both the
 * credential verifier and the Cloudflare client can share one implementation
 * instead of each rolling its own (and each getting the permission advice
 * subtly wrong, which is how "wrong permissions" became so easy to hit).
 */
const API = "https://api.cloudflare.com/client/v4";

interface CfResponse<T> {
  success: boolean;
  result: T;
  errors?: { code: number; message: string }[];
}

/**
 * The permissions this panel actually uses, and a Cloudflare template URL that
 * pre-ticks all of them. Hand the owner the link instead of a list of
 * checkboxes to find — "wrong permissions" is the single most common way the
 * Cloudflare setup fails, and it is entirely avoidable.
 *
 * - Zone:Zone:Edit             create the zone when a domain is added ("Add a site")
 * - Zone:DNS:Edit              write the proxied CNAME into the zone
 * - Account:Cloudflare Tunnel  create the named tunnel and read its connector token
 * - Account Settings:Read      read-only, and only so the account ID can be discovered
 * - Zone:Zone Settings:Edit    HTTPS, TLS floor and security level on each zone
 * - Zone:Firewall Services     the rate-limiting and probe-blocking rules
 *
 * Bot Fight Mode needs Zone:Bot Management:Edit, which Cloudflare does not
 * expose a template key for; it is listed as optional so the checklist can tell
 * the owner to tick it by hand rather than silently failing later.
 */
export const REQUIRED_PERMISSIONS = [
  { label: "Zone · Zone · Edit", why: "Adds the domain to Cloudflare as a site." },
  { label: "Zone · DNS · Edit", why: "Writes the proxied CNAME that points at the tunnel." },
  { label: "Account · Cloudflare Tunnel · Edit", why: "Creates the tunnel your node connects with." },
  { label: "Account · Account Settings · Read", why: "Read-only — lets the panel find your account ID." },
  { label: "Zone · Zone Settings · Edit", why: "Forces HTTPS and a TLS 1.2 floor on your sites." },
  { label: "Zone · Firewall Services · Edit", why: "Adds the rate limit and bad-bot blocking rules." },
] as const;

/**
 * Not in the template URL because Cloudflare publishes no key for it, so the
 * owner has to add this one manually if they want Bot Fight Mode.
 */
export const OPTIONAL_PERMISSIONS = [
  {
    label: "Zone · Bot Management · Edit",
    why: "Turns on Bot Fight Mode. Everything else works without it.",
  },
] as const;

const TEMPLATE_PERMISSIONS = [
  { key: "zone", type: "edit" },
  { key: "dns", type: "edit" },
  { key: "argotunnel", type: "edit" },
  { key: "account_settings", type: "read" },
  { key: "zone_settings", type: "edit" },
  { key: "firewall_services", type: "edit" },
];

/** One-click Cloudflare token form with every permission above already ticked. */
export const TOKEN_TEMPLATE_URL =
  "https://dash.cloudflare.com/profile/api-tokens?permissionGroupKeys=" +
  encodeURIComponent(JSON.stringify(TEMPLATE_PERMISSIONS)) +
  "&accountId=*&zoneId=all&name=" +
  encodeURIComponent("RedXAIHost");

/**
 * Cloudflare reports missing permissions as raw internal ids, e.g.
 * 'Requires permission "com.cloudflare.api.account.zone.create"'. Turn that
 * into the name of the toggle the owner has to flip.
 */
export type CfScope = "zone" | "dns" | "tunnel" | "account";

const SCOPE_NAMES: Record<CfScope, [string, string]> = {
  zone: ["Zone · Zone · Edit", "add the domain to Cloudflare"],
  dns: ["Zone · DNS · Edit", "write the proxied DNS record"],
  tunnel: ["Account · Cloudflare Tunnel · Edit", "create the tunnel"],
  account: ["Account · Account Settings · Read", "read your account ID"],
};

export function explainCloudflareError(
  error: string | undefined | null,
  /**
   * Which permission the failed call needed. Cloudflare answers a missing
   * permission with a bare "Authentication error" that names nothing, so the
   * caller's own knowledge of what it was doing is the only reliable clue.
   */
  scope?: CfScope,
): string {
  if (!error) return "Cloudflare rejected the request.";
  const raw = error.toLowerCase();
  const missing = (permission: string, action: string) =>
    `Your Cloudflare token is missing ${permission}, which is needed to ${action}. Use the "Create a token with the right permissions" button to make one with every permission ticked.`;

  // A permission refusal on a call whose scope we know needs no guessing.
  if (
    scope &&
    (raw.includes("authentication error") ||
      raw.includes("requires permission") ||
      raw.includes("unauthorized") ||
      raw.includes("forbidden") ||
      raw.includes("not have permission"))
  ) {
    return missing(...SCOPE_NAMES[scope]);
  }

  if (raw.includes("zone.create") || raw.includes("zone create")) {
    return missing("Zone · Zone · Edit", "add the domain to Cloudflare");
  }
  if (raw.includes("argo") || raw.includes("tunnel")) {
    return missing("Account · Cloudflare Tunnel · Edit", "create the tunnel");
  }
  if (raw.includes("dns_record") || raw.includes("dns record")) {
    return missing("Zone · DNS · Edit", "write the DNS record");
  }
  if (raw.includes("list zones") || raw.includes("zone.read") || raw.includes("#zone:read")) {
    return missing("Zone · Zone · Edit", "read your zones");
  }
  if (
    raw.includes("requires permission") ||
    raw.includes("authentication error") ||
    raw.includes("unauthorized") ||
    raw.includes("forbidden") ||
    raw.includes("not have permission")
  ) {
    return `Cloudflare refused the token: ${error}. Use the "Create a token with the right permissions" button — it pre-ticks all four permissions this panel needs.`;
  }
  return error;
}

export async function cf<T>(
  path: string,
  init: RequestInit & { token: string },
): Promise<{ ok: boolean; result?: T; error?: string }> {
  const { token, ...rest } = init;
  const res = await fetch(`${API}${path}`, {
    ...rest,
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
      ...rest.headers,
    },
  });
  const body = (await res.json().catch(() => null)) as CfResponse<T> | null;
  if (!res.ok || !body?.success) {
    return { ok: false, error: body?.errors?.[0]?.message ?? `Cloudflare returned ${res.status}` };
  }
  return { ok: true, result: body.result };
}

/** Is the token itself alive? Cheapest possible Cloudflare probe. */
export async function verifyToken(
  token: string,
): Promise<{ ok: boolean; status?: string; error?: string }> {
  const res = await cf<{ status: string }>("/user/tokens/verify", { token, method: "GET" });
  return { ok: res.ok, status: res.result?.status, error: res.error };
}

/**
 * Accounts the token can see. This is what lets the owner paste only a token:
 * the account id is discovered instead of hunted down in the dashboard.
 */
export async function listAccounts(
  token: string,
): Promise<{ ok: boolean; accounts: { id: string; name: string }[]; error?: string }> {
  const res = await cf<{ id: string; name: string }[]>("/accounts?per_page=50", {
    token,
    method: "GET",
  });
  return { ok: res.ok, accounts: res.result ?? [], error: res.error };
}

/**
 * Find the account id without ever demanding one specific permission.
 *
 * /accounts is the clean way, but a token scoped only to zones cannot call it —
 * that used to make a perfectly good token look broken. So fall back to reading
 * the account off any zone the token can see, which needs nothing extra.
 */
export async function discoverAccount(
  token: string,
  preferred?: string | null,
): Promise<{ id: string | null; name: string | null; via: string; error?: string }> {
  const accounts = await listAccounts(token);
  if (accounts.accounts.length > 0) {
    const match =
      accounts.accounts.find((candidate) => candidate.id === preferred) ?? accounts.accounts[0]!;
    return { id: match.id, name: match.name, via: "account list" };
  }

  const zones = await cf<{ account?: { id: string; name: string } }[]>("/zones?per_page=50", {
    token,
    method: "GET",
  });
  const fromZone = (zones.result ?? [])
    .map((zone) => zone.account)
    .find((account): account is { id: string; name: string } => Boolean(account?.id));
  if (fromZone) return { id: fromZone.id, name: fromZone.name ?? null, via: "existing zone" };

  if (preferred) return { id: preferred, name: null, via: "saved value" };
  return {
    id: null,
    name: null,
    via: "none",
    error: accounts.error ?? zones.error ?? undefined,
  };
}
