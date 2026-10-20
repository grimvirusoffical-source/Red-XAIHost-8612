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
  /** Reachability, version and (with an admin key) the server's admin status. */
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
    if (client.hasKey) {
      try {
        admin = await client.adminStatus();
      } catch (error) {
        adminError = (error as Error).message;
      }
    }
    return { url: client.url, reachable: true, keyConfigured: client.hasKey, health, admin, adminError };
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
