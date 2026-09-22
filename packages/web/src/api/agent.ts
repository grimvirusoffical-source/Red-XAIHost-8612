import type { Hono } from "hono";
import { and, eq, inArray } from "drizzle-orm";
import { GetObjectCommand } from "@aws-sdk/client-s3";
import { getSignedUrl } from "@aws-sdk/s3-request-presigner";
import { db } from "./database";
import { deployments, domains, nodes, projects, usageEvents } from "./database/schema";
import { hashToken } from "./lib/crypto";
import { newId, nowSeconds } from "./lib/ids";
import { logActivity } from "./lib/activity";
import { BUCKET, s3 } from "./lib/s3";
import { ensureTunnel } from "./lib/cloudflare";
import { AGENT_SOURCE, INSTALL_PS1, INSTALL_SH, STATIC_SERVER_SOURCE } from "./agent-assets";

/**
 * Plain-HTTP surface the node agent talks to. Agents authenticate with the
 * per-node bearer token issued when the node was created; they never use an
 * owner session, and they can only ever see their own jobs.
 */
async function authenticateNode(authorization: string | undefined) {
  const token = authorization?.replace(/^Bearer\s+/i, "").trim();
  if (!token) return null;
  const [node] = await db.select().from(nodes).where(eq(nodes.tokenHash, hashToken(token)));
  if (!node || node.status === "disabled") return null;
  return node;
}

async function buildJobPayload(deployment: typeof deployments.$inferSelect) {
  const [project] = await db.select().from(projects).where(eq(projects.id, deployment.projectId));
  if (!project) return null;

  let bundleUrl: string | null = null;
  if (project.bundleKey) {
    bundleUrl = await getSignedUrl(
      s3,
      new GetObjectCommand({ Bucket: BUCKET, Key: project.bundleKey }),
      { expiresIn: 3600 },
    );
  }

  // If the project has a domain with a tunnel, hand the connector token over so
  // the node can run cloudflared for it.
  const projectDomains = await db.select().from(domains).where(eq(domains.projectId, project.id));
  const tunnels: { hostname: string; tunnelName: string; connectorToken: string | null }[] = [];
  for (const domain of projectDomains) {
    if (!domain.tunnelName) continue;
    const tunnel = await ensureTunnel(domain.tunnelName);
    tunnels.push({
      hostname: domain.hostname,
      tunnelName: domain.tunnelName,
      connectorToken: tunnel.connectorToken ?? null,
    });
  }

  return {
    deploymentId: deployment.id,
    action: deployment.action,
    project: {
      id: project.id,
      slug: project.slug,
      name: project.name,
      runtime: project.runtime,
      sourceKind: project.sourceKind,
      gitUrl: project.gitUrl,
      gitBranch: project.gitBranch,
      bundleUrl,
      bundleName: project.bundleName,
      port: project.port ?? 8080,
      dockerfile: project.dockerfile,
      installCommand: project.installCommand,
      buildCommand: project.buildCommand,
      startCommand: project.startCommand,
      envVars: project.envVars ? (JSON.parse(project.envVars) as Record<string, string>) : {},
    },
    tunnels,
  };
}

