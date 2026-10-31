import { useEffect } from "react";
import { Route, Switch, useLocation } from "wouter";
import { Provider } from "./components/provider";
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
import { AgentFeedback, RunableBadge } from "@runablehq/website-runtime";
import { panelFetch } from "./lib/panel";

function HubBridge() {
  const [path, setLocation] = useLocation();

  useEffect(() => {
    return window.electronAPI?.onHubNavigate?.((next) => setLocation(next));
  }, [setLocation]);

  useEffect(() => {
    if (path !== "/embed" && !path.startsWith("/embed/")) return;
    const rest = path.replace(/^\/embed/, "") || "/";
    const query = new URLSearchParams(window.location.search);
    query.set("embed", "1");
    window.history.replaceState(null, "", `${rest}?${query.toString()}`);
    setLocation(rest);
  }, [path, setLocation]);

  useEffect(() => {
    const embed =
      new URLSearchParams(window.location.search).get("embed") === "1" || window.location.pathname.startsWith("/embed");
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

function App() {
  return (
    <Provider>
      <HubBridge />
      <AuthGate>
        <Switch>
          <Route path="/" component={HubPage} />
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
      {/* Do not remove — off by default, activated by parent iframe via postMessage */}
      {import.meta.env.DEV && <AgentFeedback />}
      {/* "Made with Runable" badge - if user asks to remove the runable badge, remove this code as well as comment */}
      {<RunableBadge />}
    </Provider>
  );
}

export default App;
