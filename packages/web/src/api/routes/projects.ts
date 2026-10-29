import { z } from "zod";
import { and, desc, eq, inArray } from "drizzle-orm";
import { ORPCError } from "@orpc/server";
import { tenant } from "../middleware/auth";
import { db } from "../database";
import {
  deployments,
  domains as domainsTable,
  nodes as nodesTable,
  projectReplicas,
  projects as projectsTable,
} from "../database/schema";
import { newId, nowSeconds, slugify } from "../lib/ids";
import { logActivity } from "../lib/activity";
import { reconcileHealth } from "../lib/health";
import { createBundleDownload, createBundleUpload } from "../lib/bundle-storage";
import { planDeployment } from "../lib/ai";
import { pickNode, pickNodes } from "../lib/scheduler";
import { currentTenantId } from "../lib/tenant-context";

const runtimeEnum = z.enum(["static", "node", "bun", "python", "docker", "database", "custom"]);

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
  const tenantId = currentTenantId();
  const [existing] = await db
    .select()
    .from(projectReplicas)
    .where(and(eq(projectReplicas.tenantId, tenantId), eq(projectReplicas.projectId, projectId), eq(projectReplicas.nodeId, nodeId)))
    .limit(1);
  const record = {
    tenantId,
    projectId,
    nodeId,
    status: patch.status,
    lastDeploymentId: patch.lastDeploymentId ?? existing?.lastDeploymentId ?? null,
    lastError: patch.lastError ?? null,
    lastDeployedAt: patch.lastDeployedAt ?? existing?.lastDeployedAt ?? null,
    updatedAt: nowSeconds(),
  };
  if (existing) {
    await db.update(projectReplicas).set(record).where(and(eq(projectReplicas.tenantId, tenantId), eq(projectReplicas.id, existing.id)));
  } else {
    await db.insert(projectReplicas).values({ id: newId("rep"), ...record });
  }
}

