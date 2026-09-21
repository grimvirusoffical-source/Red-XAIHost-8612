import { db } from "../database";
import { activity } from "../database/schema";
import { newId } from "./ids";

type Level = "info" | "warn" | "error" | "success";
type Scope = "node" | "project" | "domain" | "deployment" | "build" | "auth" | "system";

/** Fire-and-forget activity feed entry. Never throws into a request path. */
export async function logActivity(entry: {
  scope: Scope;
  message: string;
  level?: Level;
  refId?: string | null;
  meta?: unknown;
}): Promise<void> {
  try {
    await db.insert(activity).values({
      id: newId("act"),
      scope: entry.scope,
      message: entry.message,
      level: entry.level ?? "info",
      refId: entry.refId ?? null,
      meta: entry.meta === undefined ? null : JSON.stringify(entry.meta),
    });
  } catch {
    // activity logging must never break the operation it describes
  }
}
