const { getDefaultConfig } = require("expo/metro-config");
const { resolve } = require("path");

const config = getDefaultConfig(__dirname);

// ── Workspace module resolution ────────────────────────────────────────
// Metro's project root is packages/mobile/. In a Bun workspace, packages
// installed via `npx expo install` may end up in packages/mobile/node_modules
// while hoisted deps live at the workspace root node_modules/. Tell Metro
// to search both so every install path resolves correctly.
const workspaceRoot = resolve(__dirname, "../..");

config.watchFolders = [workspaceRoot];
config.resolver = {
  ...config.resolver,
  nodeModulesPaths: [resolve(__dirname, "node_modules"), resolve(workspaceRoot, "node_modules")],
};

module.exports = config;
