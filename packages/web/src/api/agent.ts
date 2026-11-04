import type { Hono } from "hono";
import { and, eq, inArray, ne, notInArray } from "drizzle-orm";
import { db } from "./database";
import { deployments, domains, nodes, projectReplicas, projects, usageEvents } from "./database/schema";
import { hashToken } from "./lib/crypto";
import { newId, nowSeconds } from "./lib/ids";
import { logActivity } from "./lib/activity";
import { createBundleDownload } from "./lib/bundle-storage";
import { ensureTunnel } from "./lib/cloudflare";
import { AGENT_SOURCE, INSTALL_PS1, INSTALL_SH, STATIC_SERVER_SOURCE } from "./agent-assets";
import { currentTenantId, withTenantContext } from "./lib/tenant-context";
import { REMOVING_PROJECT_STATUS, finalizeProjectRemoval, requeueNodeRemovals, safeRemovalSlug, upsertMutableReplica } from './lib/project-removal';

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

async function upsertReplica(
  projectId: string,
  nodeId: string,
  patch: {
    status: string;
    lastDeploymentId?: string | null;
    lastError?: string | null;
    lastDeployedAt?: number | null;
  },
) {
  await upsertMutableReplica(projectId,nodeId,patch);
}

async function refreshProjectStatus(projectId: string) {
  const tenantId = currentTenantId();
  const replicas = await db
    .select()
    .from(projectReplicas)
    .where(and(eq(projectReplicas.tenantId, tenantId), eq(projectReplicas.projectId, projectId)));
  const running = replicas.some((replica) => replica.status === "running");
  const deploying = replicas.some((replica) => ["pending", "deploying"].includes(replica.status));
  const allStopped = replicas.length > 0 && replicas.every((replica) => replica.status === "stopped");
  const nextStatus = running ? "running" : deploying ? "deploying" : allStopped ? "stopped" : "failed";
  await db
    .update(projects)
    .set({ status: nextStatus, updatedAt: nowSeconds() })
    .where(and(eq(projects.tenantId, tenantId), eq(projects.id, projectId),ne(projects.status,REMOVING_PROJECT_STATUS)));
}

async function queueFleetReplicas(nodeId: string, dockerAvailable: boolean) {
  const tenantId = currentTenantId();
  const wanted = await db
    .select()
    .from(projects)
    .where(
      and(
        eq(projects.tenantId, tenantId),
        eq(projects.autoDeploy, true),
        eq(projects.replicateEverywhere, true),
        inArray(projects.status, ["running", "deploying", "no_capacity"]),
      ),
    );

  for (const project of wanted) {
    const needsDocker =
      project.runtime === "docker" ||
      project.runtime === "database" ||
      Boolean(project.dockerfile);
    if (needsDocker && !dockerAvailable) continue;

    const [replica] = await db
      .select()
      .from(projectReplicas)
      .where(and(eq(projectReplicas.tenantId, tenantId), eq(projectReplicas.projectId, project.id), eq(projectReplicas.nodeId, nodeId)))
      .limit(1);
    if (replica && ["running", "deploying", "pending"].includes(replica.status)) continue;

    const active = await db
      .select({ id: deployments.id })
      .from(deployments)
      .where(
        and(
          eq(deployments.tenantId, tenantId),
          eq(deployments.projectId, project.id),
          eq(deployments.nodeId, nodeId),
          inArray(deployments.status, ["queued", "claimed", "building"]),
        ),
      )
      .limit(1);
    if (active.length) continue;

    const deploymentId = newId("dep");
    await db.insert(deployments).values({
      id: deploymentId,
      tenantId,
      projectId: project.id,
      nodeId,
      action: "deploy",
      status: "queued",
      trigger: "fleet_join",
    });
    await upsertReplica(project.id, nodeId, {
      status: "deploying",
      lastDeploymentId: deploymentId,
      lastError: null,
    });
    await logActivity({
      scope: "deployment",
      refId: deploymentId,
      level: "info",
      message: `Fleet replica queued for "${project.name}" on newly available node ${nodeId}.`,
    });
  }
}

