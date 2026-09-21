import { useState } from "react";
import { Loader2, ShieldAlert, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { authClient, OWNER_EMAIL, isOwner } from "../lib/auth";
import { Shell } from "./shell";

/**
 * RedXAIHost is a one-account panel: the server refuses to create a user row
 * for anything but the owner email, and this gate mirrors that in the UI.
 */
export function AuthGate({ children }: { children: React.ReactNode }) {
  const { data: session, isPending } = authClient.useSession();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (isPending) {
    return (
      <div className="grid min-h-screen place-items-center">
        <Loader2 className="size-5 animate-spin text-primary" />
      </div>
    );
  }

  if (!session?.user) {
    return (
      <SignIn
        busy={busy}
        error={error}
        onSignIn={async () => {
          setBusy(true);
          setError(null);
          const result = await authClient.managedAuth.signIn({ provider: "google" });
          if (result.error && result.error.code !== "POPUP_CLOSED") {
            setError(
              result.error.message?.includes("private")
                ? "That account is not the owner of this panel."
                : (result.error.message ?? "Sign-in failed."),
            );
          }
          setBusy(false);
        }}
      />
    );
  }

  if (!isOwner(session.user.email)) {
    return (
      <div className="grid min-h-screen place-items-center px-6">
        <div className="max-w-md rounded-[var(--radius)] border border-[var(--danger)]/30 bg-card p-8 text-center">
          <ShieldAlert className="mx-auto size-6 text-[var(--danger)]" />
          <h1 className="mt-4 text-lg font-semibold">Not your panel</h1>
          <p className="mt-2 text-sm text-muted-foreground">
            <span className="num">{session.user.email}</span> is not the owner account. RedXAIHost
            only ever admits one identity.
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

function SignIn({
  busy,
  error,
  onSignIn,
}: {
  busy: boolean;
  error: string | null;
  onSignIn: () => void;
}) {
  return (
    <div className="grid min-h-screen place-items-center px-6">
      <div className="rise w-full max-w-[420px]">
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

        <div className="rounded-[var(--radius)] border border-border bg-card p-6">
          <h1 className="text-lg font-semibold">Owner sign-in</h1>
          <p className="mt-1.5 text-sm text-muted-foreground">
            One account runs this panel. Google is the only way in, and every other identity is
            rejected server-side.
          </p>

          <Button className="mt-5 w-full" size="lg" onClick={onSignIn} disabled={busy}>
            {busy ? <Loader2 className="size-4 animate-spin" /> : null}
            Continue with Google
          </Button>

          {error && (
            <p className="mt-3 rounded-md border border-[var(--danger)]/30 bg-[var(--danger)]/10 px-3 py-2 text-xs text-[var(--danger)]">
              {error}
            </p>
          )}

          {OWNER_EMAIL && (
            <p className="mt-4 num text-[0.6875rem] text-muted-foreground">
              allow-list: {OWNER_EMAIL}
            </p>
          )}
        </div>

        <p className="mt-6 text-xs leading-relaxed text-muted-foreground">
          Hosting runs on your own machines. This panel schedules and watches — it never serves your
          sites itself, so nothing is exposed unless a node of yours is online.
        </p>
      </div>
    </div>
  );
}
