import { expect, test } from "bun:test";
import { currentTenantId, tenantContext, withTenantContext } from "./tenant-context";

test("tenant operations fail closed without an authenticated request context", () => {
  expect(() => currentTenantId()).toThrow("Tenant context is required");
});

test("concurrent asynchronous account requests cannot inherit each other's tenant or role", async () => {
  const accounts = [
    { tenantId: "test-a", userId: "user-a", isOwner: false },
    { tenantId: "test-b", userId: "user-b", isOwner: false },
    { tenantId: "owner", userId: "owner-user", isOwner: true },
  ];
  await Promise.all(accounts.map(account => withTenantContext(account, async () => {
    await Promise.resolve();
    expect(currentTenantId()).toBe(account.tenantId);
    expect(tenantContext()).toEqual(account);
    await Promise.resolve();
    expect(tenantContext().isOwner).toBe(account.isOwner);
  })));
  expect(() => tenantContext()).toThrow("Tenant context is required");
});
