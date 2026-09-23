import { createAuthClient } from "better-auth/react";
import { managedAuthClient } from "@runablehq/managed-auth/client";

const applicationId = import.meta.env.VITE_APPLICATION_ID;
const issuer = import.meta.env.VITE_RUNABLE_AUTH_ISSUER;
export const MANAGED_AUTH_ENABLED = Boolean(applicationId && issuer);
export const GOOGLE_AUTH_ENABLED = String(import.meta.env.VITE_GOOGLE_AUTH_ENABLED ?? "false") === "true";

const plugins = MANAGED_AUTH_ENABLED
  ? [managedAuthClient({ applicationId, issuer })]
  : [];

export const authClient = createAuthClient({
  baseURL: import.meta.env.VITE_WEBSITE_URL ?? window.location.origin,
  basePath: "/api/auth",
  plugins,
});

if (MANAGED_AUTH_ENABLED) {
  try {
    await authClient.managedAuth.handleRedirect();
  } catch {
    // Sign-in UI reports any actionable error.
  }
}

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
