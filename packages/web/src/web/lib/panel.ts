/** Panel origin the desktop shell and the browser both call. */
export function panelBase(): string {
  const configured = import.meta.env.VITE_WEBSITE_URL;
  if (typeof configured === "string" && configured.trim()) return configured.replace(/\/+$/, "");
  return window.location.origin;
}

export async function panelFetch(path: string, init?: RequestInit): Promise<Response> {
  return fetch(`${panelBase()}${path}`, {
    ...init,
    credentials: "include",
    headers: {
      accept: "application/json",
      ...(init?.body ? { "content-type": "application/json" } : {}),
      ...Object.fromEntries(new Headers(init?.headers).entries()),
    },
  });
}
