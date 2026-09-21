import { useState } from "react";
import { BarChart3, Loader2 } from "lucide-react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { PageHeader } from "../components/shell";
import { useUsageSeries } from "../queries/usage";
import { bytes, compact } from "../lib/format";

const RANGES = [
  { days: 1, label: "24h" },
  { days: 7, label: "7d" },
  { days: 30, label: "30d" },
  { days: 90, label: "90d" },
];

function Usage() {
  const [days, setDays] = useState(7);
  const query = useUsageSeries(days);
  const data = query.data;

  const buckets = data?.buckets ?? [];
  const peak = Math.max(1, ...buckets.map((bucket) => bucket.requests));
  const topProjects = data?.projects ?? [];
  const topRequests = Math.max(1, ...topProjects.map((project) => project.requests));

  return (
    <>
      <PageHeader
        eyebrow="Who is using it"
        title="Usage"
        subtitle="Traffic your nodes actually served, rolled up per hour and split by project. Reported by each node's reverse proxy."
        actions={
          <div className="flex gap-1.5">
            {RANGES.map((range) => (
              <Button
                key={range.days}
                size="sm"
                variant={range.days === days ? "default" : "outline"}
                onClick={() => setDays(range.days)}
              >
                {range.label}
              </Button>
            ))}
          </div>
        }
      />

      <div className="mb-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Stat label="Requests" value={compact(data?.totals.requests)} />
        <Stat label="Visitors" value={compact(data?.totals.visitors)} />
        <Stat label="Data out" value={bytes(data?.totals.bytesOut)} />
        <Stat
          label="Errors"
          value={compact(data?.totals.errors)}
          tone={(data?.totals.errors ?? 0) > 0 ? "danger" : undefined}
        />
      </div>

      <Card className="rise mb-4">
        <CardHeader>
          <CardTitle>Requests per hour</CardTitle>
          <span className="num text-[0.6875rem] text-muted-foreground">peak {compact(peak)}</span>
        </CardHeader>
        <CardBody>
          {query.isLoading ? (
            <Loader2 className="size-5 animate-spin text-primary" />
          ) : buckets.length === 0 ? (
            <div className="py-12 text-center">
              <BarChart3 className="mx-auto size-6 text-muted-foreground" />
              <p className="mt-3 text-sm text-muted-foreground">
                No traffic reported yet. Once a node serves a project, its hourly rollups land here.
              </p>
            </div>
          ) : (
            <div className="flex h-44 items-end gap-[3px]">
              {buckets.map((bucket) => {
                const height = Math.max(2, (bucket.requests / peak) * 100);
                const bad = bucket.errors > 0;
                return (
                  <div
                    key={bucket.bucketAt}
                    title={`${new Date(bucket.bucketAt * 1000).toLocaleString()} — ${bucket.requests} requests, ${bucket.errors} errors`}
                    className="min-w-[3px] flex-1 rounded-t-[2px] transition-opacity hover:opacity-70"
                    style={{
                      height: `${height}%`,
                      background: bad
                        ? "linear-gradient(to top, var(--danger), color-mix(in oklab, var(--danger) 55%, transparent))"
                        : "linear-gradient(to top, var(--primary), color-mix(in oklab, var(--primary) 45%, transparent))",
                    }}
                  />
                );
              })}
            </div>
          )}
        </CardBody>
      </Card>

      <Card className="rise">
        <CardHeader>
          <CardTitle>By project</CardTitle>
        </CardHeader>
        <CardBody className="space-y-3">
          {topProjects.length === 0 ? (
            <p className="text-sm text-muted-foreground">Nothing to split yet.</p>
          ) : (
            topProjects.map((project) => (
              <div key={project.projectId}>
                <div className="flex items-baseline justify-between gap-3">
                  <span className="truncate text-sm">{project.name}</span>
                  <span className="num shrink-0 text-xs text-muted-foreground">
                    {compact(project.requests)} req · {compact(project.visitors)} visitors ·{" "}
                    {bytes(project.bytesOut)}
                  </span>
                </div>
                <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-muted">
                  <div
                    className="h-full rounded-full bg-primary"
                    style={{ width: `${(project.requests / topRequests) * 100}%` }}
                  />
                </div>
              </div>
            ))
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
}: {
  label: string;
  value: string;
  tone?: "danger";
}) {
  return (
    <Card className="rise">
      <CardBody>
        <div className="eyebrow">{label}</div>
        <div
          className={`num mt-1.5 text-2xl font-medium ${tone === "danger" ? "text-[var(--danger)]" : ""}`}
        >
          {value}
        </div>
      </CardBody>
    </Card>
  );
}

export default Usage;
