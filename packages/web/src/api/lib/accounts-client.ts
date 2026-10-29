/**
 * Client for the Red-XAI Accounts service used by the website and Vortex.
 *
 * Password sign-in matches https://api.red-xai.com/v1/accounts/ (login, login/2fa,
 * login/call, me). Quick Login is the device-code handoff on that same host
 * (`/quick-login/start` and `/quick-login/poll`). Tokens stay in memory for the
 * request that checks /me. This module never touches the credential vault.
 */

export class AccountsUnavailable extends Error {
  constructor(message = "Red-XAI Accounts is not reachable. Use local owner sign-in.") {
    super(message);
    this.name = "AccountsUnavailable";
  }
}

export type AccountsJson = Record<string, unknown>;

export interface AccountsResult {
  status: number;
  json: AccountsJson;
}

type FetchLike = (input: string, init: RequestInit) => Promise<Response>;

function asRecord(value: unknown): AccountsJson {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  return value as AccountsJson;
}

export function stringField(json: AccountsJson, ...keys: string[]): string {
  for (const key of keys) {
    const value = json[key];
    if (typeof value === "string" && value.trim()) return value.trim();
  }
  return "";
}

export function numberField(json: AccountsJson, fallback: number, ...keys: string[]): number {
  for (const key of keys) {
    const value = json[key];
    if (typeof value === "number" && Number.isFinite(value) && value > 0) return value;
  }
  return fallback;
}

/** A finished sign-in, not the 2FA flow token. */
export function isCompletedSession(json: AccountsJson): boolean {
  const token = stringField(json, "token");
  if (!token) return false;
  const next = stringField(json, "next");
  return next !== "2fa" && next !== "2fa_setup";
}

export function accountIdentity(json: AccountsJson): { id: string; email: string; name: string } | null {
  const nested = json.account;
  const account = asRecord(nested && typeof nested === "object" ? nested : json);
  const id = stringField(account, "id", "sub", "key");
  const email = stringField(account, "email").toLowerCase();
  if (!id || id.length > 256 || !email.includes("@") || email.length > 254) return null;
  const name = stringField(account, "name", "username") || email;
  return { id, email, name: name.slice(0, 120) };
}

export function quickLoginView(json: AccountsJson): {
  deviceCode: string;
  userCode: string;
  verificationUrl: string;
  interval: number;
  expiresIn: number;
} | null {
  const deviceCode = stringField(json, "deviceCode", "device_code");
  const userCode = stringField(json, "userCode", "user_code");
  if (!deviceCode || !userCode) return null;
  return {
    deviceCode,
    userCode,
    verificationUrl: stringField(json, "verificationUrl", "verification_url", "verificationUri", "verification_uri"),
    interval: Math.max(2, Math.min(30, numberField(json, 3, "interval"))),
    expiresIn: Math.max(30, Math.min(1800, numberField(json, 300, "expiresIn", "expires_in"))),
  };
}

export async function accountsCall(
  base: string,
  route: string,
  init: {
    method?: "GET" | "POST";
    body?: Record<string, unknown>;
    flow?: string;
    session?: string;
    clientIp?: string;
    fetch?: FetchLike;
  } = {},
): Promise<AccountsResult> {
  const headers = new Headers();
  headers.set("accept", "application/json");
  if (init.body) headers.set("content-type", "application/json");
  if (init.flow) headers.set("x-redx-flow", init.flow);
  if (init.session) headers.set("x-redx-session", init.session);
  if (init.clientIp) headers.set("x-redxai-client-ip", init.clientIp);
  const call = init.fetch ?? fetch;
  let response: Response;
  try {
    response = await call(`${base}/${route}`, {
      method: init.method ?? (init.body ? "POST" : "GET"),
      headers,
      body: init.body ? JSON.stringify(init.body) : undefined,
      redirect: "manual",
      signal: AbortSignal.timeout(15_000),
    });
  } catch {
    throw new AccountsUnavailable();
  }
  if (response.status >= 500 || response.status === 0) {
    throw new AccountsUnavailable();
  }
  let json: AccountsJson = {};
  try {
    json = asRecord(await response.json());
  } catch {
    json = {};
  }
  return { status: response.status, json };
}
