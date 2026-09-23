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
import { chmod, mkdir, rename, rm, writeFile } from "node:fs/promises";
import { createWriteStream, existsSync, readFileSync } from "node:fs";
import { homedir, arch, cpus, freemem, platform, totalmem, hostname } from "node:os";
import { join } from "node:path";
import { pipeline } from "node:stream/promises";
import { Readable } from "node:stream";
import { createConnection } from "node:net";

const AGENT_VERSION = "1.3.0";
const URL_BASE = (process.env.RXH_URL || "").replace(/\/+$/, "");
const TOKEN = process.env.RXH_TOKEN || "";
const ROOT = process.env.RXH_HOME || join(homedir(), ".redxaihost");
const WORK = join(ROOT, "projects");
const BIN = join(ROOT, "bin");
const STATE = join(ROOT, "state");
const CLOUDFLARED_LOCAL = join(BIN, platform() === "win32" ? "cloudflared.exe" : "cloudflared");
const STATIC_SERVER = join(BIN, "static-server.mjs");

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
    cloudflaredAvailable: (await has("cloudflared")) || existsSync(CLOUDFLARED_LOCAL) || (await has("docker")),
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

// ------------------------------------------------- docker-free process tracking
/**
 * Docker is optional. A home PC serving a static site should not have to
 * install Docker Desktop first, so when Docker is missing the agent runs the
 * workload as a plain detached child process instead. The pid is remembered on
 * disk so stop and restart still reach it after the agent itself restarts.
 */
function stateFile(slug) {
  return join(STATE, slug + ".json");
}

function readTracked(slug) {
  try {
    const file = stateFile(slug);
    if (!existsSync(file)) return null;
    return JSON.parse(readFileSync(file, "utf8"));
  } catch {
    return null;
  }
}

async function writeTracked(slug, info) {
  await mkdir(STATE, { recursive: true });
  await writeFile(stateFile(slug), JSON.stringify(info), "utf8");
}

function pidAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return error.code === "EPERM";
  }
}

/** Kills a tracked native process and any children it spawned. */
async function stopTracked(slug, reporter) {
  const tracked = readTracked(slug);
  await rm(stateFile(slug), { force: true });
  if (!tracked || !tracked.pid || !pidAlive(tracked.pid)) return false;
  if (platform() === "win32") {
    await run("taskkill", ["/PID", String(tracked.pid), "/T", "/F"]);
  } else {
    try {
      process.kill(-tracked.pid, "SIGTERM");
    } catch {
      try {
        process.kill(tracked.pid, "SIGTERM");
      } catch {}
    }
  }
  if (reporter) reporter.write("stopped pid " + tracked.pid + " (" + slug + ")");
  return true;
}

/** The no-Docker static file server, fetched from the panel so it stays current. */
async function ensureStaticServer(reporter) {
  await mkdir(BIN, { recursive: true });
  const res = await fetch(URL_BASE + "/api/agent/static-server.mjs");
  if (!res.ok) throw new Error("could not fetch the static server: " + res.status);
  await writeFile(STATIC_SERVER, await res.text(), "utf8");
  reporter.write("using the built-in Node static server (no Docker needed)");
  return STATIC_SERVER;
}

function childEnv(project, port) {
  const persistRoot = join(ROOT, "data", project.slug);
  return Object.assign({}, process.env, project.envVars || {}, {
    PORT: String(port),
    REDX_PERSIST_ROOT: persistRoot,
  });
}

