/**
 * The node agent, served verbatim at /api/agent/agent.mjs.
 *
 * It is plain Node ESM with zero dependencies so it runs on a bare Windows PC
 * or a fresh VPS. It never opens an inbound port: it polls the control plane,
 * builds workloads with Docker, and fronts them with cloudflared.
 *
 * Written without template literals on purpose — the whole program lives inside
 * this exported string.
 */
export const AGENT_SOURCE = String.raw`#!/usr/bin/env node
// RedXAIHost node agent. Runs workloads for one machine (PC or VPS).
// Usage: RXH_URL="https://panel.example.com" RXH_TOKEN="rxh_..." node redxaihost-agent.mjs

import { execFile, spawn } from "node:child_process";
import { mkdir, rm, writeFile } from "node:fs/promises";
import { createWriteStream } from "node:fs";
import { homedir, arch, cpus, freemem, platform, totalmem, hostname } from "node:os";
import { join } from "node:path";
import { pipeline } from "node:stream/promises";
import { Readable } from "node:stream";

const AGENT_VERSION = "1.0.0";
const URL_BASE = (process.env.RXH_URL || "").replace(/\/+$/, "");
const TOKEN = process.env.RXH_TOKEN || "";
const ROOT = process.env.RXH_HOME || join(homedir(), ".redxaihost");
const WORK = join(ROOT, "projects");

if (!URL_BASE || !TOKEN) {
  console.error("RXH_URL and RXH_TOKEN are required.");
  process.exit(1);
}

function log(...parts) {
  console.log("[" + new Date().toISOString() + "]", ...parts);
}

function run(cmd, args, opts) {
  const options = opts || {};
  return new Promise((resolve) => {
    execFile(cmd, args, { maxBuffer: 1024 * 1024 * 32, cwd: options.cwd, shell: options.shell === true }, (error, stdout, stderr) => {
      resolve({
        ok: !error,
        code: error && typeof error.code === "number" ? error.code : error ? 1 : 0,
        stdout: String(stdout || ""),
        stderr: String(stderr || ""),
      });
    });
  });
}

async function has(cmd) {
  const probe = platform() === "win32" ? "where" : "which";
  const result = await run(probe, [cmd]);
  return result.ok;
}

async function api(path, body) {
  const res = await fetch(URL_BASE + path, {
    method: "POST",
    headers: { Authorization: "Bearer " + TOKEN, "Content-Type": "application/json" },
    body: JSON.stringify(body || {}),
  });
  if (!res.ok) throw new Error(path + " -> " + res.status);
  return res.json();
}

async function machineInfo() {
  const totalMb = Math.round(totalmem() / (1024 * 1024));
  const freeMb = Math.round(freemem() / (1024 * 1024));
  let publicIp = "";
  try {
    const res = await fetch("https://api.ipify.org?format=json");
    publicIp = (await res.json()).ip || "";
  } catch {}
  return {
    os: platform() + " (" + hostname() + ")",
    arch: arch(),
    agentVersion: AGENT_VERSION,
    cpuCores: cpus().length,
    memoryMb: totalMb,
    diskGb: 0,
    publicIp,
    dockerAvailable: await has("docker"),
    cloudflaredAvailable: (await has("cloudflared")) || (await has("docker")),
    cpuPercent: Math.min(99, Math.round((1 - freeMb / totalMb) * 100)),
    memPercent: Math.round((1 - freeMb / totalMb) * 100),
  };
}

// ---------------------------------------------------------------- job reporting
function makeReporter(deploymentId) {
  let queue = "";
  let flushing = false;
  async function flush(status, error) {
    if (flushing) return;
    flushing = true;
    const chunk = queue;
    queue = "";
    try {
      await api("/api/agent/job", { deploymentId, status, logChunk: chunk, error });
    } catch (err) {
      log("report failed:", err.message);
    }
    flushing = false;
  }
  return {
    write(line) {
      queue += line.endsWith("\n") ? line : line + "\n";
      if (queue.length > 4000) void flush("building");
    },
    async progress(status) {
      await flush(status);
    },
    async finish(status, error) {
      await flush(status, error);
    },
  };
}

// ---------------------------------------------------------------- workload steps
async function download(url, dest) {
  const res = await fetch(url);
  if (!res.ok) throw new Error("bundle download failed: " + res.status);
  await pipeline(Readable.fromWeb(res.body), createWriteStream(dest));
}

async function extract(archivePath, dir, reporter) {
  // bsdtar ships with Windows 10+ and most Linux images and handles zip + tar.
  let result = await run("tar", ["-xf", archivePath, "-C", dir]);
  if (!result.ok && (await has("unzip"))) {
    result = await run("unzip", ["-o", archivePath, "-d", dir]);
  }
  if (!result.ok) throw new Error("extract failed: " + (result.stderr || result.stdout).slice(0, 400));
  reporter.write("extracted bundle");
}

function staticDockerfile(port) {
  return [
    "FROM nginx:alpine",
    "COPY . /usr/share/nginx/html",
    "RUN printf 'server { listen " + port + "; root /usr/share/nginx/html; index index.html; location / { try_files $uri $uri/ /index.html; } }' > /etc/nginx/conf.d/default.conf",
    "EXPOSE " + port,
  ].join("\n");
}

async function streamCommand(cmd, args, cwd, reporter) {
  return new Promise((resolve) => {
    const child = spawn(cmd, args, { cwd, shell: platform() === "win32" });
    child.stdout.on("data", (data) => reporter.write(String(data).trimEnd()));
    child.stderr.on("data", (data) => reporter.write(String(data).trimEnd()));
    child.on("close", (code) => resolve(code === 0));
    child.on("error", (error) => {
      reporter.write("spawn error: " + error.message);
      resolve(false);
    });
  });
}

async function deployProject(job, reporter) {
  const project = job.project;
  const dir = join(WORK, project.slug);
  const image = "rxh-" + project.slug;
  const container = "rxh-" + project.slug;

  await reporter.progress("building");
  reporter.write("=== deploy " + project.name + " (" + project.runtime + ") ===");

  if (!(await has("docker"))) {
    throw new Error("Docker is not installed on this node. Install Docker Desktop (Windows) or docker.io (Linux).");
  }

  await rm(dir, { recursive: true, force: true });
  await mkdir(dir, { recursive: true });

  if (project.sourceKind === "git" && project.gitUrl) {
    reporter.write("cloning " + project.gitUrl + " (" + (project.gitBranch || "main") + ")");
    const cloned = await streamCommand("git", ["clone", "--depth", "1", "--branch", project.gitBranch || "main", project.gitUrl, "."], dir, reporter);
    if (!cloned) throw new Error("git clone failed");
  } else if (project.bundleUrl) {
    const archivePath = join(dir, "__bundle");
    reporter.write("downloading bundle " + (project.bundleName || ""));
    await download(project.bundleUrl, archivePath);
    await extract(archivePath, dir, reporter);
    await rm(archivePath, { force: true });
  } else {
    throw new Error("Project has neither a git URL nor an uploaded bundle.");
  }

  const dockerfile = project.dockerfile || staticDockerfile(project.port || 8080);
  await writeFile(join(dir, "Dockerfile.redxaihost"), dockerfile, "utf8");
  reporter.write("wrote Dockerfile.redxaihost");

  const built = await streamCommand("docker", ["build", "-f", "Dockerfile.redxaihost", "-t", image, "."], dir, reporter);
  if (!built) throw new Error("docker build failed");

  await run("docker", ["rm", "-f", container]);

  const port = String(project.port || 8080);
  const args = ["run", "-d", "--name", container, "--restart", "unless-stopped", "-p", port + ":" + port, "-e", "PORT=" + port];
  const env = project.envVars || {};
  for (const key of Object.keys(env)) {
    args.push("-e", key + "=" + env[key]);
  }
  args.push(image);
  const started = await streamCommand("docker", args, dir, reporter);
  if (!started) throw new Error("docker run failed");
  reporter.write("container " + container + " listening on :" + port);

  for (const tunnel of job.tunnels || []) {
    if (!tunnel.connectorToken) {
      reporter.write("tunnel " + tunnel.hostname + " has no connector token yet - skipping");
      continue;
    }
    await startTunnel(tunnel, reporter);
  }
  return true;
}

async function startTunnel(tunnel, reporter) {
  const name = "rxh-tunnel-" + tunnel.tunnelName;
  if (await has("cloudflared")) {
    reporter.write("starting cloudflared for " + tunnel.hostname);
    const child = spawn("cloudflared", ["tunnel", "run", "--token", tunnel.connectorToken], {
      detached: true,
      stdio: "ignore",
      shell: platform() === "win32",
    });
    child.unref();
    return;
  }
  await run("docker", ["rm", "-f", name]);
  const args = ["run", "-d", "--name", name, "--restart", "unless-stopped"];
  if (platform() === "linux") args.push("--network", "host");
  args.push("cloudflare/cloudflared:latest", "tunnel", "run", "--token", tunnel.connectorToken);
  const started = await streamCommand("docker", args, WORK, reporter);
  reporter.write(started ? "cloudflared container up for " + tunnel.hostname : "cloudflared container failed");
}

async function stopProject(job, reporter, removeImage) {
  const container = "rxh-" + job.project.slug;
  await reporter.progress("building");
  reporter.write("stopping " + container);
  await run("docker", ["rm", "-f", container]);
  if (removeImage) await run("docker", ["rmi", "-f", "rxh-" + job.project.slug]);
  return true;
}

async function handleJob(job) {
  const reporter = makeReporter(job.deploymentId);
  try {
    if (job.action === "deploy" || job.action === "restart") {
      await deployProject(job, reporter);
    } else if (job.action === "stop") {
      await stopProject(job, reporter, false);
    } else if (job.action === "remove") {
      await stopProject(job, reporter, true);
    }
    await reporter.finish("succeeded");
    log("job", job.deploymentId, "succeeded");
  } catch (error) {
    reporter.write("ERROR: " + error.message);
    await reporter.finish("failed", error.message);
    log("job", job.deploymentId, "failed:", error.message);
  }
}

// ---------------------------------------------------------------- main loop
let busy = false;

async function tick() {
  try {
    const info = await machineInfo();
    const res = await api("/api/agent/heartbeat", info);
    const jobs = res.jobs || [];
    if (jobs.length > 0 && !busy) {
      busy = true;
      for (const job of jobs) {
        await handleJob(job);
      }
      busy = false;
    }
  } catch (error) {
    log("heartbeat failed:", error.message);
  }
}

async function main() {
  await mkdir(WORK, { recursive: true });
  const info = await machineInfo();
  try {
    const hello = await api("/api/agent/hello", info);
    log("connected to", URL_BASE, "as node", hello.nodeId);
  } catch (error) {
    log("hello failed:", error.message, "- retrying in the heartbeat loop");
  }
  await tick();
  setInterval(() => void tick(), 20000);
}

process.on("SIGINT", () => {
  log("shutting down (containers keep running)");
  process.exit(0);
});

void main();
`;

