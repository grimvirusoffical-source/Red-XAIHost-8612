import { useState } from "react";
import {
  Cpu,
  HardDrive,
  Laptop,
  Loader2,
  Plus,
  RefreshCcw,
  Server,
  Trash2,
  X,
} from "lucide-react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input, Label, Select } from "@/components/ui/field";
import { PageHeader } from "../components/shell";
import { StatusPill, Badge } from "../components/status";
import { CopyBlock } from "../components/copy";
import {
  useCreateNode,
  useNodes,
  useRemoveNode,
  useRotateNodeToken,
  useUpdateNode,
} from "../queries/nodes";
import { ago, memory } from "../lib/format";

type Enrolment = {
  id: string;
  token: string;
  linuxCommand: string;
  windowsCommand: string;
  manualCommand: string;
  name: string;
};

function Nodes() {
  const nodes = useNodes();
  const create = useCreateNode();
  const update = useUpdateNode();
  const rotate = useRotateNodeToken();
  const remove = useRemoveNode();

  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [kind, setKind] = useState<"pc" | "vps">("pc");
  const [labels, setLabels] = useState("");
  const [enrolment, setEnrolment] = useState<Enrolment | null>(null);
  const [rotated, setRotated] = useState<{ id: string; token: string } | null>(null);

  async function submit() {
    if (!name.trim()) return;
    const result = await create.mutateAsync({ name: name.trim(), kind, labels: labels.trim() || undefined });
    setEnrolment({ ...result, name: name.trim() });
    setName("");
    setLabels("");
    setOpen(false);
  }

  return (
    <>
      <PageHeader
        eyebrow="Hosting power"
        title="Nodes"
        subtitle="Every machine that can actually run your workloads: this PC, and any VPS you add later. More nodes, more capacity."
        actions={
          <Button onClick={() => setOpen((value) => !value)}>
            <Plus className="size-4" /> Add node
          </Button>
        }
      />

      {open && (
        <Card className="rise mb-5">
          <CardHeader>
            <div>
              <CardTitle>Enrol a machine</CardTitle>
              <p className="mt-1 text-sm text-muted-foreground">
                You get a one-time agent token and a single command to paste on that machine.
              </p>
            </div>
            <button
              onClick={() => setOpen(false)}
              className="text-muted-foreground hover:text-foreground"
            >
              <X className="size-4" />
            </button>
          </CardHeader>
          <CardBody className="space-y-4">
            <div className="grid gap-3 sm:grid-cols-3">
              <div>
                <Label>Name</Label>
                <Input
                  placeholder="home-pc"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                />
              </div>
              <div>
                <Label>Kind</Label>
                <Select value={kind} onChange={(event) => setKind(event.target.value as "pc" | "vps")}>
                  <option value="pc">My PC</option>
                  <option value="vps">VPS / server</option>
                </Select>
              </div>
              <div>
                <Label>Labels (optional)</Label>
                <Input
                  placeholder="gpu, eu-west"
                  value={labels}
                  onChange={(event) => setLabels(event.target.value)}
                />
              </div>
            </div>
            <Button onClick={() => void submit()} disabled={create.isPending || !name.trim()}>
              {create.isPending && <Loader2 className="size-4 animate-spin" />} Create token
            </Button>
          </CardBody>
        </Card>
      )}

      {enrolment && (
        <Card className="rise mb-5 border-primary/30">
          <CardHeader>
            <div>
              <CardTitle>Install the agent on “{enrolment.name}”</CardTitle>
              <p className="mt-1 text-sm text-muted-foreground">
                This token is shown once. The agent needs Docker (for containers) and cloudflared
                (for public domains) — the installer checks both.
              </p>
            </div>
            <button
              onClick={() => setEnrolment(null)}
              className="text-muted-foreground hover:text-foreground"
            >
              <X className="size-4" />
            </button>
          </CardHeader>
          <CardBody className="space-y-3">
            <CopyBlock label="Agent token (save it now)" value={enrolment.token} />
            <CopyBlock label="Windows (PowerShell, as admin)" value={enrolment.windowsCommand} />
            <CopyBlock label="Linux / VPS (bash)" value={enrolment.linuxCommand} />
            <CopyBlock label="Manual (node installed already)" value={enrolment.manualCommand} />
          </CardBody>
        </Card>
      )}

      {rotated && (
        <Card className="rise mb-5 border-[var(--warn)]/40">
          <CardHeader>
            <CardTitle>New token issued</CardTitle>
            <button
              onClick={() => setRotated(null)}
              className="text-muted-foreground hover:text-foreground"
            >
              <X className="size-4" />
            </button>
          </CardHeader>
          <CardBody>
            <CopyBlock label="Update the agent's RXH_TOKEN" value={rotated.token} />
          </CardBody>
        </Card>
      )}

      {nodes.isLoading ? (
        <Loader2 className="size-5 animate-spin text-primary" />
      ) : (nodes.data ?? []).length === 0 ? (
        <Card className="rise">
          <CardBody className="py-14 text-center">
            <Server className="mx-auto size-6 text-muted-foreground" />
            <h3 className="mt-3 text-base font-semibold">No hosting power yet</h3>
            <p className="mx-auto mt-1.5 max-w-md text-sm text-muted-foreground">
              Add your PC first — it becomes the machine that runs your sites and apps. Add a VPS
              later for anything that must stay up when the PC sleeps.
            </p>
            <Button className="mt-5" onClick={() => setOpen(true)}>
              <Plus className="size-4" /> Add your first node
            </Button>
          </CardBody>
        </Card>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {(nodes.data ?? []).map((node, index) => (
            <Card key={node.id} className="rise" style={{ animationDelay: `${index * 40}ms` }}>
              <CardHeader>
                <div className="min-w-0">
                  <CardTitle className="flex items-center gap-2">
                    {node.kind === "pc" ? (
                      <Laptop className="size-4 text-primary" />
                    ) : (
                      <Server className="size-4 text-primary" />
                    )}
                    {node.name}
                  </CardTitle>
                  <p className="num mt-1 truncate text-[0.6875rem] text-muted-foreground">
                    {node.os ?? "unknown os"} · {node.arch ?? "?"} · agent{" "}
                    {node.agentVersion ?? "—"} · seen {ago(node.lastSeenAt)}
                  </p>
                </div>
                <StatusPill status={node.status} />
              </CardHeader>
              <CardBody className="space-y-3">
                <div className="grid grid-cols-3 gap-2">
                  <Metric icon={<Cpu className="size-3.5" />} label="vCPU" value={String(node.cpuCores ?? "—")} />
                  <Metric
                    icon={<Server className="size-3.5" />}
                    label="RAM"
                    value={memory(node.memoryMb)}
                  />
                  <Metric
                    icon={<HardDrive className="size-3.5" />}
                    label="Disk"
                    value={node.diskGb ? `${node.diskGb} GB` : "—"}
                  />
                </div>

                <div className="flex flex-wrap gap-1.5">
                  <Badge className={node.dockerAvailable ? "border-[var(--ok)]/30 text-[var(--ok)]" : ""}>
                    docker {node.dockerAvailable ? "ready" : "missing"}
                  </Badge>
                  <Badge
                    className={node.cloudflaredAvailable ? "border-[var(--ok)]/30 text-[var(--ok)]" : ""}
                  >
                    cloudflared {node.cloudflaredAvailable ? "ready" : "missing"}
                  </Badge>
                  {node.publicIp && <Badge>{node.publicIp}</Badge>}
                  {node.labels && <Badge>{node.labels}</Badge>}
                  <Badge>token …{node.tokenPreview}</Badge>
                </div>

                <div className="flex flex-wrap gap-2 pt-1">
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={async () => {
                      const result = await rotate.mutateAsync({ id: node.id });
                      setRotated({ id: node.id, token: result.token });
                    }}
                    disabled={rotate.isPending}
                  >
                    <RefreshCcw className="size-3.5" /> Rotate token
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() =>
                      update.mutate({
                        id: node.id,
                        status: node.status === "disabled" ? "pending" : "disabled",
                      })
                    }
                  >
                    {node.status === "disabled" ? "Enable" : "Disable"}
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    className="text-[var(--danger)] hover:text-[var(--danger)]"
                    onClick={() => {
                      if (confirm(`Remove ${node.name} from the fleet?`)) {
                        remove.mutate({ id: node.id });
                      }
                    }}
                  >
                    <Trash2 className="size-3.5" /> Remove
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

function Metric({
  icon,
  label,
  value,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
}) {
  return (
    <div className="rounded-md border border-border bg-[var(--raised)] px-3 py-2">
      <div className="flex items-center gap-1.5 text-muted-foreground">
        {icon}
        <span className="eyebrow">{label}</span>
      </div>
      <div className="num mt-1 text-sm">{value}</div>
    </div>
  );
}

export default Nodes;
