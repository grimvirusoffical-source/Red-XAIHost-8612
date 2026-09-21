import { useState } from "react";
import { Link, useParams } from "wouter";
import {
  ArrowLeft,
  Loader2,
  Play,
  RotateCcw,
  Save,
  Sparkles,
  Square,
  Trash2,
} from "lucide-react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input, Label, Select, Textarea } from "@/components/ui/field";
import { PageHeader } from "../components/shell";
import { Badge, StatusPill } from "../components/status";
import {
  useAiPlan,
  useDeployProject,
  useProject,
  useRemoveProject,
  useUpdateProject,
} from "../queries/projects";
import { useNodes } from "../queries/nodes";
import { ago, bytes, clock } from "../lib/format";

const RUNTIMES = ["static", "node", "bun", "python", "docker", "database", "custom"] as const;
type Runtime = (typeof RUNTIMES)[number];

type Draft = {
  name: string;
  runtime: Runtime;
  nodeId: string;
  port: string;
  installCommand: string;
  buildCommand: string;
  startCommand: string;
  dockerfile: string;
  envVars: string;
};

type ProjectRow = {
  name: string;
  runtime: string;
  nodeId: string | null;
  port: number | null;
  installCommand: string | null;
  buildCommand: string | null;
  startCommand: string | null;
  dockerfile: string | null;
  envVars: string | null;
};

function draftOf(project: ProjectRow): Draft {
  return {
    name: project.name,
    runtime: project.runtime as Runtime,
    nodeId: project.nodeId ?? "",
    port: project.port ? String(project.port) : "",
    installCommand: project.installCommand ?? "",
    buildCommand: project.buildCommand ?? "",
    startCommand: project.startCommand ?? "",
    dockerfile: project.dockerfile ?? "",
    envVars: project.envVars ?? "",
  };
}

