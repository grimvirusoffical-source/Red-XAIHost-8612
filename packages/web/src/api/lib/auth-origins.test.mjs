import test from "node:test";
import assert from "node:assert/strict";
import { authTrustedOrigins } from "./auth-origins.ts";
import { parseHubLink, safePanelPath } from "./hub.ts";

test("production trusts configured site origins without local development origins", () => {
  assert.deepEqual(authTrustedOrigins({ REDX_PUBLIC_URL: "https://host.example/host", REDX_HUB_ORIGINS: "https://red-xai.com" }), ["https://red-xai.com", "https://host.example"]);
});

test("local fallback remains available for a fresh self-host installation", () => {
  assert.deepEqual(authTrustedOrigins({ REDX_HUB_ORIGINS: "off" }), ["http://127.0.0.1:4200", "http://localhost:4200"]);
});

test("rejects credential-bearing, malformed, and remote cleartext public URLs", () => {
  for (const value of ["https://user:pass@host.example", "not a url", "http://host.example", "javascript:alert(1)"]) {
    assert.deepEqual(authTrustedOrigins({ REDX_PUBLIC_URL: value, REDX_HUB_ORIGINS: "off" }), []);
  }
});

test("normalizes and deduplicates explicit origins", () => {
  assert.deepEqual(authTrustedOrigins({ REDX_PUBLIC_URL: "https://red-xai.com/host", WEBSITE_URL: "https://red-xai.com", REDX_HUB_ORIGINS: "https://red-xai.com" }), ["https://red-xai.com"]);
});

test("DB and Vortex deep links keep their destination within the existing shell", () => {
  for (const path of ["/db", "/vortex"]) {
    assert.equal(safePanelPath(path), path);
    assert.equal(parseHubLink(`redxaihost://open?path=${path}`), path);
  }
  for (const path of ["//outside.example", "/api/auth/sign-in", "/db/../settings", "https://outside.example/db"]) {
    assert.equal(safePanelPath(path), "/");
  }
});
