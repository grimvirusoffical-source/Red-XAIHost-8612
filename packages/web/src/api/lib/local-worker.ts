import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { eq } from "drizzle-orm";
import { db } from "../database";
import { nodes } from "../database/schema";
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

export async function startLocalWorker(controlUrl: string): Promise<ChildProcess | null> {
  if (String(process.env.REDX_AUTO_LOCAL_NODE ?? "true").toLowerCase() === "false") return null;

  const state = await ensureState();
  stopStalePid();
  writeFileSync(agentPath, AGENT_SOURCE, "utf8");

  const nodeBin = process.env.REDX_NODE_BIN || (process.platform === "win32" ? "node.exe" : "node");
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
