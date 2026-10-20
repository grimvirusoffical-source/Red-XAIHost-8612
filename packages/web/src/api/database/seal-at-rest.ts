/**
 * P13 startup sealing. Databases written by earlier builds hold these columns in plaintext; each such
 * value is sealed in place (plaintext only: AES data is never read, see lib/crypto.ts). After sealing,
 * the WAL is checkpointed and the file vacuumed so the old plaintext pages do not stay in the file, and
 * secure_delete is on so later overwrites and deletes do not leave old values in free pages either.
 */
import type { Client } from "@libsql/client";
import { isSealed, seal, sealDet } from "../lib/colseal";
import "./schema";
import { SEALED_COLUMNS } from "./sealed-columns";

export async function sealPlaintextAtRest(client: Client): Promise<{ sealed: number }> {
  await client.execute("PRAGMA secure_delete = ON");
  const tables = new Set((await client.execute("SELECT name FROM sqlite_master WHERE type='table'")).rows.map((r) => String(r.name)));
  let sealed = 0;
  for (const { table, column, kind } of SEALED_COLUMNS) {
    if (!tables.has(table)) continue;
    const ctx = `${table}.${column}`;
    const rows = await client.execute(
      `SELECT rowid AS rid, "${column}" AS v FROM "${table}" WHERE "${column}" IS NOT NULL AND "${column}" <> ''`,
    );
    for (const r of rows.rows) {
      const v = String(r.v);
      if (isSealed(v)) continue;
      const out = kind === "d" ? sealDet(ctx, v) : seal(ctx, "", v);
      await client.execute({ sql: `UPDATE "${table}" SET "${column}" = ? WHERE rowid = ?`, args: [out, r.rid as number] });
      sealed++;
    }
  }
  if (sealed > 0) {
    await client.execute("PRAGMA wal_checkpoint(TRUNCATE)").catch(() => {});
    await client.execute("VACUUM");
  }
  return { sealed };
}
