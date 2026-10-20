/**
 * Origins Better Auth accepts for cookie-bearing POSTs. Built from configuration only:
 * the request's own Origin header is never trusted, or CSRF protection would accept any site.
 * REDX_PUBLIC_URL and WEBSITE_URL are the configured public addresses; REDX_TRUSTED_ORIGINS
 * adds more (comma separated). The local panel address on PORT (default 4200) is always allowed.
 */
export function trustedOriginsFromEnv(env: Record<string, string | undefined> = process.env): string[] {
  const port = /^\d{1,5}$/.test(env.PORT ?? "") ? env.PORT! : "4200";
  const raw = [
    env.REDX_PUBLIC_URL,
    env.WEBSITE_URL,
    ...(env.REDX_TRUSTED_ORIGINS ?? "").split(","),
    `http://127.0.0.1:${port}`,
    `http://localhost:${port}`,
  ];
  const out = new Set<string>();
  for (const value of raw) {
    const v = (value ?? "").trim();
    if (!v) continue;
    try {
      const u = new URL(v);
      if (u.protocol === "http:" || u.protocol === "https:") out.add(u.origin);
    } catch {
      // ignore malformed entries
    }
  }
  return [...out];
}
