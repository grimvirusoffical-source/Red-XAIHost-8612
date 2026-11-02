import { createHash } from "node:crypto";

export function bundleTenantSegment(tenantId: string): string {
  if (!tenantId) throw new Error("Tenant identity is required.");
  return tenantId === "owner" ? "owner" : createHash("sha256").update(tenantId).digest("hex").slice(0, 24);
}

export function safeBundleKey(key: string): boolean {
  return /^bundles\/[a-z0-9_-]{1,64}\/[A-Za-z0-9._-]{1,220}$/.test(key);
}

export function tenantOwnsBundleKey(key: string, tenantId: string): boolean {
  return safeBundleKey(key) && key.startsWith(`bundles/${bundleTenantSegment(tenantId)}/`);
}
