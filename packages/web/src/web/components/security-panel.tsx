import { AlertTriangle, Check, Loader2, Minus, RefreshCw, Shield } from "lucide-react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { useHardenZones, useSecurityStatus } from "../queries/cloudflare";

/**
 * Two layers, shown honestly and separately.
 *
 * The panel's own rate limits are always on and need no Cloudflare at all. The
 * edge rules are what actually stop a flood before it reaches a home PC, and
 * they depend on the token's permissions — so anything Cloudflare refused is
 * shown as refused rather than quietly rounded up to "protected".
 */
function Mark({ ok }: { ok: boolean | null | undefined }) {
  if (ok === null || ok === undefined) {
    return <Minus className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />;
  }
  return ok ? (
    <Check className="mt-0.5 size-3.5 shrink-0 text-[var(--ok)]" />
  ) : (
    <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-[var(--danger)]" />
  );
}

function Row({
  ok,
  label,
  detail,
}: {
  ok: boolean | null | undefined;
  label: string;
  detail: string;
}) {
  return (
    <li className="flex items-start gap-2 text-xs">
      <Mark ok={ok} />
      <span className="min-w-0">
        <span className="font-medium">{label}</span>
        <span className="text-muted-foreground"> — {detail}</span>
      </span>
    </li>
  );
}

export function SecurityPanel() {
  const status = useSecurityStatus();
  const harden = useHardenZones();
  const data = status.data;

  return (
    <Card className="rise mb-4">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Shield className="size-4 text-primary" /> Attack protection
        </CardTitle>
        <Button
          size="sm"
          variant="outline"
          onClick={() => harden.mutate({})}
          disabled={harden.isPending}
        >
          {harden.isPending ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : (
            <RefreshCw className="size-3.5" />
          )}
          Re-apply to all zones
        </Button>
      </CardHeader>
      <CardBody className="space-y-4">
        {status.isLoading ? (
          <Loader2 className="size-4 animate-spin text-primary" />
        ) : (
          <>
            <div>
              <p className="eyebrow mb-2">This panel — always on</p>
              <ul className="space-y-1.5">
                {(data?.panel.limits ?? []).map((limit) => (
                  <Row key={limit.scope} ok label={limit.scope} detail={limit.detail} />
                ))}
              </ul>
              <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
                Enforced in the panel itself, so it works with or without Cloudflare. Over the
                limit gets a 429 and a Retry-After, and a burst of bad sign-ins locks that IP out
                for five minutes. Currently tracking {data?.panel.trackedKeys ?? 0} client(s).
              </p>
            </div>

            <div>
              <p className="eyebrow mb-2">Cloudflare edge — per domain</p>
              {(data?.edge ?? []).length === 0 ? (
                <p className="text-xs text-muted-foreground">
                  No Cloudflare zones yet. Add a domain and the edge rules are applied with it.
                </p>
              ) : (
                <div className="space-y-3">
                  {(data?.edge ?? []).map((zone) => (
                    <div key={zone.zoneId} className="rounded-lg border border-border/60 bg-black/20 p-3">
                      <p className="mb-2 text-xs font-medium">{zone.hostname}</p>
                      {zone.ok ? (
                        <ul className="space-y-1.5">
                          <Row
                            ok={zone.rateLimitRules > 0}
                            label="Flood rate limit"
                            detail={
                              zone.rateLimitRules > 0
                                ? "100 requests / 10s per IP, then a managed challenge"
                                : "no rate-limit rule is active on this zone"
                            }
                          />
                          <Row
                            ok={zone.wafRules > 0}
                            label="Probe blocking"
                            detail={
                              zone.wafRules > 0
                                ? `${zone.wafRules} firewall rule(s) blocking /.env, /.git and similar`
                                : "no firewall rule is active on this zone"
                            }
                          />
                          <Row
                            ok={zone.botFightMode}
                            label="Bot Fight Mode"
                            detail={
                              zone.botFightMode === undefined
                                ? "not readable — needs Zone · Bot Management · Edit on your token"
                                : zone.botFightMode
                                  ? "on — scripted traffic is challenged at the edge"
                                  : "off"
                            }
                          />
                          <Row
                            ok={zone.alwaysUseHttps}
                            label="HTTPS"
                            detail={
                              zone.alwaysUseHttps
                                ? `forced, TLS ${zone.minTlsVersion ?? "?"} floor`
                                : "not forced yet"
                            }
                          />
                          <Row
                            ok={Boolean(zone.securityLevel)}
                            label="Security level"
                            detail={zone.securityLevel ?? "not readable with this token"}
                          />
                        </ul>
                      ) : (
                        <p className="text-xs text-[var(--danger)]">
                          Cannot read this zone's protection: {zone.error ?? "Cloudflare refused"}.
                          Make a new token with the button above — it now includes the security
                          permissions.
                        </p>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </div>

            {harden.data ? (
              <p
                className={`text-xs leading-relaxed ${
                  harden.data.ok ? "text-[var(--ok)]" : "text-[var(--danger)]"
                }`}
              >
                {harden.data.message}
              </p>
            ) : null}
          </>
        )}
      </CardBody>
    </Card>
  );
}
