import { z } from "zod";
import { and, desc, eq, inArray } from "drizzle-orm";
import { ORPCError } from "@orpc/server";
import { owner } from "../middleware/auth";
import { db } from "../database";
import {
  deployments,
  domains as domainsTable,
  nodes as nodesTable,
  projects as projectsTable,
} from "../database/schema";
import { newId, nowSeconds, slugify } from "../lib/ids";
import { logActivity } from "../lib/activity";
import { reconcileHealth } from "../lib/health";
import { createBundleUpload } from "../lib/bundle-storage";
import { planDeployment } from "../lib/ai";
import { pickNode } from "../lib/scheduler";

const runtimeEnum = z.enum(["static", "node", "bun", "python", "docker", "database", "custom"]);

export const projects = {
  list: owner.handler(async () => {
    await reconcileHealth();
    const rows = await db.select().from(projectsTable).orderBy(desc(projectsTable.updatedAt));
    const nodeRows = await db.select().from(nodesTable);
    const domainRows = await db.select().from(domainsTable);
    return rows.map((project) => ({
      ...project,
      nodeName: nodeRows.find((node) => node.id === project.nodeId)?.name ?? null,
      domains: domainRows
        .filter((domain) => domain.projectId === project.id)
        .map((domain) => ({ hostname: domain.hostname, status: domain.status })),
    }));
  }),

  get: owner.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    const [project] = await db.select().from(projectsTable).where(eq(projectsTable.id, input.id));
    if (!project) throw new ORPCError("NOT_FOUND");
    const history = await db
      .select()
      .from(deployments)
      .where(eq(deployments.projectId, project.id))
      .orderBy(desc(deployments.createdAt))
      .limit(20);
    const projectDomains = await db.select().from(domainsTable).where(eq(domainsTable.projectId, project.id));
    const [node] = project.nodeId
      ? await db.select().from(nodesTable).where(eq(nodesTable.id, project.nodeId))
      : [];
    return {
      project,
      deployments: history,
      domains: projectDomains,
      node: node ? { ...node, tokenHash: undefined } : null,
    };
  }),

  create: owner
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
        port: z.number().int().min(1).max(65535).optional(),
        envVars: z.string().optional(),
      }),
    )
    .handler(async ({ input }) => {
      const id = newId("prj");
      const existing = await db.select({ slug: projectsTable.slug }).from(projectsTable);
      const taken = new Set(existing.map((row) => row.slug));
      let slug = slugify(input.name);
      let counter = 2;
      while (taken.has(slug)) slug = `${slugify(input.name)}-${counter++}`;

      await db.insert(projectsTable).values({
        id,
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
        port: input.port ?? null,
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

  update: owner
    .input(
      z.object({
        id: z.string(),
        name: z.string().min(1).max(80).optional(),
        description: z.string().max(500).nullable().optional(),
        runtime: runtimeEnum.optional(),
        nodeId: z.string().nullable().optional(),
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
      const { id, ...patch } = input;
      await db
        .update(projectsTable)
        .set({ ...patch, updatedAt: nowSeconds() })
        .where(eq(projectsTable.id, id));
      return { ok: true };
    }),

  remove: owner.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    const [project] = await db.select().from(projectsTable).where(eq(projectsTable.id, input.id));
    if (project?.status === "running" && project.nodeId) {
      // Ask the node to tear the workload down before we forget about it.
      await db.insert(deployments).values({
        id: newId("dep"),
        projectId: project.id,
        nodeId: project.nodeId,
        action: "remove",
        status: "queued",
        trigger: "delete",
      });
    }
    await db.delete(projectsTable).where(eq(projectsTable.id, input.id));
    await db.update(domainsTable).set({ projectId: null }).where(eq(domainsTable.projectId, input.id));
    await logActivity({
      scope: "project",
      refId: input.id,
      level: "warn",
      message: `Project "${project?.name ?? input.id}" deleted.`,
    });
    return { ok: true };
  }),

  /** Presigned PUT so the project archive uploads straight to storage. */
  presignBundle: owner
    .input(z.object({ filename: z.string(), contentType: z.string().default("application/zip") }))
    .handler(async ({ input }) => {
      return createBundleUpload(input.filename, input.contentType);
    }),

  /** Reads the uploaded manifest/files and asks OpenAI how to run the project. */
  aiPlan: owner
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
      const [project] = await db.select().from(projectsTable).where(eq(projectsTable.id, input.id));
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
        .where(eq(projectsTable.id, project.id));
      await logActivity({
        scope: "project",
        refId: project.id,
        level: "success",
        message: `AI wrote a deploy plan for "${project.name}" (${plan.runtime}, port ${plan.port}).`,
      });
      return { ok: true as const, plan, source: result.source };
    }),

  /** Queues work for an agent. Returns no_capacity when the fleet is empty. */
  deploy: owner
    .input(z.object({ id: z.string(), action: z.enum(["deploy", "restart", "stop"]).default("deploy") }))
    .handler(async ({ input }) => {
      await reconcileHealth();
      const [project] = await db.select().from(projectsTable).where(eq(projectsTable.id, input.id));
      if (!project) throw new ORPCError("NOT_FOUND");

      const nodeId = await pickNode(project.nodeId);
      if (!nodeId) {
        await db
          .update(projectsTable)
          .set({ status: "no_capacity", updatedAt: nowSeconds() })
          .where(eq(projectsTable.id, project.id));
        await logActivity({
          scope: "deployment",
          refId: project.id,
          level: "error",
          message: `Cannot deploy "${project.name}" — no node is online. Start your PC agent or add a VPS.`,
        });
        return { ok: false as const, reason: "no_capacity" as const };
      }

      if (input.action === "deploy" && !project.dockerfile && project.runtime !== "static") {
        return { ok: false as const, reason: "no_plan" as const };
      }

      const deploymentId = newId("dep");
      await db.insert(deployments).values({
        id: deploymentId,
        projectId: project.id,
        nodeId,
        action: input.action,
        status: "queued",
        trigger: "manual",
      });
      await db
        .update(projectsTable)
        .set({
          nodeId,
          status: input.action === "stop" ? "stopped" : "deploying",
          updatedAt: nowSeconds(),
        })
        .where(eq(projectsTable.id, project.id));
      await logActivity({
        scope: "deployment",
        refId: deploymentId,
        message: `${input.action} queued for "${project.name}".`,
      });
      return { ok: true as const, deploymentId, nodeId };
    }),

  deploymentLog: owner.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    const [row] = await db.select().from(deployments).where(eq(deployments.id, input.id));
    if (!row) throw new ORPCError("NOT_FOUND");
    return row;
  }),

  /** Short-lived download link for the owner to re-check what was uploaded. */
  bundleUrl: owner.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    const [project] = await db.select().from(projectsTable).where(eq(projectsTable.id, input.id));
    if (!project?.bundleKey) throw new ORPCError("NOT_FOUND");
    const url = await getSignedUrl(
      s3,
      new GetObjectCommand({ Bucket: BUCKET, Key: project.bundleKey }),
      { expiresIn: 600 },
    );
    return { url };
  }),

  activeDeployments: owner.handler(async () => {
    const rows = await db
      .select()
      .from(deployments)
      .where(and(inArray(deployments.status, ["queued", "claimed", "building"])))
      .orderBy(desc(deployments.createdAt))
      .limit(25);
    return rows;
  }),
};
