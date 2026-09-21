export function bytes(value: number | null | undefined): string {
  const n = value ?? 0;
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

export function compact(value: number | null | undefined): string {
  const n = value ?? 0;
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

export function ago(seconds: number | null | undefined): string {
  if (!seconds) return "never";
  const diff = Math.max(0, Math.floor(Date.now() / 1000) - seconds);
  if (diff < 45) return "just now";
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}

export function clock(seconds: number | null | undefined): string {
  if (!seconds) return "—";
  return new Date(seconds * 1000).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function memory(mb: number | null | undefined): string {
  const n = mb ?? 0;
  if (n === 0) return "—";
  return n >= 1024 ? `${(n / 1024).toFixed(1)} GB` : `${n} MB`;
}
