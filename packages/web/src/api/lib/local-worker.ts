import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { eq } from "drizzle-orm";
import { db } from "../database";
import { deployments, nodes, projects } from "../database/schema";
import { AGENT_SOURCE } from "../agent-assets";
import { generateAgentToken, hashToken, tokenPreview } from "./crypto";
import { newId } from "./ids";

const repoRoot = fileURLToPath(new URL("../../../../../", import.meta.url));
const dataRoot = resolve(repoRoot, process.env.REDX_DATA_DIR || "data");
const statePath = resolve(dataRoot, "local-worker.json");
const agentPath = resolve(dataRoot, "redxaihost-local-agent.mjs");
const pidPath = resolve(dataRoot, "local-worker.pid");

type LocalState = { nodeId: string; token: string };

function stopStalePid() {
  try {
    if (!existsSync(pidPath)) return;
    const pid = Number(readFileSync(pidPath, "utf8").trim());
    if (!Number.isInteger(pid) || pid <= 0) return;
    if (process.platform === "win32") {
      spawnSync("taskkill", ["/PID", String(pid), "/T", "/F"], { stdio: "ignore" });
    } else {
      try {
        process.kill(pid, "SIGTERM");
      } catch {}
    }
  } catch {}
}

async function ensureState(): Promise<LocalState> {
  mkdirSync(dataRoot, { recursive: true });
  let saved: LocalState | null = null;
  try {
    saved = JSON.parse(readFileSync(statePath, "utf8")) as LocalState;
  } catch {}

  const existing = await db.select().from(nodes).where(eq(nodes.labels, "__local_worker__")).limit(1);
  const row = existing[0];

  if (row && saved?.nodeId === row.id && saved.token) {
    const expected = hashToken(saved.token);
    if (row.tokenHash !== expected) {
      await db
        .update(nodes)
        .set({ tokenHash: expected, tokenPreview: tokenPreview(saved.token), status: "pending" })
        .where(eq(nodes.id, row.id));
    }
    return saved;
  }

  const token = generateAgentToken();
  if (row) {
    await db
      .update(nodes)
      .set({
        tokenHash: hashToken(token),
        tokenPreview: tokenPreview(token),
        kind: "pc",
        status: "pending",
      })
      .where(eq(nodes.id, row.id));
    saved = { nodeId: row.id, token };
  } else {
    const nodeId = newId("node");
    await db.insert(nodes).values({
      id: nodeId,
      name: "This computer",
      kind: "pc",
      tokenHash: hashToken(token),
      tokenPreview: tokenPreview(token),
      labels: "__local_worker__",
      status: "pending",
    });
    saved = { nodeId, token };
  }

  writeFileSync(statePath, JSON.stringify(saved, null, 2), { mode: 0o600 });
  return saved;
}

async function ensureInfectedNationProject(nodeId: string) {
  if (String(process.env.REDX_BOOTSTRAP_INFECTEDNATION ?? "true").toLowerCase() === "false") return;
  const existing = await db.select().from(projects).where(eq(projects.slug, "infectednation")).limit(1);
  if (existing[0]) {
    if (existing[0].nodeId !== nodeId) {
      await db.update(projects).set({ nodeId }).where(eq(projects.id, existing[0].id));
    }
    return;
  }
  const projectId = newId("prj");
  await db.insert(projects).values({
    id: projectId,
    slug: "infectednation",
    name: "InfectedNation",
    description: "Central shared identity service for Infected apps.",
    runtime: "bun",
    sourceKind: "git",
    gitUrl: "https://github.com/grimvirusoffical-source/Red-XAIHost-8612.git",
    gitBranch: "main",
    nodeId,
    port: 8787,
    installCommand: null,
    buildCommand: null,
    startCommand: "bun services/infectednation/server.ts",
    dockerfile: null,
    envVars: JSON.stringify({ HOST: "0.0.0.0", PORT: "8787" }),
    status: "deploying",
    autoDeploy: true,
  });
  await db.insert(deployments).values({
    id: newId("dep"),
    projectId,
    nodeId,
    action: "deploy",
    status: "queued",
    trigger: "bootstrap",
  });
}

export async function startLocalWorker(controlUrl: string): Promise<ChildProcess | null> {
  if (String(process.env.REDX_AUTO_LOCAL_NODE ?? "true").toLowerCase() === "false") return null;

  const state = await ensureState();
  await ensureInfectedNationProject(state.nodeId);
  stopStalePid();
  writeFileSync(agentPath, AGENT_SOURCE, "utf8");

  // The local worker is bundled with RedXAIHost, so use the same runtime that
  // successfully launched the panel. This keeps a fresh self-host install
  // Bun-only; remote node installers may still use Node independently.
  const nodeBin = process.env.REDX_NODE_BIN || process.execPath;
  const child = spawn(nodeBin, [agentPath], {
    cwd: repoRoot,
    env: {
      ...process.env,
      RXH_URL: controlUrl.replace(/\/+$/, ""),
      RXH_TOKEN: state.token,
      RXH_HOME: resolve(dataRoot, "worker"),
    },
    stdio: "inherit",
    windowsHide: true,
  });

  child.on("error", (error) => {
    console.error("[local-worker] failed to start:", error.message);
  });
  child.on("exit", (code, signal) => {
    console.warn("[local-worker] exited", { code, signal });
  });
  if (child.pid) writeFileSync(pidPath, String(child.pid), "utf8");
  return child;
}
