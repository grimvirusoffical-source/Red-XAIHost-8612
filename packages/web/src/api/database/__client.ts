// Self-hostable database client. Turso/libSQL remains supported through
// DATABASE_URL, but a fresh RedXAIHost clone defaults to a local SQLite file.
import { drizzle } from "drizzle-orm/libsql";
import { createClient } from "@libsql/client";
import { mkdirSync } from "node:fs";
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";
import * as schema from "./schema";

const defaultPath = fileURLToPath(new URL("../../../../../data/redxaihost.db", import.meta.url));
mkdirSync(dirname(defaultPath), { recursive: true });

const client = createClient({
  url: process.env.DATABASE_URL || `file:${defaultPath}`,
  authToken: process.env.DATABASE_AUTH_TOKEN || undefined,
});

export const db = drizzle(client, { schema });
