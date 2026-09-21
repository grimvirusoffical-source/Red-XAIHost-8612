import { ipcMain, dialog, Notification, type BrowserWindow } from "electron";
import fs from "node:fs/promises";

// Starter IPC handlers backing window.electronAPI (see preload.ts and
// packages/web/src/web/lib/desktop.ts). Fully editable — change, remove, or add
// handlers to fit the app; keep the preload methods and web types in sync.
// openExternal and onDeepLink come from @runablehq/managed-auth (wired in
// preload.ts), not from here.

export function registerIpcHandlers(getWindow: () => BrowserWindow | null) {
  // Dialog
  ipcMain.handle("dialog:open", async (_, opts) => {
    const result = await dialog.showOpenDialog(opts);
    return result.canceled ? [] : result.filePaths;
  });

  ipcMain.handle("dialog:save", async (_, opts) => {
    const result = await dialog.showSaveDialog(opts);
    return result.canceled ? null : result.filePath;
  });

  // File system
  ipcMain.handle("fs:read", async (_, filePath: string) => {
    return fs.readFile(filePath, "utf-8");
  });

  ipcMain.handle("fs:write", async (_, filePath: string, data: string) => {
    await fs.writeFile(filePath, data, "utf-8");
  });

  // Notifications
  ipcMain.handle("notification:show", (_, title: string, body: string) => {
    new Notification({ title, body }).show();
  });

  // Window controls
  ipcMain.handle("window:minimize", () => getWindow()?.minimize());
  ipcMain.handle("window:maximize", () => {
    const win = getWindow();
    if (win?.isMaximized()) {
      win.unmaximize();
    } else {
      win?.maximize();
    }
  });
  ipcMain.handle("window:close", () => getWindow()?.close());
}
