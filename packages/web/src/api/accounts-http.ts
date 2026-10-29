import type { Hono } from "hono";
import { clientIp } from "./lib/rate-limit";
import {
  accountsCall,
  AccountsUnavailable,
  accountIdentity,
  isCompletedSession,
  quickLoginView,
  stringField,
  type AccountsJson,
} from "./lib/accounts-client";
import {
  accountsConfig,
  ACCOUNTS_SERVICE_REPO,
  hubManifest,
  panelOriginFromRequest,
  requestOriginAllowed,
  signupUrl,
} from "./lib/hub";
import { openOwnerSession, PanelAccessError } from "./lib/owner-session";

type FetchLike = (input: string, init: RequestInit) => Promise<Response>;

function disabledMessage(error: string | null): string {
  if (error) return error;
  return "Red-XAI Accounts is off for this panel. Use local owner sign-in.";
}

async function readBody(request: Request): Promise<AccountsJson> {
  const claimed = Number(request.headers.get("content-length") ?? "0");
  if (Number.isFinite(claimed) && claimed > 8192) {
    throw new Error("body too large");
  }
  const text = await request.text();
  if (text.length > 8192) throw new Error("body too large");
  if (!text.trim()) return {};
  const parsed: unknown = JSON.parse(text);
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
  return parsed as AccountsJson;
}

function loginHint(body: AccountsJson): string {
  return stringField(body, "login");
}

async function finishSession(
  base: string,
  result: { status: number; json: AccountsJson },
  hint: string,
  ip: string,
  fetchImpl: FetchLike | undefined,
): Promise<{ status: number; body: AccountsJson; cookie?: string }> {
  if (!isCompletedSession(result.json)) return { status: result.status, body: result.json };
  const token = stringField(result.json, "token");
  const me = await accountsCall(base, "me", { method: "GET", session: token, clientIp: ip, fetch: fetchImpl });
  const identity = accountIdentity(me.json);
  if (me.status !== 200 || !identity) {
    return {
      status: 401,
      body: { error: "unverified", message: "Red-XAI Accounts did not confirm that sign-in." },
    };
  }
  if (hint.includes("@") && hint.trim().toLowerCase() !== identity.email) {
    return {
      status: 401,
      body: { error: "mismatch", message: "Sign in with the email on your Red-XAI account." },
    };
  }
  try {
    const cookie = await openOwnerSession(identity);
    return { status: 200, body: { ok: true }, cookie };
  } catch (error) {
    if (error instanceof PanelAccessError) {
      return { status: error.status, body: { error: "forbidden", message: error.message } };
    }
    throw error;
  }
}

/**
 * Same-origin accounts routes for the panel. Upstream is Red-XAI Accounts.
 * A completed sign-in becomes a panel session cookie. The upstream token is
 * not stored and is not returned to the browser.
 */
export function registerAccountsRoutes(app: Hono, deps?: { fetch?: FetchLike }) {
  app.get("/api/hub", (c) => c.json(hubManifest(panelOriginFromRequest(c.req.raw))));
  // Same document when a proxy forwards every path to this app. The stock
  // static server does not; it serves public/.well-known/redxai-host.json,
  // which points Hub at GET /api/hub.
  app.get("/.well-known/redxai-host.json", (c) =>
    c.json(hubManifest(panelOriginFromRequest(c.req.raw))),
  );
  app.get("/api/accounts/config", (c) => {
    const accounts = accountsConfig();
    return c.json({
      enabled: Boolean(accounts.base) && !accounts.error,
      service: ACCOUNTS_SERVICE_REPO,
      signupUrl: signupUrl(),
      quickLogin: Boolean(accounts.base) && !accounts.error,
      error: accounts.error,
    });
  });

  app.post("/api/accounts/login", (c) => handleLogin(c.req.raw, deps?.fetch));
  app.post("/api/accounts/login/2fa", (c) => handleTwoFactor(c.req.raw, deps?.fetch));
  app.post("/api/accounts/login/call", (c) => handleCall(c.req.raw, deps?.fetch));
  app.post("/api/accounts/quick-login/start", (c) => handleQuickStart(c.req.raw, deps?.fetch));
  app.post("/api/accounts/quick-login/poll", (c) => handleQuickPoll(c.req.raw, deps?.fetch));
}

function refused(request: Request): Response | null {
  if (requestOriginAllowed(request)) return null;
  return Response.json({ error: "origin", message: "Cross-site request refused." }, { status: 403 });
}

function accountsOr503(): { base: string } | Response {
  const accounts = accountsConfig();
  if (!accounts.base) {
    return Response.json(
      { error: accounts.error ? "misconfigured" : "disabled", message: disabledMessage(accounts.error) },
      { status: 503 },
    );
  }
  return { base: accounts.base };
}

async function handleLogin(request: Request, fetchImpl?: FetchLike): Promise<Response> {
  const blocked = refused(request);
  if (blocked) return blocked;
  const ready = accountsOr503();
  if (ready instanceof Response) return ready;
  let body: AccountsJson;
  try {
    body = await readBody(request);
  } catch {
    return Response.json({ error: "bad_request", message: "Sign-in details were not readable." }, { status: 400 });
  }
  const login = stringField(body, "login");
  const password = typeof body.password === "string" ? body.password : "";
  if (!login || login.length > 254 || !password || password.length > 256) {
    return Response.json({ error: "bad_request", message: "Username or email, and a password, are required." }, { status: 400 });
  }
  try {
    const result = await accountsCall(ready.base, "login", {
      body: { login, password },
      clientIp: clientIp(request.headers),
      fetch: fetchImpl,
    });
    const finished = await finishSession(ready.base, result, login, clientIp(request.headers), fetchImpl);
    return jsonResult(finished);
  } catch (error) {
    return unavailable(error);
  }
}

