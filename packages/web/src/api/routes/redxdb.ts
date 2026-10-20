import { ORPCError } from "@orpc/server";
import { owner } from "../middleware/auth";
import { RedXDBError, redxdb } from "../lib/redxdb";

function toOrpc(error: unknown): never {
  if (error instanceof RedXDBError) {
    const code =
      error.status === 412 ? "PRECONDITION_FAILED" : "BAD_GATEWAY";
    throw new ORPCError(code, { message: error.message, data: { upstreamStatus: error.status, code: error.code } });
  }
  throw error;
}

export const redxdbRouter = {
  /**
   * Reachability, version and the databases the key can read. The panel needs only a read-scope key.
   * Admin status is optional extra detail: with a read key it is skipped (adminNote), not an error.
   */
  status: owner.handler(async () => {
    const client = await redxdb();
    let health;
    try {
      health = await client.health();
    } catch (error) {
      return { url: client.url, reachable: false, keyConfigured: client.hasKey, error: (error as Error).message };
    }
    let admin: Record<string, unknown> | null = null;
    let adminError: string | null = null;
    let adminNote: string | null = null;
    if (client.hasKey) {
      try {
        admin = await client.adminStatus();
      } catch (error) {
        const status = (error as { status?: number }).status;
        if (status === 401 || status === 403) {
          adminNote = "Admin status needs a host-admin key. The panel only needs a read-scope key, so this is skipped.";
        } else {
          adminError = (error as Error).message;
        }
      }
    }
    return { url: client.url, reachable: true, keyConfigured: client.hasKey, health, admin, adminError, adminNote };
  }),

  /** Databases the configured key can see. */
  listDatabases: owner.handler(async () => {
    const client = await redxdb();
    try {
      const { databases } = await client.listDatabases();
      return { url: client.url, databases };
    } catch (error) {
      toOrpc(error);
    }
  }),
};
