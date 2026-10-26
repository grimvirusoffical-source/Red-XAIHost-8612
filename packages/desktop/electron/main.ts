import { app, BrowserWindow, dialog, shell } from "electron";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createManagedDeepLinks } from "@runablehq/managed-auth/desktop/main";
import electronUpdater from "electron-updater";
import { registerIpcHandlers } from "./ipc";
import { parseHubLink } from "../../web/src/api/lib/hub.ts";

// Fully editable Electron main process — own the window, lifecycle, menus, tray,
// and IPC (starter handlers in ./ipc.ts). One platform call is enforced by
// `bun run lint`: createManagedDeepLinks. It registers the app's
// runable-<APPLICATION_ID> deep-link protocol, forwards deep links to the
// renderer, and backs managed sign-in (skills/app/references/desktop.md).

const __dirname = path.dirname(fileURLToPath(import.meta.url));

const isDev = process.env.NODE_ENV !== "production";
// The panel UI is the web package. In dev it is served on the fixed website port
// (4200, see __ports.cjs); WEBSITE_URL overrides it when the shell should point at
// a hosted panel instead of the local one.
const WEB_DEV_URL = process.env.WEBSITE_URL ?? "http://localhost:4200";
const WEB_DIST = path.join(__dirname, "../web-dist");
// Baked in by Vite from VITE_WEBSITE_URL so a packaged shell opens the panel
// over http(s). Cookies for Red-XAI Accounts then belong to that origin.
const PACKAGED_PANEL_URL = process.env.REDX_DESKTOP_PANEL_URL ?? "";

const { autoUpdater } = electronUpdater;

let win: BrowserWindow | null = null;
let pendingHubPath: string | null = null;
const getWindow = () => win;

function deliverHubLink(raw: string) {
  const next = parseHubLink(raw);
  if (!next) return;
  const target = getWindow();
  if (!target) {
    pendingHubPath = next;
    return;
  }
  pendingHubPath = null;
  target.webContents.send("hub:navigate", next);
  if (target.isMinimized()) target.restore();
  target.focus();
}

function deliverArgv(argv: string[]) {
  for (const arg of argv) {
    if (arg.startsWith("redxaihost:")) deliverHubLink(arg);
  }
}

const deepLinks = createManagedDeepLinks({
  applicationId: process.env.APPLICATION_ID,
  getWindow,
});

function createWindow() {
  win = new BrowserWindow({
    title: "RedXAIHost",
    width: 1440,
    height: 940,
    minWidth: 1040,
    minHeight: 680,
    backgroundColor: "#0a0b0d",
    autoHideMenuBar: true,
    webPreferences: {
      preload: path.join(__dirname, "preload.mjs"),
      contextIsolation: true,
      nodeIntegration: false,
    },
  });

  // Anything the panel opens for the owner (Cloudflare dashboard, CI run, a live
  // site) belongs in their real browser, not in a frameless Electron window.
  win.webContents.setWindowOpenHandler(({ url }) => {
    if (url.startsWith("http://") || url.startsWith("https://")) void shell.openExternal(url);
    return { action: "deny" };
  });

  win.webContents.on("did-finish-load", () => {
    if (!pendingHubPath || !win) return;
    win.webContents.send("hub:navigate", pendingHubPath);
    pendingHubPath = null;
  });

  if (isDev) {
    win.loadURL(WEB_DEV_URL);
  } else if (PACKAGED_PANEL_URL.startsWith("http://") || PACKAGED_PANEL_URL.startsWith("https://")) {
    win.loadURL(PACKAGED_PANEL_URL);
  } else {
    win.loadFile(path.join(WEB_DIST, "index.html"));
  }
}

/**
 * In-app updates. The installed panel checks the GitHub release feed configured in
 * electron-builder.json5, downloads in the background, and installs on quit once the
 * owner agrees — so improvements land without them re-downloading an installer.
 */
function initAutoUpdates() {
  if (isDev || !app.isPackaged) return;

  autoUpdater.autoDownload = true;
  autoUpdater.autoInstallOnAppQuit = true;

  autoUpdater.on("update-downloaded", async (info) => {
    const target = getWindow();
    const options = {
      type: "info" as const,
      buttons: ["Restart now", "Later"],
      defaultId: 0,
      cancelId: 1,
      title: "RedXAIHost update ready",
      message: `Version ${info.version} is downloaded.`,
      detail:
        "Restarting takes a few seconds. Your nodes and hosted projects keep running — they do not depend on this window.",
    };
    const { response } = target
      ? await dialog.showMessageBox(target, options)
      : await dialog.showMessageBox(options);
    if (response === 0) autoUpdater.quitAndInstall();
  });

  autoUpdater.on("error", (error) => {
    console.error("[updater]", error instanceof Error ? error.message : error);
  });

  void autoUpdater.checkForUpdates();
  // Re-check every six hours for a panel the owner leaves open.
  setInterval(() => void autoUpdater.checkForUpdates(), 6 * 60 * 60 * 1000);
}

registerIpcHandlers(getWindow);

app.on("window-all-closed", () => {
  if (process.platform !== "darwin") {
    app.quit();
    win = null;
  }
});

app.on("activate", () => {
  if (BrowserWindow.getAllWindows().length === 0) {
    createWindow();
  }
});

// Windows/Linux deliver deep links as argv — of a second instance while the app
// is running, of this instance on cold start. Keep one instance and forward both.
// macOS delivers redxaihost:// through open-url, including a cold start.
app.on("open-url", (event, url) => {
  event.preventDefault();
  deliverHubLink(url);
});

if (process.defaultApp) {
  if (process.argv.length >= 2 && process.argv[1]) {
    app.setAsDefaultProtocolClient("redxaihost", process.execPath, [path.resolve(process.argv[1])]);
  }
} else {
  app.setAsDefaultProtocolClient("redxaihost");
}

if (app.requestSingleInstanceLock()) {
  app.on("second-instance", (_event, argv) => {
    deepLinks.handleArgv(argv);
    deliverArgv(argv);
  });
  app.whenReady().then(() => {
    createWindow();
    deepLinks.handleArgv(process.argv);
    deliverArgv(process.argv);
    initAutoUpdates();
  });
} else {
  app.quit();
}
