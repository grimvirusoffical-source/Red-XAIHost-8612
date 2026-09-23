import { and, eq, inArray, lt } from "drizzle-orm";
import { db } from "../database";
import { nodes, projectReplicas, projects } from "../database/schema";
import { logActivity } from "./activity";
import { newId, nowSeconds } from "./ids";

/** A node that has not phoned home within this window counts as offline. */
export const HEARTBEAT_TIMEOUT_SECONDS = 90;

/**
 * Reconcile node and replica health.
 * A project remains running when at least one live replica is healthy.
 */
export async function reconcileHealth(): Promise<void> {
  const now = nowSeconds();
  const cutoff = now - HEARTBEAT_TIMEOUT_SECONDS;

  const wentOffline = await db
    .update(nodes)
    .set({ status: "offline", cpuPercent: null, memPercent: null })
    .where(and(eq(nodes.status, "online"), lt(nodes.lastSeenAt, cutoff)))
    .returning({ id: nodes.id, name: nodes.name });

  if (wentOffline.length) {
    const offlineIds = wentOffline.map((node) => node.id);
    const affected = await db
      .select()
      .from(projectReplicas)
      .where(
        and(
          inArray(projectReplicas.nodeId, offlineIds),
          inArray(projectReplicas.status, ["pending", "deploying", "running"]),
        ),
      );
    for (const replica of affected) {
      await db
        .update(projectReplicas)
        .set({ status: "offline", updatedAt: now })
        .where(eq(projectReplicas.id, replica.id));
    }
  }

  for (const node of wentOffline) {
    await logActivity({
      scope: "node",
      refId: node.id,
      level: "warn",
      message: `Node "${node.name}" stopped sending heartbeats. Replicas on other online nodes remain eligible to serve traffic.`,
    });
  }

  const liveNodeIds = new Set(
    (await db.select({ id: nodes.id }).from(nodes).where(eq(nodes.status, "online"))).map(
      (row) => row.id,
    ),
  );

  const managedProjects = await db
    .select()
    .from(projects)
    .where(inArray(projects.status, ["running", "deploying", "no_capacity"]));
  const allReplicas = await db.select().from(projectReplicas);

  for (const project of managedProjects) {
    let replicas = allReplicas.filter((replica) => replica.projectId === project.id);

    // Backwards-compatible migration for projects that were already running
    // before per-node replica state existed.
    if (replicas.length === 0 && project.nodeId && liveNodeIds.has(project.nodeId)) {
      const status = project.status === "deploying" ? "deploying" : "running";
      const id = newId("rep");
      await db.insert(projectReplicas).values({
        id,
        projectId: project.id,
        nodeId: project.nodeId,
        status,
        lastDeployedAt: project.lastDeployedAt,
        updatedAt: now,
      });
      replicas = [
        {
          id,
          projectId: project.id,
          nodeId: project.nodeId,
          status,
          lastDeploymentId: null,
          lastError: null,
          lastDeployedAt: project.lastDeployedAt,
          updatedAt: now,
        },
      ];
    }

    const liveReplicas = replicas.filter((replica) => liveNodeIds.has(replica.nodeId));
    const hasRunning = liveReplicas.some((replica) => replica.status === "running");
    const hasDeploying = liveReplicas.some((replica) =>
      ["pending", "deploying"].includes(replica.status),
    );
    const nextStatus = hasRunning ? "running" : hasDeploying ? "deploying" : "no_capacity";

    if (project.status !== nextStatus) {
      await db
        .update(projects)
        .set({ status: nextStatus, updatedAt: now })
        .where(eq(projects.id, project.id));
      if (nextStatus === "no_capacity") {
        await logActivity({
          scope: "project",
          refId: project.id,
          level: "warn",
          message: `"${project.name}" has no healthy online replica right now.`,
        });
      }
    }
  }
}
