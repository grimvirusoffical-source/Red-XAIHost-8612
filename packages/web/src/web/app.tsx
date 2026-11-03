import { useEffect } from "react";
import { Route, Router, Switch, useLocation } from "wouter";
import { AuthGate } from "./components/auth-gate";
import HubPage from "./pages/hub";
import Dashboard from "./pages/index";
import Nodes from "./pages/nodes";
import Projects from "./pages/projects";
import ProjectDetail from "./pages/project";
import Domains from "./pages/domains";
import Usage from "./pages/usage";
import Builds from "./pages/builds";
import ActivityPage from "./pages/activity";
import SettingsPage from "./pages/settings";
import VortexPage from "./pages/vortex";
import DatabasePage from "./pages/database";
import { PANEL_MOUNT, panelFetch } from "./lib/panel";
import { HOST_EMBED_MODE, siteEmbedRequested } from "./lib/site-embed";

function HubBridge() {
  const [path, setLocation] = useLocation();

  useEffect(() => {
    if (!HOST_EMBED_MODE.site) return;
    const applyTheme = (theme: string | null) => {
      document.documentElement.dataset.hostTheme = theme === "light" ? "light" : "dark";
    };
    let observer: MutationObserver | undefined;
    // The production website and Host share an origin. Appearance follows the
    // site's theme, while authentication remains entirely server-validated.
    try {
      const parentRoot = window.parent.document.documentElement;
      applyTheme(parentRoot.getAttribute("data-theme"));
      observer = new MutationObserver(() => applyTheme(parentRoot.getAttribute("data-theme")));
      observer.observe(parentRoot, { attributes: true, attributeFilter: ["data-theme"] });
    } catch {
      applyTheme(null);
    }
    return () => observer?.disconnect();
  }, []);

  useEffect(() => {
    return window.electronAPI?.onHubNavigate?.((next) => setLocation(next));
  }, [setLocation]);

  useEffect(() => {
    if (!HOST_EMBED_MODE.site || siteEmbedRequested(window.location.search)) return;
    const current = window.location.pathname + window.location.search + window.location.hash;
    window.history.replaceState(null, "", HOST_EMBED_MODE.href(current));
  }, [path]);

  useEffect(() => {
    if (path !== "/embed" && !path.startsWith("/embed/")) return;
    const rest = path.replace(/^\/embed/, "") || "/";
    const query = new URLSearchParams(window.location.search);
    query.set("embed", HOST_EMBED_MODE.site ? "site" : "1");
    window.history.replaceState(null, "", `${PANEL_MOUNT}${rest}?${query.toString()}`);
    setLocation(rest);
  }, [path, setLocation]);

  useEffect(() => {
    const embed =
      HOST_EMBED_MODE.site || new URLSearchParams(window.location.search).get("embed") === "1" || window.location.pathname.startsWith(`${PANEL_MOUNT}/embed`);
    if (!embed || window.parent === window) return;
    let cancelled = false;
    void panelFetch("/api/hub")
      .then((response) => response.json())
      .then((hub: { frameAncestors?: string[] }) => {
        if (cancelled) return;
        for (const origin of hub.frameAncestors ?? []) {
          window.parent.postMessage({ source: "redxaihost", type: "ready", launchProtocol: "redxaihost" }, origin);
        }
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  return null;
}

function AppContent() {
  return (
    <>
      <HubBridge />
      <AuthGate>
        <Switch>
          <Route path="/" component={PANEL_MOUNT || HOST_EMBED_MODE.site ? Dashboard : HubPage} />
          <Route path="/host" component={Dashboard} />
          <Route path="/nodes" component={Nodes} />
          <Route path="/projects" component={Projects} />
          <Route path="/projects/:id" component={ProjectDetail} />
          <Route path="/domains" component={Domains} />
          <Route path="/usage" component={Usage} />
          <Route path="/builds" component={Builds} />
          <Route path="/activity" component={ActivityPage} />
          <Route path="/settings" component={SettingsPage} />
          <Route path="/db" component={DatabasePage} />
          <Route path="/vortex" component={VortexPage} />
          <Route>
            <div className="py-24 text-center text-sm text-muted-foreground">
              Nothing here. Pick a section from the left.
            </div>
          </Route>
        </Switch>
      </AuthGate>
    </>
  );
}

function App() {
  return <Router base={PANEL_MOUNT}><AppContent /></Router>;
}

export default App;
