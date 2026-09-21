import { Route, Switch } from "wouter";
import { Provider } from "./components/provider";
import { AuthGate } from "./components/auth-gate";
import Dashboard from "./pages/index";
import Nodes from "./pages/nodes";
import Projects from "./pages/projects";
import ProjectDetail from "./pages/project";
import Domains from "./pages/domains";
import Usage from "./pages/usage";
import Builds from "./pages/builds";
import ActivityPage from "./pages/activity";
import SettingsPage from "./pages/settings";
import { AgentFeedback, RunableBadge } from "@runablehq/website-runtime";

function App() {
  return (
    <Provider>
      <AuthGate>
        <Switch>
          <Route path="/" component={Dashboard} />
          <Route path="/nodes" component={Nodes} />
          <Route path="/projects" component={Projects} />
          <Route path="/projects/:id" component={ProjectDetail} />
          <Route path="/domains" component={Domains} />
          <Route path="/usage" component={Usage} />
          <Route path="/builds" component={Builds} />
          <Route path="/activity" component={ActivityPage} />
          <Route path="/settings" component={SettingsPage} />
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
