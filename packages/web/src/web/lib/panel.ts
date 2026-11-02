import { deploymentPath } from "../../deployment-path";

export const PANEL_MOUNT = deploymentPath(import.meta.env.VITE_BASE_PATH);

/** Panel base URL, including its optional first-party mount. */
export function panelBase(): string {
  const configured = import.meta.env.VITE_WEBSITE_URL;
  if (typeof configured === "string" && configured.trim()) return configured.replace(/\/+$/, "");
  return window.location.origin + PANEL_MOUNT;
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
