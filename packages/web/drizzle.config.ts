import { defineConfig } from "drizzle-kit";
import { mkdirSync } from "node:fs";
import { resolve } from "node:path";

const localDb = resolve(process.cwd(), "../../data/redxaihost.db");
mkdirSync(resolve(process.cwd(), "../../data"), { recursive: true });

export default defineConfig({
  dialect: "turso",
  schema: "./src/api/database/schema.ts",
  out: "./drizzle",
  dbCredentials: {
    url: process.env.DATABASE_URL || `file:${localDb}`,
    authToken: process.env.DATABASE_AUTH_TOKEN || undefined,
  },
});
