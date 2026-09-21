import { Link } from "wouter";
import {
  AlertTriangle,
  ArrowUpRight,
  Boxes,
  Globe,
  Hammer,
  KeyRound,
  Server,
  Zap,
} from "lucide-react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { StatusPill, Dot } from "../components/status";
import { PageHeader } from "../components/shell";
import { useOverview } from "../queries/overview";
import { ago, bytes, compact, memory } from "../lib/format";

function Stat({
  label,
  value,
  hint,
  tone,
}: {
  label: string;
  value: string;
  hint?: string;
  tone?: "ok" | "warn" | "danger";
}) {
  const color =
    tone === "ok"
      ? "text-[var(--ok)]"
      : tone === "warn"
        ? "text-[var(--warn)]"
        : tone === "danger"
          ? "text-[var(--danger)]"
          : "text-foreground";
  return (
    <div className="rounded-md border border-border bg-[var(--raised)] px-4 py-3">
      <div className="eyebrow">{label}</div>
      <div className={`num mt-1.5 text-2xl font-medium ${color}`}>{value}</div>
      {hint && <div className="mt-0.5 text-[0.6875rem] text-muted-foreground">{hint}</div>}
    </div>
  );
}

function Dashboard() {
  const overview = useOverview();
  const data = overview.data;
  const capacityMissing = data ? !data.fleet.capacityAvailable : false;

  return (
    <>
      <PageHeader
        eyebrow="Control plane"
        title="Dashboard"
        subtitle="Everything you host, the machines hosting it, and what it is doing right now."
        actions={
          <>
            <Button variant="outline" asChild>
              <Link href="/nodes">
                <Server className="size-4" /> Fleet
              </Link>
            </Button>
            <Button asChild>
              <Link href="/projects">
                <Zap className="size-4" /> Host something
              </Link>
            </Button>
          </>
        }
      />

      {capacityMissing && (
        <div className="rise mb-6 flex flex-wrap items-center justify-between gap-4 rounded-[var(--radius)] border border-[var(--danger)]/35 bg-[var(--danger)]/8 px-5 py-4">
          <div className="flex items-start gap-3">
            <AlertTriangle className="mt-0.5 size-4.5 shrink-0 text-[var(--danger)]" />
            <div>
              <div className="text-sm font-semibold text-[var(--danger)]">
                No node online — nothing you host is reachable
              </div>
              <p className="mt-0.5 text-xs text-muted-foreground">
                This panel never serves traffic itself. Start the agent on your PC or add a VPS to
                bring hosting back up.
              </p>
            </div>
          </div>
          <Button variant="outline" asChild>
            <Link href="/nodes">
              Add capacity <ArrowUpRight className="size-3.5" />
            </Link>
          </Button>
        </div>
      )}

      <div className="grid gap-4 lg:grid-cols-[1.35fr_1fr]">
        <Card className="rise">
          <CardHeader>
            <div>
              <CardTitle>Fleet</CardTitle>
              <p className="mt-1 text-sm text-muted-foreground">
                Your machines. Capacity is whatever they add up to.
              </p>
            </div>
            <StatusPill status={data?.fleet.online ? "online" : "offline"} />
          </CardHeader>
          <CardBody className="grid gap-3 sm:grid-cols-3">
            <Stat
              label="Nodes online"
              value={`${data?.fleet.online ?? 0}/${data?.fleet.total ?? 0}`}
              tone={data?.fleet.online ? "ok" : "danger"}
              hint={data?.fleet.total ? undefined : "no machines enrolled yet"}
            />
            <Stat label="vCPU" value={String(data?.fleet.cores ?? 0)} hint="across online nodes" />
            <Stat label="Memory" value={memory(data?.fleet.memoryMb)} hint="across online nodes" />
          </CardBody>
        </Card>

        <Card className="rise" style={{ animationDelay: "60ms" }}>
          <CardHeader>
            <div>
              <CardTitle>Traffic · last 24h</CardTitle>
              <p className="mt-1 text-sm text-muted-foreground">Reported by the node proxies.</p>
            </div>
          </CardHeader>
          <CardBody className="grid grid-cols-2 gap-3">
            <Stat label="Requests" value={compact(data?.usage24h.requests)} />
            <Stat label="Visitors" value={compact(data?.usage24h.visitors)} />
            <Stat
              label="Errors"
              value={compact(data?.usage24h.errors)}
              tone={data?.usage24h.errors ? "warn" : undefined}
            />
            <Stat label="Egress" value={bytes(data?.usage24h.bytesOut)} />
          </CardBody>
        </Card>
      </div>

      <div className="mt-4 grid gap-4 lg:grid-cols-3">
        <Card className="rise" style={{ animationDelay: "90ms" }}>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <Boxes className="size-4 text-primary" /> Projects
            </CardTitle>
            <Link href="/projects" className="text-xs text-muted-foreground hover:text-primary">
              open
            </Link>
          </CardHeader>
          <CardBody className="space-y-2 text-sm">
            <Row label="Running" value={data?.projects.running ?? 0} status="running" />
            <Row label="Deploying" value={data?.projects.deploying ?? 0} status="deploying" />
            <Row label="Failed" value={data?.projects.failed ?? 0} status="failed" />
            <Row label="No capacity" value={data?.projects.down ?? 0} status="no_capacity" />
            <Row label="Drafts" value={data?.projects.drafts ?? 0} status="draft" />
          </CardBody>
        </Card>

        <Card className="rise" style={{ animationDelay: "120ms" }}>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <Globe className="size-4 text-primary" /> Domains
            </CardTitle>
            <Link href="/domains" className="text-xs text-muted-foreground hover:text-primary">
              open
            </Link>
          </CardHeader>
          <CardBody className="space-y-2 text-sm">
            <Row label="Live" value={data?.domains.live ?? 0} status="live" />
            <Row label="Propagating" value={data?.domains.pending ?? 0} status="dns_set" />
            <Row label="Errored" value={data?.domains.error ?? 0} status="error" />
            <Row label="Total" value={data?.domains.total ?? 0} status="draft" />
          </CardBody>
        </Card>

        <Card className="rise" style={{ animationDelay: "150ms" }}>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <KeyRound className="size-4 text-primary" /> Credentials
            </CardTitle>
            <Link href="/settings" className="text-xs text-muted-foreground hover:text-primary">
              manage
            </Link>
          </CardHeader>
          <CardBody className="space-y-2 text-sm">
            {(data?.credentials ?? []).length === 0 ? (
              <p className="text-xs text-muted-foreground">
                Nothing connected yet. Add OpenAI, Cloudflare and your registrar keys to unlock AI
                setup and domains.
              </p>
            ) : (
              data!.credentials.map((credential) => (
                <div key={credential.id} className="flex items-center justify-between">
                  <span className="capitalize text-muted-foreground">{credential.id}</span>
                  <StatusPill status={credential.verifyStatus} />
                </div>
              ))
            )}
          </CardBody>
        </Card>
      </div>

      <div className="mt-4 grid gap-4 lg:grid-cols-[1fr_1fr]">
        <Card className="rise" style={{ animationDelay: "180ms" }}>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <Hammer className="size-4 text-primary" /> Recent app builds
            </CardTitle>
            <Link href="/builds" className="text-xs text-muted-foreground hover:text-primary">
              open
            </Link>
          </CardHeader>
          <CardBody className="space-y-2">
            {(data?.builds ?? []).length === 0 ? (
              <p className="text-xs text-muted-foreground">
                No binary builds dispatched. iOS/Android/Windows builds run in CI, not on your PC.
              </p>
            ) : (
              data!.builds.map((build) => (
                <div
                  key={build.id}
                  className="flex items-center justify-between rounded-md border border-border bg-[var(--raised)] px-3 py-2 text-sm"
                >
                  <span className="capitalize">{build.platform}</span>
                  <span className="flex items-center gap-3">
                    <span className="num text-[0.6875rem] text-muted-foreground">
                      {ago(build.createdAt)}
                    </span>
                    <StatusPill status={build.status} />
                  </span>
                </div>
              ))
            )}
          </CardBody>
        </Card>

        <Card className="rise" style={{ animationDelay: "210ms" }}>
          <CardHeader>
            <CardTitle>Activity</CardTitle>
            <Link href="/activity" className="text-xs text-muted-foreground hover:text-primary">
              full log
            </Link>
          </CardHeader>
          <CardBody className="space-y-2.5">
            {(data?.feed ?? []).length === 0 ? (
              <p className="text-xs text-muted-foreground">Nothing has happened yet.</p>
            ) : (
              data!.feed.map((entry) => (
                <div key={entry.id} className="flex items-start gap-2.5 text-sm">
                  <Dot
                    status={entry.level === "success" ? "running" : entry.level}
                    className="mt-1.5 shrink-0"
                  />
                  <span className="min-w-0 flex-1 text-muted-foreground">{entry.message}</span>
                  <span className="num shrink-0 text-[0.6875rem] text-muted-foreground/70">
                    {ago(entry.createdAt)}
                  </span>
                </div>
              ))
            )}
          </CardBody>
        </Card>
      </div>
    </>
  );
}

function Row({ label, value, status }: { label: string; value: number; status: string }) {
  return (
    <div className="flex items-center justify-between">
      <span className="flex items-center gap-2 text-muted-foreground">
        <Dot status={status} />
        {label}
      </span>
      <span className="num">{value}</span>
    </div>
  );
}

export default Dashboard;
