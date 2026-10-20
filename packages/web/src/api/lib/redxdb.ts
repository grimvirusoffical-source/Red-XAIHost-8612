/**
 * RedX DB client for the Host. Talks to a redxd server over its /v1 HTTP API
 * (see RedX api/openapi.json): GET /healthz (no key), GET /v1/dbs (read),
 * GET /v1/admin/status (host-admin). Authentication is `Authorization: Bearer`.
 *
 * Configuration: the URL comes from the saved "redxdb" credential, then
 * REDX_DB_URL, then http://127.0.0.1:7777. The API key comes from the saved
 * credential (ChaCha20-Poly1305 via lib/crypto), falling back to REDX_DB_KEY.
 */
import { getCredential } from "./credentials";

export const DEFAULT_REDX_DB_URL = "http://127.0.0.1:7777";

export class RedXDBError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message);
    this.name = "RedXDBError";
  }
}

export type RedXDBConfig = { url?: string; key?: string; timeoutMs?: number };

export type RedXHealth = { status: string; role?: string; version?: string; [k: string]: unknown };

function normalizeUrl(raw: string): string {
  const url = new URL(raw);
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new RedXDBError("REDX_DB_URL must be http(s).", 400, "bad_url");
  }
  return url.toString().replace(/\/+$/, "");
}

/** The API key may travel only over https, or plain http to this machine (loopback). */
export function keySafeOver(base: string): boolean {
  const u = new URL(base);
  if (u.protocol === "https:") return true;
  const h = u.hostname.replace(/^\[|\]$/g, "").toLowerCase();
  return h === "localhost" || h === "::1" || /^127(\.\d{1,3}){3}$/.test(h);
}

export function redxdbFrom(config: RedXDBConfig) {
  const base = normalizeUrl(config.url || process.env.REDX_DB_URL || DEFAULT_REDX_DB_URL);
  const key = (config.key ?? "").trim();
  const timeoutMs = config.timeoutMs ?? 8_000;

  async function call<T>(path: string, needsKey: boolean): Promise<T> {
    if (needsKey && !key) throw new RedXDBError("No RedX DB API key is configured.", 412, "no_key");
    if (needsKey && !keySafeOver(base)) {
      throw new RedXDBError(
        "Refusing to send the RedX DB API key over plain http to a non-loopback host. Use an https:// URL.",
        400,
        "insecure_url",
      );
    }
    let res: Response;
    try {
      res = await fetch(base + path, {
        headers: {
          accept: "application/json",
          ...(needsKey ? { authorization: `Bearer ${key}` } : {}),
        },
        signal: AbortSignal.timeout(timeoutMs),
        redirect: "error",
      });
    } catch (error) {
      throw new RedXDBError(
        `RedX DB at ${base} is unreachable: ${error instanceof Error ? error.message : String(error)}`,
        503,
        "unreachable",
      );
    }
    const text = await res.text();
    let body: unknown = null;
    try {
      body = text ? JSON.parse(text) : null;
    } catch {
      throw new RedXDBError(`RedX DB returned non-JSON (${res.status}).`, 502, "bad_json");
    }
    if (!res.ok) {
      const b = (body ?? {}) as { error?: unknown; message?: unknown; code?: unknown };
      const msg = String(b.error ?? b.message ?? `HTTP ${res.status}`);
      throw new RedXDBError(`RedX DB ${path}: ${msg}`, res.status, typeof b.code === "string" ? b.code : undefined);
    }
    return body as T;
  }

  return {
    url: base,
    hasKey: Boolean(key),
    health: () => call<RedXHealth>("/healthz", false),
    adminStatus: () => call<Record<string, unknown>>("/v1/admin/status", true),
    async listDatabases(): Promise<{ databases: unknown[]; raw: unknown }> {
      const raw = await call<unknown>("/v1/dbs", true);
      return { databases: extractList(raw), raw };
    },
  };
}

/** /v1/dbs answers an object; accept the list under the usual keys or a bare array. */
function extractList(raw: unknown): unknown[] {
  if (Array.isArray(raw)) return raw;
  if (raw && typeof raw === "object") {
    for (const k of ["dbs", "databases", "items"]) {
      const v = (raw as Record<string, unknown>)[k];
      if (Array.isArray(v)) return v;
    }
  }
  return [];
}

/** Client built from the saved credential, with env fallbacks. */
export async function redxdb() {
  const saved = await getCredential("redxdb");
  return redxdbFrom({
    url: saved?.url || undefined,
    key: saved?.apiKey || process.env.REDX_DB_KEY || "",
  });
}
