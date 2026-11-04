import { createAuthClient } from "better-auth/react";
import { PANEL_MOUNT, panelBase } from "./panel";

export const GOOGLE_AUTH_ENABLED = String(import.meta.env.VITE_GOOGLE_AUTH_ENABLED ?? "false") === "true";

export const authClient = createAuthClient({
  baseURL: new URL(panelBase()).origin,
  basePath: `${PANEL_MOUNT}/api/auth`,
  fetchOptions: { credentials: "include" },
});

export const OWNER_EMAIL: string = String(
  import.meta.env.VITE_OWNER_EMAIL ?? "grimvirusoffical@gmail.com",
).toLowerCase();

export function isOwner(email?: string | null): boolean {
  if (!email) return false;
  return OWNER_EMAIL.split(",")
    .map((entry) => entry.trim())
    .filter(Boolean)
    .includes(email.toLowerCase());
}
