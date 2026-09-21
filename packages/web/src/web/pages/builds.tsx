import { useState } from "react";
import { ExternalLink, Hammer, Loader2, RefreshCcw, Send, Trash2 } from "lucide-react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input, Label, Select } from "@/components/ui/field";
import { PageHeader } from "../components/shell";
import { Badge, StatusPill } from "../components/status";
import {
  useBuildTargets,
  useBuilds,
  useDispatchBuild,
  useRemoveBuild,
  useSyncBuild,
} from "../queries/builds";
import { useProjects } from "../queries/projects";
import { ago } from "../lib/format";

type Platform = "ios" | "android" | "windows" | "mac" | "linux";

function Builds() {
  const builds = useBuilds();
  const targets = useBuildTargets();
  const projects = useProjects();
  const dispatch = useDispatchBuild();
  const sync = useSyncBuild();
  const remove = useRemoveBuild();

  const [platform, setPlatform] = useState<Platform>("windows");
  const [projectId, setProjectId] = useState("");
  const [ref, setRef] = useState("main");
  const [version, setVersion] = useState("");
  const [notice, setNotice] = useState<{ tone: "ok" | "bad"; text: string } | null>(null);

  const repoConnected = targets.data?.repoConnected ?? false;

  async function send() {
    setNotice(null);
    const result = await dispatch.mutateAsync({
      platform,
      projectId: projectId || undefined,
      ref: ref.trim() || "main",
      version: version.trim() || undefined,
    });
    setNotice(
      result.ok
        ? { tone: "ok", text: "Dispatched. CI picks it up in a few seconds — hit Sync for status." }
        : { tone: "bad", text: result.error },
    );
  }

  return (
    <>
      <PageHeader
        eyebrow="Mobile & desktop binaries"
        title="App builds"
        subtitle="Installers and app-store binaries are compiled in CI, not on your PC — iOS needs macOS and signing certs, every other platform needs its own toolchain. You trigger and track them from here."
      />

      <Card className="rise mb-4">
        <CardHeader>
          <div>
            <CardTitle>Dispatch a build</CardTitle>
            <p className="mt-1 text-sm text-muted-foreground">
              {repoConnected ? (
                <>
                  Wired to <span className="num">{targets.data?.repo}</span> via GitHub Actions.
                </>
              ) : (
                "Add a GitHub token and owner/repo under Settings → Credentials first."
              )}
            </p>
          </div>
          {repoConnected && <Badge className="border-[var(--ok)]/30 text-[var(--ok)]">github connected</Badge>}
        </CardHeader>
        <CardBody className="space-y-4">
          <div className="grid gap-3 sm:grid-cols-4">
            <div>
              <Label>Platform</Label>
              <Select
                value={platform}
                onChange={(event) => setPlatform(event.target.value as Platform)}
              >
                {(targets.data?.platforms ?? []).map((option) => (
                  <option key={`${option.id}-${option.file}`} value={option.id}>
                    {option.label}
                  </option>
                ))}
              </Select>
            </div>
            <div>
              <Label>Project (optional)</Label>
              <Select value={projectId} onChange={(event) => setProjectId(event.target.value)}>
                <option value="">Not tied to a project</option>
                {(projects.data ?? []).map((project) => (
                  <option key={project.id} value={project.id}>
                    {project.name}
                  </option>
                ))}
              </Select>
            </div>
            <div>
              <Label>Git ref</Label>
              <Input value={ref} onChange={(event) => setRef(event.target.value)} />
            </div>
            <div>
              <Label>Version (optional)</Label>
              <Input
                placeholder="1.0.3"
                value={version}
                onChange={(event) => setVersion(event.target.value)}
              />
            </div>
          </div>

          {notice && (
            <p
              className={`text-sm ${notice.tone === "ok" ? "text-[var(--ok)]" : "text-[var(--danger)]"}`}
            >
              {notice.text}
            </p>
          )}

          <Button onClick={() => void send()} disabled={dispatch.isPending}>
            {dispatch.isPending ? (
              <Loader2 className="size-4 animate-spin" />
            ) : (
              <Send className="size-4" />
            )}
            Dispatch build
          </Button>
        </CardBody>
      </Card>

      {builds.isLoading ? (
        <Loader2 className="size-5 animate-spin text-primary" />
      ) : (builds.data ?? []).length === 0 ? (
        <Card className="rise">
          <CardBody className="py-14 text-center">
            <Hammer className="mx-auto size-6 text-muted-foreground" />
            <h3 className="mt-3 text-base font-semibold">No builds yet</h3>
            <p className="mx-auto mt-1.5 max-w-md text-sm text-muted-foreground">
              Dispatch one above. Windows, macOS and Linux go through GitHub Actions; iOS and Android
              go through EAS with your Expo token.
            </p>
          </CardBody>
        </Card>
      ) : (
        <div className="space-y-3">
          {(builds.data ?? []).map((build, index) => (
            <Card key={build.id} className="rise" style={{ animationDelay: `${index * 30}ms` }}>
              <CardBody className="flex flex-wrap items-center justify-between gap-4">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <Hammer className="size-4 text-primary" />
                    <span className="text-sm font-semibold capitalize">{build.platform}</span>
                    <StatusPill status={build.status} />
                  </div>
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    <Badge>{build.provider}</Badge>
                    {build.workflow && <Badge>{build.workflow}</Badge>}
                    {build.version && <Badge>v{build.version}</Badge>}
                    <Badge>{ago(build.createdAt)}</Badge>
                  </div>
                  {build.error && <p className="mt-1.5 text-xs text-[var(--danger)]">{build.error}</p>}
                  {build.notes && (
                    <p className="mt-1.5 text-xs text-muted-foreground">{build.notes}</p>
                  )}
                </div>
                <div className="flex items-center gap-2">
                  {build.runUrl && (
                    <Button size="sm" variant="outline" asChild>
                      <a href={build.runUrl} target="_blank" rel="noreferrer">
                        <ExternalLink className="size-3.5" /> CI run
                      </a>
                    </Button>
                  )}
                  {build.artifactUrl && (
                    <Button size="sm" variant="outline" asChild>
                      <a href={build.artifactUrl} target="_blank" rel="noreferrer">
                        Download
                      </a>
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => sync.mutate({ id: build.id })}
                    disabled={sync.isPending}
                  >
                    <RefreshCcw className="size-3.5" /> Sync
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    className="text-[var(--danger)] hover:text-[var(--danger)]"
                    onClick={() => remove.mutate({ id: build.id })}
                  >
                    <Trash2 className="size-3.5" />
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

export default Builds;
