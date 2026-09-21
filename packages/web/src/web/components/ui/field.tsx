import * as React from "react";
import { cn } from "@/lib/utils";

const base =
  "w-full rounded-md border border-input bg-[var(--raised)] px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground/70 outline-none transition focus:border-primary/60 focus:ring-[3px] focus:ring-primary/20 disabled:opacity-50";

export function Input({ className, ...props }: React.ComponentProps<"input">) {
  return <input className={cn(base, "h-9", className)} {...props} />;
}

export function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return <textarea className={cn(base, "min-h-24 font-mono text-xs leading-relaxed", className)} {...props} />;
}

export function Select({ className, ...props }: React.ComponentProps<"select">) {
  return <select className={cn(base, "h-9 appearance-none pr-8", className)} {...props} />;
}

export function Label({ className, children, ...props }: React.ComponentProps<"label">) {
  return (
    // biome-ignore lint: a bare caption label is intentional here — most fields in this
    // panel are labelled visually and wired with aria-label on the control itself.
    // oxlint-disable-next-line jsx-a11y/label-has-associated-control
    <label
      className={cn(
        "mb-1.5 block text-[0.6875rem] font-semibold uppercase tracking-[0.12em] text-muted-foreground",
        className,
      )}
      {...props}
    >
      {children}
    </label>
  );
}

export function FieldRow({ className, ...props }: React.ComponentProps<"div">) {
  return <div className={cn("grid gap-3 sm:grid-cols-2", className)} {...props} />;
}
