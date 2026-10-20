import { z } from "zod";
import { and, desc, eq, isNotNull, ne } from "drizzle-orm";
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

/** GitHub REST base; overridable for GitHub Enterprise. */
const GITHUB_API = (process.env.GITHUB_API_URL || "https://api.github.com").replace(/\/+$/, "");

function ghHeaders(token: string): Record<string, string> {
  return {
    Authorization: `Bearer ${token}`,
    Accept: "application/vnd.github+json",
    "User-Agent": "redxaihost",
  };
}

type GhRun = {
  id: number;
  status: string;
  conclusion: string | null;
  html_url: string;
  head_sha: string;
  event: string;
  created_at: string;
};

/**
 * workflow_dispatch returns no run id, so the run is matched afterwards: same
 * workflow, event workflow_dispatch, same head commit, created no earlier than
 * the dispatch, and not already claimed by another build. The oldest such run
 * is ours (dispatches are matched in order).
 */
export function pickDispatchedRun(
  runs: GhRun[],
  want: { headSha: string | null; dispatchedAt: number | null },
  claimed: Set<number>,
): GhRun | null {
  const since = (want.dispatchedAt ?? 0) - 5; // small clock skew allowance
  const candidates = runs
    .filter((r) => r.event === "workflow_dispatch")
    .filter((r) => !claimed.has(r.id))
    .filter((r) => !want.headSha || r.head_sha === want.headSha)
    .filter((r) => Date.parse(r.created_at) / 1000 >= since)
    .sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at) || a.id - b.id);
  return candidates[0] ?? null;
}

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

      // Resolve the ref first so the run can be matched by commit later.
      let headSha: string | null = null;
      const shaRes = await fetch(
        `${GITHUB_API}/repos/${context.repo}/commits/${encodeURIComponent(input.ref)}`,
        { headers: { ...ghHeaders(context.token), Accept: "application/vnd.github.sha" } },
      ).catch(() => null);
      if (shaRes?.ok) {
        const text = (await shaRes.text()).trim();
        if (/^[0-9a-f]{40}$/.test(text)) headSha = text;
      }
      const dispatchedAt = nowSeconds();

      const res = await fetch(
        `${GITHUB_API}/repos/${context.repo}/actions/workflows/${workflow.file}/dispatches`,
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
        headSha,
        dispatchedAt: ok ? dispatchedAt : null,
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

  /** Pulls the state of the run this build dispatched (not just the workflow's latest run). */
  sync: owner.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    const [build] = await db.select().from(appBuilds).where(eq(appBuilds.id, input.id));
    if (!build?.workflow) return { ok: false, error: "Unknown build." };
    const context = await githubContext();
    if (!context) return { ok: false, error: "GitHub not connected." };

    let run: GhRun | null = null;
    if (build.runId) {
      const res = await fetch(`${GITHUB_API}/repos/${context.repo}/actions/runs/${build.runId}`, {
        headers: ghHeaders(context.token),
      });
      if (!res.ok) return { ok: false, error: `GitHub returned ${res.status}` };
      run = (await res.json()) as GhRun;
    } else {
      if (!build.dispatchedAt) return { ok: false, error: "This build was never dispatched." };
      const params = new URLSearchParams({
        event: "workflow_dispatch",
        per_page: "50",
        created: `>=${new Date((build.dispatchedAt - 5) * 1000).toISOString().replace(/\.\d{3}Z$/, "Z")}`,
      });
      if (build.headSha) params.set("head_sha", build.headSha);
      const res = await fetch(
        `${GITHUB_API}/repos/${context.repo}/actions/workflows/${build.workflow}/runs?${params}`,
        { headers: ghHeaders(context.token) },
      );
      if (!res.ok) return { ok: false, error: `GitHub returned ${res.status}` };
      const body = (await res.json()) as { workflow_runs?: GhRun[] };
      const others = await db
        .select({ runId: appBuilds.runId })
        .from(appBuilds)
        .where(and(isNotNull(appBuilds.runId), ne(appBuilds.id, build.id)));
      const claimed = new Set(others.map((o) => o.runId!));
      run = pickDispatchedRun(body.workflow_runs ?? [], build, claimed);
      if (!run) return { ok: false, error: "The dispatched run has not appeared yet." };
    }

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
        runId: run.id,
        runUrl: run.html_url,
        finishedAt: run.status === "completed" ? nowSeconds() : null,
      })
      .where(eq(appBuilds.id, build.id));
    return { ok: true, status, runId: run.id, runUrl: run.html_url };
  }),

  remove: owner.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    await db.delete(appBuilds).where(eq(appBuilds.id, input.id));
    return { ok: true };
  }),
};