/** Runs the workload without Docker. Static sites always work; others need a start command. */
async function deployNative(job, reporter, dir) {
  const project = job.project;
  const port = project.port || 8080;
  await stopTracked(project.slug, reporter);

  if (project.runtime === "bun" && !(await has("bun"))) {
    throw new Error("This Bun project needs Bun installed on the selected node. Install Bun, restart the RedXAIHost agent, then redeploy.");
  }
  if (project.runtime === "node" && !(await has("node"))) {
    throw new Error("This Node project needs Node.js installed on the selected node.");
  }
  if (project.runtime === "python" && !(await has("python3")) && !(await has("python"))) {
    throw new Error("This Python project needs Python installed on the selected node.");
  }

  if (project.runtime === "static") {
    const server = await ensureStaticServer(reporter);
    const child = spawn(process.execPath, [server, dir, String(port)], {
      cwd: dir,
      detached: true,
      stdio: "ignore",
      shell: false,
      env: childEnv(project, port),
    });
    child.unref();
    if (!child.pid) throw new Error("could not start the static server");
    await writeTracked(project.slug, { pid: child.pid, port, kind: "static", startedAt: Date.now() });
    reporter.write("serving " + project.slug + " on :" + port + " (pid " + child.pid + ")");
    return;
  }

  if (!project.startCommand) {
    throw new Error(
      "Docker is not installed on this node, so a " +
        project.runtime +
        " project needs a start command. Add one to the project, or install Docker to build it in a container.",
    );
  }
  if (project.installCommand) {
    reporter.write("$ " + project.installCommand);
    if (!(await streamCommand(project.installCommand, [], dir, reporter, true))) {
      throw new Error("install command failed");
    }
  }
  if (project.buildCommand) {
    reporter.write("$ " + project.buildCommand);
    if (!(await streamCommand(project.buildCommand, [], dir, reporter, true))) {
      throw new Error("build command failed");
    }
  }
  reporter.write("$ " + project.startCommand);
  const child = spawn(project.startCommand, {
    cwd: dir,
    detached: true,
    stdio: "ignore",
    shell: true,
    env: childEnv(project, port),
  });
  child.unref();
  if (!child.pid) throw new Error("start command did not launch");
  await writeTracked(project.slug, { pid: child.pid, port, kind: "process", startedAt: Date.now() });
  reporter.write("started " + project.slug + " on :" + port + " (pid " + child.pid + ")");
}

function staticDockerfile(port) {
  return [
    "FROM nginx:alpine",
    "COPY . /usr/share/nginx/html",
    "RUN printf 'server { listen " + port + "; root /usr/share/nginx/html; index index.html; location / { try_files $uri $uri/ /index.html; } }' > /etc/nginx/conf.d/default.conf",
    "EXPOSE " + port,
  ].join("\n");
}

async function streamCommand(cmd, args, cwd, reporter, useShell) {
  return new Promise((resolve) => {
    const child = spawn(cmd, args, { cwd, shell: useShell === true || platform() === "win32" });
    child.stdout.on("data", (data) => reporter.write(String(data).trimEnd()));
    child.stderr.on("data", (data) => reporter.write(String(data).trimEnd()));
    child.on("close", (code) => resolve(code === 0));
    child.on("error", (error) => {
      reporter.write("spawn error: " + error.message);
      resolve(false);
    });
  });
}

async function waitForPort(port, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const ok = await new Promise((resolve) => {
      const socket = createConnection({ host: "127.0.0.1", port, timeout: 1500 });
      socket.once("connect", () => {
        socket.destroy();
        resolve(true);
      });
      socket.once("timeout", () => {
        socket.destroy();
        resolve(false);
      });
      socket.once("error", () => resolve(false));
    });
    if (ok) return true;
    await new Promise((resolve) => setTimeout(resolve, 750));
  }
  return false;
}

