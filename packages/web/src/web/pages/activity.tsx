import { useState } from "react";
import { Activity as ActivityIcon, Loader2 } from "lucide-react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { PageHeader } from "../components/shell";
import { Badge } from "../components/status";
import { useActivityFeed } from "../queries/overview";
import { clock } from "../lib/format";

const SCOPES = ["all", "node", "project", "deployment", "domain", "build", "auth", "system"];

const LEVEL_CLASS: Record<string, string> = {
  success: "bg-[var(--ok)]",
  info: "bg-[var(--info)]",
  warn: "bg-[var(--warn)]",
  error: "bg-[var(--danger)]",
};

function ActivityPage() {
  const [scope, setScope] = useState("all");
  const feed = useActivityFeed(200);

  const rows = (feed.data ?? []).filter((row) => scope === "all" || row.scope === scope);

  return (
    <>
      <PageHeader
        eyebrow="Audit trail"
        title="Activity"
        subtitle="Every node enrolment, deploy, domain change and credential save, newest first."
        actions={
          <div className="flex flex-wrap gap-1.5">
            {SCOPES.map((option) => (
              <Button
                key={option}
                size="sm"
                variant={option === scope ? "default" : "outline"}
                onClick={() => setScope(option)}
              >
                {option}
              </Button>
            ))}
          </div>
        }
      />

      <Card className="rise">
        <CardHeader>
          <CardTitle>{rows.length} events</CardTitle>
        </CardHeader>
        <CardBody>
          {feed.isLoading ? (
            <Loader2 className="size-5 animate-spin text-primary" />
          ) : rows.length === 0 ? (
            <div className="py-12 text-center">
              <ActivityIcon className="mx-auto size-6 text-muted-foreground" />
              <p className="mt-3 text-sm text-muted-foreground">Nothing logged in this scope yet.</p>
            </div>
          ) : (
            <ol className="relative space-y-4 border-l border-border pl-5">
              {rows.map((row) => (
                <li key={row.id} className="relative">
                  <span
                    className={`absolute -left-[1.4375rem] top-1.5 size-2 rounded-full ${
                      LEVEL_CLASS[row.level] ?? "bg-muted-foreground"
                    }`}
                  />
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge>{row.scope}</Badge>
                    <span className="num text-[0.6875rem] text-muted-foreground">
                      {clock(row.createdAt)}
                    </span>
                  </div>
                  <p className="mt-1 text-sm leading-relaxed">{row.message}</p>
                </li>
              ))}
            </ol>
          )}
        </CardBody>
      </Card>
    </>
  );
}

export default ActivityPage;
