import type { Hono } from "hono";
import { createHmac, timingSafeEqual } from "node:crypto";
import { and, eq } from "drizzle-orm";
import { db } from "./database";
import { deployments, projects } from "./database/schema";
import { newId, nowSeconds } from "./lib/ids";
import { logActivity } from "./lib/activity";
import { getCredential } from "./lib/credentials";
import { pickNode } from "./lib/scheduler";
import { withTenantContext } from "./lib/tenant-context";

/**
 * GitHub push webhook — this is what makes a project's "auto deploy on push"
 * switch real. GitHub signs every delivery with the secret the owner pasted
 * into Settings → GitHub (webhookSecret); an unsigned or mis-signed delivery is
 * rejected before the body is trusted.
 */

interface PushPayload {
  ref?: string;
  after?: string;
  deleted?: boolean;
  repository?: { full_name?: string; clone_url?: string; ssh_url?: string; html_url?: string };
}

/** github.com/Owner/Repo.git, git@github.com:Owner/Repo.git → owner/repo */
function repoSlug(url: string | null | undefined): string | null {
  if (!url) return null;
  const match = /github\.com[/:]+([^/]+)\/([^/]+?)(?:\.git)?\/?$/i.exec(url.trim());
  if (!match) return null;
  return `${match[1]}/${match[2]}`.toLowerCase();
}

function signatureMatches(secret: string, body: string, header: string | undefined): boolean {
  if (!header) return false;
  const expected = `sha256=${createHmac("sha256", secret).update(body).digest("hex")}`;
  const a = Buffer.from(expected);
  const b = Buffer.from(header);
  return a.length === b.length && timingSafeEqual(a, b);
}

export function registerWebhookRoutes(app: Hono) {
  app.post("/api/hooks/github", async (c) => {
    const raw = await c.req.text();
    const event = c.req.header("x-github-event") ?? "";
    const signature = c.req.header("x-hub-signature-256");

    let payload: PushPayload;
    try {
      payload = raw ? (JSON.parse(raw) as PushPayload) : {};
    } catch {
      return c.json({ error: "invalid json" }, 400);
    }

    const slug = repoSlug(
      payload.repository?.clone_url ?? payload.repository?.html_url ?? payload.repository?.ssh_url,
    );
    const fullName = payload.repository?.full_name?.toLowerCase() ?? null;
    const branch = payload.ref?.replace(/^refs\/heads\//, "") ?? null;

    // The repository name is not trusted for authorization. It only narrows
    // candidate tenants before each tenant's own secret verifies the signature.
    const candidates = (await db.select().from(projects).where(eq(projects.sourceKind, "git"))).filter(
      (project) => {
        const projectSlug = repoSlug(project.gitUrl);
        return Boolean(projectSlug && (projectSlug === slug || projectSlug === fullName));
      },
    );
    const tenantIds = [...new Set(candidates.map((project) => project.tenantId))];

    const verifiedTenants = new Set<string>();
    for (const tenantId of tenantIds) {
      await withTenantContext(
        { tenantId, isOwner: tenantId === "owner", userId: "webhook" },
        async () => {
          const creds = await getCredential("github");
          const secret = creds?.webhookSecret?.trim();
          if (secret && signatureMatches(secret, raw, signature)) verifiedTenants.add(tenantId);
        },
      );
    }

    if (verifiedTenants.size === 0) {
      return c.json({ error: "bad signature" }, 401);
    }
    if (event === "ping") return c.json({ ok: true, pong: true }, 200);
    if (event !== "push") return c.json({ ok: true, ignored: event }, 200);
    if (payload.deleted) return c.json({ ok: true, ignored: "branch deleted" }, 200);

    const matched = candidates.filter((project) => {
      if (!verifiedTenants.has(project.tenantId) || !project.autoDeploy) return false;
      return !branch || (project.gitBranch ?? "main") === branch;
    });

    const queued: string[] = [];
    for (const project of matched) {
      await withTenantContext(
        { tenantId: project.tenantId, isOwner: project.tenantId === "owner", userId: "webhook" },
        async () => {
          const nodeId = await pickNode(project.nodeId);
          if (!nodeId) {
            await db
              .update(projects)
              .set({ status: "no_capacity", updatedAt: nowSeconds() })
              .where(and(eq(projects.tenantId, project.tenantId), eq(projects.id, project.id)));
            await logActivity({
              scope: "deployment",
              refId: project.id,
              level: "error",
              message: `Push to ${slug ?? "repo"} could not deploy "${project.name}" — no node is online.`,
            });
            return;
          }
          if (!project.dockerfile && project.runtime !== "static") {
            await logActivity({
              scope: "deployment",
              refId: project.id,
              level: "warn",
              message: `Push to ${slug ?? "repo"} skipped "${project.name}" — run AI setup first so it has a Dockerfile.`,
            });
            return;
          }

          const deploymentId = newId("dep");
          await db.insert(deployments).values({
            id: deploymentId,
            tenantId: project.tenantId,
            projectId: project.id,
            nodeId,
            action: "deploy",
            status: "queued",
            trigger: "push",
            commitSha: payload.after ?? null,
          });
          await db
            .update(projects)
            .set({ nodeId, status: "deploying", updatedAt: nowSeconds() })
            .where(and(eq(projects.tenantId, project.tenantId), eq(projects.id, project.id)));
          await logActivity({
            scope: "deployment",
            refId: deploymentId,
            level: "success",
            message: `Push to ${slug ?? "repo"}${branch ? `@${branch}` : ""} queued a deploy for "${project.name}".`,
          });
          queued.push(deploymentId);
        },
      );
    }

    return c.json({ ok: true, queued: queued.length, deployments: queued }, 200);
  });
}
