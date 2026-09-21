import { and, eq, inArray, lt, ne } from "drizzle-orm";
import { db } from "../database";
import { nodes, projects } from "../database/schema";
import { logActivity } from "./activity";
import { nowSeconds } from "./ids";

/** A node that has not phoned home within this window counts as offline. */
export const HEARTBEAT_TIMEOUT_SECONDS = 90;

/**
 * The control plane never serves traffic. When the last node backing a project
 * drops off, the project is marked `no_capacity` so the dashboard reports it as
 * down instead of pretending it is live.
 */
export async function reconcileHealth(): Promise<void> {
  const cutoff = nowSeconds() - HEARTBEAT_TIMEOUT_SECONDS;

  const wentOffline = await db
    .update(nodes)
    .set({ status: "offline", cpuPercent: null, memPercent: null })
    .where(and(eq(nodes.status, "online"), lt(nodes.lastSeenAt, cutoff)))
    .returning({ id: nodes.id, name: nodes.name });

  for (const node of wentOffline) {
    await logActivity({
      scope: "node",
      refId: node.id,
      level: "warn",
      message: `Node "${node.name}" stopped sending heartbeats — workloads on it are down.`,
    });
  }

  const liveNodeIds = (
    await db.select({ id: nodes.id }).from(nodes).where(eq(nodes.status, "online"))
  ).map((row) => row.id);

  // Projects whose node is gone (or was never assigned) can't be running.
  const runningProjects = await db
    .select({ id: projects.id, nodeId: projects.nodeId, name: projects.name })
    .from(projects)
    .where(inArray(projects.status, ["running", "deploying"]));

  for (const project of runningProjects) {
    if (!project.nodeId || !liveNodeIds.includes(project.nodeId)) {
      await db
        .update(projects)
        .set({ status: "no_capacity", updatedAt: nowSeconds() })
        .where(eq(projects.id, project.id));
      await logActivity({
        scope: "project",
        refId: project.id,
        level: "warn",
        message: `"${project.name}" has no online node and is offline.`,
      });
    }
  }

  // A project marked no_capacity whose node is back gets re-queued by the owner,
  // but we surface it as stopped rather than leaving a stale error state.
  if (liveNodeIds.length > 0) {
    await db
      .update(projects)
      .set({ status: "stopped", updatedAt: nowSeconds() })
      .where(
        and(
          eq(projects.status, "no_capacity"),
          inArray(projects.nodeId, liveNodeIds),
          ne(projects.autoDeploy, true),
        ),
      );
  }
}
