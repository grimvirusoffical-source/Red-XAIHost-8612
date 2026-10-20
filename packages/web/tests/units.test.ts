import { describe, expect, test } from "bun:test";
import { join } from "node:path";
import { SECRETS, tempDir } from "./helpers";

Object.assign(process.env, SECRETS, { DATABASE_URL: `file:${join(tempDir("unit-"), "u.db")}` });
const { isOff, infectedNationHost } = await import("../src/api/lib/local-worker");
const { resolveClientIp } = await import("../src/api/lib/client-ip");

describe("isOff", () => {
  test("0/false/no/off in any case turn a switch off", () => {
    for (const v of ["0", "false", "FALSE", "No", "off", " Off "]) expect(isOff(v)).toBe(true);
  });
  test("unset and other values leave it on", () => {
    for (const v of [undefined, "", "1", "true", "yes", "on"]) expect(isOff(v)).toBe(false);
  });
});

describe("infectedNationHost", () => {
  test("loopback unless configured", () => {
    expect(infectedNationHost({})).toBe("127.0.0.1");
    expect(infectedNationHost({ REDX_INFECTEDNATION_HOST: "0.0.0.0" })).toBe("0.0.0.0");
  });
});

describe("resolveClientIp", () => {
  const h = (o: Record<string, string>) => new Headers(o);
  test("ignores forwarding headers by default", () => {
    expect(resolveClientIp(h({ "cf-connecting-ip": "1.2.3.4", "x-forwarded-for": "5.6.7.8" }), "10.0.0.1", {})).toBe("10.0.0.1");
  });
  test("uses only the configured header", () => {
    const env = { REDX_TRUSTED_IP_HEADER: "cf-connecting-ip" };
    expect(resolveClientIp(h({ "cf-connecting-ip": "1.2.3.4", "x-forwarded-for": "5.6.7.8" }), "10.0.0.1", env)).toBe("1.2.3.4");
    expect(resolveClientIp(h({ "x-forwarded-for": "5.6.7.8" }), "10.0.0.1", env)).toBe("10.0.0.1");
  });
  test("x-forwarded-for uses the right-most entry", () => {
    expect(resolveClientIp(h({ "x-forwarded-for": "6.6.6.6, 1.2.3.4" }), "10.0.0.1", { REDX_TRUSTED_IP_HEADER: "x-forwarded-for" })).toBe("1.2.3.4");
  });
});

import { trustedOriginsFromEnv } from "../src/api/lib/trusted-origins";

describe("trustedOriginsFromEnv", () => {
  test("only configured origins plus the local panel port", () => {
    expect(trustedOriginsFromEnv({ PORT: "4321", REDX_PUBLIC_URL: "https://host.example.com/path", WEBSITE_URL: "" }).sort()).toEqual(
      ["http://127.0.0.1:4321", "http://localhost:4321", "https://host.example.com"].sort(),
    );
  });
  test("extra origins from REDX_TRUSTED_ORIGINS; junk and non-http are dropped", () => {
    const o = trustedOriginsFromEnv({ REDX_TRUSTED_ORIGINS: "https://a.example, not a url, javascript:alert(1)" });
    expect(o).toContain("https://a.example");
    expect(o.some((x) => x.startsWith("javascript"))).toBe(false);
    expect(o).toContain("http://127.0.0.1:4200");
  });
});
