import { useState } from "react";
import {
  BookOpen,
  Check,
  Globe,
  Link2,
  Loader2,
  Plus,
  RefreshCcw,
  Trash2,
  X,
} from "lucide-react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input, Label, Select } from "@/components/ui/field";
import { PageHeader } from "../components/shell";
import { Badge, StatusPill } from "../components/status";
import { CopyBlock } from "../components/copy";
import { CloudflareBanner, ManualSetup } from "../components/domain-help";
import {
  useAddDomain,
  useConnectDomain,
  useDomains,
  useRecheckDomain,
  useRegistrarDomains,
  useRemoveDomain,
  useUpdateDomain,
} from "../queries/domains";
import { useProjects } from "../queries/projects";
import { ago } from "../lib/format";

type Registrar = "godaddy" | "namecheap" | "cloudflare" | "manual";

type ConnectResult = {
  id: string;
  ok: boolean;
  steps: { step: string; ok: boolean; detail: string }[];
  nameServers: string[];
  connectorToken?: string | null;
  skipped?: string | null;
};

function Domains() {
  const domains = useDomains();
  const projects = useProjects();
  const add = useAddDomain();
  const update = useUpdateDomain();
  const remove = useRemoveDomain();
  const connect = useConnectDomain();
  const recheck = useRecheckDomain();

  const [open, setOpen] = useState(false);
  const [hostname, setHostname] = useState("");
  const [registrar, setRegistrar] = useState<Registrar>("manual");
  const [projectId, setProjectId] = useState("");
  const [browse, setBrowse] = useState<"godaddy" | "namecheap" | null>(null);
  const [result, setResult] = useState<ConnectResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [manual, setManual] = useState<string | null>(null);
  /** What attaching a domain to a project actually did, per domain id. */
  const [attached, setAttached] = useState<Record<string, { ok: boolean; detail: string }>>({});

  const registrarList = useRegistrarDomains(browse ?? "godaddy", browse !== null);

  async function submit() {
    setError(null);
    try {
      const response = await add.mutateAsync({
        hostname: hostname.trim().toLowerCase(),
        registrar,
        projectId: projectId || undefined,
      });
      if (response.auto) {
        setResult({
          id: response.id,
          ok: response.auto.ok,
          steps: response.auto.steps,
          nameServers: response.auto.nameServers,
          connectorToken: response.auto.connectorToken,
          skipped: response.auto.skipped ?? null,
        });
      }
      setHostname("");
      setOpen(false);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not add that hostname.");
    }
  }

  const rows = domains.data ?? [];

  return (
    <>
      <PageHeader
        eyebrow="Public reachability"
        title="Domains"
        subtitle="Add a hostname and the panel tries Cloudflare itself — zone, tunnel, ingress and DNS. No port forwarding, works behind home internet. Prefer doing it yourself? Every domain has a by-hand walkthrough."
        actions={
          <Button onClick={() => setOpen((value) => !value)}>
            <Plus className="size-4" /> Add domain
          </Button>
        }
      />

      <CloudflareBanner />

      {open && (
        <Card className="rise mb-5">
          <CardHeader>
            <div>
              <CardTitle>Point a domain at a project</CardTitle>
              <p className="mt-1 text-sm text-muted-foreground">
                Adding it is enough: if Cloudflare is connected, the zone, tunnel, ingress and DNS
                are set up in the same pass. Nothing to press afterwards.
              </p>
            </div>
            <button onClick={() => setOpen(false)} className="text-muted-foreground hover:text-foreground">
              <X className="size-4" />
            </button>
          </CardHeader>
          <CardBody className="space-y-4">
            <div className="grid gap-3 sm:grid-cols-3">
              <div>
                <Label>Hostname</Label>
                <Input
                  placeholder="app.example.com"
                  value={hostname}
                  onChange={(event) => setHostname(event.target.value)}
                />
              </div>
              <div>
                <Label>Registrar</Label>
                <Select
                  value={registrar}
                  onChange={(event) => setRegistrar(event.target.value as Registrar)}
                >
                  <option value="manual">Other / set nameservers myself</option>
                  <option value="godaddy">GoDaddy (API)</option>
                  <option value="namecheap">Namecheap (API)</option>
                  <option value="cloudflare">Already on Cloudflare</option>
                </Select>
              </div>
              <div>
                <Label>Project</Label>
                <Select value={projectId} onChange={(event) => setProjectId(event.target.value)}>
                  <option value="">Decide later</option>
                  {(projects.data ?? []).map((project) => (
                    <option key={project.id} value={project.id}>
                      {project.name}
                    </option>
                  ))}
                </Select>
              </div>
            </div>

            {(registrar === "godaddy" || registrar === "namecheap") && (
              <div className="rounded-md border border-border bg-[var(--raised)] px-3 py-2.5">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <p className="text-xs text-muted-foreground">
                    RedXAIHost talks to {registrar === "godaddy" ? "GoDaddy" : "Namecheap"} through
                    their official API key — no account password is ever stored or typed in here.
                  </p>
                  <Button size="sm" variant="outline" onClick={() => setBrowse(registrar)}>
                    List my domains
                  </Button>
                </div>
                {browse === registrar && (
                  <div className="mt-2.5">
                    {registrarList.isLoading ? (
                      <Loader2 className="size-4 animate-spin text-primary" />
                    ) : registrarList.data?.ok ? (
                      <div className="flex flex-wrap gap-1.5">
                        {(registrarList.data.domains ?? []).map((name) => (
                          <button
                            key={name}
                            onClick={() => setHostname(name)}
                            className="num rounded-md border border-border px-2 py-1 text-[0.6875rem] hover:border-primary/40 hover:text-primary"
                          >
                            {name}
                          </button>
                        ))}
                      </div>
                    ) : (
                      <p className="text-xs text-[var(--danger)]">
                        {registrarList.data?.error ??
                          "Could not reach the registrar. Add its API key under Settings."}
                      </p>
                    )}
                  </div>
                )}
              </div>
            )}

            {error && <p className="text-sm text-[var(--danger)]">{error}</p>}

            <Button onClick={() => void submit()} disabled={add.isPending || hostname.trim().length < 3}>
              {add.isPending && <Loader2 className="size-4 animate-spin" />} Add domain
            </Button>
          </CardBody>
        </Card>
      )}

      {result && (
        <Card className={`rise mb-5 ${result.ok ? "border-[var(--ok)]/30" : "border-[var(--danger)]/30"}`}>
          <CardHeader>
            <CardTitle>{result.ok ? "Connected" : "Connect stopped early"}</CardTitle>
            <button onClick={() => setResult(null)} className="text-muted-foreground hover:text-foreground">
              <X className="size-4" />
            </button>
          </CardHeader>
          <CardBody className="space-y-2.5">
            {result.steps.map((step) => (
              <div key={step.step} className="flex items-start gap-2.5">
                {step.ok ? (
                  <Check className="mt-0.5 size-4 shrink-0 text-[var(--ok)]" />
                ) : (
                  <X className="mt-0.5 size-4 shrink-0 text-[var(--danger)]" />
                )}
                <div className="min-w-0">
                  <p className="text-sm font-medium">{step.step}</p>
                  <p className="num break-all text-[0.6875rem] text-muted-foreground">{step.detail}</p>
                </div>
              </div>
            ))}
            {result.skipped && (
              <p className="text-xs text-[var(--warn)]">{result.skipped}</p>
            )}
            {result.nameServers.length > 0 && (
              <CopyBlock label="Cloudflare nameservers" value={result.nameServers.join("\n")} />
            )}
            {result.connectorToken && (
              <CopyBlock
                label="Tunnel token — the node agent needs this"
                value={result.connectorToken}
              />
            )}
          </CardBody>
        </Card>
      )}

      {domains.isLoading ? (
        <Loader2 className="size-5 animate-spin text-primary" />
      ) : rows.length === 0 ? (
        <Card className="rise">
          <CardBody className="py-14 text-center">
            <Globe className="mx-auto size-6 text-muted-foreground" />
            <h3 className="mt-3 text-base font-semibold">No domains connected</h3>
            <p className="mx-auto mt-1.5 max-w-md text-sm text-muted-foreground">
              Add a hostname you own. Cloudflare Tunnel then reaches your node without opening a
              single port on your router.
            </p>
            <Button className="mt-5" onClick={() => setOpen(true)}>
              <Plus className="size-4" /> Add your first domain
            </Button>
          </CardBody>
        </Card>
      ) : (
        <div className="space-y-3">
          {rows.map((domain, index) => (
            <Card key={domain.id} className="rise" style={{ animationDelay: `${index * 35}ms` }}>
              <CardBody className="flex flex-wrap items-center justify-between gap-4">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <Globe className="size-4 text-primary" />
                    <a
                      href={`https://${domain.hostname}`}
                      target="_blank"
                      rel="noreferrer"
                      className="num truncate text-sm font-semibold hover:text-primary"
                    >
                      {domain.hostname}
                    </a>
                    <StatusPill status={domain.status} />
                  </div>
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    <Badge>{domain.registrar}</Badge>
                    {domain.tunnelName && <Badge>{domain.tunnelName}</Badge>}
                    {domain.cnameTarget && <Badge>CNAME {domain.cnameTarget}</Badge>}
                    <Badge>checked {ago(domain.lastCheckedAt)}</Badge>
                  </div>
                  {domain.lastError && (
                    <p className="mt-1.5 text-xs text-[var(--danger)]">{domain.lastError}</p>
                  )}
                  {attached[domain.id] && (
                    <p
                      className={`mt-1.5 text-xs ${
                        attached[domain.id]!.ok ? "text-[var(--ok)]" : "text-[var(--danger)]"
                      }`}
                    >
                      {attached[domain.id]!.detail}
                    </p>
                  )}
                </div>

                <div className="flex flex-wrap items-center gap-2">
                  <Select
                    className="h-9 w-44"
                    value={domain.projectId ?? ""}
                    onChange={async (event) => {
                      const response = await update.mutateAsync({
                        id: domain.id,
                        projectId: event.target.value || null,
                      });
                      if (response.detail) {
                        setAttached((current) => ({
                          ...current,
                          [domain.id]: { ok: response.ok, detail: response.detail! },
                        }));
                      }
                    }}
                  >
                    <option value="">No project</option>
                    {(projects.data ?? []).map((project) => (
                      <option key={project.id} value={project.id}>
                        {project.name}
                      </option>
                    ))}
                  </Select>
                  <Button
                    size="sm"
                    onClick={async () => {
                      const response = await connect.mutateAsync({
                        id: domain.id,
                        setNameserversAtRegistrar: true,
                      });
                      setResult({ id: domain.id, ...response });
                    }}
                    disabled={connect.isPending}
                  >
                    {connect.isPending ? (
                      <Loader2 className="size-3.5 animate-spin" />
                    ) : (
                      <Link2 className="size-3.5" />
                    )}
                    Connect
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => recheck.mutate({ id: domain.id })}
                    disabled={recheck.isPending}
                  >
                    <RefreshCcw className="size-3.5" /> Recheck
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => setManual((current) => (current === domain.id ? null : domain.id))}
                  >
                    <BookOpen className="size-3.5" /> Manual setup
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    className="text-[var(--danger)] hover:text-[var(--danger)]"
                    onClick={() => {
                      if (confirm(`Stop managing ${domain.hostname}?`)) remove.mutate({ id: domain.id });
                    }}
                  >
                    <Trash2 className="size-3.5" />
                  </Button>
                </div>
              </CardBody>
              {manual === domain.id && (
                <div className="px-5 pb-5">
                  <ManualSetup domainId={domain.id} onClose={() => setManual(null)} />
                </div>
              )}
            </Card>
          ))}
        </div>
      )}
    </>
  );
}

export default Domains;