async function buildJobPayload(deployment: typeof deployments.$inferSelect) {
  const tenantId = currentTenantId();
  if (deployment.tenantId !== tenantId) return null;
  const [project] = await db.select().from(projects).where(and(eq(projects.tenantId, tenantId), eq(projects.id, deployment.projectId)));
  if (!project) return null;
  if(deployment.action==='remove') {
    if(!safeRemovalSlug(project.slug))return null;
    if(deployment.trigger==='delete'&&project.status!==REMOVING_PROJECT_STATUS)return null;
    // Teardown needs stable identifiers, not source files, environment secrets,
    // signed bundle URLs, or provider connector tokens.
    return {deploymentId:deployment.id,action:deployment.action,project:{
      id:project.id,slug:project.slug,name:project.name,
      runtime:project.dockerfile?'docker':project.runtime,sourceKind:'upload',
      gitUrl:null,gitBranch:null,bundleUrl:null,bundleName:null,port:project.port??8080,
      dockerfile:null,installCommand:null,buildCommand:null,startCommand:null,envVars:{},
    },tunnels:[]};
  }
  if(project.status===REMOVING_PROJECT_STATUS)return null;

  let bundleUrl: string | null = null;
  if (project.bundleKey) {
    bundleUrl = await createBundleDownload(project.bundleKey);
  }

  // If the project has a domain with a tunnel, hand the connector token over so
  // the node can run cloudflared for it.
  const projectDomains = await db.select().from(domains).where(and(eq(domains.tenantId, tenantId), eq(domains.projectId, project.id)));
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
    return withTenantContext({ tenantId: node.tenantId, isOwner: node.tenantId === "owner", userId: "agent:"+node.id }, async () => {
    const body = (await c.req.json().catch(() => ({}))) as Record<string, unknown>;

    await requeueNodeRemovals(node.id,{restart:true});

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
      .where(and(eq(nodes.tenantId, node.tenantId), eq(nodes.id, node.id)));

    await logActivity({
      scope: "node",
      refId: node.id,
      level: "success",
      message: `Node "${node.name}" came online (${body.os ?? "unknown os"}, docker ${
        body.dockerAvailable ? "ready" : "missing"
      }).`,
    });

    await queueFleetReplicas(node.id, Boolean(body.dockerAvailable));

    return c.json({ ok: true, nodeId: node.id, heartbeatSeconds: 20 }, 200);
    });
  });

  /** Heartbeat doubles as the job queue poll. */
  app.post("/api/agent/heartbeat", async (c) => {
    const node = await authenticateNode(c.req.header("authorization"));
    if (!node) return c.json({ error: "unauthorized" }, 401);
    return withTenantContext({ tenantId: node.tenantId, isOwner: node.tenantId === "owner", userId: "agent:"+node.id }, async () => {
    const body = (await c.req.json().catch(() => ({}))) as Record<string, unknown>;

    const offline=node.lastSeenAt===null||node.lastSeenAt<nowSeconds()-90;
    await requeueNodeRemovals(node.id,{restart:offline,busy:body.busy===true});

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
      .where(and(eq(nodes.tenantId, node.tenantId), eq(nodes.id, node.id)));

    if(body.busy===true)return c.json({ok:true,jobs:[],heartbeatSeconds:20},200);

    const queued = await db
      .select()
      .from(deployments)
      .where(and(eq(deployments.tenantId, node.tenantId), eq(deployments.nodeId, node.id), eq(deployments.status, "queued")))
      .limit(3);

    const jobs = [];
    for (const deployment of queued) {
      const payload = await buildJobPayload(deployment);
      if (!payload) {
        await db.update(deployments).set({status:deployment.action==='remove'?'failed':'cancelled',error:'Project is unavailable for this job.',finishedAt:nowSeconds()})
          .where(and(eq(deployments.tenantId,node.tenantId),eq(deployments.id,deployment.id),eq(deployments.nodeId,node.id),eq(deployments.status,'queued')));
        continue;
      }
      const claimed=await db
        .update(deployments)
        .set({ status: "claimed", startedAt: nowSeconds() })
        .where(and(eq(deployments.tenantId, node.tenantId), eq(deployments.id, deployment.id),eq(deployments.nodeId,node.id),eq(deployments.status,'queued'))).returning({id:deployments.id});
      if(!claimed.length)continue;
      if(deployment.action!=='remove')await upsertReplica(deployment.projectId, node.id, {
        status: "deploying",
        lastDeploymentId: deployment.id,
        lastError: null,
      });
      jobs.push(payload);
    }

    return c.json({ ok: true, jobs, heartbeatSeconds: 20 }, 200);
    });
  });

  /** Streaming-ish log + status updates from a running job. */
  app.post("/api/agent/job", async (c) => {
    const node = await authenticateNode(c.req.header("authorization"));
    if (!node) return c.json({ error: "unauthorized" }, 401);
    return withTenantContext({ tenantId: node.tenantId, isOwner: node.tenantId === "owner", userId: "agent:"+node.id }, async () => {
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
      .where(and(eq(deployments.tenantId, node.tenantId), eq(deployments.id, body.deploymentId), eq(deployments.nodeId, node.id)));
    if (!deployment) return c.json({ error: "not found" }, 404);
    if(deployment.action==='remove'&&body.status!==undefined&&
      (typeof body.status!=='string'||!['queued','claimed','building','running','succeeded','failed','cancelled'].includes(body.status)))return c.json({error:'invalid status'},400);

    const status = body.status ?? deployment.status;
    const logs = body.logChunk
      ? `${deployment.logs}${body.logChunk}`.slice(-200_000)
      : deployment.logs;
    const finished = ["succeeded", "failed", "cancelled"].includes(status);

    const updated=await db
      .update(deployments)
      .set({
        status,
        logs,
        error: body.error ?? deployment.error,
        finishedAt: finished ? nowSeconds() : null,
      })
      .where(and(eq(deployments.tenantId, node.tenantId), eq(deployments.id, deployment.id),eq(deployments.nodeId,node.id),notInArray(deployments.status,['succeeded','failed','cancelled']))).returning({id:deployments.id});
    if(!updated.length) {
      // A success acknowledgement may have committed before a temporary purge
      // failure. Retrying it can finish the same owned removal without changing
      // any terminal job or recreating its project/replica.
      if(deployment.action==='remove'&&deployment.trigger==='delete') {
        const [current]=await db.select({status:deployments.status}).from(deployments)
          .where(and(eq(deployments.tenantId,node.tenantId),eq(deployments.id,deployment.id),eq(deployments.nodeId,node.id)));
        if(current?.status==='succeeded')await finalizeProjectRemoval(deployment.projectId);
      }
      return c.json({ok:true},200);
    }

    if (finished) {
      if(deployment.action==='remove'&&deployment.trigger==='delete') {
        await db.update(projectReplicas).set({status:status==='succeeded'?'stopped':'failed',lastDeploymentId:deployment.id,lastError:status==='succeeded'?null:'Workload removal failed.',updatedAt:nowSeconds()})
          .where(and(eq(projectReplicas.tenantId,node.tenantId),eq(projectReplicas.projectId,deployment.projectId),eq(projectReplicas.nodeId,node.id)));
        if(status==='succeeded')await finalizeProjectRemoval(deployment.projectId);
        await logActivity({scope:'deployment',refId:deployment.id,level:status==='succeeded'?'success':'error',message:status==='succeeded'?`Workload removal confirmed by node "${node.name}".`:`Workload removal failed on node "${node.name}"; project removal remains pending.`});
        return c.json({ok:true},200);
      }
      const replicaStatus =
        status === "succeeded"
          ? deployment.action === "stop" || deployment.action === "remove"
            ? "stopped"
            : "running"
          : "failed";

      await upsertReplica(deployment.projectId, node.id, {
        status: replicaStatus,
        lastDeploymentId: deployment.id,
        lastError: status === "succeeded" ? null : body.error ?? deployment.error ?? "Deployment failed.",
        lastDeployedAt:
          status === "succeeded" && !["stop", "remove"].includes(deployment.action)
            ? nowSeconds()
            : undefined,
      });

      if (status === "succeeded" && !["stop", "remove"].includes(deployment.action)) {
        await db
          .update(projects)
          .set({ lastDeployedAt: nowSeconds(), updatedAt: nowSeconds() })
          .where(and(eq(projects.tenantId, node.tenantId), eq(projects.id, deployment.projectId),ne(projects.status,REMOVING_PROJECT_STATUS)));
      }
      await refreshProjectStatus(deployment.projectId);

      await logActivity({
        scope: "deployment",
        refId: deployment.id,
        level: status === "succeeded" ? "success" : "error",
        message: `${deployment.action} ${status} on node "${node.name}".`,
      });
    }

    return c.json({ ok: true }, 200);
    });
  });

  /** Hourly traffic rollups from the node's reverse proxy. */
  app.post("/api/agent/usage", async (c) => {
    const node = await authenticateNode(c.req.header("authorization"));
    if (!node) return c.json({ error: "unauthorized" }, 401);
    return withTenantContext({ tenantId: node.tenantId, isOwner: node.tenantId === "owner", userId: "agent:"+node.id }, async () => {
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
      .where(and(eq(projects.tenantId, node.tenantId), inArray(projects.id, projectIds)));
    const knownIds = new Set(known.map((row) => row.id));

    let written = 0;
    for (const bucket of buckets) {
      if (!knownIds.has(bucket.projectId)) continue;
      await db.insert(usageEvents).values({
        id: newId("use"),
        tenantId: node.tenantId,
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