/** Linux/macOS enrolment: installs the agent as a systemd service when possible. */
export const INSTALL_SH = String.raw`#!/usr/bin/env bash
set -euo pipefail

if [ -z "$RXH_URL" ]; then echo "RXH_URL is not set"; exit 1; fi
if [ -z "$RXH_TOKEN" ]; then echo "RXH_TOKEN is not set"; exit 1; fi

INSTALL_DIR="/opt/redxaihost"
echo "RedXAIHost agent installer"

if ! command -v node >/dev/null 2>&1; then
  echo "Installing Node.js 20..."
  curl -fsSL https://deb.nodesource.com/setup_20.x | bash - >/dev/null 2>&1 || true
  apt-get install -y nodejs >/dev/null 2>&1 || {
    echo "Could not install Node.js automatically. Install Node 18+ and re-run."; exit 1; }
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "Installing Docker..."
  curl -fsSL https://get.docker.com | sh
fi

mkdir -p "$INSTALL_DIR"
curl -fsSL "$RXH_URL/api/agent/agent.mjs" -o "$INSTALL_DIR/redxaihost-agent.mjs"

cat >/etc/systemd/system/redxaihost-agent.service <<EOF
[Unit]
Description=RedXAIHost node agent
After=network-online.target docker.service
Wants=network-online.target

[Service]
Environment=RXH_URL=$RXH_URL
Environment=RXH_TOKEN=$RXH_TOKEN
ExecStart=$(command -v node) $INSTALL_DIR/redxaihost-agent.mjs
Restart=always
RestartSec=5
User=root

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now redxaihost-agent
echo "Agent installed. Logs: journalctl -u redxaihost-agent -f"
`;

