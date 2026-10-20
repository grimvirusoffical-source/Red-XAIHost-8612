import { Database, Loader2, AlertTriangle, CheckCircle2, Link2 } from "lucide-react";
import { Link } from "wouter";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { PageHeader } from "../components/shell";
import { Badge, StatusPill } from "../components/status";
import { useRedxdbDatabases, useRedxdbStatus } from "../queries/redxdb";

function errMessage(error: unknown): string {
  if (!error) return "Request failed";
  if (error instanceof Error && error.message) return error.message;
  const e = error as { message?: string; data?: { message?: string } };
  return e.message || e.data?.message || String(error);
}

function RedXDBPage() {
  const status = useRedxdbStatus();
  const databases = useRedxdbDatabases();
  const s = status.data;

  return (
    <>
      <PageHeader
        eyebrow="RedX database"
        title="Database"
        subtitle="Live view of the linked RedX DB server (redxd). Configure the URL and API key under Settings → redxdb, or set REDX_DB_URL / REDX_DB_KEY."
        actions={
          <Button asChild variant="outline" size="sm">
            <Link href="/settings">
              <Link2 className="size-4" /> Settings
            </Link>
          </Button>
        }
      />

      <div className="mb-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Stat
          label="Reachable"
          value={status.isLoading ? "…" : s?.reachable ? "yes" : "no"}
          tone={s?.reachable ? "ok" : s ? "danger" : undefined}
        />
        <Stat label="URL" value={s?.url ?? "—"} mono />
        <Stat label="API key" value={s?.keyConfigured ? "configured" : "missing"} tone={s?.keyConfigured ? "ok" : "warn"} />
        <Stat
          label="Version"
          value={s?.reachable && s.health && typeof s.health === "object" && "version" in s.health ? String((s.health as { version?: string }).version ?? "—") : "—"}
          mono
        />
      </div>

      <Card className="rise mb-4" id="redxdb-status">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Database className="size-4 text-primary" /> Link status
          </CardTitle>
          {s?.reachable ? <StatusPill status="online" /> : s ? <StatusPill status="offline" /> : null}
        </CardHeader>
        <CardBody className="space-y-3 text-sm">
          {status.isLoading ? (
            <Loader2 className="size-5 animate-spin text-primary" />
          ) : status.isError ? (
            <Callout kind="err" text={errMessage(status.error)} />
          ) : !s?.reachable ? (
            <Callout
              kind="warn"
              text={s?.error || "RedX DB is not reachable. Start redxd, or fix the URL in Settings."}
            />
          ) : (
            <>
              <Row label="Health" value={String((s.health as { status?: string })?.status ?? "ok")} ok />
              <Row label="Role" value={String((s.health as { role?: string })?.role ?? "—")} />
              {s.adminNote ? (
                <Callout kind="warn" text={s.adminNote} />
              ) : s.adminError ? (
                <Callout kind="warn" text={`Admin status: ${s.adminError}`} />
              ) : s.admin ? (
                <>
                  <Row label="Node" value={String(s.admin.node_id ?? "—")} mono />
                  <Row label="Databases on host" value={String(s.admin.databases ?? "—")} />
                  <Row label="Listen" value={String(s.admin.listen ?? "—")} mono />
                </>
              ) : !s.keyConfigured ? (
                <Callout kind="warn" text="No API key configured. Add one under Settings (redxdb) or set REDX_DB_KEY to list databases and see admin status." />
              ) : null}
            </>
          )}
        </CardBody>
      </Card>

      <Card className="rise" id="redxdb-databases">
        <CardHeader>
          <CardTitle>Databases</CardTitle>
          <Badge>{(databases.data?.databases ?? []).length} visible</Badge>
        </CardHeader>
        <CardBody>
          {databases.isLoading ? (
            <Loader2 className="size-5 animate-spin text-primary" />
          ) : databases.isError ? (
            <Callout kind="err" text={errMessage(databases.error)} />
          ) : (databases.data?.databases ?? []).length === 0 ? (
            <div className="py-10 text-center">
              <Database className="mx-auto size-6 text-muted-foreground" />
              <p className="mt-3 text-sm text-muted-foreground">
                No databases visible to this key yet. Create one in the RedX console, or check the key's scope.
              </p>
            </div>
          ) : (
            <ul className="divide-y divide-border">
              {(databases.data?.databases ?? []).map((db, i) => {
                const name = typeof db === "string" ? db : String((db as { name?: string }).name ?? JSON.stringify(db));
                return (
                  <li key={name + i} className="flex items-center justify-between py-2.5 text-sm">
                    <span className="num font-medium">{name}</span>
                    <CheckCircle2 className="size-4 text-[var(--ok)]" />
                  </li>
                );
              })}
            </ul>
          )}
        </CardBody>
      </Card>
    </>
  );
}

function Stat({
  label,
  value,
  tone,
  mono,
}: {
  label: string;
  value: string;
  tone?: "ok" | "warn" | "danger";
  mono?: boolean;
}) {
  const color =
    tone === "ok" ? "text-[var(--ok)]" : tone === "warn" ? "text-[var(--warn)]" : tone === "danger" ? "text-[var(--danger)]" : "";
  return (
    <Card className="rise">
      <CardBody className="py-3">
        <div className="eyebrow">{label}</div>
        <div className={`mt-1 truncate text-sm font-medium ${mono ? "num" : ""} ${color}`} title={value}>
          {value}
        </div>
      </CardBody>
    </Card>
  );
}

function Row({ label, value, ok, mono }: { label: string; value: string; ok?: boolean; mono?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <span className="eyebrow">{label}</span>
      <span className={`num text-right ${mono ? "" : ""} ${ok ? "text-[var(--ok)]" : ""}`}>{value}</span>
    </div>
  );
}

function Callout({ kind, text }: { kind: "err" | "warn"; text: string }) {
  return (
    <div
      className={`flex items-start gap-2 rounded-md border px-3 py-2 text-xs leading-relaxed ${
        kind === "err" ? "border-[var(--danger)]/40 bg-[var(--danger)]/10" : "border-[var(--warn)]/40 bg-[var(--warn)]/10"
      }`}
      role="status"
    >
      <AlertTriangle className={`mt-0.5 size-4 shrink-0 ${kind === "err" ? "text-[var(--danger)]" : "text-[var(--warn)]"}`} />
      <span className="text-muted-foreground">{text}</span>
    </div>
  );
}

export default RedXDBPage;
