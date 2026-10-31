import { useEffect, useState } from "react";
import { Link } from "wouter";
import { BarChart3, Bell, CheckCircle2, ExternalLink, Info, Loader2, Save, ShieldCheck, Trash2 } from "lucide-react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/field";
import { PageHeader } from "../components/shell";
import { CloudflarePermissions } from "../components/cloudflare-permissions";
import { SecurityPanel } from "../components/security-panel";
import { Badge, StatusPill } from "../components/status";
import {
  useOwnerInfo,
  useProviders,
  useRemoveProvider,
  useSaveProvider,
  useVerifyProvider,
} from "../queries/settings";
import { ago } from "../lib/format";

const WHY: Record<string, string> = {
  openai: "Reads your uploaded project and writes the run plan — runtime, commands, port, Dockerfile.",
  cloudflare: "Creates the zone, the named tunnel and the proxied DNS record that make a project public.",
  godaddy: "Points a GoDaddy domain's nameservers at Cloudflare over GoDaddy's official API.",
  namecheap: "Same for Namecheap, over their official XML API. Whitelist your IP in their dashboard first.",
  github: "Dispatches installer/app builds and drives auto-deploy on push.",
  expo: "Builds the iOS and Android binaries through EAS.",
};

function BrowserPreferences() {
  const initialNotification = typeof Notification === "undefined" ? "unsupported" : Notification.permission;
  const [notification, setNotification] = useState(initialNotification);
  const [analytics, setAnalytics] = useState<"yes" | "no" | "unknown">("unknown");
  const [message, setMessage] = useState("");

  useEffect(() => {
    const readAnalytics = () => {
      const value = (window as typeof window & { __redxaiAnalyticsConsent?: string }).__redxaiAnalyticsConsent;
      setAnalytics(value === "yes" || value === "no" ? value : "unknown");
    };
    readAnalytics();
    const handler = (event: Event) => {
      const allowed = (event as CustomEvent<{ allowed?: boolean }>).detail?.allowed;
      if (typeof allowed === "boolean") setAnalytics(allowed ? "yes" : "no");
    };
    window.addEventListener("redxai-analytics-consent", handler);
    return () => window.removeEventListener("redxai-analytics-consent", handler);
  }, []);

  async function enableNotifications() {
    setMessage("");
    if (typeof Notification === "undefined") {
      setNotification("unsupported");
      setMessage("This browser does not support web notifications.");
      return;
    }
    let permission = Notification.permission;
    if (permission === "default") permission = await Notification.requestPermission();
    setNotification(permission);
    if (permission !== "granted") {
      setMessage("Notifications are not enabled. You can change the permission in your browser settings.");
      return;
    }
    if ("serviceWorker" in navigator) {
      const registration = await navigator.serviceWorker.register("/redxai-connect/notifications-sw.js", {
        scope: "/redxai-connect/",
      });
      await registration.showNotification("Red-XAI notifications enabled", {
        body: "This browser is ready for Red-XAI account activity and service notifications.",
        tag: "redxai-notifications-settings-test",
      });
    } else {
      new Notification("Red-XAI notifications enabled", {
        body: "This browser is ready for Red-XAI notifications.",
      });
    }
    setMessage("Notifications are enabled and the browser test was sent.");
  }

  return (
    <Card className="rise mb-4">
      <CardHeader>
        <CardTitle>Browser preferences</CardTitle>
      </CardHeader>
      <CardBody className="grid gap-4 md:grid-cols-2">
        <div className="rounded-md border border-border bg-[var(--raised)] p-4">
          <div className="flex items-center gap-2 font-medium"><Bell className="size-4 text-primary" /> Notifications</div>
          <p className="my-2 text-xs leading-relaxed text-muted-foreground">
            Permission: <span className="num">{notification}</span>. The login prompt only asks after a user gesture; this control lets you retry later.
          </p>
          <Button size="sm" variant="outline" onClick={() => void enableNotifications()}>
            Enable & test
          </Button>
          {message && <p className="mt-2 text-xs text-muted-foreground">{message}</p>}
        </div>
        <div className="rounded-md border border-border bg-[var(--raised)] p-4">
          <div className="flex items-center gap-2 font-medium"><BarChart3 className="size-4 text-primary" /> Optional analytics</div>
          <p className="my-2 text-xs leading-relaxed text-muted-foreground">
            Current login choice: <span className="num">{analytics === "yes" ? "allowed" : analytics === "no" ? "declined" : "not chosen yet"}</span>.
          </p>
          <p className="text-xs leading-relaxed text-muted-foreground">
            Analytics remains optional and never gates Host, DB, Vortex or account access.
          </p>
        </div>
      </CardBody>
    </Card>
  );
}

