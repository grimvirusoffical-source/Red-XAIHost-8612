// InfectedNation column sealing (P13), built on shared/colseal-core.ts.
// Master secret: INFECTEDNATION_DATA_KEY, else CREDENTIAL_SECRET (at least 16 characters; no fallback,
// the server refuses to start without one). Randomised columns are bound to their row id in the AD, so a
// value copied to another row or column does not open.
import type { Database } from "bun:sqlite";
import { isSealed, makeColseal, SealError } from "../../shared/colseal-core";

export class DataKeyMissing extends Error {
  constructor() { super("InfectedNation data key is not configured (set INFECTEDNATION_DATA_KEY or CREDENTIAL_SECRET, 16+ characters)"); }
}
export function natDataSecret(env: Record<string, string | undefined> = process.env): string {
  const s = (env.INFECTEDNATION_DATA_KEY || env.CREDENTIAL_SECRET || "").trim();
  if (s.length < 16) throw new DataKeyMissing();
  return s;
}

export const ns = makeColseal("infectednation", () => natDataSecret());
export { isSealed, SealError };

/** Randomised, row-bound columns ("s1."), keyed by table; each table's rows are identified by `id`. */
export const SEALED: Record<string, string[]> = {
  accounts: ["username", "dob", "first_name", "middle_initial", "last_name", "company_name", "phone", "stage_name", "password_hash"],
  sessions: ["ip", "user_agent"],
  security_events: ["ip", "user_agent"],
  oauth_identities: ["email"],
};
/** Deterministic lookup columns ("d1."); the stored plaintext is normalised first (see below). */
export const LOOKUP: Record<string, string[]> = {
  accounts: ["email"],
  oauth_identities: ["provider_subject"],
  entitlements: ["external_id"],
};
/** Blind-index-only columns ("b1."), compared but never read back. */
export const INDEX: Record<string, string[]> = {
  accounts: ["username_idx"],
  sessions: ["token_hash"],
  api_keys: ["key_hash"],
  connect_requests: ["secret_hash"],
  email_verifications: ["token_hash"],
  rate_buckets: ["key"],
};

export const sealCol = (table: string, column: string, rowId: string, v: string) =>
  v === "" ? "" : ns.seal(`${table}.${column}`, rowId, v);
export const lookup = (table: string, column: string, v: string) => (v === "" ? "" : ns.sealDet(`${table}.${column}`, v));
export const index = (table: string, column: string, v: string) => ns.blindIndex(`${table}.${column}`, v);

/** Email lookup value: the address is stored lower-cased (normalizeEmail) and sealed deterministically. */
export const emailLookup = (email: string) => lookup("accounts", "email", email.trim().toLowerCase());
/** Username uniqueness/lookup: keyed index of the lower-cased name (the name itself is sealed per row). */
export const usernameIndex = (username: string) => index("accounts", "username_idx", username.trim().toLowerCase());
/** Stored username_idx: NULL while the username is still empty (OAuth signup before profile), so the unique index allows many. */
export const usernameIdxOrNull = (username: string | null | undefined) => (String(username ?? "").trim() ? usernameIndex(String(username)) : null);

/** Opens the sealed columns of one result row from `table` (rows must include `id` when any are selected). */
export function openRow(table: string, row: any): any {
  if (!row || typeof row !== "object") return row;
  for (const col of SEALED[table] ?? []) {
    if (!(col in row) || row[col] == null || row[col] === "") continue;
    const v = String(row[col]);
    if (!v.startsWith("s1.")) throw new SealError(`unsealed value in ${table}.${col}`);
    if (row.id == null) throw new SealError(`${table}.${col} selected without the row id`);
    row[col] = ns.open(`${table}.${col}`, String(row.id), v);
  }
  for (const col of LOOKUP[table] ?? []) {
    if (!(col in row) || row[col] == null || row[col] === "") continue;
    const v = String(row[col]);
    if (!v.startsWith("d1.")) throw new SealError(`unsealed value in ${table}.${col}`);
    row[col] = ns.openDet(`${table}.${col}`, v);
  }
  return row;
}

