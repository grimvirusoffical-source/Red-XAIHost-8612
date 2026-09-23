import { eq, inArray } from "drizzle-orm";
import { db } from "../database";
import { nodes as nodesTable, projectReplicas } from "../database/schema";

async function rankedOnlineNodes() {
  const online = await db.select().from(nodesTable).where(eq(nodesTable.status, "online"));
  if (online.length === 0) return [];

  const replicas = await db
    .select({ nodeId: projectReplicas.nodeId, status: projectReplicas.status })
    .from(projectReplicas)
    .where(inArray(projectReplicas.status, ["pending", "deploying", "running"]));

  const load = new Map(online.map((node) => [node.id, 0]));
  for (const replica of replicas) {
    if (load.has(replica.nodeId)) load.set(replica.nodeId, (load.get(replica.nodeId) ?? 0) + 1);
  }

  return [...online].sort((a, b) => {
    const byLoad = (load.get(a.id) ?? 0) - (load.get(b.id) ?? 0);
    if (byLoad !== 0) return byLoad;
    const aMem = a.memPercent ?? 0;
    const bMem = b.memPercent ?? 0;
    if (aMem !== bMem) return aMem - bMem;
    return a.id.localeCompare(b.id);
  });
}

/** Pick the least-loaded healthy node, keeping a preferred node first when possible. */
export async function pickNode(preferred?: string | null): Promise<string | null> {
  const ranked = await rankedOnlineNodes();
  if (ranked.length === 0) return null;
  if (preferred) {
    const match = ranked.find((node) => node.id === preferred);
    if (match) return match.id;
  }
  return ranked[0]?.id ?? null;
}

/**
 * Pick placement for a project.
 * When replicateEverywhere is true, every online node becomes a live replica.
 * Otherwise only the preferred/least-loaded node is selected.
 */
export async function pickNodes(
  preferred?: string | null,
  replicateEverywhere = true,
): Promise<string[]> {
  const ranked = await rankedOnlineNodes();
  if (ranked.length === 0) return [];

  if (!replicateEverywhere) {
    const single = preferred && ranked.find((node) => node.id === preferred);
    return [single?.id ?? ranked[0]!.id];
  }

  if (!preferred) return ranked.map((node) => node.id);
  const preferredNode = ranked.find((node) => node.id === preferred);
  if (!preferredNode) return ranked.map((node) => node.id);
  return [preferredNode.id, ...ranked.filter((node) => node.id !== preferredNode.id).map((node) => node.id)];
}
