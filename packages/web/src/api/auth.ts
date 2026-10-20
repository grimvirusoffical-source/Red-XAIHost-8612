import { betterAuth } from "better-auth";
import { trustedOriginsFromEnv } from "./lib/trusted-origins";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { runableManagedAuth } from "@runablehq/managed-auth/server";
import { db } from "./database";
import { CLIENT_IP_HEADER } from "./lib/client-ip";
import { identitySource, nationIdentityHook } from "./lib/identity";

// Validate at boot so a typo fails loudly instead of silently using local accounts.
identitySource();

// Explicit OWNER_EMAILS replaces the defaults; a legacy single OWNER_EMAIL is added to them.
export const OWNER_EMAILS = (
  process.env.OWNER_EMAILS ||
  ["grimvirusoffical@gmail.com,support@infected-nation.com", process.env.OWNER_EMAIL].filter(Boolean).join(",")
)
  .split(",")
  .map((email) => email.trim().toLowerCase())
  .filter(Boolean);

export function isOwnerEmail(email: string | null | undefined): boolean {
  if (!email) return false;
  return OWNER_EMAILS.includes(email.toLowerCase());
}

const managedEnabled = Boolean(
  process.env.APPLICATION_ID && process.env.VITE_RUNABLE_AUTH_ISSUER,
);
const googleEnabled = Boolean(
  process.env.GOOGLE_CLIENT_ID && process.env.GOOGLE_CLIENT_SECRET,
);

const plugins = managedEnabled
  ? runableManagedAuth({
      applicationId: process.env.APPLICATION_ID!,
      issuer: process.env.VITE_RUNABLE_AUTH_ISSUER!,
    })
  : [];

export const auth = betterAuth({
  basePath: "/api/auth",
  baseURL: process.env.REDX_PUBLIC_URL || process.env.WEBSITE_URL || "http://127.0.0.1:4200",
  database: drizzleAdapter(db, { provider: "sqlite" }),
  emailAndPassword: {
    enabled: true,
    minPasswordLength: 12,
  },
  socialProviders: googleEnabled
    ? {
        google: {
          clientId: process.env.GOOGLE_CLIENT_ID!,
          clientSecret: process.env.GOOGLE_CLIENT_SECRET!,
        },
      }
    : undefined,
  secret: process.env.BETTER_AUTH_SECRET,
  advanced: {
    // Same client IP the panel limiter uses: socket peer, or REDX_TRUSTED_IP_HEADER when opted in.
    ipAddress: { ipAddressHeaders: [CLIENT_IP_HEADER] },
  },
  // Config only; never echo the request Origin (see lib/trusted-origins.ts).
  trustedOrigins: trustedOriginsFromEnv(),
  hooks: { before: nationIdentityHook },
  databaseHooks: {
    user: {
      create: {
        before: async (user) => {
          if (!isOwnerEmail(user.email)) {
            throw new Error("This deployment is private. Only the owner account may sign in.");
          }
          return { data: user };
        },
      },
    },
  },
  plugins,
});