const TABLES = new Set([...Object.keys(SEALED), ...Object.keys(LOOKUP)]);
function tableOf(sql: string): string | null {
  const m = /\bFROM\s+([a-z_]+)/i.exec(sql);
  return m && TABLES.has(m[1]!) ? m[1]! : null;
}

/** Wraps db.prepare so get()/all() results from sealed tables come back opened. Writes seal explicitly. */
export function openOnRead(db: Database): Database {
  const prepare = db.prepare.bind(db);
  (db as any).prepare = (sql: string, ...rest: any[]) => {
    const st: any = prepare(sql, ...rest);
    const table = tableOf(sql);
    if (!table || /^\s*(INSERT|UPDATE|DELETE)\b/i.test(sql)) return st;
    const get = st.get.bind(st), all = st.all.bind(st);
    st.get = (...a: any[]) => openRow(table, get(...a));
    st.all = (...a: any[]) => (all(...a) as any[]).map((r) => openRow(table, r));
    return st;
  };
  return db;
}

/**
 * Startup: seal plaintext left by earlier builds in place (plaintext only). Hash and index columns from
 * earlier builds were unkeyed SHA-256 and cannot be converted: short-lived rows (sessions, connect
 * requests, email verifications, rate buckets) are removed and recreated on next use; API keys keep
 * working through a one-time lookup by the old hash that rewrites them (server.ts apiKeyRow).
 * Afterwards the WAL is checkpointed and the file vacuumed so old plaintext pages are gone.
 */
export function sealAtRest(db: Database): { sealed: number; removed: number } {
  db.exec("PRAGMA secure_delete = ON");
  let sealed = 0, removed = 0;
  const has = (t: string) => !!db.query("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?").get(t);
  for (const [table, cols] of Object.entries(SEALED)) {
    if (!has(table)) continue;
    for (const col of cols) {
      const rows = db.query(`SELECT id, "${col}" AS v FROM ${table} WHERE "${col}" IS NOT NULL AND "${col}" <> '' AND "${col}" NOT LIKE 's1.%'`).all() as any[];
      for (const r of rows) {
        db.query(`UPDATE ${table} SET "${col}"=? WHERE id=?`).run(ns.seal(`${table}.${col}`, String(r.id), String(r.v)), r.id);
        sealed++;
      }
    }
  }
  for (const [table, cols] of Object.entries(LOOKUP)) {
    if (!has(table)) continue;
    for (const col of cols) {
      const rows = db.query(`SELECT rowid AS rid, "${col}" AS v FROM ${table} WHERE "${col}" IS NOT NULL AND "${col}" <> '' AND "${col}" NOT LIKE 'd1.%'`).all() as any[];
      for (const r of rows) {
        const plain = table === "accounts" && col === "email" ? String(r.v).trim().toLowerCase() : String(r.v);
        db.query(`UPDATE ${table} SET "${col}"=? WHERE rowid=?`).run(ns.sealDet(`${table}.${col}`, plain), r.rid);
        sealed++;
      }
    }
  }
  // username_idx is derived from the (now sealed) username.
  if (has("accounts")) {
    const rows = db.query("SELECT id, username, username_idx FROM accounts WHERE username_idx IS NULL OR username_idx NOT LIKE 'b1.%'").all() as any[];
    for (const r of rows) {
      const name = r.username ? ns.open("accounts.username", String(r.id), String(r.username)) : "";
      const idx = usernameIdxOrNull(name);
      if (idx === null && r.username_idx == null) continue;
      db.query("UPDATE accounts SET username_idx=? WHERE id=?").run(idx, r.id);
      sealed++;
    }
  }
  for (const [table, col] of [["sessions", "token_hash"], ["connect_requests", "secret_hash"], ["email_verifications", "token_hash"], ["rate_buckets", "key"]] as const) {
    if (!has(table)) continue;
    removed += db.query(`DELETE FROM ${table} WHERE "${col}" NOT LIKE 'b1.%'`).run().changes;
  }
  if (sealed + removed > 0) {
    db.exec("PRAGMA wal_checkpoint(TRUNCATE)");
    db.exec("VACUUM");
  }
  return { sealed, removed };
}
