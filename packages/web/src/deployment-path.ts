/** Optional first-party mount; empty keeps standalone deployments unchanged. */
export function deploymentPath(value: string | undefined): string {
  const path = (value ?? "").trim().replace(/\/+$/, "");
  if (!path) return "";
  if (!/^(?:\/[A-Za-z0-9_-]+)+$/.test(path)) throw new Error("Invalid Host deployment path.");
  return path;
}

export function internalRequest(request: Request, base: string): Request | null {
  if (!base) return request;
  const url = new URL(request.url);
  if (url.pathname !== base && !url.pathname.startsWith(`${base}/`)) return null;
  url.pathname = url.pathname.slice(base.length) || "/";
  return new Request(url.href, request);
}

export function publicRequest(request: Request, base: string): Request {
  if (!base) return request;
  const url = new URL(request.url);
  url.pathname = base + url.pathname;
  return new Request(url.href, request);
}

export function publicResponse(response: Response, base: string): Response {
  const location = response.headers.get("location");
  if (!base || !location?.startsWith("/") || location.startsWith("//") || location === base || location.startsWith(`${base}/`)) return response;
  const headers = new Headers(response.headers);
  headers.set("location", base + location);
  return new Response(response.body, { status: response.status, statusText: response.statusText, headers });
}
