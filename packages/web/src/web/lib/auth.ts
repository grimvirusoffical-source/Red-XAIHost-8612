import { createAuthClient } from "better-auth/react";
import { managedAuthClient } from "@runablehq/managed-auth/client";

// Injected into the app env and exposed to the browser by Vite. `issuer` is
// required — there is no default broker.
const config = {
  applicationId: import.meta.env.VITE_APPLICATION_ID,
  issuer: import.meta.env.VITE_RUNABLE_AUTH_ISSUER,
};

export const authClient = createAuthClient({
  // Packaged desktop builds load from file://, so the page origin is not the server there.
  baseURL: import.meta.env.VITE_WEBSITE_URL ?? window.location.origin,
  basePath: "/api/auth",
  plugins: [managedAuthClient(config)],
});

// Complete a returning managed sign-in (the top-level redirect leg on web; a
// no-op under Electron) before anything that imports this module evaluates.
// Top-level await here holds the whole app graph — including __main's render —
// until the token from the redirect has been stored.
try {
  await authClient.managedAuth.handleRedirect();
} catch {
  // A failed or absent redirect is not fatal — the sign-in screen handles it.
}

/** The single account allowed into this control panel. */
export const OWNER_EMAIL: string = String(import.meta.env.VITE_OWNER_EMAIL ?? "").toLowerCase();

export function isOwner(email?: string | null): boolean {
  if (!email) return false;
  if (!OWNER_EMAIL) return true;
  return OWNER_EMAIL.split(",")
    .map((entry) => entry.trim())
    .includes(email.toLowerCase());
}
