import { desc, gte } from "drizzle-orm";
import { owner } from "../middleware/auth";
import { db } from "../database";
import { activity, appBuilds, credentials, domains, nodes, projects, usageEvents } from "../database/schema";
import { reconcileHealth } from "../lib/health";
import { nowSeconds } from "../lib/ids";

export const overview = owner.handler(async () => {
  await reconcileHealth();

  const [nodeRows, projectRows, domainRows, buildRows, credentialRows] = await Promise.all([
    db.select().from(nodes),
    db.select().from(projects),
    db.select().from(domains),
    db.select().from(appBuilds).orderBy(desc(appBuilds.createdAt)).limit(5),
    db.select().from(credentials),
  ]);

  const since = nowSeconds() - 86400;
  const usage = await db.select().from(usageEvents).where(gte(usageEvents.bucketAt, since));
  const feed = await db.select().from(activity).orderBy(desc(activity.createdAt)).limit(12);

  const onlineNodes = nodeRows.filter((node) => node.status === "online");

  return {
    fleet: {
      total: nodeRows.length,
      online: onlineNodes.length,
      cores: onlineNodes.reduce((sum, node) => sum + (node.cpuCores ?? 0), 0),
      memoryMb: onlineNodes.reduce((sum, node) => sum + (node.memoryMb ?? 0), 0),
      /** Nothing can be hosted with an empty fleet — the panel says so loudly. */
      capacityAvailable: onlineNodes.length > 0,
    },
    projects: {
      total: projectRows.length,
      running: projectRows.filter((project) => project.status === "running").length,
      deploying: projectRows.filter((project) => project.status === "deploying").length,
      failed: projectRows.filter((project) => project.status === "failed").length,
      down: projectRows.filter((project) => project.status === "no_capacity").length,
      drafts: projectRows.filter((project) => project.status === "draft").length,
    },
    domains: {
      total: domainRows.length,
      live: domainRows.filter((domain) => domain.status === "live").length,
      pending: domainRows.filter((domain) => domain.status !== "live" && domain.status !== "error")
        .length,
      error: domainRows.filter((domain) => domain.status === "error").length,
    },
    usage24h: {
      requests: usage.reduce((sum, row) => sum + row.requests, 0),
      visitors: usage.reduce((sum, row) => sum + row.uniqueVisitors, 0),
      errors: usage.reduce((sum, row) => sum + row.errors, 0),
      bytesOut: usage.reduce((sum, row) => sum + row.bytesOut, 0),
    },
    builds: buildRows,
    credentials: credentialRows.map((row) => ({
      id: row.id,
      verifyStatus: row.verifyStatus,
    })),
    feed,
  };
});
