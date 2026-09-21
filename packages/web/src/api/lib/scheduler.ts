import { eq, inArray } from "drizzle-orm";
import { db } from "../database";
import { nodes as nodesTable, projects as projectsTable } from "../database/schema";

/**
 * Picks the online node with the least assigned work. The control panel never
 * serves traffic itself, so with no node online there is nowhere to place work
 * and callers must report no_capacity.
 */
export async function pickNode(preferred?: string | null): Promise<string | null> {
  const online = await db.select().from(nodesTable).where(eq(nodesTable.status, "online"));
  if (online.length === 0) return null;
  if (preferred && online.some((node) => node.id === preferred)) return preferred;
  const assigned = await db
    .select({ id: projectsTable.id, nodeId: projectsTable.nodeId })
    .from(projectsTable)
    .where(inArray(projectsTable.status, ["running", "deploying"]));
  const counts = new Map(online.map((node) => [node.id, 0]));
  for (const project of assigned) {
    if (project.nodeId && counts.has(project.nodeId)) {
      counts.set(project.nodeId, (counts.get(project.nodeId) ?? 0) + 1);
    }
  }
  return [...counts.entries()].sort((a, b) => a[1] - b[1])[0]?.[0] ?? null;
}
