import * as React from "react";
import { cn } from "@/lib/utils";

export function Card({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      className={cn("rounded-[var(--radius)] border border-border bg-card", className)}
      {...props}
    />
  );
}

export function CardHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      className={cn("flex items-start justify-between gap-4 border-b border-border px-5 py-4", className)}
      {...props}
    />
  );
}

export function CardTitle({ className, children, ...props }: React.ComponentProps<"h3">) {
  return (
    <h3 className={cn("text-base font-semibold tracking-tight", className)} {...props}>
      {children}
    </h3>
  );
}

export function CardDescription({ className, ...props }: React.ComponentProps<"p">) {
  return <p className={cn("mt-1 text-sm text-muted-foreground", className)} {...props} />;
}

export function CardBody({ className, ...props }: React.ComponentProps<"div">) {
  return <div className={cn("px-5 py-4", className)} {...props} />;
}
