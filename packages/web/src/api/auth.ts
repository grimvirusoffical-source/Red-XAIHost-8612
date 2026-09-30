import { betterAuth } from "better-auth";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { username, twoFactor } from "better-auth/plugins";
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
const appleEnabled = Boolean(
  process.env.APPLE_CLIENT_ID && process.env.APPLE_CLIENT_SECRET,
);
const discordEnabled = Boolean(
  process.env.DISCORD_CLIENT_ID && process.env.DISCORD_CLIENT_SECRET,
);

const plugins = [
  username({
    minUsernameLength: 5,
    maxUsernameLength: 64,
    usernameValidator: (value) =>
      /^[A-Za-z0-9_.-]+$/.test(value) &&
      (value.match(/[A-Za-z]/g)?.length ?? 0) >= 3,
  }),
  twoFactor({
    issuer: "Red-XAI",
  }),
  ...(managedEnabled
    ? [
        runableManagedAuth({
          applicationId: process.env.APPLICATION_ID!,
          issuer: process.env.VITE_RUNABLE_AUTH_ISSUER!,
        }),
      ]
    : []),
];

const socialProviders = {
  ...(googleEnabled
    ? {
        google: {
          clientId: process.env.GOOGLE_CLIENT_ID!,
          clientSecret: process.env.GOOGLE_CLIENT_SECRET!,
        },
      }
    : {}),
  ...(appleEnabled
    ? {
        apple: {
          clientId: process.env.APPLE_CLIENT_ID!,
          clientSecret: process.env.APPLE_CLIENT_SECRET!,
        },
      }
    : {}),
  ...(discordEnabled
    ? {
        discord: {
          clientId: process.env.DISCORD_CLIENT_ID!,
          clientSecret: process.env.DISCORD_CLIENT_SECRET!,
        },
      }
    : {}),
};

export const auth = betterAuth({
  basePath: "/api/auth",
  baseURL:
    process.env.REDX_PUBLIC_URL ||
    process.env.WEBSITE_URL ||
    "http://127.0.0.1:4200",
  database: drizzleAdapter(db, { provider: "sqlite" }),
  emailAndPassword: {
    enabled: true,
    minPasswordLength: 8,
    maxPasswordLength: 600,
  },
  user: {
    additionalFields: {
      dob: {
        type: "string",
        required: false,
        input: true,
      },
      termsAcceptedAt: {
        type: "number",
        required: false,
        input: true,
      },
      role: {
        type: "string",
        required: false,
        defaultValue: "user",
        input: false,
      },
    },
  },
  socialProviders:
    Object.keys(socialProviders).length > 0 ? socialProviders : undefined,
  secret: process.env.BETTER_AUTH_SECRET,
  trustedOrigins: (request) => {
    const origin = request?.headers.get("origin");
    const configured =
      process.env.REDX_PUBLIC_URL || process.env.WEBSITE_URL;
    return [
      ...new Set(
        [
          origin,
          configured,
          "http://127.0.0.1:4200",
          "http://localhost:4200",
        ].filter(Boolean) as string[],
      ),
    ];
  },
  databaseHooks: {
    user: {
      create: {
        before: async (candidate) => ({
          data: {
            ...candidate,
            role: isOwnerEmail(candidate.email) ? "owner" : "user",
          },
        }),
      },
    },
  },
  plugins,
});
