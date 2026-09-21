import { betterAuth } from "better-auth";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { runableManagedAuth } from "@runablehq/managed-auth/server";
import { db } from "./database";

/**
 * RedXAIHost is a single-owner control plane. Sign-in is Google only, and the
 * owner allow-list below is enforced again inside every protected procedure
 * (src/api/middleware/auth.ts) so a session for any other account is useless.
 */
export const OWNER_EMAILS = (process.env.OWNER_EMAIL ?? "grimvirusoffical@gmail.com")
  .split(",")
  .map((email) => email.trim().toLowerCase())
  .filter(Boolean);

export function isOwnerEmail(email: string | null | undefined): boolean {
  if (!email) return false;
  return OWNER_EMAILS.includes(email.toLowerCase());
}

export const auth = betterAuth({
  basePath: "/api/auth",
  baseURL: process.env.WEBSITE_URL,
  database: drizzleAdapter(db, { provider: "sqlite" }),
  emailAndPassword: { enabled: false },
  secret: process.env.BETTER_AUTH_SECRET,
  trustedOrigins: (request) => {
    const origin = request?.headers.get("origin");
    return origin ? [origin] : ["*"];
  },
  databaseHooks: {
    user: {
      create: {
        before: async (user) => {
          // Hard gate: nobody but the owner ever gets a user row.
          if (!isOwnerEmail(user.email)) {
            throw new Error("This deployment is private. Only the owner account may sign in.");
          }
          return { data: user };
        },
      },
    },
  },
  plugins: [
    ...runableManagedAuth({
      applicationId: process.env.APPLICATION_ID!,
      issuer: process.env.VITE_RUNABLE_AUTH_ISSUER!,
    }),
  ],
});
