import { Link, useLocation } from "wouter";
import {
  Activity,
  BarChart3,
  Boxes,
  Globe,
  Hammer,
  LayoutDashboard,
  LogOut,
  Server,
  Settings,
  ShieldCheck,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { authClient } from "../lib/auth";
import { useCapacity } from "../queries/nodes";
import { memory } from "../lib/format";

const NAV = [
  { href: "/", label: "Dashboard", icon: LayoutDashboard },
  { href: "/nodes", label: "Nodes", icon: Server },
  { href: "/projects", label: "Projects", icon: Boxes },
  { href: "/domains", label: "Domains", icon: Globe },
  { href: "/usage", label: "Usage", icon: BarChart3 },
  { href: "/builds", label: "App builds", icon: Hammer },
  { href: "/activity", label: "Activity", icon: Activity },
  { href: "/settings", label: "Settings", icon: Settings },
];

function Sidebar({ email }: { email?: string | null }) {
  const [path] = useLocation();
  const capacity = useCapacity();
  const online = capacity.data?.online ?? 0;

  return (
    <aside className="flex w-[248px] shrink-0 flex-col border-r border-border bg-card/60 backdrop-blur">
      <div className="flex items-center gap-2.5 border-b border-border px-5 py-5">
        <div className="grid size-9 place-items-center rounded-lg bg-primary/15 ring-1 ring-primary/40">
          <ShieldCheck className="size-4.5 text-primary" />
        </div>
        <div className="leading-tight">
          <div className="font-display text-sm font-bold tracking-tight">
            Red<span className="text-primary">X</span>AIHost
          </div>
          <div className="text-[0.625rem] uppercase tracking-[0.14em] text-muted-foreground">
            Private control plane
          </div>
        </div>
      </div>

      <nav className="flex-1 space-y-0.5 p-3">
        {NAV.map((item, index) => {
          const active = path === item.href || (item.href !== "/" && path.startsWith(item.href));
          return (
            <Link
              key={item.href}
              href={item.href}
              style={{ animationDelay: `${index * 35}ms` }}
              className={cn(
                "rise flex items-center gap-2.5 rounded-md px-3 py-2 text-sm transition-colors",
                active
                  ? "bg-primary/12 text-foreground ring-1 ring-primary/30"
                  : "text-muted-foreground hover:bg-muted hover:text-foreground",
              )}
            >
              <item.icon className={cn("size-4", active && "text-primary")} />
              {item.label}
            </Link>
          );
        })}
      </nav>

      <div className="space-y-3 border-t border-border p-4">
        <div className="rounded-md border border-border bg-[var(--raised)] p-3">
          <div className="eyebrow">Fleet capacity</div>
          <div className="mt-1.5 flex items-baseline gap-1.5">
            <span
              className={cn(
                "num text-xl font-medium",
                online > 0 ? "text-[var(--ok)]" : "text-[var(--danger)]",
              )}
            >
              {online}
            </span>
            <span className="text-xs text-muted-foreground">
              / {capacity.data?.total ?? 0} nodes online
            </span>
          </div>
          <div className="mt-1 num text-[0.6875rem] text-muted-foreground">
            {capacity.data?.cores ?? 0} vCPU · {memory(capacity.data?.memoryMb)}
          </div>
        </div>

        <div className="flex items-center justify-between gap-2">
          <div className="min-w-0">
            <div className="eyebrow">Owner</div>
            <div className="truncate text-xs text-muted-foreground" title={email ?? ""}>
              {email ?? "—"}
            </div>
          </div>
          <button
            type="button"
            onClick={() => void authClient.signOut()}
            title="Sign out"
            className="grid size-8 shrink-0 place-items-center rounded-md border border-border text-muted-foreground transition hover:border-primary/40 hover:text-primary"
          >
            <LogOut className="size-3.5" />
          </button>
        </div>
      </div>
    </aside>
  );
}

export function Shell({ children, email }: { children: React.ReactNode; email?: string | null }) {
  return (
    <div className="flex min-h-screen">
      <Sidebar email={email} />
      <main className="min-w-0 flex-1 overflow-x-hidden">
        <div className="mx-auto max-w-[1240px] px-6 py-8 lg:px-10">{children}</div>
      </main>
    </div>
  );
}

export function PageHeader({
  eyebrow,
  title,
  subtitle,
  actions,
}: {
  eyebrow: string;
  title: string;
  subtitle?: string;
  actions?: React.ReactNode;
}) {
  return (
    <header className="rise mb-7 flex flex-wrap items-end justify-between gap-4">
      <div>
        <div className="eyebrow">{eyebrow}</div>
        <h1 className="mt-1 text-[1.75rem] font-bold leading-tight">{title}</h1>
        {subtitle && <p className="mt-1.5 max-w-2xl text-sm text-muted-foreground">{subtitle}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </header>
  );
}
