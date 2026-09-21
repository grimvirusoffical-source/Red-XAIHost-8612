import { useRef, useState } from "react";
import { Link } from "wouter";
import {
  Boxes,
  Database,
  FileArchive,
  GitBranch,
  Globe,
  Loader2,
  Plus,
  Rocket,
  Terminal,
  Upload,
  X,
} from "lucide-react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input, Label, Select, Textarea } from "@/components/ui/field";
import { PageHeader } from "../components/shell";
import { Badge, StatusPill } from "../components/status";
import { useCreateProject, useDeployProject, useProjects, usePresignBundle } from "../queries/projects";
import { useNodes } from "../queries/nodes";
import { ago, bytes } from "../lib/format";

const RUNTIMES = [
  { id: "static", label: "Static site (HTML/CSS/JS build output)" },
  { id: "node", label: "Node app / API" },
  { id: "bun", label: "Bun app / API" },
  { id: "python", label: "Python app" },
  { id: "docker", label: "Docker container (any stack)" },
  { id: "database", label: "Database (Postgres / MySQL / Mongo)" },
  { id: "custom", label: "Something else — let AI figure it out" },
] as const;

type Runtime = (typeof RUNTIMES)[number]["id"];

function runtimeIcon(runtime: string) {
  if (runtime === "static") return <Globe className="size-4 text-primary" />;
  if (runtime === "docker") return <Boxes className="size-4 text-primary" />;
  if (runtime === "database") return <Database className="size-4 text-primary" />;
  return <Terminal className="size-4 text-primary" />;
}

