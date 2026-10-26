import { describe, expect, test } from "bun:test";
import {
  accountsBaseUrl,
  framePolicy,
  hubManifest,
  hubOrigins,
  parseHubLink,
  requestOriginAllowed,
  safePanelPath,
} from "./hub.ts";

describe("hub launch paths", () => {
  test("keeps panel routes and drops anything else", () => {
    expect(safePanelPath("/projects")).toBe("/projects");
    expect(safePanelPath("/projects/abc")).toBe("/projects/abc");
    expect(safePanelPath("/settings?x=1")).toBe("/settings");
    expect(safePanelPath("//evil.example")).toBe("/");
    expect(safePanelPath("/../secrets")).toBe("/");
    expect(safePanelPath("https://red-xai.com")).toBe("/");
    expect(safePanelPath("/api/accounts/login")).toBe("/");
  });

  test("parses the redxaihost launch protocol", () => {
    expect(parseHubLink("redxaihost://open/projects")).toBe("/projects");
    expect(parseHubLink("redxaihost://open?path=/settings")).toBe("/settings");
    expect(parseHubLink("redxaihost://embed")).toBe("/");
    expect(parseHubLink("redxaihost://other/projects")).toBeNull();
    expect(parseHubLink("https://red-xai.com/host")).toBeNull();
  });
});

describe("accounts and hub config", () => {
  test("defaults to the public accounts service and can be turned off", () => {
    expect(accountsBaseUrl({})).toBe("https://api.red-xai.com/v1/accounts");
    expect(accountsBaseUrl({ REDX_ACCOUNTS_URL: "off" })).toBeNull();
    expect(accountsBaseUrl({ REDX_ACCOUNTS_URL: "http://127.0.0.1:9/v1/accounts" })).toBe(
      "http://127.0.0.1:9/v1/accounts",
    );
    expect(() => accountsBaseUrl({ REDX_ACCOUNTS_URL: "http://accounts.example/v1" })).toThrow();
  });

  test("frame policy follows the hub allow-list", () => {
    expect(framePolicy([])).toEqual({ frameOptions: "DENY", csp: "frame-ancestors 'none'" });
    expect(hubOrigins({ REDX_HUB_ORIGINS: "off" })).toEqual([]);
    expect(hubOrigins({ REDX_HUB_ORIGINS: "https://red-xai.com/" })).toEqual(["https://red-xai.com"]);
    expect(framePolicy(["https://red-xai.com"]).csp).toContain("https://red-xai.com");
    expect(framePolicy(["https://red-xai.com"]).frameOptions).toBeNull();
  });

  test("manifest names the embed URL, protocol, and quick login routes", () => {
    const manifest = hubManifest("https://panel.example", {
      REDX_ACCOUNTS_URL: "off",
      REDX_HUB_ORIGINS: "https://red-xai.com",
      REDX_HUB_URL: "https://red-xai.com",
    });
    expect(manifest.embedUrl).toBe("https://panel.example/embed");
    expect(manifest.deepLink).toBe("redxaihost://open");
    expect(manifest.quickLogin.start).toBe("https://panel.example/api/accounts/quick-login/start");
    expect(manifest.accountsEnabled).toBe(false);
    expect(manifest.accountsService).toContain("red-xai-accounts");
  });

  test("refuses a foreign browser origin", () => {
    const request = new Request("http://127.0.0.1:4200/api/accounts/login", {
      headers: { origin: "https://evil.example" },
    });
    expect(requestOriginAllowed(request, { REDX_HUB_ORIGINS: "https://red-xai.com" })).toBe(false);
    const hub = new Request("http://127.0.0.1:4200/api/accounts/login", {
      headers: { origin: "https://red-xai.com" },
    });
    expect(requestOriginAllowed(hub, { REDX_HUB_ORIGINS: "https://red-xai.com" })).toBe(true);
  });
});