function ProjectDetail() {
  const params = useParams<{ id: string }>();
  const id = params.id ?? "";
  const query = useProject(id);
  const nodes = useNodes();
  const update = useUpdateProject();
  const deploy = useDeployProject();
  const plan = useAiPlan();
  const remove = useRemoveProject();

  const [draft, setDraft] = useState<Draft | null>(null);
  const [loadedRevision, setLoadedRevision] = useState<string | null>(null);
  const [manifest, setManifest] = useState("");
  const [keyFile, setKeyFile] = useState("");
  const [notice, setNotice] = useState<{ tone: "ok" | "bad"; text: string } | null>(null);

  const project = query.data?.project;

  // Reset the form during render whenever a different project — or a newer
  // revision of the same one — arrives. This is the sanctioned "adjust state on
  // prop change" pattern: no effect, so the first paint already shows the
  // server's values instead of a stale draft.
  const revision = project ? `${project.id}:${project.updatedAt}` : null;
  if (project && revision !== loadedRevision) {
    setLoadedRevision(revision);
    setDraft(draftOf(project));
  }

  if (query.isLoading || !project || !draft) {
    return <Loader2 className="size-5 animate-spin text-primary" />;
  }

  function set<K extends keyof Draft>(key: K, value: Draft[K]) {
    setDraft((current) => (current ? { ...current, [key]: value } : current));
  }

  async function save() {
    if (!draft) return;
    await update.mutateAsync({
      id,
      name: draft.name.trim() || project!.name,
      runtime: draft.runtime,
      nodeId: draft.nodeId || null,
      port: draft.port ? Number(draft.port) : null,
      installCommand: draft.installCommand || null,
      buildCommand: draft.buildCommand || null,
      startCommand: draft.startCommand || null,
      dockerfile: draft.dockerfile || null,
      envVars: draft.envVars || null,
    });
    setNotice({ tone: "ok", text: "Saved." });
  }

  async function askAi() {
    setNotice(null);
    const result = await plan.mutateAsync({
      id,
      manifest: manifest
        .split("\n")
        .map((line) => line.trim())
        .filter(Boolean)
        .slice(0, 600),
      files: keyFile.trim()
        ? [{ path: "package.json", content: keyFile.trim().slice(0, 20000) }]
        : [],
    });
    setNotice(
      result.ok
        ? { tone: "ok", text: "AI wrote the run plan below — review it, then deploy." }
        : { tone: "bad", text: result.error ?? "AI could not work it out." },
    );
  }

  async function run(action: "deploy" | "restart" | "stop") {
    const result = await deploy.mutateAsync({ id, action });
    if (result.ok) {
      setNotice({ tone: "ok", text: `${action} queued on the node.` });
    } else if (result.reason === "no_capacity") {
      setNotice({
        tone: "bad",
        text: "No node is online. Start the agent on your PC, or add a VPS under Nodes.",
      });
    } else {
      setNotice({
        tone: "bad",
        text: "No run plan yet — ask AI to write one (or fill the commands in) first.",
      });
    }
  }

  return (
    <>
      <Link
        href="/projects"
        className="mb-4 inline-flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="size-3.5" /> All projects
      </Link>

      <PageHeader
        eyebrow={project.slug}
        title={project.name}
        subtitle={project.description ?? "No description."}
        actions={
          <div className="flex flex-wrap gap-2">
            <Button onClick={() => void run("deploy")} disabled={deploy.isPending}>
              <Play className="size-4" /> Deploy
            </Button>
            <Button variant="outline" onClick={() => void run("restart")} disabled={deploy.isPending}>
              <RotateCcw className="size-4" /> Restart
            </Button>
            <Button variant="outline" onClick={() => void run("stop")} disabled={deploy.isPending}>
              <Square className="size-4" /> Stop
            </Button>
          </div>
        }
      />

      {notice && (
        <div
          className={`rise mb-5 rounded-md border px-4 py-3 text-sm ${
            notice.tone === "ok"
              ? "border-[var(--ok)]/30 bg-[var(--ok)]/10 text-[var(--ok)]"
              : "border-[var(--danger)]/30 bg-[var(--danger)]/10 text-[var(--danger)]"
          }`}
        >
          {notice.text}
        </div>
      )}

      <div className="grid gap-4 lg:grid-cols-[1.4fr_1fr]">
        <div className="space-y-4">
          <Card className="rise">
            <CardHeader>
              <CardTitle>How it runs</CardTitle>
              <StatusPill status={project.status} />
            </CardHeader>
            <CardBody className="space-y-3">
              <div className="grid gap-3 sm:grid-cols-2">
                <div>
                  <Label>Name</Label>
                  <Input value={draft.name} onChange={(event) => set("name", event.target.value)} />
                </div>
                <div>
                  <Label>Runtime</Label>
                  <Select
                    value={draft.runtime}
                    onChange={(event) => set("runtime", event.target.value as Runtime)}
                  >
                    {RUNTIMES.map((option) => (
                      <option key={option} value={option}>
                        {option}
                      </option>
                    ))}
                  </Select>
                </div>
                <div>
                  <Label>Node</Label>
                  <Select value={draft.nodeId} onChange={(event) => set("nodeId", event.target.value)}>
                    <option value="">Auto — least busy online node</option>
                    {(nodes.data ?? []).map((node) => (
                      <option key={node.id} value={node.id}>
                        {node.name} ({node.status})
                      </option>
                    ))}
                  </Select>
                </div>
                <div>
                  <Label>Port inside the node</Label>
                  <Input
                    placeholder="8080"
                    value={draft.port}
                    onChange={(event) => set("port", event.target.value.replace(/\D/g, ""))}
                  />
                </div>
              </div>

              <div className="grid gap-3 sm:grid-cols-3">
                <div>
                  <Label>Install</Label>
                  <Input
                    placeholder="npm ci"
                    value={draft.installCommand}
                    onChange={(event) => set("installCommand", event.target.value)}
                  />
                </div>
                <div>
                  <Label>Build</Label>
                  <Input
                    placeholder="npm run build"
                    value={draft.buildCommand}
                    onChange={(event) => set("buildCommand", event.target.value)}
                  />
                </div>
                <div>
                  <Label>Start</Label>
                  <Input
                    placeholder="node server.js"
                    value={draft.startCommand}
                    onChange={(event) => set("startCommand", event.target.value)}
                  />
                </div>
              </div>

              <div>
                <Label>Dockerfile</Label>
                <Textarea
                  className="min-h-40"
                  placeholder="FROM node:22-alpine…"
                  value={draft.dockerfile}
                  onChange={(event) => set("dockerfile", event.target.value)}
                />
              </div>

              <div>
                <Label>Environment variables</Label>
                <Textarea
                  placeholder="KEY=value per line"
                  value={draft.envVars}
                  onChange={(event) => set("envVars", event.target.value)}
                />
              </div>

              <div className="flex flex-wrap items-center gap-2">
                <Button onClick={() => void save()} disabled={update.isPending}>
                  {update.isPending ? (
                    <Loader2 className="size-4 animate-spin" />
                  ) : (
                    <Save className="size-4" />
                  )}
                  Save
                </Button>
                <Button
                  variant="outline"
                  className="text-[var(--danger)] hover:text-[var(--danger)]"
                  onClick={() => {
                    if (confirm(`Delete "${project.name}"? The node tears it down.`)) {
                      remove.mutate({ id });
                      window.location.assign("/projects");
                    }
                  }}
                >
                  <Trash2 className="size-4" /> Delete project
                </Button>
              </div>
            </CardBody>
          </Card>

          <Card className="rise">
            <CardHeader>
              <div>
                <CardTitle>Let AI configure it</CardTitle>
                <p className="mt-1 text-sm text-muted-foreground">
                  Paste the file list from your zip (and package.json if there is one). AI fills the
                  runtime, commands, port and Dockerfile above using your own OpenAI key.
                </p>
              </div>
            </CardHeader>
            <CardBody className="space-y-3">
              <div>
                <Label>File list (one path per line)</Label>
                <Textarea
                  placeholder={"package.json\nsrc/index.ts\nDockerfile"}
                  value={manifest}
                  onChange={(event) => setManifest(event.target.value)}
                />
              </div>
              <div>
                <Label>package.json / requirements.txt contents (optional)</Label>
                <Textarea value={keyFile} onChange={(event) => setKeyFile(event.target.value)} />
              </div>
              <Button onClick={() => void askAi()} disabled={plan.isPending}>
                {plan.isPending ? (
                  <Loader2 className="size-4 animate-spin" />
                ) : (
                  <Sparkles className="size-4" />
                )}
                Write the run plan
              </Button>
              {project.aiNotes && (
                <p className="rounded-md border border-border bg-[var(--raised)] px-3 py-2 text-xs leading-relaxed text-muted-foreground">
                  {project.aiNotes}
                </p>
              )}
            </CardBody>
          </Card>
        </div>

        <div className="space-y-4">
          <Card className="rise">
            <CardHeader>
              <CardTitle>Source</CardTitle>
            </CardHeader>
            <CardBody className="space-y-2 text-sm">
              <Row label="Kind" value={project.sourceKind} />
              {project.sourceKind === "git" ? (
                <>
                  <Row label="Repo" value={project.gitUrl ?? "—"} />
                  <Row label="Branch" value={project.gitBranch ?? "main"} />
                </>
              ) : (
                <>
                  <Row label="Bundle" value={project.bundleName ?? "none uploaded"} />
                  <Row label="Size" value={project.bundleSize ? bytes(project.bundleSize) : "—"} />
                </>
              )}
              <Row label="Auto deploy" value={project.autoDeploy ? "on push" : "off"} />
              <Row label="Last deployed" value={clock(project.lastDeployedAt)} />
              <Row label="Node" value={query.data?.node?.name ?? "unassigned"} />
              {project.sourceKind === "git" && project.autoDeploy && (
                <div className="mt-3 rounded-lg border border-white/10 bg-black/20 p-3">
                  <p className="text-xs text-white/60">
                    Add this as a <span className="text-white/80">push</span> webhook on the repo
                    (content type JSON), with the secret from Settings → GitHub:
                  </p>
                  <code className="mt-1 block break-all text-[11px] text-white/80">
                    {`${window.location.origin}/api/hooks/github`}
                  </code>
                </div>
              )}
            </CardBody>
          </Card>

          <Card className="rise">
            <CardHeader>
              <CardTitle>Domains</CardTitle>
              <Button size="sm" variant="outline" asChild>
                <Link href="/domains">Manage</Link>
              </Button>
            </CardHeader>
            <CardBody className="space-y-2">
              {(query.data?.domains ?? []).length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  No domain points here yet. Connect one under Domains to put it on the internet.
                </p>
              ) : (
                (query.data?.domains ?? []).map((domain) => (
                  <div key={domain.id} className="flex items-center justify-between gap-3">
                    <span className="num truncate text-sm">{domain.hostname}</span>
                    <StatusPill status={domain.status} />
                  </div>
                ))
              )}
            </CardBody>
          </Card>

          <Card className="rise">
            <CardHeader>
              <CardTitle>Deploy history</CardTitle>
            </CardHeader>
            <CardBody className="space-y-3">
              {(query.data?.deployments ?? []).length === 0 ? (
                <p className="text-sm text-muted-foreground">Never deployed.</p>
              ) : (
                (query.data?.deployments ?? []).map((row) => (
                  <div key={row.id} className="border-b border-border pb-2.5 last:border-0 last:pb-0">
                    <div className="flex items-center justify-between gap-2">
                      <Badge>{row.action}</Badge>
                      <StatusPill status={row.status} />
                    </div>
                    <p className="num mt-1 text-[0.6875rem] text-muted-foreground">
                      {ago(row.createdAt)} · {row.trigger}
                    </p>
                    {row.error && (
                      <p className="mt-1 text-xs text-[var(--danger)]">{row.error}</p>
                    )}
                    {row.logs && (
                      <pre className="mt-1.5 max-h-32 overflow-auto rounded-md border border-border bg-[var(--raised)] p-2 font-mono text-[0.6875rem] leading-relaxed text-muted-foreground">
                        {row.logs.slice(-1200)}
                      </pre>
                    )}
                  </div>
                ))
              )}
            </CardBody>
          </Card>
        </div>
      </div>
    </>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-start justify-between gap-3">
      <span className="eyebrow pt-0.5">{label}</span>
      <span className="num min-w-0 break-all text-right text-[0.8125rem]">{value}</span>
    </div>
  );
}

export default ProjectDetail;
