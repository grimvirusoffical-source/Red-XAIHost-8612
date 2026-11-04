export const HOST_WORKSPACE_ROUTES = [
  { key: "overview", href: "/", label: "Overview" },
  { key: "nodes", href: "/nodes", label: "Nodes" },
  { key: "projects", href: "/projects", label: "Projects" },
  { key: "domains", href: "/domains", label: "Domains" },
  { key: "usage", href: "/usage", label: "Usage" },
  { key: "builds", href: "/builds", label: "Builds" },
  { key: "activity", href: "/activity", label: "Activity" },
  { key: "settings", href: "/settings", label: "Host settings" },
] as const;

export function siteEmbedRequested(search: string): boolean {
  return new URLSearchParams(search).get("embed") === "site";
}

/** Retain the initial presentation mode when SPA links omit their search string. */
export function createHostEmbedMode(initialSearch: string) {
  const site = siteEmbedRequested(initialSearch);
  return Object.freeze({
    site,
    href(path: string): string {
      if (!site) return path;
      if (!path.startsWith("/") || path.startsWith("//")) throw new Error("Host navigation requires a local path");
      const url = new URL(path, "https://host.invalid");
      url.searchParams.set("embed", "site");
      return url.pathname + url.search + url.hash;
    },
  });
}

export const HOST_EMBED_MODE = createHostEmbedMode(
  typeof window === "undefined" ? "" : window.location.search,
);
