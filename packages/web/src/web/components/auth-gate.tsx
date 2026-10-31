import { useEffect, useState } from "react";
import { Loader2, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { authClient, GOOGLE_AUTH_ENABLED, MANAGED_AUTH_ENABLED, OWNER_EMAIL } from "../lib/auth";
import { panelFetch } from "../lib/panel";
import { Shell } from "./shell";

export function AuthGate({ children }: { children: React.ReactNode }) {
  const { data: session, isPending } = authClient.useSession();

  if (isPending) {
    return (
      <div className="grid min-h-screen place-items-center">
        <Loader2 className="size-5 animate-spin text-primary" />
      </div>
    );
  }

  if (!session?.user) return <SignIn />;

  return <Shell email={session.user.email}>{children}</Shell>;
}

type AccountsConfig = {
  enabled: boolean;
  signupUrl: string;
  quickLogin: boolean;
  error: string | null;
};

type QuickLogin = {
  deviceCode: string;
  userCode: string;
  verificationUrl: string;
  interval: number;
  expiresIn: number;
};

type TwoFactor = {
  token: string;
  totp: boolean;
  call: boolean;
};

type SiteTrustMode = "no" | "5days" | "20days" | "30days" | "indefinitely" | "custom";

async function readJson(response: Response): Promise<Record<string, unknown>> {
  try {
    const value: unknown = await response.json();
    if (!value || typeof value !== "object" || Array.isArray(value)) return {};
    return value as Record<string, unknown>;
  } catch {
    return {};
  }
}

function messageOf(json: Record<string, unknown>, fallback: string): string {
  return typeof json.message === "string" && json.message ? json.message : fallback;
}

function SignIn() {
  const [login, setLogin] = useState("");
  const [accountPassword, setAccountPassword] = useState("");
  const [localPassword, setLocalPassword] = useState("");
  const [setupPassword, setSetupPassword] = useState("");
  const [code, setCode] = useState("");
  const [method, setMethod] = useState<"totp" | "call">("totp");
  const [factor, setFactor] = useState<TwoFactor | null>(null);
  const [quick, setQuick] = useState<QuickLogin | null>(null);
  const [trustMode, setTrustMode] = useState<SiteTrustMode>("30days");
  const [customTrustDays, setCustomTrustDays] = useState("30");
  const [config, setConfig] = useState<AccountsConfig | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const message = (value: unknown) => (value instanceof Error ? value.message : "Sign-in failed.");
  const customDays = Number(customTrustDays);
  const trustValid = trustMode !== "custom" || (Number.isInteger(customDays) && customDays >= 1 && customDays <= 3650);
  const siteTrust = () => trustMode === "custom"
    ? { mode: trustMode, days: customDays }
    : { mode: trustMode };

  useEffect(() => {
    let cancelled = false;
    void panelFetch("/api/accounts/config")
      .then((response) => readJson(response))
      .then((json) => {
        if (cancelled) return;
        setConfig({
          enabled: json.enabled === true,
          signupUrl: typeof json.signupUrl === "string" ? json.signupUrl : "https://red-xai.com/signup",
          quickLogin: json.quickLogin === true,
          error: typeof json.error === "string" ? json.error : null,
        });
      })
      .catch(() => {
        if (!cancelled) setConfig({ enabled: false, signupUrl: "https://red-xai.com/signup", quickLogin: false, error: null });
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (!quick) return;
    let stopped = false;
    const started = Date.now();
    let timer = 0;
    const poll = async () => {
      if (stopped) return;
      if (Date.now() - started > quick.expiresIn * 1000) {
        setQuick(null);
        setError("Quick Login expired. Start again, or sign in with your password.");
        return;
      }
      try {
        const response = await panelFetch("/api/accounts/quick-login/poll", {
          method: "POST",
          body: JSON.stringify({ deviceCode: quick.deviceCode, trust: siteTrust() }),
        });
        const json = await readJson(response);
        if (stopped) return;
        if (response.status === 200 && json.ok === true) {
          window.location.assign("/");
          return;
        }
        if (response.status === 403 || response.status === 401 || response.status === 410 || response.status === 404 || response.status === 400) {
          setQuick(null);
          setError(messageOf(json, "Quick Login was not approved."));
          return;
        }
      } catch {
        if (!stopped) setError("Red-XAI Accounts is not reachable. Use local owner sign-in.");
      }
      if (!stopped) timer = window.setTimeout(() => void poll(), quick.interval * 1000);
    };
    timer = window.setTimeout(() => void poll(), quick.interval * 1000);
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, [quick, trustMode, customTrustDays]);

  async function accountsLogin() {
    setBusy(true);
    setError(null);
    try {
      const response = await panelFetch("/api/accounts/login", {
        method: "POST",
        body: JSON.stringify({ login: login.trim(), password: accountPassword, trust: siteTrust() }),
      });
      const json = await readJson(response);
      if (response.status === 200 && json.ok === true) {
        window.location.assign("/");
        return;
      }
      if (response.status === 200 && json.next === "2fa") {
        const methods = json.methods && typeof json.methods === "object" ? (json.methods as Record<string, unknown>) : {};
        const totp = methods.totp === true;
        const call = methods.call === true;
        setFactor({
          token: typeof json.token === "string" ? json.token : "",
          totp,
          call,
        });
        setMethod(totp ? "totp" : "call");
        setAccountPassword("");
        return;
      }
      if (response.status === 200 && json.next === "2fa_setup") {
        setError("Finish two-step setup on red-xai.com, then sign in here.");
        return;
      }
      setError(messageOf(json, "Red-XAI account sign-in failed."));
    } catch (cause) {
      setError(message(cause));
    } finally {
      setBusy(false);
    }
  }

  async function verifyFactor() {
    if (!factor) return;
    setBusy(true);
    setError(null);
    try {
      const response = await panelFetch("/api/accounts/login/2fa", {
        method: "POST",
        headers: { "x-redx-flow": factor.token },
        body: JSON.stringify({ method, code: code.trim(), trust: siteTrust() }),
      });
      const json = await readJson(response);
      if (response.status === 200 && json.ok === true) {
        window.location.assign("/");
        return;
      }
      if (response.status === 401) {
        setFactor(null);
        setCode("");
      }
      setError(messageOf(json, "That code is not right."));
    } catch (cause) {
      setError(message(cause));
    } finally {
      setBusy(false);
    }
  }

  async function callMe() {
    if (!factor) return;
    setBusy(true);
    setError(null);
    try {
      const response = await panelFetch("/api/accounts/login/call", {
        method: "POST",
        headers: { "x-redx-flow": factor.token },
        body: JSON.stringify({}),
      });
      const json = await readJson(response);
      if (response.status !== 200) setError(messageOf(json, "The call could not be placed."));
    } catch (cause) {
      setError(message(cause));
    } finally {
      setBusy(false);
    }
  }

  async function startQuickLogin() {
    setBusy(true);
    setError(null);
    try {
      const response = await panelFetch("/api/accounts/quick-login/start", {
        method: "POST",
        body: JSON.stringify({ client: "redxaihost" }),
      });
      const json = await readJson(response);
      if (response.status !== 200) {
        setError(messageOf(json, "Quick Login is not available. Use your account password."));
        return;
      }
      const deviceCode = typeof json.deviceCode === "string" ? json.deviceCode : "";
      const userCode = typeof json.userCode === "string" ? json.userCode : "";
      if (!deviceCode || !userCode) {
        setError("Quick Login did not return a code. Use your account password.");
        return;
      }
      setQuick({
        deviceCode,
        userCode,
        verificationUrl: typeof json.verificationUrl === "string" ? json.verificationUrl : "",
        interval: typeof json.interval === "number" ? json.interval : 3,
        expiresIn: typeof json.expiresIn === "number" ? json.expiresIn : 300,
      });
    } catch (cause) {
      setError(message(cause));
    } finally {
      setBusy(false);
    }
  }

  async function localLogin() {
    setBusy(true);
    setError(null);
    try {
      const result = await authClient.signIn.email({ email: OWNER_EMAIL, password: localPassword });
      if (result.error) setError(result.error.message ?? "Local owner sign-in failed.");
    } catch (cause) {
      setError(message(cause));
    } finally {
      setBusy(false);
    }
  }

  async function firstSetup() {
    if (setupPassword.length < 12) {
      setError("Choose an owner password with at least 12 characters.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const result = await authClient.signUp.email({
        email: OWNER_EMAIL,
        password: setupPassword,
        name: "RedXAIHost Owner",
      });
      if (result.error) {
        setError(
          result.error.message?.toLowerCase().includes("exist")
            ? "The local owner already exists. Use Local owner sign-in instead."
            : result.error.message ?? "Owner setup failed.",
        );
      }
    } catch (cause) {
      setError(message(cause));
    } finally {
      setBusy(false);
    }
  }

  async function googleLogin() {
    setBusy(true);
    setError(null);
    try {
      if (MANAGED_AUTH_ENABLED) {
        const result = await authClient.managedAuth.signIn({ provider: "google" });
        if (result.error && result.error.code !== "POPUP_CLOSED") {
          setError(result.error.message ?? "Google sign-in failed.");
        }
      } else {
        await authClient.signIn.social({ provider: "google", callbackURL: window.location.href });
      }
    } catch (cause) {
      setError(message(cause));
    } finally {
      setBusy(false);
    }
  }

  const accountsOn = config?.enabled !== false;

  return (
    <div className="grid min-h-screen place-items-center px-6 py-10">
      <div className="rise w-full max-w-[460px]">
        <div className="mb-8 flex items-center gap-3">
          <div className="grid size-11 place-items-center rounded-xl bg-primary/15 ring-1 ring-primary/40">
            <ShieldCheck className="size-5 text-primary" />
          </div>
          <div>
            <div className="font-display text-xl font-bold tracking-tight">
              Red<span className="text-primary">X</span>AIHost
            </div>
            <div className="eyebrow">Private self-hosting control plane</div>
          </div>
        </div>

        <div className="space-y-5 rounded-[var(--radius)] border border-border bg-card p-6">
          <div>
            <h1 className="text-lg font-semibold">Sign in to Red-XAI Host</h1>
            <p className="mt-1.5 text-sm text-muted-foreground">
              Use the same Red-XAI account as the website. Your Host workspace is isolated from every other account.
              The local owner password remains available only as an offline administrative fallback.
            </p>
          </div>

          <fieldset className="space-y-2 rounded-md border border-border bg-background/40 p-3">
            <legend className="px-1 text-xs font-semibold text-muted-foreground">Stay signed in to this website</legend>
            <select
              aria-label="Stay signed in duration"
              value={trustMode}
              onChange={(event) => setTrustMode(event.target.value as SiteTrustMode)}
              className="h-10 w-full rounded-md border border-border bg-background px-3 text-sm"
            >
              <option value="no">No — this browser session only</option>
              <option value="5days">5 days</option>
              <option value="20days">20 days</option>
              <option value="30days">30 days</option>
              <option value="indefinitely">Indefinitely, until revoked</option>
              <option value="custom">Custom number of days</option>
            </select>
            {trustMode === "custom" && (
              <input
                type="number"
                min={1}
                max={3650}
                inputMode="numeric"
                aria-label="Custom stay signed in days"
                value={customTrustDays}
                onChange={(event) => setCustomTrustDays(event.target.value)}
                className="h-10 w-full rounded-md border border-border bg-background px-3 text-sm"
              />
            )}
            <p className="text-[0.6875rem] leading-relaxed text-muted-foreground">
              This controls the Red-XAI website session only. Quick Login app-device trust is separate and keeps its own revocation controls.
            </p>
          </fieldset>

          {factor ? (
            <div className="space-y-3">
              <p className="text-sm text-muted-foreground">
                {method === "totp"
                  ? "Enter the 6-digit code from your authenticator app."
                  : "We can call you and read out a code."}
              </p>
              {factor.totp && factor.call && (
                <div className="grid grid-cols-2 gap-2">
                  <Button type="button" variant={method === "totp" ? "default" : "outline"} onClick={() => setMethod("totp")}>
                    Authenticator
                  </Button>
                  <Button type="button" variant={method === "call" ? "default" : "outline"} onClick={() => setMethod("call")}>
                    Phone call
                  </Button>
                </div>
              )}
              {method === "call" && (
                <Button type="button" variant="outline" className="w-full" disabled={busy} onClick={() => void callMe()}>
                  Call me with the code
                </Button>
              )}
              <input
                id="accounts-2fa"
                inputMode="numeric"
                autoComplete="one-time-code"
                value={code}
                onChange={(event) => setCode(event.target.value)}
                className="h-11 w-full rounded-md border border-border bg-background px-3 text-sm"
              />
              <Button className="w-full" disabled={busy || !trustValid || code.trim().length < 4} onClick={() => void verifyFactor()}>
                {busy ? <Loader2 className="size-4 animate-spin" /> : null}
                Sign in
              </Button>
              <Button
                type="button"
                variant="outline"
                className="w-full"
                onClick={() => {
                  setFactor(null);
                  setCode("");
                }}
              >
                Start over
              </Button>
            </div>
          ) : quick ? (
            <div className="space-y-3">
              <p className="text-sm text-muted-foreground">
                Approve this code on the Hub while you are signed in there. This page will open when it is accepted.
              </p>
              <div className="num rounded-md border border-border bg-background px-3 py-4 text-center text-2xl tracking-[0.2em]">
                {quick.userCode}
              </div>
              {quick.verificationUrl && (
                <a className="block text-center text-sm text-primary underline-offset-4 hover:underline" href={quick.verificationUrl}>
                  Open the approval page
                </a>
              )}
              <p className="text-center text-xs text-muted-foreground">Waiting for Red-XAI Accounts…</p>
              <Button type="button" variant="outline" className="w-full" onClick={() => setQuick(null)}>
                Cancel
              </Button>
            </div>
          ) : (
            <>
              {accountsOn && (
                <div className="space-y-3">
                  {config?.quickLogin && (
                    <Button className="w-full" size="lg" disabled={busy || !trustValid} onClick={() => void startQuickLogin()}>
                      {busy ? <Loader2 className="size-4 animate-spin" /> : null}
                      Quick Login
                    </Button>
                  )}
                  <label className="text-xs text-muted-foreground" htmlFor="accounts-login">
                    Red-XAI username or email
                  </label>
                  <input
                    id="accounts-login"
                    autoComplete="username"
                    value={login}
                    onChange={(event) => setLogin(event.target.value)}
                    className="h-11 w-full rounded-md border border-border bg-background px-3 text-sm"
                  />
                  <label className="text-xs text-muted-foreground" htmlFor="accounts-password">
                    Password
                  </label>
                  <input
                    id="accounts-password"
                    type="password"
                    autoComplete="current-password"
                    value={accountPassword}
                    onChange={(event) => setAccountPassword(event.target.value)}
                    className="h-11 w-full rounded-md border border-border bg-background px-3 text-sm"
                  />
                  <Button
                    className="w-full"
                    onClick={() => void accountsLogin()}
                    disabled={busy || !trustValid || !login.trim() || !accountPassword}
                  >
                    Sign in with Red-XAI
                  </Button>
                  <a
                    className="block text-center text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
                    href={config?.signupUrl ?? "https://red-xai.com/signup"}
                  >
                    Create a Red-XAI account
                  </a>
                </div>
              )}

              {(GOOGLE_AUTH_ENABLED || MANAGED_AUTH_ENABLED) && (
                <Button className="w-full" size="lg" variant="outline" onClick={() => void googleLogin()} disabled={busy}>
                  Continue with Google
                </Button>
              )}

              <div className="space-y-2 border-t border-border pt-4">
                <label className="text-xs text-muted-foreground" htmlFor="local-owner-password">
                  Local owner password
                </label>
                <input
                  id="local-owner-password"
                  type="password"
                  autoComplete="current-password"
                  value={localPassword}
                  onChange={(event) => setLocalPassword(event.target.value)}
                  className="h-11 w-full rounded-md border border-border bg-background px-3 text-sm"
                />
                <Button className="w-full" variant="outline" onClick={() => void localLogin()} disabled={busy || !localPassword}>
                  Local owner sign-in
                </Button>
                <p className="text-[0.6875rem] text-muted-foreground">
                  Offline fallback for this machine. It does not change saved provider keys.
                </p>
              </div>

              <details className="border-t border-border pt-4">
                <summary className="cursor-pointer text-xs font-semibold text-muted-foreground">
                  First launch only: create local owner login
                </summary>
                <div className="mt-3 space-y-2">
                  <input
                    type="password"
                    autoComplete="new-password"
                    placeholder="12+ character owner password"
                    value={setupPassword}
                    onChange={(event) => setSetupPassword(event.target.value)}
                    className="h-11 w-full rounded-md border border-border bg-background px-3 text-sm"
                  />
                  <Button variant="outline" className="w-full" onClick={() => void firstSetup()} disabled={busy}>
                    Create local owner
                  </Button>
                </div>
              </details>
            </>
          )}

          {error && (
            <p className="rounded-md border border-[var(--danger)]/30 bg-[var(--danger)]/10 px-3 py-2 text-xs text-[var(--danger)]">
              {error}
            </p>
          )}

          
        </div>

        <p className="mt-6 text-xs leading-relaxed text-muted-foreground">
          Hosted projects run on your own worker machines. The panel can bootstrap a local worker, so a
          separate VPS is optional. Hub can open this panel with <span className="num">redxaihost://open</span> or
          embed <span className="num">/embed</span>.
        </p>
      </div>
    </div>
  );
}
