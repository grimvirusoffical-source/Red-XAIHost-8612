import { z } from "zod";
import { desc, eq } from "drizzle-orm";
import { owner } from "../middleware/auth";
import { db } from "../database";
import { appBuilds } from "../database/schema";
import { newId, nowSeconds } from "../lib/ids";
import { logActivity } from "../lib/activity";
import { getCredential } from "../lib/credentials";

/**
 * App binaries are built in CI, not on the owner's PC: iOS needs macOS +
 * Xcode + signing certificates, and Android/Windows/Mac each need their own
 * toolchain. RedXAIHost dispatches the matching GitHub Actions workflow and
 * tracks the run, so the dashboard is the trigger and the status board.
 */
const WORKFLOWS: Record<string, { file: string; label: string; runner: string }> = {
  ios: { file: "build-ios.yml", label: "iOS (EAS on macOS runner)", runner: "macos-latest" },
  android: { file: "build-android.yml", label: "Android (EAS)", runner: "ubuntu-latest" },
  windows: { file: "build-desktop.yml", label: "Windows installer", runner: "windows-latest" },
  mac: { file: "build-desktop.yml", label: "macOS dmg", runner: "macos-latest" },
  linux: { file: "build-desktop.yml", label: "Linux AppImage", runner: "ubuntu-latest" },
};

const platformEnum = z.enum(["ios", "android", "windows", "mac", "linux"]);

async function githubContext(): Promise<{ token: string; repo: string } | null> {
  const creds = await getCredential("github");
  if (!creds?.token) return null;
  const repo = creds.repo ?? process.env.GITHUB_REPO ?? "";
  if (!repo.includes("/")) return null;
  return { token: creds.token, repo };
}

export const builds = {
  list: owner.handler(async () => {
    return db.select().from(appBuilds).orderBy(desc(appBuilds.createdAt)).limit(50);
  }),

  targets: owner.handler(async () => {
    const context = await githubContext();
    return {
      repoConnected: Boolean(context),
      repo: context?.repo ?? null,
      platforms: Object.entries(WORKFLOWS).map(([id, value]) => ({ id, ...value })),
    };
  }),

  dispatch: owner
    .input(
      z.object({
        platform: platformEnum,
        projectId: z.string().optional(),
        ref: z.string().default("main"),
        version: z.string().optional(),
        notes: z.string().max(500).optional(),
      }),
    )
    .handler(async ({ input }) => {
      const id = newId("bld");
      const workflow = WORKFLOWS[input.platform]!;
      const context = await githubContext();

      if (!context) {
        await db.insert(appBuilds).values({
          id,
          projectId: input.projectId ?? null,
          platform: input.platform,
          status: "failed",
          provider: "github_actions",
          workflow: workflow.file,
          version: input.version ?? null,
          notes: input.notes ?? null,
          error: "GitHub token or owner/repo missing — add them in Settings → Credentials.",
          finishedAt: nowSeconds(),
        });
        return {
          ok: false as const,
          error: "Connect GitHub (token + owner/repo) in Settings before dispatching builds.",
        };
      }

      const res = await fetch(
        `https://api.github.com/repos/${context.repo}/actions/workflows/${workflow.file}/dispatches`,
        {
          method: "POST",
          headers: {
            Authorization: `Bearer ${context.token}`,
            Accept: "application/vnd.github+json",
            "Content-Type": "application/json",
            "User-Agent": "redxaihost",
          },
          body: JSON.stringify({
            ref: input.ref,
            inputs: {
              platform: input.platform,
              version: input.version ?? "",
            },
          }),
        },
      );

      const ok = res.status === 204;
      const error = ok ? null : `GitHub returned ${res.status}: ${(await res.text()).slice(0, 200)}`;

      await db.insert(appBuilds).values({
        id,
        projectId: input.projectId ?? null,
        platform: input.platform,
        status: ok ? "dispatched" : "failed",
        provider: input.platform === "ios" || input.platform === "android" ? "eas" : "github_actions",
        workflow: workflow.file,
        runUrl: ok ? `https://github.com/${context.repo}/actions/workflows/${workflow.file}` : null,
        version: input.version ?? null,
        notes: input.notes ?? null,
        error,
        finishedAt: ok ? null : nowSeconds(),
      });

      await logActivity({
        scope: "build",
        refId: id,
        level: ok ? "success" : "error",
        message: ok
          ? `${workflow.label} build dispatched on ${context.repo}@${input.ref}.`
          : `${workflow.label} dispatch failed: ${error}`,
      });

      return ok ? { ok: true as const, id } : { ok: false as const, error: error! };
    }),

  /** Pulls the latest run state for a workflow so the board is not stale. */
  sync: owner.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    const [build] = await db.select().from(appBuilds).where(eq(appBuilds.id, input.id));
    if (!build?.workflow) return { ok: false, error: "Unknown build." };
    const context = await githubContext();
    if (!context) return { ok: false, error: "GitHub not connected." };

    const res = await fetch(
      `https://api.github.com/repos/${context.repo}/actions/workflows/${build.workflow}/runs?per_page=1`,
      {
        headers: {
          Authorization: `Bearer ${context.token}`,
          Accept: "application/vnd.github+json",
          "User-Agent": "redxaihost",
        },
      },
    );
    if (!res.ok) return { ok: false, error: `GitHub returned ${res.status}` };
    const body = (await res.json()) as {
      workflow_runs?: { status: string; conclusion: string | null; html_url: string }[];
    };
    const run = body.workflow_runs?.[0];
    if (!run) return { ok: false, error: "No runs found yet." };

    const status =
      run.status === "completed"
        ? run.conclusion === "success"
          ? "succeeded"
          : "failed"
        : "building";
    await db
      .update(appBuilds)
      .set({
        status,
        runUrl: run.html_url,
        finishedAt: run.status === "completed" ? nowSeconds() : null,
      })
      .where(eq(appBuilds.id, build.id));
    return { ok: true, status, runUrl: run.html_url };
  }),

  remove: owner.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    await db.delete(appBuilds).where(eq(appBuilds.id, input.id));
    return { ok: true };
  }),
};