function Projects() {
  const projects = useProjects();
  const nodes = useNodes();
  const create = useCreateProject();
  const presign = usePresignBundle();
  const deploy = useDeployProject();

  const fileRef = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [runtime, setRuntime] = useState<Runtime>("static");
  const [sourceKind, setSourceKind] = useState<"upload" | "git">("upload");
  const [gitUrl, setGitUrl] = useState("");
  const [gitBranch, setGitBranch] = useState("main");
  const [envVars, setEnvVars] = useState("");
  const [nodeId, setNodeId] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const busy = create.isPending || uploading;

  async function submit() {
    if (!name.trim()) return;
    setError(null);
    try {
      let bundle: { bundleKey?: string; bundleName?: string; bundleSize?: number } = {};

      if (sourceKind === "upload" && file) {
        setUploading(true);
        const target = await presign.mutateAsync({
          filename: file.name,
          contentType: file.type || "application/zip",
        });
        const put = await fetch(target.url, {
          method: "PUT",
          body: file,
          headers: { "Content-Type": file.type || "application/zip" },
        });
        if (!put.ok) throw new Error(`Upload failed (${put.status}).`);
        bundle = { bundleKey: target.key, bundleName: file.name, bundleSize: file.size };
        setUploading(false);
      }

      await create.mutateAsync({
        name: name.trim(),
        description: description.trim() || undefined,
        runtime,
        sourceKind,
        gitUrl: sourceKind === "git" && gitUrl.trim() ? gitUrl.trim() : undefined,
        gitBranch: sourceKind === "git" ? gitBranch.trim() || "main" : undefined,
        envVars: envVars.trim() || undefined,
        nodeId: nodeId || undefined,
        ...bundle,
      });

      setName("");
      setDescription("");
      setEnvVars("");
      setGitUrl("");
      setFile(null);
      if (fileRef.current) fileRef.current.value = "";
      setOpen(false);
    } catch (cause) {
      setUploading(false);
      setError(cause instanceof Error ? cause.message : "Could not create the project.");
    }
  }

  const rows = projects.data ?? [];

  return (
    <>
      <PageHeader
        eyebrow="What you host"
        title="Projects"
        subtitle="Drop in a zip or point at a repo. AI writes the run plan, a node runs it, Cloudflare Tunnel puts it online."
        actions={
          <Button onClick={() => setOpen((value) => !value)}>
            <Plus className="size-4" /> New project
          </Button>
        }
      />

      {open && (
        <Card className="rise mb-5">
          <CardHeader>
            <div>
              <CardTitle>Add a project</CardTitle>
              <p className="mt-1 text-sm text-muted-foreground">
                Nothing goes live yet — you review the AI plan and hit deploy on the next screen.
              </p>
            </div>
            <button onClick={() => setOpen(false)} className="text-muted-foreground hover:text-foreground">
              <X className="size-4" />
            </button>
          </CardHeader>
          <CardBody className="space-y-4">
            <div className="grid gap-3 sm:grid-cols-2">
              <div>
                <Label>Name</Label>
                <Input
                  placeholder="my-portfolio"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                />
              </div>
              <div>
                <Label>What is it</Label>
                <Select value={runtime} onChange={(event) => setRuntime(event.target.value as Runtime)}>
                  {RUNTIMES.map((option) => (
                    <option key={option.id} value={option.id}>
                      {option.label}
                    </option>
                  ))}
                </Select>
              </div>
            </div>

            <div>
              <Label>Description (optional)</Label>
              <Input
                placeholder="Landing page for the store"
                value={description}
                onChange={(event) => setDescription(event.target.value)}
              />
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <div>
                <Label>Source</Label>
                <Select
                  value={sourceKind}
                  onChange={(event) => setSourceKind(event.target.value as "upload" | "git")}
                >
                  <option value="upload">Upload a zip of the code</option>
                  <option value="git">Pull from a Git repo</option>
                </Select>
              </div>
              <div>
                <Label>Run on</Label>
                <Select value={nodeId} onChange={(event) => setNodeId(event.target.value)}>
                  <option value="">Pick the least busy online node</option>
                  {(nodes.data ?? []).map((node) => (
                    <option key={node.id} value={node.id}>
                      {node.name} ({node.status})
                    </option>
                  ))}
                </Select>
              </div>
            </div>

            {sourceKind === "upload" ? (
              <div>
                <Label>Code bundle (.zip)</Label>
                <div className="flex flex-wrap items-center gap-3">
                  <input
                    ref={fileRef}
                    type="file"
                    aria-label="Code bundle"
                    accept=".zip,.tar,.tgz,.gz"
                    onChange={(event) => setFile(event.target.files?.[0] ?? null)}
                    className="hidden"
                  />
                  <Button variant="outline" onClick={() => fileRef.current?.click()}>
                    <Upload className="size-4" /> Choose file
                  </Button>
                  <span className="num text-xs text-muted-foreground">
                    {file ? `${file.name} · ${bytes(file.size)}` : "no file selected"}
                  </span>
                </div>
              </div>
            ) : (
              <div className="grid gap-3 sm:grid-cols-[2fr_1fr]">
                <div>
                  <Label>Repo URL</Label>
                  <Input
                    placeholder="https://github.com/you/your-repo"
                    value={gitUrl}
                    onChange={(event) => setGitUrl(event.target.value)}
                  />
                </div>
                <div>
                  <Label>Branch</Label>
                  <Input value={gitBranch} onChange={(event) => setGitBranch(event.target.value)} />
                </div>
              </div>
            )}

            <div>
              <Label>Environment variables (optional)</Label>
              <Textarea
                placeholder={"DATABASE_URL=postgres://...\nAPI_KEY=..."}
                value={envVars}
                onChange={(event) => setEnvVars(event.target.value)}
              />
            </div>

            {error && <p className="text-sm text-[var(--danger)]">{error}</p>}

            <Button onClick={() => void submit()} disabled={busy || !name.trim()}>
              {busy && <Loader2 className="size-4 animate-spin" />}
              {uploading ? "Uploading bundle…" : "Create project"}
            </Button>
          </CardBody>
        </Card>
      )}

      {projects.isLoading ? (
        <Loader2 className="size-5 animate-spin text-primary" />
      ) : rows.length === 0 ? (
        <Card className="rise">
          <CardBody className="py-14 text-center">
            <FileArchive className="mx-auto size-6 text-muted-foreground" />
            <h3 className="mt-3 text-base font-semibold">Nothing hosted yet</h3>
            <p className="mx-auto mt-1.5 max-w-md text-sm text-muted-foreground">
              Add a project, let AI work out how to run it, then deploy it to one of your nodes.
            </p>
            <Button className="mt-5" onClick={() => setOpen(true)}>
              <Plus className="size-4" /> New project
            </Button>
          </CardBody>
        </Card>
      ) : (
        <div className="space-y-3">
          {rows.map((project, index) => (
            <Card key={project.id} className="rise" style={{ animationDelay: `${index * 35}ms` }}>
              <CardBody className="flex flex-wrap items-center justify-between gap-4">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    {runtimeIcon(project.runtime)}
                    <Link
                      href={`/projects/${project.id}`}
                      className="truncate text-base font-semibold hover:text-primary"
                    >
                      {project.name}
                    </Link>
                    <StatusPill status={project.status} />
                  </div>
                  <p className="num mt-1.5 truncate text-[0.6875rem] text-muted-foreground">
                    {project.runtime}
                    {project.port ? ` · :${project.port}` : ""} ·{" "}
                    {project.nodeName ? `on ${project.nodeName}` : "unassigned"} · updated{" "}
                    {ago(project.updatedAt)}
                  </p>
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    {project.sourceKind === "git" ? (
                      <Badge>
                        <GitBranch className="mr-1 size-3" />
                        {project.gitBranch ?? "main"}
                      </Badge>
                    ) : (
                      project.bundleName && <Badge>{project.bundleName}</Badge>
                    )}
                    {project.domains.map((domain) => (
                      <Badge
                        key={domain.hostname}
                        className={domain.status === "live" ? "border-[var(--ok)]/30 text-[var(--ok)]" : ""}
                      >
                        {domain.hostname}
                      </Badge>
                    ))}
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <Button
                    size="sm"
                    onClick={() => deploy.mutate({ id: project.id, action: "deploy" })}
                    disabled={deploy.isPending}
                  >
                    <Rocket className="size-3.5" /> Deploy
                  </Button>
                  <Button size="sm" variant="outline" asChild>
                    <Link href={`/projects/${project.id}`}>Open</Link>
                  </Button>
                </div>
              </CardBody>
            </Card>
          ))}
        </div>
      )}
    </>
  );
}

export default Projects;