/** Windows enrolment: registers a scheduled task that keeps the agent running. */
export const INSTALL_PS1 = String.raw`# RedXAIHost agent installer (Windows)
$ErrorActionPreference = "Stop"

if (-not $env:RXH_URL -or -not $env:RXH_TOKEN) {
  Write-Error "Set RXH_URL and RXH_TOKEN before running this installer."
  exit 1
}

$installDir = Join-Path $env:LOCALAPPDATA "RedXAIHost"
New-Item -ItemType Directory -Force -Path $installDir | Out-Null

Write-Host "Downloading agent..."
Invoke-WebRequest -Uri "$($env:RXH_URL)/api/agent/agent.mjs" -OutFile (Join-Path $installDir "redxaihost-agent.mjs")

if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
  Write-Warning "Node.js 18+ is required. Install it from https://nodejs.org and re-run this installer."
  exit 1
}
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
  Write-Warning "Docker Desktop is required to build and run workloads. Install it from https://docs.docker.com/desktop/install/windows-install/"
}

# Persist credentials for the scheduled task.
[Environment]::SetEnvironmentVariable("RXH_URL", $env:RXH_URL, "User")
[Environment]::SetEnvironmentVariable("RXH_TOKEN", $env:RXH_TOKEN, "User")

$agentPath = Join-Path $installDir "redxaihost-agent.mjs"
$taskArgument = '"' + $agentPath + '"'
$action = New-ScheduledTaskAction -Execute "node" -Argument $taskArgument -WorkingDirectory $installDir
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -AllowStartIfOnBatteries
Register-ScheduledTask -TaskName "RedXAIHost Agent" -Action $action -Trigger $trigger -Settings $settings -Force | Out-Null

Start-ScheduledTask -TaskName "RedXAIHost Agent"
Write-Host "Agent installed and started. It will run automatically at every logon."
`;
