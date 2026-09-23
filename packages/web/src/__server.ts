import app from "./api";
import { startLocalWorker } from "./api/lib/local-worker";

const port = Number(process.env.PORT ?? 4200);
const host = process.env.REDX_BIND_HOST ?? "127.0.0.1";
const distDir = `${import.meta.dirname}/../dist`;
const indexPath = `${distDir}/index.html`;

const server = Bun.serve({
  port,
  hostname: host,
  async fetch(request) {
    const url = new URL(request.url);

    if (url.pathname.startsWith("/api")) {
      return app.fetch(request);
    }

    const filePath = getStaticFilePath(url.pathname);
    const file = Bun.file(filePath);

    if (await file.exists()) {
      return new Response(file);
    }

    const index = Bun.file(indexPath);
    if (await index.exists()) {
      return new Response(index, {
        headers: { "Content-Type": "text/html; charset=utf-8" },
      });
    }

    return new Response("Build output not found. Run bun run build first.", {
      status: 500,
      headers: { "Content-Type": "text/plain; charset=utf-8" },
    });
  },
});

const localControlUrl = `http://127.0.0.1:${server.port}`;
console.log(`RedXAIHost panel listening on http://${host}:${server.port}`);

let localWorker: Awaited<ReturnType<typeof startLocalWorker>> = null;
try {
  localWorker = await startLocalWorker(localControlUrl);
  if (localWorker) console.log("[local-worker] starting on this computer");
} catch (error) {
  console.error("[local-worker] bootstrap failed:", error);
}

for (const signal of ["SIGINT", "SIGTERM"] as const) {
  process.on(signal, () => {
    try {
      localWorker?.kill();
    } catch {}
    process.exit(0);
  });
}

function getStaticFilePath(pathname: string) {
  const cleanPath = decodeURIComponent(pathname).replace(/^\/+/, "").replaceAll("..", "");
  return cleanPath ? `${distDir}/${cleanPath}` : indexPath;
}
