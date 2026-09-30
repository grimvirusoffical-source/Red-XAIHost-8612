import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import tailwind from "@tailwindcss/vite";
import path from "path";
import fs from "fs";
import runableAnalyticsPlugin from "./vite/__plugins/runable-analytics-plugin";
import honoDevPlugin from "./vite/__plugins/hono-dev-plugin";
import assetOptimizerPlugin from "./vite/__plugins/asset-optimizer-plugin";

const root = path.resolve(__dirname, "../..");
const runablePortsPath = path.resolve(root, ".runable/ports.json");

function developmentPort(): number {
  try {
    if (fs.existsSync(runablePortsPath)) {
      const ports = JSON.parse(fs.readFileSync(runablePortsPath, "utf8")) as { website?: number };
      if (Number.isInteger(ports.website) && Number(ports.website) > 0) return Number(ports.website);
    }
  } catch {
    // Production/Railway builds do not include Runable's private preview metadata.
  }
  return Number(process.env.PORT || 4200);
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, root, "");
  Object.assign(process.env, env);

  return {
    // All env files live at the repo root when developing in the full workspace.
    // Railway can safely build packages/web as its own root without requiring
    // Runable-only files outside the service build context.
    envDir: fs.existsSync(root) ? root : __dirname,
    plugins: [
      honoDevPlugin(),
      react(),
      runableAnalyticsPlugin(),
      tailwind(),
      assetOptimizerPlugin(),
    ],
    resolve: {
      alias: {
        "@": path.resolve(__dirname, "./src/web"),
      },
    },
    server: {
      port: developmentPort(),
      strictPort: true,
      allowedHosts: true,
      hmr: { overlay: false },
      cors: false,
    },
  };
});
