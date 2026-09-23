import { useState } from "react";
import { Loader2, ShieldAlert, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  authClient,
  GOOGLE_AUTH_ENABLED,
  MANAGED_AUTH_ENABLED,
  OWNER_EMAIL,
  isOwner,
} from "../lib/auth";
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

  if (!isOwner(session.user.email)) {
    return (
      <div className="grid min-h-screen place-items-center px-6">
        <div className="max-w-md rounded-[var(--radius)] border border-[var(--danger)]/30 bg-card p-8 text-center">
          <ShieldAlert className="mx-auto size-6 text-[var(--danger)]" />
          <h1 className="mt-4 text-lg font-semibold">Not your panel</h1>
          <p className="mt-2 text-sm text-muted-foreground">
            <span className="num">{session.user.email}</span> is not the owner account.
          </p>
          <Button className="mt-5" variant="outline" onClick={() => void authClient.signOut()}>
            Sign out
          </Button>
        </div>
      </div>
    );
  }

  return <Shell email={session.user.email}>{children}</Shell>;
}

function SignIn() {
  const [password, setPassword] = useState("");
  const [setupPassword, setSetupPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const message = (value: unknown) =>
    value instanceof Error ? value.message : "Sign-in failed.";

  async function localLogin() {
    setBusy(true);
    setError(null);
    try {
      const result = await authClient.signIn.email({
        email: OWNER_EMAIL,
        password,
      });
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
        await authClient.signIn.social({
          provider: "google",
          callbackURL: window.location.href,
        });
      }
    } catch (cause) {
      setError(message(cause));
    } finally {
      setBusy(false);
    }
  }

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
            <h1 className="text-lg font-semibold">Owner sign-in</h1>
            <p className="mt-1.5 text-sm text-muted-foreground">
              A fresh self-host can use a local owner password. Google remains optional when
              standard Google OAuth or the managed broker is configured.
            </p>
          </div>

          {(GOOGLE_AUTH_ENABLED || MANAGED_AUTH_ENABLED) && (
            <Button className="w-full" size="lg" onClick={() => void googleLogin()} disabled={busy}>
              {busy ? <Loader2 className="size-4 animate-spin" /> : null}
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
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              className="h-11 w-full rounded-md border border-border bg-background px-3 text-sm"
            />
            <Button className="w-full" onClick={() => void localLogin()} disabled={busy || !password}>
              Local owner sign-in
            </Button>
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

          {error && (
            <p className="rounded-md border border-[var(--danger)]/30 bg-[var(--danger)]/10 px-3 py-2 text-xs text-[var(--danger)]">
              {error}
            </p>
          )}

          <p className="num text-[0.6875rem] text-muted-foreground">owner: {OWNER_EMAIL}</p>
        </div>

        <p className="mt-6 text-xs leading-relaxed text-muted-foreground">
          Hosted projects run on your own worker machines. The panel can now bootstrap a local
          worker, so a separate VPS is optional.
        </p>
      </div>
    </div>
  );
}
