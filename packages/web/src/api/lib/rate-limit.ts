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
 * Cloudflare's published edge ranges (https://www.cloudflare.com/ips/).
 * A request only counts as "behind Cloudflare" when the socket peer is in one
 * of these, or in REDX_TRUSTED_PROXIES (for example 127.0.0.1 for a local
 * cloudflared tunnel).
 */
export const CLOUDFLARE_RANGES = [
  "173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
  "141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
  "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
  "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
  "2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
  "2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
] as const;

/** Parses an IPv4 or IPv6 address into 16 bytes (IPv4 as ::ffff:a.b.c.d). */
function parseIp(raw: string): Uint8Array | null {
  let s = raw.trim();
  if (s.startsWith("[") && s.endsWith("]")) s = s.slice(1, -1);
  const zone = s.indexOf("%");
  if (zone >= 0) s = s.slice(0, zone);
  if (!s || s.length > 64) return null;
  const v4 = (t: string): number[] | null => {
    const parts = t.split(".");
    if (parts.length !== 4) return null;
    const out: number[] = [];
    for (const p of parts) {
      if (!/^\d{1,3}$/.test(p)) return null;
      const n = Number(p);
      if (n > 255) return null;
      out.push(n);
    }
    return out;
  };
  const out = new Uint8Array(16);
  if (!s.includes(":")) {
    const b = v4(s);
    if (!b) return null;
    out[10] = 0xff;
    out[11] = 0xff;
    out.set(b, 12);
    return out;
  }
  let tail: number[] = [];
  const lastColon = s.lastIndexOf(":");
  if (s.slice(lastColon + 1).includes(".")) {
    const b = v4(s.slice(lastColon + 1));
    if (!b) return null;
    tail = b;
    s = s.slice(0, lastColon + 1) + "0:0";
  }
  const halves = s.split("::");
  if (halves.length > 2) return null;
  const groups = (h: string) => (h === "" ? [] : h.split(":"));
  const head = groups(halves[0]!);
  const rest = halves.length === 2 ? groups(halves[1]!) : [];
  const missing = 8 - head.length - rest.length;
  if (halves.length === 1 ? missing !== 0 : missing < 1) return null;
  const all = [...head, ...Array(halves.length === 2 ? missing : 0).fill("0"), ...rest];
  for (let i = 0; i < 8; i++) {
    const g = all[i]!;
    if (!/^[0-9a-fA-F]{1,4}$/.test(g)) return null;
    const n = parseInt(g, 16);
    out[i * 2] = n >> 8;
    out[i * 2 + 1] = n & 0xff;
  }
  if (tail.length) out.set(tail, 12);
  return out;
}

interface Cidr {
  bytes: Uint8Array;
  bits: number;
}

function parseCidr(raw: string): Cidr | null {
  const [addr, len] = raw.trim().split("/");
  const bytes = parseIp(addr ?? "");
  if (!bytes) return null;
  const isV4 = !addr!.includes(":");
  const max = isV4 ? 32 : 128;
  const n = len === undefined ? max : /^\d{1,3}$/.test(len) ? Number(len) : NaN;
  if (!Number.isInteger(n) || n < 0 || n > max) return null;
  return { bytes, bits: isV4 ? n + 96 : n };
}

function inCidr(ip: Uint8Array, c: Cidr): boolean {
  let bits = c.bits;
  for (let i = 0; i < 16 && bits > 0; i++, bits -= 8) {
    const mask = bits >= 8 ? 0xff : (0xff << (8 - bits)) & 0xff;
    if ((ip[i]! & mask) !== (c.bytes[i]! & mask)) return false;
  }
  return true;
}

const CF_CIDRS = CLOUDFLARE_RANGES.map((r) => parseCidr(r)!);

export interface ClientIpOptions {
  /** Honour cf-connecting-ip (only from a Cloudflare or trusted peer). Env REDX_TRUST_CLOUDFLARE=true. */
  trustCloudflare: boolean;
  /** Proxies whose X-Forwarded-For / X-Real-IP / cf-connecting-ip are believed. Env REDX_TRUSTED_PROXIES (comma separated IPs or CIDRs). */
  trustedProxies: string[];
}

export function clientIpOptionsFromEnv(env: Record<string, string | undefined> = process.env): ClientIpOptions {
  return {
    trustCloudflare: env.REDX_TRUST_CLOUDFLARE === "true",
    trustedProxies: (env.REDX_TRUSTED_PROXIES ?? "").split(",").map((s) => s.trim()).filter(Boolean),
  };
}

/**
 * Client IP for rate limiting. By default this is the socket peer address and
 * every forwarding header is ignored, because anyone reaching the panel
 * directly can set them. cf-connecting-ip is used only when
 * REDX_TRUST_CLOUDFLARE=true and the peer is a Cloudflare edge address or a
 * configured trusted proxy. X-Forwarded-For / X-Real-IP are used only when the
 * peer is a configured trusted proxy.
 */
export function clientIp(
  headers: Headers,
  peer: string | undefined,
  opts: ClientIpOptions = clientIpOptionsFromEnv(),
): string {
  const peerIp = (peer ?? "").trim().slice(0, 64);
  const peerBytes = parseIp(peerIp);
  if (!peerBytes) return peerIp || "unknown";
  const trusted = opts.trustedProxies.map(parseCidr).filter((c): c is Cidr => c !== null);
  const isTrustedProxy = trusted.some((c) => inCidr(peerBytes, c));
  const isCloudflare = CF_CIDRS.some((c) => inCidr(peerBytes, c));

  if (opts.trustCloudflare && (isCloudflare || isTrustedProxy)) {
    const cf = headers.get("cf-connecting-ip")?.trim();
    if (cf && parseIp(cf)) return cf;
  }
  if (isTrustedProxy) {
    // Walk X-Forwarded-For from the right, skipping our own proxies.
    const chain = (headers.get("x-forwarded-for") ?? "").split(",").map((s) => s.trim()).filter(Boolean);
    for (let i = chain.length - 1; i >= 0; i--) {
      const hop = parseIp(chain[i]!);
      if (!hop) break;
      if (!trusted.some((c) => inCidr(hop, c))) return chain[i]!;
    }
    const real = headers.get("x-real-ip")?.trim();
    if (real && parseIp(real)) return real;
  }
  return peerIp;
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