function SettingsPage() {
  const providers = useProviders();
  const ownerInfo = useOwnerInfo();
  const save = useSaveProvider();
  const verify = useVerifyProvider();
  const remove = useRemoveProvider();

  const [drafts, setDrafts] = useState<Record<string, Record<string, string>>>({});
  const [results, setResults] = useState<
    Record<
      string,
      { ok: boolean; message: string; autoConnected?: { hostname: string; ok: boolean; detail: string }[] }
    >
  >({});
  const [pending, setPending] = useState<string | null>(null);

  function setField(provider: string, key: string, value: string) {
    setDrafts((current) => ({
      ...current,
      [provider]: { ...current[provider], [key]: value },
    }));
  }

  return (
    <>
      <PageHeader
        eyebrow="Keys and access"
        title="Settings"
        subtitle="Manage your Red-XAI workspace identity, browser preferences, security status and tenant-scoped Host integrations."
      />

      <Card className="rise mb-4">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <ShieldCheck className="size-4 text-primary" /> This panel
          </CardTitle>
          <StatusPill status="online" />
        </CardHeader>
        <CardBody className="space-y-2 text-sm">
          <div className="flex items-center justify-between gap-3">
            <span className="eyebrow">Signed in as</span>
            <span className="num">{ownerInfo.data?.email ?? "—"}</span>
          </div>
          <div className="flex items-center justify-between gap-3">
            <span className="eyebrow">Workspace role</span>
            <span className="num">{ownerInfo.data?.isOwner ? "Owner" : "Member"}</span>
          </div>
          <p className="pt-1 text-xs leading-relaxed text-muted-foreground">
            Your verified Red-XAI identity selects an isolated Host tenant. Member accounts see only their own projects, nodes, domains, credentials and usage; owner-only infrastructure controls remain restricted.
          </p>
        </CardBody>
      </Card>

      <BrowserPreferences />

      {/* What is actually standing between a flood and the home PC behind the
          tunnel. Two layers, reported separately, because one of them depends
          on the token's permissions and the other never does. */}
      <SecurityPanel />

      <div className="rise mb-4 flex items-start gap-2.5 rounded-md border border-[var(--info)]/30 bg-[var(--info)]/8 px-4 py-3">
        <Info className="mt-0.5 size-4 shrink-0 text-[var(--info)]" />
        <p className="text-xs leading-relaxed text-muted-foreground">
          Registrar automation runs on GoDaddy's and Namecheap's own API keys, not your registrar
          username and password. Driving their web login with a bot breaks their terms of service,
          fails on every 2FA prompt, and gets domain accounts locked — an API key does the one thing
          needed here (moving nameservers to Cloudflare) and you can revoke it any time.
        </p>
      </div>

      <div className="rise mb-4 rounded-md border border-border bg-[var(--raised)] px-4 py-3 text-xs leading-relaxed text-muted-foreground">
        Host integration credentials below are stored per Red-XAI tenant. Saving a provider key here never makes it visible to another account.
      </div>

      {providers.isLoading ? (
        <Loader2 className="size-5 animate-spin text-primary" />
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {(providers.data ?? []).map((provider, index) => {
            const draft = drafts[provider.id] ?? {};
            const result = results[provider.id];
            const busy = pending === provider.id;
            return (
              <Card key={provider.id} className="rise" style={{ animationDelay: `${index * 35}ms` }}>
                <CardHeader>
                  <div className="min-w-0">
                    <CardTitle>{provider.label}</CardTitle>
                    <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
                      {WHY[provider.id]}
                    </p>
                  </div>
                  <StatusPill status={provider.connected ? provider.verifyStatus : "unknown"} />
                </CardHeader>
                <CardBody className="space-y-3">
                  {provider.connected && (
                    <div className="flex flex-wrap items-center gap-1.5">
                      <Badge className="border-[var(--ok)]/30 text-[var(--ok)]">
                        <CheckCircle2 className="mr-1 size-3" /> saved …{provider.hint}
                      </Badge>
                      <Badge>updated {ago(provider.updatedAt)}</Badge>
                    </div>
                  )}

                  {provider.verifyMessage && !result && (
                    <p
                      className={`text-xs ${
                        provider.verifyStatus === "valid"
                          ? "text-[var(--ok)]"
                          : provider.verifyStatus === "invalid"
                            ? "text-[var(--danger)]"
                            : "text-muted-foreground"
                      }`}
                    >
                      {provider.verifyMessage}
                    </p>
                  )}

                  {/* Cloudflare is the one provider where a valid key can still
                      be the wrong key. Give it the pre-ticked token link and a
                      real permission-by-permission check. */}
                  {provider.id === "cloudflare" && (
                    <CloudflarePermissions defaultOpen={provider.verifyStatus === "invalid"} />
                  )}

                  {provider.fields.map((field) => (
                    <div key={field.key}>
                      <Label>
                        {field.label}
                        {field.optional ? " (optional)" : ""}
                      </Label>
                      <Input
                        type={field.key === "repo" || field.key.includes("Id") || field.key === "apiUser" || field.key === "clientIp" ? "text" : "password"}
                        placeholder={provider.connected ? "•••••••• (leave blank to keep)" : ""}
                        value={draft[field.key] ?? ""}
                        onChange={(event) => setField(provider.id, field.key, event.target.value)}
                      />
                    </div>
                  ))}

                  {result && (
                    <p
                      className={`text-xs ${result.ok ? "text-[var(--ok)]" : "text-[var(--danger)]"}`}
                    >
                      {result.message}
                    </p>
                  )}

                  {/* Saving a Cloudflare token immediately wires every waiting
                      domain — show what that attempt did, right here. */}
                  {result?.autoConnected && result.autoConnected.length > 0 && (
                    <div className="rounded-lg border border-border/60 bg-black/20 p-3">
                      <p className="mb-1.5 text-[11px] uppercase tracking-wider text-muted-foreground">
                        Domains wired with this token
                      </p>
                      <ul className="space-y-1">
                        {result.autoConnected.map((domain) => (
                          <li key={domain.hostname} className="text-xs">
                            <span className="font-mono">{domain.hostname}</span>{" "}
                            <span className={domain.ok ? "text-[var(--ok)]" : "text-[var(--warn)]"}>
                              {domain.detail}
                            </span>
                          </li>
                        ))}
                      </ul>
                      <Link
                        href="/domains"
                        className="mt-2 inline-flex items-center gap-1 text-xs text-primary hover:underline"
                      >
                        Open Domains <ExternalLink className="size-3" />
                      </Link>
                    </div>
                  )}

                  <div className="flex flex-wrap items-center gap-2">
                    <Button
                      size="sm"
                      disabled={busy || Object.values(draft).every((value) => !value?.trim())}
                      onClick={async () => {
                        setPending(provider.id);
                        try {
                          const response = await save.mutateAsync({
                            provider: provider.id,
                            values: draft,
                          });
                          setResults((current) => ({ ...current, [provider.id]: response }));
                          setDrafts((current) => ({ ...current, [provider.id]: {} }));
                        } finally {
                          setPending(null);
                        }
                      }}
                    >
                      {busy ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
                      Save & verify
                    </Button>
                    {provider.connected && (
                      <>
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={verify.isPending}
                          onClick={async () => {
                            const response = await verify.mutateAsync({ provider: provider.id });
                            setResults((current) => ({ ...current, [provider.id]: response }));
                          }}
                        >
                          Test
                        </Button>
                        <Button
                          size="sm"
                          variant="outline"
                          className="text-[var(--danger)] hover:text-[var(--danger)]"
                          onClick={() => {
                            if (confirm(`Remove the ${provider.label} credentials?`)) {
                              remove.mutate({ provider: provider.id });
                              setResults((current) => ({ ...current, [provider.id]: undefined as never }));
                            }
                          }}
                        >
                          <Trash2 className="size-3.5" />
                        </Button>
                      </>
                    )}
                    <a
                      href={provider.docs}
                      target="_blank"
                      rel="noreferrer"
                      className="ml-auto inline-flex items-center gap-1 text-[0.6875rem] text-muted-foreground hover:text-primary"
                    >
                      get a key <ExternalLink className="size-3" />
                    </a>
                  </div>
                </CardBody>
              </Card>
            );
          })}
        </div>
      )}
    </>
  );
}

export default SettingsPage;