async function deployProject(job, reporter) {
  const project = job.project;
  const dir = join(WORK, project.slug);
  const image = "rxh-" + project.slug;
  const container = "rxh-" + project.slug;
  const port = Number(project.port || 8080);

  await reporter.progress("building");
  reporter.write("=== deploy " + project.name + " (" + project.runtime + ") ===");

  const dockerAvailable = await has("docker");
  const needsDocker =
    project.runtime === "docker" ||
    project.runtime === "database" ||
    Boolean(project.dockerfile);
  if (needsDocker && !dockerAvailable) {
    throw new Error(
      "This project explicitly requires Docker, but Docker is not available on this node. " +
      "Static, Node, Bun, Python and custom projects can run without Docker when install/build/start commands are configured.",
    );
  }
  reporter.write(
    needsDocker
      ? "deployment mode: Docker"
      : "deployment mode: native process (Docker is optional for this runtime)",
  );

  // Native processes may keep their working directory locked on Windows.
  // Stop any tracked workload before replacing the checkout; otherwise restart
  // can fail with EBUSY while trying to remove the project directory.
  await stopTracked(project.slug, reporter);
  await rm(dir, { recursive: true, force: true });
  await mkdir(dir, { recursive: true });

  if (project.sourceKind === "git" && project.gitUrl) {
    if (!(await has("git"))) throw new Error("Git is required for Git-backed projects on this node.");
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

  if (!needsDocker) {
    await deployNative(job, reporter, dir);
  } else {
    const dockerfile = project.dockerfile;
    if (!dockerfile) throw new Error("Docker projects require a Dockerfile or generated Docker plan.");
    await writeFile(join(dir, "Dockerfile.redxaihost"), dockerfile, "utf8");
    reporter.write("wrote Dockerfile.redxaihost");

    const built = await streamCommand("docker", ["build", "-f", "Dockerfile.redxaihost", "-t", image, "."], dir, reporter);
    if (!built) throw new Error("docker build failed");

    await run("docker", ["rm", "-f", container]);

    const portText = String(port);
    const args = ["run", "-d", "--name", container, "--restart", "unless-stopped", "-p", portText + ":" + portText, "-e", "PORT=" + portText];
    const env = project.envVars || {};
    for (const key of Object.keys(env)) {
      args.push("-e", key + "=" + env[key]);
    }
    args.push(image);
    const started = await streamCommand("docker", args, dir, reporter);
    if (!started) throw new Error("docker run failed");
    reporter.write("container " + container + " requested on :" + portText);
  }

  if (!await waitForPort(port, 30000)) {
    throw new Error("Workload started but did not open port " + port + " within 30 seconds.");
  }
  reporter.write("health check passed on 127.0.0.1:" + port);

  for (const tunnel of job.tunnels || []) {
    if (!tunnel.connectorToken) {
      reporter.write("tunnel " + tunnel.hostname + " has no connector token yet - skipping");
      continue;
    }
    await startTunnel(tunnel, reporter);
  }
  return true;
}

/**
 * Cloudflare, without the owner installing anything: use cloudflared if it is
 * on PATH, otherwise fetch the official static binary into ~/.redxaihost/bin
 * once and reuse it, and only fall back to the Docker image if neither works.
 */
async function ensureCloudflared(reporter) {
  if (await has("cloudflared")) return "cloudflared";
  if (existsSync(CLOUDFLARED_LOCAL)) return CLOUDFLARED_LOCAL;

  // Cloudflare publishes raw binaries for Linux and Windows, but ships macOS as a .tgz.
  const cpu = arch() === "arm64" ? "arm64" : arch() === "arm" ? "arm" : "amd64";
  const base = "https://github.com/cloudflare/cloudflared/releases/latest/download/";
  let url = null;
  let archive = false;
  if (platform() === "win32") url = base + "cloudflared-windows-" + (cpu === "arm64" ? "amd64" : cpu) + ".exe";
  else if (platform() === "linux") url = base + "cloudflared-linux-" + cpu;
  else if (platform() === "darwin") {
    url = base + "cloudflared-darwin-" + (cpu === "arm" ? "amd64" : cpu) + ".tgz";
    archive = true;
  }
  if (!url) return null;

  try {
    reporter.write("cloudflared not found - downloading it from Cloudflare");
    await mkdir(BIN, { recursive: true });
    await rm(CLOUDFLARED_LOCAL, { force: true });
    if (archive) {
      const tgz = join(BIN, "cloudflared.tgz");
      await download(url, tgz);
      await extract(tgz, BIN, reporter);
      await rm(tgz, { force: true });
      if (!existsSync(CLOUDFLARED_LOCAL)) return null;
      await chmod(CLOUDFLARED_LOCAL, 0o755);
    } else {
      const tmp = CLOUDFLARED_LOCAL + ".part";
      await download(url, tmp);
      if (platform() !== "win32") await chmod(tmp, 0o755);
      await rename(tmp, CLOUDFLARED_LOCAL);
      if (!existsSync(CLOUDFLARED_LOCAL)) return null;
    }
    const version = await run(CLOUDFLARED_LOCAL, ["--version"]);
    reporter.write("cloudflared ready: " + (version.stdout || version.stderr).trim().slice(0, 120));
    return CLOUDFLARED_LOCAL;
  } catch (error) {
    reporter.write("cloudflared download failed: " + error.message);
    return null;
  }
}

async function startTunnel(tunnel, reporter) {
  const name = "rxh-tunnel-" + tunnel.tunnelName;
  const binary = await ensureCloudflared(reporter);
  if (binary) {
    // Every restart used to spawn another connector and leave the old one
    // running, so a few redeploys left several cloudflared processes all
    // answering for the same hostname. Track it like any other native process
    // and replace it instead.
    await stopTracked(name, reporter);
    reporter.write("starting cloudflared for " + tunnel.hostname);
    const child = spawn(binary, ["tunnel", "--no-autoupdate", "run", "--token", tunnel.connectorToken], {
      detached: true,
      stdio: "ignore",
      shell: false,
    });
    child.unref();
    await writeTracked(name, {
      pid: child.pid,
      kind: "tunnel",
      hostname: tunnel.hostname,
      startedAt: Date.now(),
    });
    return;
  }
  if (!(await has("docker"))) {
    reporter.write("no cloudflared and no docker on this node - " + tunnel.hostname + " stays offline");
    return;
  }
  await run("docker", ["rm", "-f", name]);
  const args = ["run", "-d", "--name", name, "--restart", "unless-stopped"];
  if (platform() === "linux") args.push("--network", "host");
  args.push("cloudflare/cloudflared:latest", "tunnel", "--no-autoupdate", "run", "--token", tunnel.connectorToken);
  const started = await streamCommand("docker", args, WORK, reporter);
  reporter.write(started ? "cloudflared container up for " + tunnel.hostname : "cloudflared container failed");
}

async function stopProject(job, reporter, removeImage) {
  const slug = job.project.slug;
  const container = "rxh-" + slug;
  await reporter.progress("building");
  reporter.write("stopping " + slug);
  // Either mode may have started it, so clear both rather than guessing.
  await stopTracked(slug, reporter);
  if (await has("docker")) {
    await run("docker", ["rm", "-f", container]);
    if (removeImage) await run("docker", ["rmi", "-f", container]);
  }
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
  await mkdir(BIN, { recursive: true });
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

/**
 * The Docker-free static file server, served at /api/agent/static-server.mjs.
 *
 * Nothing about serving a folder of HTML needs a container, so when a node has
 * no Docker the agent downloads this and runs it with the Node it already has.
 * Zero dependencies, read-only, GET/HEAD only, and it refuses to walk out of
 * the project directory.
 */
export const STATIC_SERVER_SOURCE = String.raw`#!/usr/bin/env node
// RedXAIHost static server. Usage: node static-server.mjs <root> <port>
import { createServer } from "node:http";
import { createReadStream, existsSync, statSync } from "node:fs";
import { extname, join, normalize, resolve } from "node:path";

const root = resolve(process.argv[2] || ".");
const port = Number(process.argv[3] || 8080);

const TYPES = {
  ".html": "text/html; charset=utf-8",
  ".htm": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".map": "application/json; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".gif": "image/gif",
  ".webp": "image/webp",
  ".avif": "image/avif",
  ".ico": "image/x-icon",
  ".woff": "font/woff",
  ".woff2": "font/woff2",
  ".ttf": "font/ttf",
  ".otf": "font/otf",
  ".eot": "application/vnd.ms-fontobject",
  ".mp4": "video/mp4",
  ".webm": "video/webm",
  ".mp3": "audio/mpeg",
  ".ogg": "audio/ogg",
  ".wasm": "application/wasm",
  ".txt": "text/plain; charset=utf-8",
  ".xml": "application/xml; charset=utf-8",
  ".pdf": "application/pdf",
  ".zip": "application/zip",
};

function safeTarget(urlPath) {
  let clean = String(urlPath || "/").split("?")[0].split("#")[0];
  try {
    clean = decodeURIComponent(clean);
  } catch {}
  if (clean.indexOf("\0") !== -1) return null;
  const rel = normalize(clean).replace(/^[\\/]+/, "");
  const target = resolve(join(root, rel));
  // resolve() has already collapsed any "..", so a target outside root is an escape attempt.
  if (target !== root && target.indexOf(root + (process.platform === "win32" ? "\\" : "/")) !== 0) {
    return null;
  }
  return target;
}

function fileFor(urlPath) {
  const target = safeTarget(urlPath);
  if (!target) return null;
  if (existsSync(target)) {
    const stat = statSync(target);
    if (stat.isFile()) return target;
    if (stat.isDirectory()) {
      const index = join(target, "index.html");
      if (existsSync(index)) return index;
    }
  }
  // Extensionless URLs: try foo.html, then fall back to the SPA entry point.
  if (!extname(target)) {
    const html = target + ".html";
    if (existsSync(html)) return html;
    const spa = join(root, "index.html");
    if (existsSync(spa)) return spa;
  }
  return null;
}

createServer((req, res) => {
  if (req.method !== "GET" && req.method !== "HEAD") {
    res.writeHead(405, { "Content-Type": "text/plain; charset=utf-8", Allow: "GET, HEAD" });
    res.end("method not allowed");
    return;
  }
  const file = fileFor(req.url);
  if (!file) {
    res.writeHead(404, { "Content-Type": "text/plain; charset=utf-8" });
    res.end("not found");
    return;
  }
  let stat;
  try {
    stat = statSync(file);
  } catch {
    res.writeHead(404, { "Content-Type": "text/plain; charset=utf-8" });
    res.end("not found");
    return;
  }
  const headers = {
    "Content-Type": TYPES[extname(file).toLowerCase()] || "application/octet-stream",
    "Content-Length": String(stat.size),
    "X-Content-Type-Options": "nosniff",
    "Last-Modified": stat.mtime.toUTCString(),
  };
  res.writeHead(200, headers);
  if (req.method === "HEAD") {
    res.end();
    return;
  }
  const stream = createReadStream(file);
  stream.on("error", () => res.destroy());
  stream.pipe(res);
}).listen(port, "0.0.0.0", () => {
  console.log("[redxaihost] serving " + root + " on :" + port);
});
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
  echo "Docker not found. That is OK for static, Node, Bun, Python and custom process projects."
  echo "Install Docker later only for Docker/database workloads or projects that explicitly use a Dockerfile."
fi

mkdir -p "$INSTALL_DIR"
curl -fsSL "$RXH_URL/api/agent/agent.mjs" -o "$INSTALL_DIR/redxaihost-agent.mjs"

cat >/etc/systemd/system/redxaihost-agent.service <<EOF
[Unit]
Description=RedXAIHost node agent
After=network-online.target
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

# This script is normally piped into iex, so it must never call exit: that would close
# the owner's PowerShell window and hide the message. Throw instead.
if (-not $env:RXH_URL -or -not $env:RXH_TOKEN) {
  throw "Set RXH_URL and RXH_TOKEN first, then re-run. Copy the full command from the Nodes page."
}

# A trailing slash on the panel URL would build https://host//api/agent/... requests.
$env:RXH_URL = $env:RXH_URL.TrimEnd("/")

if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
  throw "Node.js 18+ is required. Install it from https://nodejs.org, reopen PowerShell, and re-run this installer."
}
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
  Write-Host "Docker Desktop not found. That is OK for static, Node, Bun, Python and custom process projects."
  Write-Host "Install Docker later only for Docker/database workloads or projects that explicitly use a Dockerfile."
}

$installDir = Join-Path $env:LOCALAPPDATA "RedXAIHost"
New-Item -ItemType Directory -Force -Path $installDir | Out-Null

Write-Host "Downloading agent from $($env:RXH_URL) ..."
Invoke-WebRequest -Uri "$($env:RXH_URL)/api/agent/agent.mjs" -OutFile (Join-Path $installDir "redxaihost-agent.mjs") -UseBasicParsing

# Persist credentials for the scheduled task.
[Environment]::SetEnvironmentVariable("RXH_URL", $env:RXH_URL, "User")
[Environment]::SetEnvironmentVariable("RXH_TOKEN", $env:RXH_TOKEN, "User")

$agentPath = Join-Path $installDir "redxaihost-agent.mjs"
$nodePath = (Get-Command node -ErrorAction Stop).Source
$taskArgument = '"' + $agentPath + '"'
$action = New-ScheduledTaskAction -Execute $nodePath -Argument $taskArgument -WorkingDirectory $installDir
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -AllowStartIfOnBatteries
Register-ScheduledTask -TaskName "RedXAIHost Agent" -Action $action -Trigger $trigger -Settings $settings -Force | Out-Null

Start-ScheduledTask -TaskName "RedXAIHost Agent"
Write-Host "Agent installed and started. It will run automatically at every logon."
`;
