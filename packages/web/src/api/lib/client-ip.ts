/**
 * One rule for the client IP, shared by the panel rate limiter and Better Auth.
 *
 * By default only the TCP peer address is used; forwarded headers are ignored
 * because any client can set them. Behind a reverse proxy you trust, set
 * REDX_TRUSTED_IP_HEADER to the single header that proxy overwrites
 * (e.g. `cf-connecting-ip`, `x-real-ip`, or `x-forwarded-for`). For
 * x-forwarded-for the right-most entry is used: it is the one your proxy appended.
 *
 * The server entry resolves the IP once per request and stamps it into
 * CLIENT_IP_HEADER, after stripping any copy the client sent.
 */
export const CLIENT_IP_HEADER = "x-redx-client-ip";

export function trustedIpHeader(env: Record<string, string | undefined> = process.env): string | null {
  const h = (env.REDX_TRUSTED_IP_HEADER ?? "").trim().toLowerCase();
  return /^[a-z0-9-]{1,64}$/.test(h) && h !== CLIENT_IP_HEADER ? h : null;
}

function clean(ip: string | null | undefined): string | null {
  const v = (ip ?? "").trim();
  return v && v.length <= 64 && /^[0-9a-fA-F:.[\]%a-zA-Z-]+$/.test(v) ? v : null;
}

export function resolveClientIp(
  headers: Headers,
  peer: string | null | undefined,
  env: Record<string, string | undefined> = process.env,
): string {
  const trusted = trustedIpHeader(env);
  if (trusted) {
    const raw = headers.get(trusted);
    if (raw) {
      const value = trusted === "x-forwarded-for" ? raw.split(",").pop() : raw;
      const ip = clean(value);
      if (ip) return ip;
    }
  }
  return clean(peer) ?? "unknown";
}

/** Copy of the request with the resolved client IP stamped in (client copies dropped). */
export function withClientIp(request: Request, peer: string | null | undefined): Request {
  const headers = new Headers(request.headers);
  headers.delete(CLIENT_IP_HEADER);
  headers.set(CLIENT_IP_HEADER, resolveClientIp(request.headers, peer));
  return new Request(request, { headers });
}