async function handleTwoFactor(request: Request, fetchImpl?: FetchLike): Promise<Response> {
  const blocked = refused(request);
  if (blocked) return blocked;
  const ready = accountsOr503();
  if (ready instanceof Response) return ready;
  const flow = request.headers.get("x-redx-flow")?.trim() ?? "";
  if (!flow || flow.length > 512) {
    return Response.json({ error: "bad_request", message: "This sign-in expired. Start again." }, { status: 400 });
  }
  let body: AccountsJson;
  try {
    body = await readBody(request);
  } catch {
    return Response.json({ error: "bad_request", message: "The code was not readable." }, { status: 400 });
  }
  const method = stringField(body, "method");
  const code = stringField(body, "code");
  if ((method !== "totp" && method !== "call") || code.length < 4 || code.length > 16) {
    return Response.json({ error: "bad_request", message: "Enter the code from your second step." }, { status: 400 });
  }
  try {
    const result = await accountsCall(ready.base, "login/2fa", {
      body: { method, code },
      flow,
      clientIp: clientIp(request.headers),
      fetch: fetchImpl,
    });
    const finished = await finishSession(ready.base, result, "", clientIp(request.headers), fetchImpl);
    return jsonResult(finished);
  } catch (error) {
    return unavailable(error);
  }
}

async function handleCall(request: Request, fetchImpl?: FetchLike): Promise<Response> {
  const blocked = refused(request);
  if (blocked) return blocked;
  const ready = accountsOr503();
  if (ready instanceof Response) return ready;
  const flow = request.headers.get("x-redx-flow")?.trim() ?? "";
  if (!flow || flow.length > 512) {
    return Response.json({ error: "bad_request", message: "This sign-in expired. Start again." }, { status: 400 });
  }
  try {
    const result = await accountsCall(ready.base, "login/call", {
      body: {},
      flow,
      clientIp: clientIp(request.headers),
      fetch: fetchImpl,
    });
    return Response.json(result.json, { status: result.status });
  } catch (error) {
    return unavailable(error);
  }
}

async function handleQuickStart(request: Request, fetchImpl?: FetchLike): Promise<Response> {
  const blocked = refused(request);
  if (blocked) return blocked;
  const ready = accountsOr503();
  if (ready instanceof Response) return ready;
  try {
    const result = await accountsCall(ready.base, "quick-login/start", {
      body: { client: "redxaihost" },
      clientIp: clientIp(request.headers),
      fetch: fetchImpl,
    });
    if (result.status === 404) return quickUnsupported();
    const view = quickLoginView(result.json);
    if (result.status === 200 && view) return Response.json(view);
    return Response.json(result.json, { status: result.status });
  } catch (error) {
    return unavailable(error);
  }
}

async function handleQuickPoll(request: Request, fetchImpl?: FetchLike): Promise<Response> {
  const blocked = refused(request);
  if (blocked) return blocked;
  const ready = accountsOr503();
  if (ready instanceof Response) return ready;
  let body: AccountsJson;
  try {
    body = await readBody(request);
  } catch {
    return Response.json({ error: "bad_request", message: "Quick Login could not be checked." }, { status: 400 });
  }
  const deviceCode = stringField(body, "deviceCode", "device_code");
  if (!deviceCode || deviceCode.length > 512) {
    return Response.json({ error: "bad_request", message: "Quick Login expired. Start again." }, { status: 400 });
  }
  try {
    const result = await accountsCall(ready.base, "quick-login/poll", {
      body: { deviceCode, device_code: deviceCode },
      clientIp: clientIp(request.headers),
      fetch: fetchImpl,
    });
    if (result.status === 404) return quickUnsupported();
    const finished = await finishSession(ready.base, result, "", clientIp(request.headers), fetchImpl);
    return jsonResult(finished);
  } catch (error) {
    return unavailable(error);
  }
}

function quickUnsupported(): Response {
  return Response.json(
    {
      error: "unsupported",
      message:
        "This Red-XAI Accounts host does not offer Quick Login yet. Sign in with your account password, or use local owner sign-in.",
    },
    { status: 404 },
  );
}

function jsonResult(result: { status: number; body: AccountsJson; cookie?: string }): Response {
  const headers = new Headers();
  if (result.cookie) headers.append("set-cookie", result.cookie);
  return Response.json(result.body, { status: result.status, headers });
}

function unavailable(error: unknown): Response {
  if (error instanceof AccountsUnavailable) {
    return Response.json({ error: "unavailable", message: error.message }, { status: 503 });
  }
  if (error instanceof PanelAccessError) {
    return Response.json({ error: "forbidden", message: error.message }, { status: error.status });
  }
  return Response.json({ error: "unavailable", message: "Red-XAI Accounts is not reachable. Use local owner sign-in." }, { status: 503 });
}
