/**
 * How Red-XAI Hub finds this control panel.
 *
 * The website (https://red-xai.com) is the hub. It can embed the panel, or
 * launch the desktop shell with the redxaihost:// protocol. Nothing here reads
 * or writes the encrypted credential vault.
 */

export const HUB_PROTOCOL = "redxaihost";
export const DEFAULT_HUB_URL = "https://red-xai.com";
export const DEFAULT_HUB_ORIGINS = "https://red-xai.com";
export const ACCOUNTS_SERVICE_REPO = "https://github.com/grimvirusoffical-source/red-xai-accounts";
export const DEFAULT_ACCOUNTS_URL = "https://api.red-xai.com/v1/accounts";
export const DEFAULT_SIGNUP_URL = "https://red-xai.com/signup";

const PANEL_PATH =
  /^\/(?:$|nodes\/?|projects(?:\/[^/]+)?\/?|domains\/?|usage\/?|builds\/?|activity\/?|settings\/?)$/;

export function safePanelPath(input: string | null | undefined): string {
  if (!input) return "/";
  let value = input.trim();
  if (!value) return "/";
  try {
    value = decodeURIComponent(value);
  } catch {
    return "/";
  }
  const noHash = value.split("#")[0] ?? "";
  const path = noHash.split("?")[0] ?? "";
  if (!path.startsWith("/") || path.startsWith("//") || path.includes("\\") || path.includes("://")) return "/";
  if (path.includes("..")) return "/";
  const normalized = path.length > 1 && path.endsWith("/") ? path.slice(0, -1) : path;
  return PANEL_PATH.test(normalized) ? normalized : "/";
}

/** redxaihost://open/projects or redxaihost://open?path=/settings */
export function parseHubLink(raw: string): string | null {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  if (url.protocol !== `${HUB_PROTOCOL}:`) return null;
  const host = url.hostname.toLowerCase();
  if (host !== "open" && host !== "embed") return null;
  const queried = url.searchParams.get("path");
  const fromPath = url.pathname && url.pathname !== "/" ? url.pathname : "/";
  return safePanelPath(queried || fromPath);
}

function loopback(hostname: string): boolean {
  return hostname === "localhost" || hostname === "127.0.0.1" || hostname === "::1";
}

/** Blank, "off", or "local" disables the remote accounts service. */
export function accountsBaseUrl(env: Record<string, string | undefined> = process.env): string | null {
  const raw = (env.REDX_ACCOUNTS_URL ?? DEFAULT_ACCOUNTS_URL).trim();
  if (!raw || /^(off|local|false|0)$/i.test(raw)) return null;
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new Error("REDX_ACCOUNTS_URL is not a URL");
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") {
    throw new Error("REDX_ACCOUNTS_URL must be http or https");
  }
  if (url.protocol === "http:" && !loopback(url.hostname)) {
    throw new Error("REDX_ACCOUNTS_URL may use http only on loopback");
  }
  if (url.username || url.password) throw new Error("REDX_ACCOUNTS_URL must not include credentials");
  return url.toString().replace(/\/+$/, "");
}

export function accountsConfig(env: Record<string, string | undefined> = process.env): {
  base: string | null;
  error: string | null;
} {
  try {
    return { base: accountsBaseUrl(env), error: null };
  } catch (error) {
    return { base: null, error: error instanceof Error ? error.message : "Invalid accounts URL" };
  }
}

export function hubOrigins(env: Record<string, string | undefined> = process.env): string[] {
  const raw = (env.REDX_HUB_ORIGINS ?? DEFAULT_HUB_ORIGINS).trim();
  if (!raw || /^(off|none|false|0)$/i.test(raw)) return [];
  const origins: string[] = [];
  for (const part of raw.split(",")) {
    const trimmed = part.trim();
    if (!trimmed) continue;
    try {
      const url = new URL(trimmed);
      if (url.protocol !== "https:" && url.protocol !== "http:") continue;
      if (url.username || url.password) continue;
      if ((url.pathname && url.pathname !== "/") || url.search || url.hash) continue;
      origins.push(url.origin);
    } catch {
      continue;
    }
  }
  return origins;
}

export function hubUrl(env: Record<string, string | undefined> = process.env): string {
  const raw = (env.REDX_HUB_URL ?? DEFAULT_HUB_URL).trim() || DEFAULT_HUB_URL;
  try {
    const url = new URL(raw);
    if (url.protocol !== "https:" && url.protocol !== "http:") return DEFAULT_HUB_URL;
    return url.toString().replace(/\/+$/, "");
  } catch {
    return DEFAULT_HUB_URL;
  }
}

export function signupUrl(env: Record<string, string | undefined> = process.env): string {
  const raw = (env.REDX_ACCOUNTS_SIGNUP_URL ?? DEFAULT_SIGNUP_URL).trim() || DEFAULT_SIGNUP_URL;
  try {
    const url = new URL(raw);
    if (url.protocol !== "https:" && url.protocol !== "http:") return DEFAULT_SIGNUP_URL;
    return url.toString();
  } catch {
    return DEFAULT_SIGNUP_URL;
  }
}

export function framePolicy(origins: string[]): { frameOptions: "DENY" | null; csp: string } {
  if (origins.length === 0) return { frameOptions: "DENY", csp: "frame-ancestors 'none'" };
  return { frameOptions: null, csp: `frame-ancestors 'self' ${origins.join(" ")}` };
}

export function panelOriginFromRequest(request: Request, env: Record<string, string | undefined> = process.env): string {
  const configured = (env.REDX_PUBLIC_URL || "").trim().replace(/\/+$/, "");
  if (configured) {
    try {
      return new URL(configured).origin;
    } catch {
      // Fall through to the request URL.
    }
  }
  return new URL(request.url).origin;
}

export function hubManifest(origin: string, env: Record<string, string | undefined> = process.env) {
  const accounts = accountsConfig(env);
  const ancestors = hubOrigins(env);
  return {
    name: "Red-XAI Host",
    product: "redxaihost",
    accountsService: ACCOUNTS_SERVICE_REPO,
    accountsApi: accounts.base,
    accountsEnabled: Boolean(accounts.base),
    signupUrl: signupUrl(env),
    embedUrl: `${origin}/embed`,
    embedQuery: "embed=1",
    launchProtocol: HUB_PROTOCOL,
    deepLink: `${HUB_PROTOCOL}://open`,
    quickLogin: {
      start: `${origin}/api/accounts/quick-login/start`,
      poll: `${origin}/api/accounts/quick-login/poll`,
    },
    login: `${origin}/api/accounts/login`,
    localLogin: `${origin}/api/auth/sign-in/email`,
    hubUrl: hubUrl(env),
    frameAncestors: ancestors,
  };
}

const SELF_ORIGINS = ["http://127.0.0.1:4200", "http://localhost:4200"];

export function requestOriginAllowed(request: Request, env: Record<string, string | undefined> = process.env): boolean {
  const origin = request.headers.get("origin");
  if (!origin || origin === "null") return true;
  let expected: string;
  try {
    expected = new URL(request.url).origin;
  } catch {
    return false;
  }
  if (origin === expected) return true;
  const allowed = new Set<string>([...hubOrigins(env), ...SELF_ORIGINS]);
  const configured = [env.REDX_PUBLIC_URL, env.WEBSITE_URL, env.VITE_WEBSITE_URL];
  for (const value of configured) {
    if (!value) continue;
    try {
      allowed.add(new URL(value).origin);
    } catch {
      continue;
    }
  }
  return allowed.has(origin);
}
