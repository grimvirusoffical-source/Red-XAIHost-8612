import { z } from "zod";
import { desc, eq } from "drizzle-orm";
import { ORPCError } from "@orpc/server";
import { owner } from "../middleware/auth";
import { db } from "../database";
import { nodes as nodesTable } from "../database/schema";
import { generateAgentToken, hashToken, tokenPreview } from "../lib/crypto";
import { newId, nowSeconds } from "../lib/ids";
import { logActivity } from "../lib/activity";
import { reconcileHealth } from "../lib/health";

/**
 * Base URL agents talk back to. WEBSITE_URL often carries a trailing slash, which
 * would produce `https://host//api/agent/...` in the enrolment commands, so it is
 * always trimmed here.
 */
const controlUrl = () => (process.env.WEBSITE_URL ?? "http://localhost:4200").replace(/\/+$/, "");

/** Wraps a value for a PowerShell single-quoted literal (no interpolation). */
const psQuote = (value: string) => `'${value.replace(/'/g, "''")}'`;

export const nodes = {
  list: owner.handler(async () => {
    await reconcileHealth();
    const rows = await db.select().from(nodesTable).orderBy(desc(nodesTable.createdAt));
    return rows.map((node) => ({ ...node, tokenHash: undefined }));
  }),

  /** Creates a node slot and returns the agent token exactly once. */
  create: owner
    .input(
      z.object({
        name: z.string().min(1).max(60),
        kind: z.enum(["pc", "vps"]).default("vps"),
        labels: z.string().max(200).optional(),
      }),
    )
    .handler(async ({ input }) => {
      const token = generateAgentToken();
      const id = newId("node");
      await db.insert(nodesTable).values({
        id,
        name: input.name,
        kind: input.kind,
        tokenHash: hashToken(token),
        tokenPreview: tokenPreview(token),
        labels: input.labels ?? null,
        status: "pending",
      });
      await logActivity({
        scope: "node",
        refId: id,
        level: "success",
        message: `Node "${input.name}" registered (${input.kind}). Waiting for the agent to connect.`,
      });

      const base = controlUrl();
      return {
        id,
        token,
        // Copy-paste enrolment, one line per platform.
        linuxCommand: `curl -fsSL ${base}/api/agent/install.sh | sudo RXH_URL="${base}" RXH_TOKEN="${token}" bash`,
        // Paste straight into a PowerShell window. It is deliberately NOT wrapped in
        // `powershell -Command "..."`: inside double quotes the shell you paste into
        // expands $env:RXH_URL itself (to nothing) before the child shell ever sees it,
        // which is why a wrapped one-liner fails with "the term '=https://...'".
        windowsCommand: `$env:RXH_URL=${psQuote(base)}; $env:RXH_TOKEN=${psQuote(token)}; irm ${psQuote(`${base}/api/agent/install.ps1`)} | iex`,
        // For cmd.exe, where the outer shell does not touch $env: at all.
        windowsCmdCommand: `powershell -NoProfile -ExecutionPolicy Bypass -Command "$env:RXH_URL=${psQuote(base)}; $env:RXH_TOKEN=${psQuote(token)}; irm ${psQuote(`${base}/api/agent/install.ps1`)} | iex"`,
        manualCommand: `RXH_URL="${base}" RXH_TOKEN="${token}" node redxaihost-agent.mjs`,
      };
    }),

  update: owner
    .input(
      z.object({
        id: z.string(),
        name: z.string().min(1).max(60).optional(),
        labels: z.string().max(200).optional(),
        status: z.enum(["pending", "online", "offline", "disabled"]).optional(),
      }),
    )
    .handler(async ({ input }) => {
      const { id, ...patch } = input;
      await db.update(nodesTable).set(patch).where(eq(nodesTable.id, id));
      return { ok: true };
    }),

  rotateToken: owner.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    const [node] = await db.select().from(nodesTable).where(eq(nodesTable.id, input.id));
    if (!node) throw new ORPCError("NOT_FOUND");
    const token = generateAgentToken();
    await db
      .update(nodesTable)
      .set({ tokenHash: hashToken(token), tokenPreview: tokenPreview(token), status: "pending" })
      .where(eq(nodesTable.id, input.id));
    await logActivity({
      scope: "node",
      refId: input.id,
      level: "warn",
      message: `Agent token rotated for "${node.name}" — the old token stopped working.`,
    });
    return { token };
  }),

  remove: owner.input(z.object({ id: z.string() })).handler(async ({ input }) => {
    const [node] = await db.select().from(nodesTable).where(eq(nodesTable.id, input.id));
    await db.delete(nodesTable).where(eq(nodesTable.id, input.id));
    await logActivity({
      scope: "node",
      refId: input.id,
      level: "warn",
      message: `Node "${node?.name ?? input.id}" removed from the fleet.`,
    });
    return { ok: true };
  }),

  /** Capacity summary used by the dashboard header. */
  capacity: owner.handler(async () => {
    await reconcileHealth();
    const rows = await db.select().from(nodesTable);
    const online = rows.filter((node) => node.status === "online");
    return {
      total: rows.length,
      online: online.length,
      cores: online.reduce((sum, node) => sum + (node.cpuCores ?? 0), 0),
      memoryMb: online.reduce((sum, node) => sum + (node.memoryMb ?? 0), 0),
      diskGb: online.reduce((sum, node) => sum + (node.diskGb ?? 0), 0),
      dockerReady: online.filter((node) => node.dockerAvailable).length,
      tunnelReady: online.filter((node) => node.cloudflaredAvailable).length,
      nowSeconds: nowSeconds(),
    };
  }),
};
