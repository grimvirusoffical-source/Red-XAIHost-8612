import { hubOrigins } from "./hub.ts";

/** Authentication trust comes from deployment configuration, never request headers. */
export function authTrustedOrigins(env: Record<string, string | undefined> = process.env): string[] {
  const origins = new Set(hubOrigins(env));
  for (const raw of [env.REDX_PUBLIC_URL, env.WEBSITE_URL]) {
    if (!raw) continue;
    try {
      const url = new URL(raw);
      const local = ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname);
      if (url.username || url.password) continue;
      if (url.protocol === "https:" || (url.protocol === "http:" && local)) origins.add(url.origin);
    } catch {
      // Invalid deployment values cannot expand the trust boundary.
    }
  }
  // Match the local auth base URL fallback only when no public URL is configured.
  if (!env.REDX_PUBLIC_URL && !env.WEBSITE_URL) {
    origins.add("http://127.0.0.1:4200");
    origins.add("http://localhost:4200");
  }
  return [...origins];
}
