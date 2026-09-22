/**
 * In-process rate limiting for the panel itself.
 *
 * This is deliberately independent of Cloudflare: the edge rules protect the
 * hosted sites, but the control plane has to defend itself even when it is
 * reached directly, and it must keep working before any Cloudflare token is
 * configured. A fixed-window counter per key is enough — the goal is to make
 * floods and credential stuffing expensive, not to meter traffic precisely.
 */
export interface RateLimitRule {
  /** Window length in milliseconds. */
  windowMs: number;
  /** Requests allowed per window, per key. */
  max: number;
}

interface Counter {
  count: number;
  resetAt: number;
}

const buckets = new Map<string, Counter>();

/** Cap on distinct tracked keys, so a spoofed-IP flood cannot grow the map without bound. */
const MAX_KEYS = 20_000;

function sweep(now: number) {
  for (const [key, counter] of buckets) {
    if (counter.resetAt <= now) buckets.delete(key);
  }
}

export interface RateLimitResult {
  allowed: boolean;
  remaining: number;
  limit: number;
  /** Seconds until the window resets — served as Retry-After. */
  retryAfter: number;
}

export function hitRateLimit(key: string, rule: RateLimitRule): RateLimitResult {
  const now = Date.now();
  let counter = buckets.get(key);

  if (!counter || counter.resetAt <= now) {
    if (buckets.size >= MAX_KEYS) sweep(now);
    counter = { count: 0, resetAt: now + rule.windowMs };
    buckets.set(key, counter);
  }

  counter.count += 1;
  const retryAfter = Math.max(1, Math.ceil((counter.resetAt - now) / 1000));
  return {
    allowed: counter.count <= rule.max,
    remaining: Math.max(0, rule.max - counter.count),
    limit: rule.max,
    retryAfter,
  };
}

/** Forgets a key — used so a successful login does not leave failures counted against it. */
export function clearRateLimit(key: string) {
  buckets.delete(key);
}

/**
 * Best-effort client IP. Cloudflare's own header is trusted first because the
 * panel is expected to sit behind it; the rest are the usual proxy headers.
 * A spoofed value only ever limits the spoofer, never another visitor.
 */
export function clientIp(headers: Headers): string {
  const cf = headers.get("cf-connecting-ip");
  if (cf) return cf.trim();
  const forwarded = headers.get("x-forwarded-for");
  if (forwarded) return forwarded.split(",")[0]!.trim();
  return headers.get("x-real-ip")?.trim() || "unknown";
}

/**
 * The limits, tuned so a normal person clicking around the panel never meets
 * them. Login is the strict one: it is the only endpoint where guessing pays.
 */
export const LIMITS = {
  /** Sign-in and any other auth flow: 10 attempts per 5 minutes per IP. */
  auth: { windowMs: 5 * 60_000, max: 10 } satisfies RateLimitRule,
  /** Agent traffic: heartbeats are every 20s per node, so this is generous. */
  agent: { windowMs: 60_000, max: 240 } satisfies RateLimitRule,
  /** Everything else on the API. */
  api: { windowMs: 60_000, max: 600 } satisfies RateLimitRule,
} as const;

/** Snapshot for the security panel. */
export function rateLimitStats() {
  const now = Date.now();
  let active = 0;
  for (const counter of buckets.values()) if (counter.resetAt > now) active += 1;
  return { trackedKeys: active, limits: LIMITS };
}
