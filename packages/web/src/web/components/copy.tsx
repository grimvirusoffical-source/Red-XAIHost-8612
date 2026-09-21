import { useState } from "react";
import { Check, Copy } from "lucide-react";
import { cn } from "@/lib/utils";

export function CopyBlock({
  value,
  label,
  className,
}: {
  value: string;
  label?: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);

  return (
    <div className={cn("rounded-md border border-border bg-[var(--raised)]", className)}>
      {label && (
        <div className="flex items-center justify-between border-b border-border px-3 py-1.5">
          <span className="eyebrow">{label}</span>
        </div>
      )}
      <div className="flex items-start gap-2 px-3 py-2.5">
        <code className="min-w-0 flex-1 whitespace-pre-wrap break-all font-mono text-[0.75rem] leading-relaxed text-foreground/90">
          {value}
        </code>
        <button
          type="button"
          onClick={() => {
            void navigator.clipboard?.writeText(value);
            setCopied(true);
            setTimeout(() => setCopied(false), 1600);
          }}
          className="grid size-7 shrink-0 place-items-center rounded-md border border-border text-muted-foreground transition hover:border-primary/40 hover:text-primary"
          title="Copy"
        >
          {copied ? <Check className="size-3.5 text-[var(--ok)]" /> : <Copy className="size-3.5" />}
        </button>
      </div>
    </div>
  );
}
