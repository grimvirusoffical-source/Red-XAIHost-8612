import { AsyncLocalStorage } from "node:async_hooks";

export interface TenantContext {
  tenantId: string;
  isOwner: boolean;
  userId: string;
}

const tenantStorage = new AsyncLocalStorage<TenantContext>();

export function withTenantContext<T>(value: TenantContext, run: () => T): T {
  return tenantStorage.run(value, run);
}

export function tenantContext(): TenantContext {
  const value = tenantStorage.getStore();
  if (!value) throw new Error("Tenant context is required for this operation.");
  return value;
}

export function currentTenantId(): string {
  return tenantContext().tenantId;
}
