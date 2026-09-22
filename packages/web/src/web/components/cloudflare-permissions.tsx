import { useState } from "react";
import { AlertTriangle, Check, ExternalLink, Loader2, Minus, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useCloudflarePermissions } from "../queries/cloudflare";

/**
 * The one-click token link. Cloudflare accepts a template URL that arrives with
 * every permission this panel needs already ticked, so the owner never has to
 * find four checkboxes in a long list — which is the only reason a token ever
 * comes back with the wrong permissions.
 *
 * Kept as a constant here too so the button renders before any request:
 * the server sends the same string back with the checklist.
 */
export const CLOUDFLARE_TOKEN_TEMPLATE_URL =
  "https://dash.cloudflare.com/profile/api-tokens?permissionGroupKeys=" +
  encodeURIComponent(
    JSON.stringify([
      { key: "zone", type: "edit" },
      { key: "dns", type: "edit" },
      { key: "argotunnel", type: "edit" },
      { key: "account_settings", type: "read" },
      { key: "zone_settings", type: "edit" },
      { key: "firewall_services", type: "edit" },
    ]),
  ) +
  "&accountId=*&zoneId=all&name=RedXAIHost";

/** Big obvious "make me a correct token" button. */
export function CreateTokenButton({
  url = CLOUDFLARE_TOKEN_TEMPLATE_URL,
  size = "sm",
}: {
  url?: string;
  size?: "sm" | "default";
}) {
  return (
    <Button size={size} asChild>
      <a href={url} target="_blank" rel="noreferrer">
        <ShieldCheck className="size-3.5" />
        Create a token with the right permissions
        <ExternalLink className="size-3" />
      </a>
    </Button>
  );
}

function Mark({ ok }: { ok: boolean | null }) {
  if (ok === null) return <Minus className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />;
  return ok ? (
    <Check className="mt-0.5 size-3.5 shrink-0 text-[var(--ok)]" />
  ) : (
    <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-[var(--danger)]" />
  );
}

/**
 * What the token must be able to do, and — once checked — what it actually can
 * do. Every line is a real Cloudflare call, not a guess from the token's
 * metadata, so a permission that looks ticked but does not work still shows up.
 */
export function CloudflarePermissions({ defaultOpen = false }: { defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  const check = useCloudflarePermissions(open);
  const data = check.data;
  const failing = (data?.checks ?? []).filter((item) => item.ok === false);

  return (
    <div className="rounded-lg border border-border/60 bg-black/20 p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-[11px] uppercase tracking-wider text-muted-foreground">
          Token permissions
        </p>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              if (open) void check.refetch();
              setOpen(true);
            }}
            disabled={check.isFetching}
          >
            {check.isFetching ? <Loader2 className="size-3.5 animate-spin" /> : null}
            {open ? "Re-check" : "Check my token"}
          </Button>
          <CreateTokenButton url={data?.templateUrl} />
        </div>
      </div>

      {!open && (
        <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
          The button above opens Cloudflare's token form with every permission already ticked —
          scroll to the bottom, press Continue, then Create Token, and paste it here. Nothing else
          to configure.
        </p>
      )}

      {open && (
        <div className="mt-2.5">
          {check.isLoading ? (
            <Loader2 className="size-4 animate-spin text-primary" />
          ) : check.isError ? (
            <p className="text-xs text-[var(--danger)]">Could not reach Cloudflare to check.</p>
          ) : !data?.tokenValid && (data?.checks.length ?? 0) === 0 ? (
            <p className="text-xs text-muted-foreground">
              No token saved yet — make one with the button above and paste it in.
            </p>
          ) : (
            <>
              <ul className="space-y-1.5">
                {(data?.checks ?? []).map((item) => (
                  <li key={item.name} className="flex items-start gap-2 text-xs">
                    <Mark ok={item.ok} />
                    <span className="min-w-0">
                      <span className="font-medium">{item.name}</span>
                      <span className="text-muted-foreground"> — {item.detail}</span>
                    </span>
                  </li>
                ))}
              </ul>
              <p
                className={`mt-2.5 text-xs leading-relaxed ${
                  failing.length > 0 ? "text-[var(--danger)]" : "text-[var(--ok)]"
                }`}
              >
                {failing.length > 0
                  ? `${failing.length} permission${failing.length > 1 ? "s" : ""} missing. Make a replacement token with the button above and paste it in — you do not have to edit the old one.`
                  : "Everything this panel needs works. Domains, tunnels and DNS can be set up automatically."}
              </p>
            </>
          )}
        </div>
      )}
    </div>
  );
}
