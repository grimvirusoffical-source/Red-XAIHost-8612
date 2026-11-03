import { describe, expect, test } from "bun:test";
import { deploymentPath, internalRequest, publicRequest, publicResponse } from "../deployment-path";

describe("Host mounted beside an existing website", () => {
  test("accepts explicit paths and rejects ambiguous or encoded mounts", () => {
    expect(deploymentPath(undefined)).toBe("");
    expect(deploymentPath("/host/")).toBe("/host");
    for (const value of ["host", "//host", "/a/../host", "/host?x", "/host%2fapi", "https://other.test"]) {
      expect(() => deploymentPath(value)).toThrow();
    }
  });

  test("only handles its own path boundary", () => {
    expect(internalRequest(new Request("https://red-xai.test/host"), "/host")?.url).toBe("https://red-xai.test/");
    expect(internalRequest(new Request("https://red-xai.test/hostile/api"), "/host")).toBeNull();
    expect(internalRequest(new Request("https://red-xai.test/settings"), "/host")).toBeNull();
    expect(internalRequest(new Request("https://red-xai.test/host/../api"), "/host")).toBeNull();
  });

  test("preserves authentication, query and request bodies across the mount", async () => {
    const request = new Request("https://red-xai.test/host/api/accounts/login?flow=synthetic", {
      method: "POST", headers: { cookie: "test_session=synthetic", origin: "https://red-xai.test" }, body: '{"login":"test"}',
    });
    const inner = internalRequest(request, "/host")!;
    expect(new URL(inner.url).pathname).toBe("/api/accounts/login");
    expect(new URL(inner.url).search).toBe("?flow=synthetic");
    expect(inner.headers.get("cookie")).toBe("test_session=synthetic");
    expect(await inner.text()).toBe('{"login":"test"}');
    expect(publicRequest(new Request("https://red-xai.test/api/auth/get-session"), "/host").url).toBe("https://red-xai.test/host/api/auth/get-session");
  });

  test("keeps internal redirects mounted and leaves external identity providers intact", () => {
    expect(publicResponse(Response.redirect("https://accounts.test/login"), "/host").headers.get("location")).toBe("https://accounts.test/login");
    expect(publicResponse(new Response(null, { status: 302, headers: { location: "/db" } }), "/host").headers.get("location")).toBe("/host/db");
    expect(publicResponse(new Response(null, { status: 302, headers: { location: "/host/db" } }), "/host").headers.get("location")).toBe("/host/db");
  });
});
