import { useState } from "react";
import { Link } from "wouter";
import {
  AlertTriangle,
  Check,
  CheckCircle2,
  Cloud,
  ExternalLink,
  Loader2,
  RefreshCcw,
  X,
} from "lucide-react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "./status";
import { CopyBlock } from "./copy";
import { useCloudflareAutoConnect, useCloudflareStatus, useManualSetup } from "../queries/cloudflare";
import { CloudflarePermissions, CreateTokenButton } from "./cloudflare-permissions";

/**
 * Cloudflare state, and the one button that tries to connect everything. The
 * panel attempts this by itself whenever a token is saved or a domain is added;
 * this is the manual nudge for when the owner wants it to happen right now.
 */
export function CloudflareBanner() {
  const status = useCloudflareStatus();
  const auto = useCloudflareAutoConnect();
  const [result, setResult] = useState<{
    ok: boolean;
    message: string;
    accountName: string | null;
    domains: { hostname: string; ok: boolean; detail: string }[];
  } | null>(null);

  const data = status.data;
  const connected = Boolean(data?.tokenValid && data?.accountId);
  const tone = connected
    ? "border-[var(--ok)]/30 bg-[var(--ok)]/8"
    : data?.configured
      ? "border-[var(--danger)]/30 bg-[var(--danger)]/8"
      : "border-[var(--warn)]/30 bg-[var(--warn)]/8";

  return (
    <div className={`rise mb-4 rounded-md border px-4 py-3 ${tone}`}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-2.5">
          {status.isLoading ? (
            <Loader2 className="mt-0.5 size-4 shrink-0 animate-spin text-primary" />
          ) : connected ? (
            <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-[var(--ok)]" />
          ) : (
            <Cloud className="mt-0.5 size-4 shrink-0 text-[var(--warn)]" />
          )}
          <div className="min-w-0">
            <p className="text-sm font-medium">
              {status.isLoading
                ? "Checking Cloudflare…"
                : connected
                  ? `Cloudflare connected${data?.accountName ? ` — ${data.accountName}` : ""}`
                  : data?.configured
                    ? "Cloudflare token saved but not usable"
                    : "Cloudflare not connected yet"}
            </p>
            {connected ? (
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                <Badge>{data?.zones.length ?? 0} zones</Badge>
                <Badge>{data?.tunnels.length ?? 0} tunnels</Badge>
                {data?.accountId && <Badge className="num">acct …{data.accountId.slice(-6)}</Badge>}
              </div>
            ) : (
              <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
                {data?.error ??
                  "Paste a Cloudflare API token under Settings — the account ID is discovered for you, then zones, tunnels and DNS are set up automatically."}
              </p>
            )}
          </div>
        </div>
        <div className="flex items-center gap-2">
          {!connected && !data?.configured && <CreateTokenButton />}
          {!connected && (
            <Button size="sm" variant="outline" asChild>
              <Link href="/settings">
                Paste token <ExternalLink className="size-3.5" />
              </Link>
            </Button>
          )}
          <Button
            size="sm"
            disabled={auto.isPending}
            onClick={async () => {
              const response = await auto.mutateAsync({});
              setResult({
                ok: response.ok,
                message: response.message,
                accountName: response.accountName,
                domains: response.domains,
              });
              void status.refetch();
            }}
          >
            {auto.isPending ? (
              <Loader2 className="size-3.5 animate-spin" />
            ) : (
              <RefreshCcw className="size-3.5" />
            )}
            Connect Cloudflare now
          </Button>
        </div>
      </div>

      {/* A saved-but-unusable token is almost always a permissions problem —
          put the checklist and the pre-ticked token link right where the
          failure is reported. */}
      {data?.configured && (
        <div className="mt-3">
          <CloudflarePermissions defaultOpen={!connected} />
        </div>
      )}

      {result && (
        <div className="mt-3 border-t border-border pt-2.5">
          <div className="flex items-start justify-between gap-2">
            <p className={`text-xs ${result.ok ? "text-[var(--ok)]" : "text-[var(--danger)]"}`}>
              {result.message}
            </p>
            <button
              onClick={() => setResult(null)}
              className="text-muted-foreground hover:text-foreground"
            >
              <X className="size-3.5" />
            </button>
          </div>
          {result.domains.length > 0 && (
            <ul className="mt-2 space-y-1">
              {result.domains.map((domain) => (
                <li key={domain.hostname} className="flex items-start gap-2 text-xs">
                  {domain.ok ? (
                    <Check className="mt-0.5 size-3.5 shrink-0 text-[var(--ok)]" />
                  ) : (
                    <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-[var(--danger)]" />
                  )}
                  <span className="num">{domain.hostname}</span>
                  <span className="text-muted-foreground">{domain.detail}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <div className="flex gap-3">
      <span className="num grid size-6 shrink-0 place-items-center rounded-full border border-border bg-[var(--raised)] text-[0.6875rem] font-semibold text-primary">
        {n}
      </span>
      <div className="min-w-0 flex-1 space-y-2">
        <p className="text-sm font-medium">{title}</p>
        <div className="space-y-2 text-xs leading-relaxed text-muted-foreground">{children}</div>
      </div>
    </div>
  );
}

/**
 * Step-by-step for wiring a hostname without any registrar or Cloudflare
 * automation — every value pre-filled with this domain's real numbers.
 */
export function ManualSetup({ domainId, onClose }: { domainId: string; onClose: () => void }) {
  const setup = useManualSetup(domainId, true);
  const data = setup.data;
  const cname = data?.cnameTarget ?? "<tunnel-id>.cfargotunnel.com";

  return (
    <Card className="rise mt-3 border-[var(--info)]/30">
      <CardHeader>
        <div>
          <CardTitle>Set this domain up by hand</CardTitle>
          <p className="mt-1 text-xs text-muted-foreground">
            Same result as the Connect button, done in the Cloudflare and registrar dashboards. Do
            it this way when you would rather not give the panel a registrar API key.
          </p>
        </div>
        <button onClick={onClose} className="text-muted-foreground hover:text-foreground">
          <X className="size-4" />
        </button>
      </CardHeader>
      <CardBody className="space-y-5">
        {setup.isLoading ? (
          <Loader2 className="size-4 animate-spin text-primary" />
        ) : (
          <>
            <Step n={1} title="Add the root domain to Cloudflare">
              <p>
                In{" "}
                <a
                  className="text-primary hover:underline"
                  href="https://dash.cloudflare.com/?to=/:account/add-site"
                  target="_blank"
                  rel="noreferrer"
                >
                  Cloudflare → Add a site
                </a>
                , enter the root domain below and pick the Free plan. Cloudflare imports your
                existing DNS records — check nothing is missing before continuing.
              </p>
              <CopyBlock label="Root domain" value={data?.rootDomain ?? ""} />
            </Step>

            <Step n={2} title="Point the registrar at Cloudflare's nameservers">
              <p>
                Open your registrar (GoDaddy, Namecheap, Squarespace, whoever sold you the domain),
                find <em>Nameservers</em> / <em>DNS management</em>, choose <em>Custom</em> and
                replace both entries with the pair Cloudflare shows on that domain's overview page.
                {data?.nameServers?.length
                  ? " Yours are below."
                  : " Cloudflare shows them after step 1 — they look like ada.ns.cloudflare.com."}
              </p>
              {data?.nameServers?.length ? (
                <CopyBlock label="Cloudflare nameservers" value={data.nameServers.join("\n")} />
              ) : null}
              <p>
                Propagation takes anywhere from a few minutes to 24 hours. The panel rechecks every
                10 minutes on its own and flips the domain to <span className="num">live</span> when
                Cloudflare reports the zone active — nothing else to press.
              </p>
            </Step>

            <Step n={3} title="Create a tunnel for your machine">
              <p>
                In{" "}
                <a
                  className="text-primary hover:underline"
                  href="https://one.dash.cloudflare.com/"
                  target="_blank"
                  rel="noreferrer"
                >
                  Zero Trust → Networks → Tunnels
                </a>
                , create a tunnel of type <em>Cloudflared</em> and name it exactly as below, so the
                panel recognises it as this domain's tunnel.
              </p>
              <CopyBlock label="Tunnel name" value={data?.tunnelName ?? ""} />
              <p>
                Cloudflare then shows an install command containing a long token. Run it on the
                machine that hosts the project — that is what installs and starts{" "}
                <span className="num">cloudflared</span> as a service. On Windows use the{" "}
                <em>Windows</em> tab of that dialog; on a VPS use the Debian/RPM tab.
              </p>
            </Step>

            <Step n={4} title="Route the hostname into the tunnel">
              <p>
                Still in the tunnel, open <em>Public Hostname</em> → <em>Add a public hostname</em>{" "}
                and fill it in exactly like this:
              </p>
              <CopyBlock
                label="Public hostname"
                value={[
                  `Subdomain:  ${data?.recordName ?? "@"}`,
                  `Domain:     ${data?.rootDomain ?? ""}`,
                  `Type:       HTTP`,
                  `URL:        localhost:${data?.port ?? 8080}`,
                ].join("\n")}
              />
              <p>
                The port is the one{" "}
                {data?.projectName ? (
                  <>
                    <span className="num">{data.projectName}</span> listens on
                  </>
                ) : (
                  "your project listens on"
                )}{" "}
                inside the node. Adding the public hostname here creates the DNS record for you —
                skip step 5 if you use it.
              </p>
            </Step>

            <Step n={5} title="Or add the DNS record yourself">
              <p>
                If you would rather write the record by hand, add this to the zone's DNS tab. The
                orange cloud (proxied) must be on — the tunnel only answers proxied traffic.
              </p>
              <CopyBlock
                label="DNS record"
                value={[
                  `Type:    CNAME`,
                  `Name:    ${data?.recordName ?? "@"}`,
                  `Target:  ${cname}`,
                  `Proxy:   Proxied (orange cloud)`,
                  `TTL:     Auto`,
                ].join("\n")}
              />
              {!data?.cnameTarget && (
                <p>
                  The target contains the tunnel's ID — copy it from the tunnel's overview page in
                  Zero Trust, or press Connect once and the panel fills this in.
                </p>
              )}
            </Step>

            <Step n={6} title="Check it">
              <p>
                Deploy the project from its page, then open{" "}
                <a
                  className="num text-primary hover:underline"
                  href={`https://${data?.hostname ?? ""}`}
                  target="_blank"
                  rel="noreferrer"
                >
                  https://{data?.hostname}
                </a>
                . A Cloudflare 502/1033 means the tunnel is up but nothing is listening on{" "}
                <span className="num">localhost:{data?.port ?? 8080}</span> — start the project. A
                DNS error means the nameserver change has not landed yet.
              </p>
            </Step>
          </>
        )}
      </CardBody>
    </Card>
  );
}
