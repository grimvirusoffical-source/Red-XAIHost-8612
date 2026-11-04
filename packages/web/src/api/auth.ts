import { betterAuth } from "better-auth";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { db } from "./database";
import { authTrustedOrigins } from "./lib/auth-origins";
import { deploymentPath } from "../deployment-path";
import { ownerBootstrapEnabled } from "./lib/owner-bootstrap";

export const OWNER_EMAILS = (process.env.OWNER_EMAIL ?? "grimvirusoffical@gmail.com")
  .split(",")
  .map((email) => email.trim().toLowerCase())
  .filter(Boolean);

export function isOwnerEmail(email: string | null | undefined): boolean {
  if (!email) return false;
  return OWNER_EMAILS.includes(email.toLowerCase());
}

const googleEnabled = Boolean(
  process.env.GOOGLE_CLIENT_ID && process.env.GOOGLE_CLIENT_SECRET,
);

export const auth = betterAuth({
  basePath: `${deploymentPath(process.env.REDX_BASE_PATH)}/api/auth`,
  baseURL: new URL(process.env.REDX_PUBLIC_URL || process.env.WEBSITE_URL || "http://127.0.0.1:4200").origin,
  advanced: process.env.REDX_BASE_PATH ? { cookiePrefix: "redx-host" } : undefined,
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
  trustedOrigins: () => authTrustedOrigins(),
  databaseHooks: {
    user: {
      create: {
        before: async (user) => {
          if (!isOwnerEmail(user.email)) {
            throw new Error("This deployment is private. Only the owner account may sign in.");
          }
          if (!user.emailVerified && !ownerBootstrapEnabled()) {
            throw new Error("Local owner setup is disabled. Sign in with Red-XAI Accounts.");
          }
          return { data: user };
        },
      },
    },
  },
});
