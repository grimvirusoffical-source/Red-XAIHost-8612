import test from "node:test";
import assert from "node:assert/strict";
import { bundleTenantSegment, tenantOwnsBundleKey } from "./bundle-key.ts";

test("synthetic tenants retain separate bundle namespaces including owner", () => {
  for (const tenant of ["owner", "test-account-a", "test-account-b"]) {
    const key = `bundles/${bundleTenantSegment(tenant)}/project.zip`;
    for (const actor of ["owner", "test-account-a", "test-account-b"]) {
      assert.equal(tenantOwnsBundleKey(key, actor), tenant === actor);
    }
  }
});

test("bundle paths cannot escape or extend a tenant namespace", () => {
  const segment = bundleTenantSegment("test-account-a");
  for (const key of [`bundles/${segment}/../other.zip`, `bundles/${segment}-other/project.zip`, `bundles/${segment}/nested/project.zip`, `bundles/${segment}/project.zip?other=1`]) {
    assert.equal(tenantOwnsBundleKey(key, "test-account-a"), false);
  }
  assert.throws(() => bundleTenantSegment(""));
});
