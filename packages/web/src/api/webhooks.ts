import type { Hono } from "hono";
import { createHmac, timingSafeEqual } from "node:crypto";
import { eq } from "drizzle-orm";
import { db } from "./database";
import { deployments, projects } from "./database/schema";
import { newId, nowSeconds } from "./lib/ids";
import { logActivity } from "./lib/activity";
import { getCredential } from "./lib/credentials";
import { pickNode } from "./lib/scheduler";

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
    const creds = await getCredential("github");
    const secret = creds?.webhookSecret?.trim();
    if (!secret) {
      return c.json({ error: "no webhook secret configured in Settings → GitHub" }, 503);
    }
    if (!signatureMatches(secret, raw, c.req.header("x-hub-signature-256"))) {
      await logActivity({
        scope: "deployment",
        level: "warn",
        message: "Rejected a GitHub webhook delivery with a bad signature.",
      });
      return c.json({ error: "bad signature" }, 401);
    }

    const event = c.req.header("x-github-event") ?? "";
    if (event === "ping") return c.json({ ok: true, pong: true }, 200);
    if (event !== "push") return c.json({ ok: true, ignored: event }, 200);

    let payload: PushPayload;
    try {
      payload = JSON.parse(raw) as PushPayload;
    } catch {
      return c.json({ error: "invalid json" }, 400);
    }
    if (payload.deleted) return c.json({ ok: true, ignored: "branch deleted" }, 200);

    const slug = repoSlug(
      payload.repository?.clone_url ?? payload.repository?.html_url ?? payload.repository?.ssh_url,
    );
    const fullName = payload.repository?.full_name?.toLowerCase() ?? null;
    const branch = payload.ref?.replace(/^refs\/heads\//, "") ?? null;

    const candidates = await db
      .select()
      .from(projects)
      .where(eq(projects.sourceKind, "git"));
    const matched = candidates.filter((project) => {
      if (!project.autoDeploy) return false;
      const projectSlug = repoSlug(project.gitUrl);
      if (!projectSlug || (projectSlug !== slug && projectSlug !== fullName)) return false;
      return !branch || (project.gitBranch ?? "main") === branch;
    });

    if (matched.length === 0) {
      return c.json({ ok: true, queued: 0, reason: "no matching auto-deploy project" }, 200);
    }

    const queued: string[] = [];
    for (const project of matched) {
      const nodeId = await pickNode(project.nodeId);
      if (!nodeId) {
        await db
          .update(projects)
          .set({ status: "no_capacity", updatedAt: nowSeconds() })
          .where(eq(projects.id, project.id));
        await logActivity({
          scope: "deployment",
          refId: project.id,
          level: "error",
          message: `Push to ${slug ?? "repo"} could not deploy "${project.name}" — no node is online.`,
        });
        continue;
      }
      if (!project.dockerfile && project.runtime !== "static") {
        await logActivity({
          scope: "deployment",
          refId: project.id,
          level: "warn",
          message: `Push to ${slug ?? "repo"} skipped "${project.name}" — run AI setup first so it has a Dockerfile.`,
        });
        continue;
      }

      const deploymentId = newId("dep");
      await db.insert(deployments).values({
        id: deploymentId,
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
        .where(eq(projects.id, project.id));
      await logActivity({
        scope: "deployment",
        refId: deploymentId,
        level: "success",
        message: `Push to ${slug ?? "repo"}${branch ? `@${branch}` : ""} queued a deploy for "${project.name}".`,
      });
      queued.push(deploymentId);
    }

    return c.json({ ok: true, queued: queued.length, deployments: queued }, 200);
  });
}
