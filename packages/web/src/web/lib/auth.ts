import { createAuthClient } from "better-auth/react";
import { managedAuthClient } from "@runablehq/managed-auth/client";

const applicationId = import.meta.env.VITE_APPLICATION_ID;
const issuer = import.meta.env.VITE_RUNABLE_AUTH_ISSUER;
export const MANAGED_AUTH_ENABLED = Boolean(applicationId && issuer);
export const GOOGLE_AUTH_ENABLED = String(import.meta.env.VITE_GOOGLE_AUTH_ENABLED ?? "false") === "true";

const plugins = applicationId && issuer
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

/**
 * Display-only: the address pre-filled in the local owner sign-in form (first
 * entry of VITE_OWNER_EMAIL). Whether a session is an owner is decided by the
 * server (`me.owner`), never by this value.
 */
export const DEFAULT_OWNER_EMAIL: string = String(
  import.meta.env.VITE_OWNER_EMAIL ?? "grimvirusoffical@gmail.com",
)
  .split(",")[0]!
  .trim()
  .toLowerCase();
