import { customType } from "drizzle-orm/sqlite-core";
import { open, openDet, seal, SealError, sealDet } from "../lib/colseal";

/**
 * drizzle column types that store Red-SecretsXAI sealed values (see lib/colseal.ts).
 *   sealedText(table, column)   randomised ChaCha20-Poly1305, AD binds table and column
 *   sealedLookup(table, column) deterministic (synthetic IV), so equality WHERE clauses keep working
 * drizzle applies toDriver to bound WHERE parameters too, so `eq(user.email, x)` matches.
 * A column type only sees the value, not the row, so the AD carries table and column but not the row id.
 * An empty string passes through unsealed: SQLite DEFAULT '' never goes through toDriver and leaks nothing.
 * Any other unsealed value is refused on read (fail closed); startup sealing (seal-at-rest.ts) seals
 * plaintext left by earlier builds before the first query.
 */
/** Every sealed column, for startup sealing and the at-rest tests. kind: s = randomised, d = deterministic. */
export const SEALED_COLUMNS: { table: string; column: string; kind: "s" | "d" }[] = [];

function readSealed(ctx: string, v: unknown, det: boolean): string {
  const s = String(v);
  if (s === "") return "";
  if (det ? !s.startsWith("d1.") : !s.startsWith("s1.")) throw new SealError(`unsealed value in ${ctx}`);
  return det ? openDet(ctx, s) : open(ctx, "", s);
}

export const sealedText = (table: string, column: string) => {
  const ctx = `${table}.${column}`;
  SEALED_COLUMNS.push({ table, column, kind: "s" });
  return customType<{ data: string; driverData: string }>({
    dataType: () => "text",
    toDriver: (v: string) => (v === "" ? "" : seal(ctx, "", v)),
    fromDriver: (v: unknown) => readSealed(ctx, v, false),
  })(column);
};

export const sealedLookup = (table: string, column: string) => {
  const ctx = `${table}.${column}`;
  SEALED_COLUMNS.push({ table, column, kind: "d" });
  return customType<{ data: string; driverData: string }>({
    dataType: () => "text",
    toDriver: (v: string) => (v === "" ? "" : sealDet(ctx, v)),
    fromDriver: (v: unknown) => readSealed(ctx, v, true),
  })(column);
};

