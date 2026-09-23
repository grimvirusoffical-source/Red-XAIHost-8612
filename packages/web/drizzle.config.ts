import { defineConfig } from "drizzle-kit";
import { mkdirSync } from "node:fs";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const dataDir = resolve(process.cwd(), "../../data");
const localDb = resolve(dataDir, "redxaihost.db");
mkdirSync(dataDir, { recursive: true });

const databaseUrl = (process.env.DATABASE_URL || "").trim();
const common = {
  schema: "./src/api/database/schema.ts",
  out: "./drizzle",
};

export default databaseUrl
  ? defineConfig({
      ...common,
      dialect: "turso",
      dbCredentials: {
        url: databaseUrl,
        authToken: process.env.DATABASE_AUTH_TOKEN || undefined,
      },
    })
  : defineConfig({
      ...common,
      dialect: "sqlite",
      dbCredentials: {
        url: pathToFileURL(localDb).href,
      },
    });