export function registerAgentRoutes(app: Hono) {
  /** First contact: the agent reports what the machine is and can do. */
  app.post("/api/agent/hello", async (c) => {
    const node = await authenticateNode(c.req.header("authorization"));
    if (!node) return c.json({ error: "unauthorized" }, 401);
    const body = (await c.req.json().catch(() => ({}))) as Record<string, unknown>;

    await db
      .update(nodes)
      .set({
        os: String(body.os ?? ""),
        arch: String(body.arch ?? ""),
        agentVersion: String(body.agentVersion ?? ""),
        publicIp: String(body.publicIp ?? c.req.header("cf-connecting-ip") ?? ""),
        cpuCores: Number(body.cpuCores ?? 0) || null,
        memoryMb: Number(body.memoryMb ?? 0) || null,
        diskGb: Number(body.diskGb ?? 0) || null,
        dockerAvailable: Boolean(body.dockerAvailable),
        cloudflaredAvailable: Boolean(body.cloudflaredAvailable),
        status: "online",
        lastSeenAt: nowSeconds(),
      })
      .where(eq(nodes.id, node.id));

    await logActivity({
      scope: "node",
      refId: node.id,
      level: "success",
      message: `Node "${node.name}" came online (${body.os ?? "unknown os"}, docker ${
        body.dockerAvailable ? "ready" : "missing"
      }).`,
    });

    return c.json({ ok: true, nodeId: node.id, heartbeatSeconds: 20 }, 200);
  });

  /** Heartbeat doubles as the job queue poll. */
  app.post("/api/agent/heartbeat", async (c) => {
    const node = await authenticateNode(c.req.header("authorization"));
    if (!node) return c.json({ error: "unauthorized" }, 401);
    const body = (await c.req.json().catch(() => ({}))) as Record<string, unknown>;

    await db
      .update(nodes)
      .set({
        status: "online",
        lastSeenAt: nowSeconds(),
        cpuPercent: Number(body.cpuPercent ?? 0) || null,
        memPercent: Number(body.memPercent ?? 0) || null,
        dockerAvailable: body.dockerAvailable === undefined ? node.dockerAvailable : Boolean(body.dockerAvailable),
        cloudflaredAvailable:
          body.cloudflaredAvailable === undefined
            ? node.cloudflaredAvailable
            : Boolean(body.cloudflaredAvailable),
      })
      .where(eq(nodes.id, node.id));

    const queued = await db
      .select()
      .from(deployments)
      .where(and(eq(deployments.nodeId, node.id), eq(deployments.status, "queued")))
      .limit(3);

    const jobs = [];
    for (const deployment of queued) {
      const payload = await buildJobPayload(deployment);
      if (!payload) continue;
      await db
        .update(deployments)
        .set({ status: "claimed", startedAt: nowSeconds() })
        .where(eq(deployments.id, deployment.id));
      jobs.push(payload);
    }

    return c.json({ ok: true, jobs, heartbeatSeconds: 20 }, 200);
  });

  /** Streaming-ish log + status updates from a running job. */
  app.post("/api/agent/job", async (c) => {
    const node = await authenticateNode(c.req.header("authorization"));
    if (!node) return c.json({ error: "unauthorized" }, 401);
    const body = (await c.req.json().catch(() => ({}))) as {
      deploymentId?: string;
      status?: string;
      logChunk?: string;
      error?: string;
    };
    if (!body.deploymentId) return c.json({ error: "deploymentId required" }, 400);

    const [deployment] = await db
      .select()
      .from(deployments)
      .where(and(eq(deployments.id, body.deploymentId), eq(deployments.nodeId, node.id)));
    if (!deployment) return c.json({ error: "not found" }, 404);

    const status = body.status ?? deployment.status;
    const logs = body.logChunk
      ? `${deployment.logs}${body.logChunk}`.slice(-200_000)
      : deployment.logs;
    const finished = ["succeeded", "failed", "cancelled"].includes(status);

    await db
      .update(deployments)
      .set({
        status,
        logs,
        error: body.error ?? deployment.error,
        finishedAt: finished ? nowSeconds() : null,
      })
      .where(eq(deployments.id, deployment.id));

    if (finished) {
      const projectStatus =
        status === "succeeded"
          ? deployment.action === "stop" || deployment.action === "remove"
            ? "stopped"
            : "running"
          : "failed";
      await db
        .update(projects)
        .set({
          status: projectStatus,
          lastDeployedAt: status === "succeeded" ? nowSeconds() : undefined,
          updatedAt: nowSeconds(),
        })
        .where(eq(projects.id, deployment.projectId));
      await logActivity({
        scope: "deployment",
        refId: deployment.id,
        level: status === "succeeded" ? "success" : "error",
        message: `${deployment.action} ${status} on node "${node.name}".`,
      });
    }

    return c.json({ ok: true }, 200);
  });

  /** Hourly traffic rollups from the node's reverse proxy. */
  app.post("/api/agent/usage", async (c) => {
    const node = await authenticateNode(c.req.header("authorization"));
    if (!node) return c.json({ error: "unauthorized" }, 401);
    const body = (await c.req.json().catch(() => ({}))) as {
      buckets?: {
        projectId: string;
        bucketAt: number;
        requests?: number;
        uniqueVisitors?: number;
        bytesOut?: number;
        errors?: number;
        avgMs?: number;
        topPath?: string;
        topCountry?: string;
      }[];
    };
    const buckets = body.buckets ?? [];
    if (buckets.length === 0) return c.json({ ok: true, written: 0 }, 200);

    const projectIds = [...new Set(buckets.map((bucket) => bucket.projectId))];
    const known = await db
      .select({ id: projects.id })
      .from(projects)
      .where(inArray(projects.id, projectIds));
    const knownIds = new Set(known.map((row) => row.id));

    let written = 0;
    for (const bucket of buckets) {
      if (!knownIds.has(bucket.projectId)) continue;
      await db.insert(usageEvents).values({
        id: newId("use"),
        projectId: bucket.projectId,
        nodeId: node.id,
        bucketAt: Math.floor(bucket.bucketAt / 3600) * 3600,
        requests: bucket.requests ?? 0,
        uniqueVisitors: bucket.uniqueVisitors ?? 0,
        bytesOut: bucket.bytesOut ?? 0,
        errors: bucket.errors ?? 0,
        avgMs: bucket.avgMs ?? 0,
        topPath: bucket.topPath ?? null,
        topCountry: bucket.topCountry ?? null,
      });
      written += 1;
    }
    return c.json({ ok: true, written }, 200);
  });

  /** The agent program itself, plus one-line installers that fetch it. */
  app.get("/api/agent/agent.mjs", (c) =>
    c.text(AGENT_SOURCE, 200, { "Content-Type": "text/javascript; charset=utf-8" }),
  );
  app.get("/api/agent/static-server.mjs", (c) =>
    c.text(STATIC_SERVER_SOURCE, 200, { "Content-Type": "text/javascript; charset=utf-8" }),
  );
  app.get("/api/agent/install.sh", (c) =>
    c.text(INSTALL_SH, 200, { "Content-Type": "text/x-shellscript; charset=utf-8" }),
  );
  app.get("/api/agent/install.ps1", (c) =>
    c.text(INSTALL_PS1, 200, { "Content-Type": "text/plain; charset=utf-8" }),
  );
}