export const projects = {
  list: tenant.handler(async () => {
    await reconcileHealth();
    const tenantId = currentTenantId();
    const rows = await db.select().from(projectsTable).where(eq(projectsTable.tenantId, tenantId)).orderBy(desc(projectsTable.updatedAt));
    const nodeRows = await db.select().from(nodesTable).where(eq(nodesTable.tenantId, tenantId));
    const domainRows = await db.select().from(domainsTable).where(eq(domainsTable.tenantId, tenantId));
    const replicaRows = await db.select().from(projectReplicas).where(eq(projectReplicas.tenantId, tenantId));
    return rows.map((project) => {
      const replicas = replicaRows.filter((replica) => replica.projectId === project.id);
      return {
        ...project,
        nodeName: nodeRows.find((node) => node.id === project.nodeId)?.name ?? null,
        replicaSummary: {
          total: replicas.length,
          running: replicas.filter((replica) => replica.status === "running").length,
          deploying: replicas.filter((replica) => ["pending", "deploying"].includes(replica.status)).length,
          failed: replicas.filter((replica) => ["failed", "offline"].includes(replica.status)).length,
        },
        domains: domainRows
          .filter((domain) => domain.projectId === project.id)
          .map((domain) => ({ hostname: domain.hostname, status: domain.status })),
      };
    });
  }),

  get: tenant.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    const tenantId = currentTenantId();
    const [project] = await db.select().from(projectsTable).where(and(eq(projectsTable.tenantId, tenantId), eq(projectsTable.id, input.id)));
    if (!project) throw new ORPCError("NOT_FOUND");
    const history = await db
      .select()
      .from(deployments)
      .where(and(eq(deployments.tenantId, tenantId), eq(deployments.projectId, project.id)))
      .orderBy(desc(deployments.createdAt))
      .limit(20);
    const projectDomains = await db.select().from(domainsTable).where(and(eq(domainsTable.tenantId, tenantId), eq(domainsTable.projectId, project.id)));
    const [node] = project.nodeId
      ? await db.select().from(nodesTable).where(and(eq(nodesTable.tenantId, tenantId), eq(nodesTable.id, project.nodeId)))
      : [];
    const replicaRows = await db
      .select()
      .from(projectReplicas)
      .where(and(eq(projectReplicas.tenantId, tenantId), eq(projectReplicas.projectId, project.id)));
    const allNodes = await db.select().from(nodesTable).where(eq(nodesTable.tenantId, tenantId));
    return {
      project,
      deployments: history,
      domains: projectDomains,
      node: node ? { ...node, tokenHash: undefined } : null,
      replicas: replicaRows.map((replica) => ({
        ...replica,
        nodeName: allNodes.find((candidate) => candidate.id === replica.nodeId)?.name ?? replica.nodeId,
        nodeStatus: allNodes.find((candidate) => candidate.id === replica.nodeId)?.status ?? "missing",
      })),
    };
  }),

  create: tenant
    .input(
      z.object({
        name: z.string().min(1).max(80),
        description: z.string().max(500).optional(),
        runtime: runtimeEnum.default("static"),
        sourceKind: z.enum(["upload", "git"]).default("upload"),
        gitUrl: z.string().url().optional(),
        gitBranch: z.string().max(80).optional(),
        bundleKey: z.string().optional(),
        bundleName: z.string().optional(),
        bundleSize: z.number().optional(),
        nodeId: z.string().optional(),
        replicateEverywhere: z.boolean().default(true),
        port: z.number().int().min(1).max(65535).optional(),
        envVars: z.string().optional(),
      }),
    )
    .handler(async ({ input }) => {
      const tenantId = currentTenantId();
      const id = newId("prj");
      if (input.nodeId) {
        const [ownedNode] = await db.select({ id: nodesTable.id }).from(nodesTable)
          .where(and(eq(nodesTable.tenantId, tenantId), eq(nodesTable.id, input.nodeId)));
        if (!ownedNode) throw new ORPCError("BAD_REQUEST", { message: "Choose a node from your own Host workspace." });
      }
      const existing = await db
        .select({ slug: projectsTable.slug, port: projectsTable.port })
        .from(projectsTable);
      const taken = new Set(existing.map((row) => row.slug));
      const usedPorts = new Set(existing.map((row) => row.port).filter((port): port is number => !!port));
      let assignedPort = input.port ?? 8100;
      while (usedPorts.has(assignedPort) && assignedPort < 65000) assignedPort += 1;
      if (usedPorts.has(assignedPort)) throw new ORPCError("BAD_REQUEST", { message: "No automatic project port is available." });
      let slug = slugify(input.name);
      let counter = 2;
      while (taken.has(slug)) slug = `${slugify(input.name)}-${counter++}`;

      await db.insert(projectsTable).values({
        id,
        tenantId,
        slug,
        name: input.name,
        description: input.description ?? null,
        runtime: input.runtime,
        sourceKind: input.sourceKind,
        gitUrl: input.gitUrl ?? null,
        gitBranch: input.gitBranch ?? "main",
        bundleKey: input.bundleKey ?? null,
        bundleName: input.bundleName ?? null,
        bundleSize: input.bundleSize ?? null,
        nodeId: input.nodeId ?? null,
        replicateEverywhere: input.replicateEverywhere,
        port: assignedPort,
        envVars: input.envVars ?? null,
        status: "draft",
      });
      await logActivity({
        scope: "project",
        refId: id,
        level: "success",
        message: `Project "${input.name}" created (${input.runtime}).`,
      });
      return { id, slug };
    }),

  update: tenant
    .input(
      z.object({
        id: z.string(),
        name: z.string().min(1).max(80).optional(),
        description: z.string().max(500).nullable().optional(),
        runtime: runtimeEnum.optional(),
        nodeId: z.string().nullable().optional(),
        replicateEverywhere: z.boolean().optional(),
        port: z.number().int().min(1).max(65535).nullable().optional(),
        installCommand: z.string().nullable().optional(),
        buildCommand: z.string().nullable().optional(),
        startCommand: z.string().nullable().optional(),
        dockerfile: z.string().nullable().optional(),
        envVars: z.string().nullable().optional(),
        autoDeploy: z.boolean().optional(),
        gitUrl: z.string().nullable().optional(),
        gitBranch: z.string().nullable().optional(),
        bundleKey: z.string().nullable().optional(),
        bundleName: z.string().nullable().optional(),
        bundleSize: z.number().nullable().optional(),
      }),
    )
    .handler(async ({ input }) => {
      const tenantId = currentTenantId();
      const { id, ...patch } = input;
      if (patch.nodeId) {
        const [ownedNode] = await db.select({ id: nodesTable.id }).from(nodesTable)
          .where(and(eq(nodesTable.tenantId, tenantId), eq(nodesTable.id, patch.nodeId)));
        if (!ownedNode) throw new ORPCError("BAD_REQUEST", { message: "Choose a node from your own Host workspace." });
      }
      await db
        .update(projectsTable)
        .set({ ...patch, updatedAt: nowSeconds() })
        .where(and(eq(projectsTable.tenantId, tenantId), eq(projectsTable.id, id)));
      return { ok: true };
    }),

  remove: tenant.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    const tenantId = currentTenantId();
    const [project] = await db.select().from(projectsTable).where(and(eq(projectsTable.tenantId, tenantId), eq(projectsTable.id, input.id)));
    if (project?.status === "running" && project.nodeId) {
      // Ask the node to tear the workload down before we forget about it.
      await db.insert(deployments).values({
        id: newId("dep"),
        tenantId,
        projectId: project.id,
        nodeId: project.nodeId,
        action: "remove",
        status: "queued",
        trigger: "delete",
      });
    }
    await db.delete(projectsTable).where(and(eq(projectsTable.tenantId, tenantId), eq(projectsTable.id, input.id)));
    await db.update(domainsTable).set({ projectId: null }).where(and(eq(domainsTable.tenantId, tenantId), eq(domainsTable.projectId, input.id)));
    await logActivity({
      scope: "project",
      refId: input.id,
      level: "warn",
      message: `Project "${project?.name ?? input.id}" deleted.`,
    });
    return { ok: true };
  }),

  /** Presigned PUT so the project archive uploads straight to storage. */
  presignBundle: tenant
    .input(z.object({ filename: z.string(), contentType: z.string().default("application/zip") }))
    .handler(async ({ input }) => {
      return createBundleUpload(input.filename, input.contentType);
    }),

  /** Reads the uploaded manifest/files and asks OpenAI how to run the project. */
  aiPlan: tenant
    .input(
      z.object({
        id: z.string(),
        manifest: z.array(z.string()).max(600).default([]),
        files: z
          .array(z.object({ path: z.string(), content: z.string().max(20000) }))
          .max(12)
          .default([]),
      }),
    )
    .handler(async ({ input }) => {
      const tenantId = currentTenantId();
      const [project] = await db.select().from(projectsTable).where(and(eq(projectsTable.tenantId, tenantId), eq(projectsTable.id, input.id)));
      if (!project) throw new ORPCError("NOT_FOUND");

      const result = await planDeployment({
        projectName: project.name,
        runtimeHint: project.runtime,
        manifest: input.manifest.length > 0 ? input.manifest : [project.bundleName ?? "bundle.zip"],
        files: input.files,
      });
      if (!result.ok || !result.plan) {
        await logActivity({
          scope: "project",
          refId: project.id,
          level: "error",
          message: `AI setup failed for "${project.name}": ${result.error}`,
        });
        return { ok: false as const, error: result.error ?? "AI planning failed." };
      }

      const plan = result.plan;
      await db
        .update(projectsTable)
        .set({
          runtime: plan.runtime,
          installCommand: plan.installCommand,
          buildCommand: plan.buildCommand,
          startCommand: plan.startCommand,
          port: plan.port,
          dockerfile: plan.dockerfile,
          aiNotes: plan.notes,
          updatedAt: nowSeconds(),
        })
        .where(and(eq(projectsTable.tenantId, tenantId), eq(projectsTable.id, project.id)));
      await logActivity({
        scope: "project",
        refId: project.id,
        level: "success",
        message: `AI wrote a deploy plan for "${project.name}" (${plan.runtime}, port ${plan.port}).`,
      });
      return { ok: true as const, plan, source: result.source };
    }),

  /** Queues work for an agent. Returns no_capacity when the fleet is empty. */
  deploy: tenant
    .input(z.object({ id: z.string(), action: z.enum(["deploy", "restart", "stop"]).default("deploy") }))
    .handler(async ({ input }) => {
      await reconcileHealth();
      const tenantId = currentTenantId();
      const [project] = await db.select().from(projectsTable).where(and(eq(projectsTable.tenantId, tenantId), eq(projectsTable.id, input.id)));
      if (!project) throw new ORPCError("NOT_FOUND");

      if (input.action === "deploy") {
        const requiresDocker = project.runtime === "docker" || project.runtime === "database";
        const needsNativeStart =
          ["node", "bun", "python", "custom"].includes(project.runtime) &&
          !project.startCommand;
        if ((requiresDocker && !project.dockerfile) || needsNativeStart) {
          return { ok: false as const, reason: "no_plan" as const };
        }
      }

      let nodeIds: string[] = [];
      if (input.action === "stop") {
        const replicas = await db
          .select()
          .from(projectReplicas)
          .where(and(eq(projectReplicas.tenantId, tenantId), eq(projectReplicas.projectId, project.id)));
        const online = await db.select({ id: nodesTable.id }).from(nodesTable).where(and(eq(nodesTable.tenantId, tenantId), eq(nodesTable.status, "online")));
        const onlineIds = new Set(online.map((row) => row.id));
        nodeIds = [...new Set(replicas.map((replica) => replica.nodeId).filter((id) => onlineIds.has(id)))];
        if (nodeIds.length === 0) {
          const fallback = await pickNode(project.nodeId);
          if (fallback) nodeIds = [fallback];
        }
      } else {
        nodeIds = await pickNodes(project.nodeId, project.replicateEverywhere !== false);
      }

      if (nodeIds.length === 0) {
        await db
          .update(projectsTable)
          .set({ status: "no_capacity", updatedAt: nowSeconds() })
          .where(and(eq(projectsTable.tenantId, tenantId), eq(projectsTable.id, project.id)));
        await logActivity({
          scope: "deployment",
          refId: project.id,
          level: "error",
          message: `Cannot ${input.action} "${project.name}" — no node is online. Start your PC agent or add a VPS.`,
        });
        return { ok: false as const, reason: "no_capacity" as const };
      }

      const deploymentIds: string[] = [];
      for (const nodeId of nodeIds) {
        const deploymentId = newId("dep");
        deploymentIds.push(deploymentId);
        await db.insert(deployments).values({
          id: deploymentId,
          tenantId,
          projectId: project.id,
          nodeId,
          action: input.action,
          status: "queued",
          trigger: "manual",
        });
        await upsertReplica(project.id, nodeId, {
          status: input.action === "stop" ? "deploying" : "deploying",
          lastDeploymentId: deploymentId,
          lastError: null,
        });
      }

      await db
        .update(projectsTable)
        .set({
          nodeId: nodeIds[0] ?? project.nodeId,
          status: input.action === "stop" ? "stopped" : "deploying",
          updatedAt: nowSeconds(),
        })
        .where(and(eq(projectsTable.tenantId, tenantId), eq(projectsTable.id, project.id)));
      await logActivity({
        scope: "deployment",
        refId: deploymentIds[0] ?? project.id,
        message: `${input.action} queued for "${project.name}" on ${nodeIds.length} node${nodeIds.length === 1 ? "" : "s"}.`,
      });
      return {
        ok: true as const,
        deploymentId: deploymentIds[0]!,
        nodeId: nodeIds[0]!,
        deploymentIds,
        nodeIds,
      };
    }),

  deploymentLog: tenant.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    const tenantId = currentTenantId();
    const [row] = await db.select().from(deployments).where(and(eq(deployments.tenantId, tenantId), eq(deployments.id, input.id)));
    if (!row) throw new ORPCError("NOT_FOUND");
    return row;
  }),

  /** Short-lived download link for the owner to re-check what was uploaded. */
  bundleUrl: tenant.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    const tenantId = currentTenantId();
      const [project] = await db.select().from(projectsTable).where(and(eq(projectsTable.tenantId, tenantId), eq(projectsTable.id, input.id)));
    if (!project?.bundleKey) throw new ORPCError("NOT_FOUND");
    return { url: await createBundleDownload(project.bundleKey) };
  }),

  activeDeployments: tenant.handler(async () => {
    const rows = await db
      .select()
      .from(deployments)
      .where(and(eq(deployments.tenantId, currentTenantId()), inArray(deployments.status, ["queued", "claimed", "building"])))
      .orderBy(desc(deployments.createdAt))
      .limit(25);
    return rows;
  }),
};
