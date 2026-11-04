const VORTEX = "https://45.77.105.187/vortex/";
const CLOUD_PC = "https://45.77.105.187/vortex/cloudpc/";

/** Leave the mounted Host router when opening another website workspace. */
export function hostSidebarLink(href: string, mount: string) {
  if (!mount) return { href, browserNavigation: false };
  if (href === "/" || href === "/db") return { href, browserNavigation: true };
  if (href === "/host") return { href: "/", browserNavigation: false };
  if (href === "/vortex" || href === "/cloud-pc") {
    return {
      href: href === "/vortex" ? VORTEX : CLOUD_PC,
      browserNavigation: true,
      target: "_blank" as const,
      rel: "noopener noreferrer" as const,
    };
  }
  return { href, browserNavigation: false };
}
