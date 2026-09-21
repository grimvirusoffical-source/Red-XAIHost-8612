import { cn } from "@/lib/utils";

type Tone = "ok" | "warn" | "danger" | "info" | "idle";

const TONES: Record<string, Tone> = {
  online: "ok",
  running: "ok",
  live: "ok",
  succeeded: "ok",
  valid: "ok",
  pending: "warn",
  deploying: "warn",
  building: "warn",
  dns_set: "warn",
  claimed: "warn",
  dispatched: "warn",
  queued: "info",
  draft: "idle",
  stopped: "idle",
  disabled: "idle",
  unknown: "idle",
  offline: "danger",
  failed: "danger",
  error: "danger",
  invalid: "danger",
  no_capacity: "danger",
};

const TONE_CLASS: Record<Tone, string> = {
  ok: "text-[var(--ok)] bg-[var(--ok)]/10 border-[var(--ok)]/30",
  warn: "text-[var(--warn)] bg-[var(--warn)]/10 border-[var(--warn)]/30",
  danger: "text-[var(--danger)] bg-[var(--danger)]/10 border-[var(--danger)]/30",
  info: "text-[var(--info)] bg-[var(--info)]/10 border-[var(--info)]/30",
  idle: "text-muted-foreground bg-muted border-border",
};

const DOT_CLASS: Record<Tone, string> = {
  ok: "bg-[var(--ok)]",
  warn: "bg-[var(--warn)]",
  danger: "bg-[var(--danger)]",
  info: "bg-[var(--info)]",
  idle: "bg-muted-foreground",
};

export function toneOf(status: string): Tone {
  return TONES[status] ?? "idle";
}

const LABELS: Record<string, string> = {
  no_capacity: "no capacity",
  dns_set: "dns set",
};

export function StatusPill({ status, className }: { status: string; className?: string }) {
  const tone = toneOf(status);
  const pulsing = tone === "warn";
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-[0.6875rem] font-medium uppercase tracking-wider",
        TONE_CLASS[tone],
        className,
      )}
    >
      <span className={cn("size-1.5 rounded-full", DOT_CLASS[tone], pulsing && "animate-pulse")} />
      {LABELS[status] ?? status}
    </span>
  );
}

export function Dot({ status, className }: { status: string; className?: string }) {
  const tone = toneOf(status);
  return <span className={cn("size-2 rounded-full", DOT_CLASS[tone], className)} />;
}

export function Badge({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-md border border-border bg-muted px-2 py-0.5 text-[0.6875rem] uppercase tracking-wider text-muted-foreground",
        className,
      )}
    >
      {children}
    </span>
  );
}
