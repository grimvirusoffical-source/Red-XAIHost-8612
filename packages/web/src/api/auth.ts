import { betterAuth } from "better-auth";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { runableManagedAuth } from "@runablehq/managed-auth/server";
import { db } from "./database";

export const OWNER_EMAILS = (process.env.OWNER_EMAIL ?? "grimvirusoffical@gmail.com")
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
  baseURL: process.env.WEBSITE_URL ?? "http://127.0.0.1:4200",
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
  trustedOrigins: (request) => {
    const origin = request?.headers.get("origin");
    const configured = process.env.WEBSITE_URL;
    return [...new Set([origin, configured, "http://127.0.0.1:4200", "http://localhost:4200"].filter(Boolean) as string[])];
  },
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
