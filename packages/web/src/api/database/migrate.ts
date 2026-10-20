/**
 * Versioned schema migrations (packages/web/drizzle, made by `bun run db:generate`).
 * Applied at server boot. Databases created earlier with `db:push` have the
 * tables but no migration journal; they are baselined at 0000 once, after
 * checking every table from that migration really exists, then migrated forward.
 */
import type { Client } from "@libsql/client";
import { readMigrationFiles } from "drizzle-orm/migrator";
import { migrate } from "drizzle-orm/libsql/migrator";
import type { LibSQLDatabase } from "drizzle-orm/libsql";
import { fileURLToPath } from "node:url";

export const MIGRATIONS_FOLDER = fileURLToPath(new URL("../../../drizzle", import.meta.url));
const JOURNAL = "__drizzle_migrations";

async function tableNames(client: Client): Promise<Set<string>> {
  const rs = await client.execute("SELECT name FROM sqlite_master WHERE type='table'");
  return new Set(rs.rows.map((r) => String(r.name)));
}

export async function runMigrations(db: LibSQLDatabase<any>): Promise<{ baselined: boolean }> {
  const client = db.$client as Client;
  const files = readMigrationFiles({ migrationsFolder: MIGRATIONS_FOLDER });
  if (!files.length) throw new Error(`No migrations found in ${MIGRATIONS_FOLDER}.`);
  const tables = await tableNames(client);
  let baselined = false;
  if (!tables.has(JOURNAL) && tables.size > 0) {
    const first = files[0]!;
    const created = [...first.sql.join("\n").matchAll(/CREATE TABLE `([^`]+)`/g)].map((m) => m[1]!);
    const missing = created.filter((t) => !tables.has(t));
    if (missing.length) {
      throw new Error(
        `Database has tables but no migration journal and is missing ${missing.join(", ")}; ` +
          "refusing to guess. Back it up and start from an empty database, or repair it by hand.",
      );
    }
    await client.execute(
      `CREATE TABLE IF NOT EXISTS ${JOURNAL} (id INTEGER PRIMARY KEY AUTOINCREMENT, hash text NOT NULL, created_at numeric)`,
    );
    await client.execute({
      sql: `INSERT INTO ${JOURNAL} (hash, created_at) VALUES (?, ?)`,
      args: [first.hash, first.folderMillis],
    });
    baselined = true;
  }
  await migrate(db, { migrationsFolder: MIGRATIONS_FOLDER });
  return { baselined };
}
