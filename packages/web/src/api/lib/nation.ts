/**
 * InfectedNation client for the RedXAIHost control plane.
 *
 * Avery's rule: panel `/api/auth` is backed by Nation `/api/v1` so grim's
 * owner login and studio clients share one identity source. The local
 * Better Auth session is only a cookie jar for the panel UI after Nation
 * accepts the credentials.
 */
export const NATION_ORIGIN = (
  process.env.INFECTEDNATION_URL ||
  process.env.NATION_ORIGIN ||
  "http://127.0.0.1:8787"
).replace(/\/+$/, "");

export const NATION_APP_ID = process.env.REDX_NATION_APP_ID || "redxai-host";

type NationJson = Record<string, unknown>;

export async function nationPost(
  path: string,
  body: NationJson = {},
  init: { signal?: AbortSignal } = {},
): Promise<{ ok: boolean; status: number; data: NationJson }> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 15_000);
  try {
    const response = await fetch(`${NATION_ORIGIN}${path}`, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        accept: "application/json",
      },
      body: JSON.stringify(body),
      signal: init.signal ?? controller.signal,
      redirect: "error",
    });
    const text = await response.text();
    let data: NationJson = {};
    try {
      data = text ? (JSON.parse(text) as NationJson) : {};
    } catch {
      data = { error: "InfectedNation returned unreadable JSON." };
    }
    return { ok: response.ok, status: response.status, data };
  } catch (error) {
    const message =
      error instanceof Error && error.name === "AbortError"
        ? "InfectedNation timed out."
        : error instanceof Error
          ? error.message
          : "InfectedNation is unreachable.";
    return { ok: false, status: 503, data: { error: message } };
  } finally {
    clearTimeout(timer);
  }
}

export async function nationGet(
  path: string,
): Promise<{ ok: boolean; status: number; data: NationJson }> {
  try {
    const response = await fetch(`${NATION_ORIGIN}${path}`, {
      method: "GET",
      headers: { accept: "application/json" },
      signal: AbortSignal.timeout(8_000),
      redirect: "error",
    });
    const text = await response.text();
    let data: NationJson = {};
    try {
      data = text ? (JSON.parse(text) as NationJson) : {};
    } catch {
      data = { error: "InfectedNation returned unreadable JSON." };
    }
    return { ok: response.ok, status: response.status, data };
  } catch (error) {
    return {
      ok: false,
      status: 503,
      data: {
        error: error instanceof Error ? error.message : "InfectedNation is unreachable.",
      },
    };
  }
}

export async function nationLoginEmail(email: string, password: string) {
  return nationPost("/api/v1/login/email", {
    email,
    password,
    appId: NATION_APP_ID,
  });
}

export async function nationSignupEmail(fields: NationJson) {
  return nationPost("/api/v1/signup/email", {
    ...fields,
    appId: NATION_APP_ID,
  });
}

export async function nationSessionMe(token: string) {
  return nationPost("/api/v1/session/me", { token });
}

export async function nationHealth() {
  return nationGet("/api/health");
}
