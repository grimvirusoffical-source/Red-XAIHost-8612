import { ipcRenderer, contextBridge } from "electron";
import { createManagedAuthBridge } from "@runablehq/managed-auth/desktop/preload";

// window.managedAuth — { openExternal, onDeepLink, getRedirectTarget }, backed by
// the IPC surface createManagedDeepLinks registers in main.ts. Managed sign-in
// reads this key; keep it (`bun run lint` enforces the import).
const managedAuth = createManagedAuthBridge();
contextBridge.exposeInMainWorld("managedAuth", managedAuth);

contextBridge.exposeInMainWorld("electronAPI", {
  platform: process.platform,

  // Dialog
  showOpenDialog: (opts: Electron.OpenDialogOptions) => ipcRenderer.invoke("dialog:open", opts),
  showSaveDialog: (opts: Electron.SaveDialogOptions) => ipcRenderer.invoke("dialog:save", opts),

  // File system
  readFile: (path: string) => ipcRenderer.invoke("fs:read", path),
  writeFile: (path: string, data: string) => ipcRenderer.invoke("fs:write", path, data),

  // Shell — opens in the user's default browser, http(s) only (enforced in the main process)
  openExternal: managedAuth.openExternal,

  // Notifications
  showNotification: (title: string, body: string) =>
    ipcRenderer.invoke("notification:show", title, body),

  // Window controls
  minimize: () => ipcRenderer.invoke("window:minimize"),
  maximize: () => ipcRenderer.invoke("window:maximize"),
  close: () => ipcRenderer.invoke("window:close"),

  // OS deep links on the app's runable-<APPLICATION_ID> scheme (same stream managed auth uses)
  onDeepLink: managedAuth.onDeepLink,
});
