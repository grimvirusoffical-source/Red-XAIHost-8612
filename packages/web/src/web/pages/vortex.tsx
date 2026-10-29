import { useEffect, useState } from "react";
import { Download, ExternalLink, RefreshCw } from "lucide-react";
import { PageHeader } from "../components/shell";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { panelFetch } from "../lib/panel";

type Asset = { name: string; url: string; size: number };
type Release = { version: string; publishedAt: string; releaseUrl: string; assets: Asset[] };

function size(bytes: number) {
  if (!bytes) return "";
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

export default function VortexPage() {
  const [release, setRelease] = useState<Release | null>(null);
  const [error, setError] = useState("");
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setError("");
    void panelFetch("/api/releases/vortex")
      .then(async (r) => {
        const json = await r.json() as Release & { error?: string };
        if (!r.ok) throw new Error(json.error || "Could not load the current Vortex release.");
        if (!cancelled) setRelease(json);
      })
      .catch((e) => { if (!cancelled) setError(e instanceof Error ? e.message : "Could not load Vortex."); });
    return () => { cancelled = true; };
  }, [nonce]);

  const find = (needle: string) => release?.assets.find((a) => a.name.toLowerCase().includes(needle));

  return (
    <>
      <PageHeader
        eyebrow="Red-XAI"
        title="Vortex"
        subtitle="Download the latest stable Vortex release without leaving your Red-XAI account workspace."
        actions={<Button variant="outline" onClick={() => setNonce((x) => x + 1)}><RefreshCw className="size-4" /> Refresh</Button>}
      />
      {error && <div className="mb-4 rounded-md border border-[var(--danger)]/30 bg-[var(--danger)]/10 p-4 text-sm text-[var(--danger)]">{error}</div>}
      <Card>
        <CardHeader>
          <div>
            <CardTitle>{release ? `Vortex ${release.version}` : "Loading current release…"}</CardTitle>
            {release?.publishedAt && <p className="mt-1 text-xs text-muted-foreground">Published {new Date(release.publishedAt).toLocaleString()}</p>}
          </div>
        </CardHeader>
        <CardBody className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {[
            ["mac.dmg", "macOS (.dmg)", find("mac.dmg")],
            ["windows", "Windows (x86-64)", find("windows-amd64.zip")],
            ["linux", "Linux (x86-64)", find("linux-amd64.tar.gz")],
          ].map(([key, label, asset]) => {
            const item = asset as Asset | undefined;
            return (
              <div key={String(key)} className="rounded-md border border-border bg-[var(--raised)] p-4">
                <div className="font-medium">{String(label)}</div>
                <div className="mb-3 mt-1 text-xs text-muted-foreground">{item ? `${item.name} · ${size(item.size)}` : "Not published for this release"}</div>
                <Button asChild disabled={!item}>
                  <a href={item?.url ?? "#"}><Download className="size-4" /> Download</a>
                </Button>
              </div>
            );
          })}
          {release && (
            <div className="sm:col-span-2 lg:col-span-3">
              <Button variant="outline" asChild>
                <a href={release.releaseUrl}><ExternalLink className="size-4" /> Release notes and verification files</a>
              </Button>
            </div>
          )}
        </CardBody>
      </Card>
    </>
  );
}
