import { defineConfig } from "vite";
import path from "node:path";
import electron from "vite-plugin-electron/simple";
import ports from "../../__ports.cjs";

const panelUrl = process.env.VITE_WEBSITE_URL || process.env.WEBSITE_URL || "";

export default defineConfig({
  define: {
    "process.env.REDX_DESKTOP_PANEL_URL": JSON.stringify(panelUrl),
  },
  build: {
    rollupOptions: {
      input: path.join(__dirname, "electron/__no-renderer.ts"),
    },
  },
  plugins: [
    electron({
      main: {
        entry: "electron/main.ts",
      },
      preload: {
        input: path.join(__dirname, "electron/preload.ts"),
      },
    }),
  ],
  server: {
    port: ports.desktop,
    strictPort: true,
    allowedHosts: true,
  },
});
