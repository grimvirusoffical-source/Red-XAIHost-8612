import { describe, expect, test } from "bun:test";
import { clientIp, clientIpOptionsFromEnv } from "../src/api/lib/rate-limit";

const h = (o: Record<string, string>) => new Headers(o);
const OFF = { trustCloudflare: false, trustedProxies: [] };

describe("clientIp", () => {
  test("spoofed cf-connecting-ip from a non-Cloudflare peer is ignored (flag off)", () => {
    expect(clientIp(h({ "cf-connecting-ip": "9.9.9.9" }), "203.0.113.7", OFF)).toBe("203.0.113.7");
  });
  test("spoofed cf-connecting-ip from a non-Cloudflare peer is ignored (flag on)", () => {
    const on = { trustCloudflare: true, trustedProxies: [] };
    expect(clientIp(h({ "cf-connecting-ip": "9.9.9.9" }), "203.0.113.7", on)).toBe("203.0.113.7");
  });
  test("X-Forwarded-For and X-Real-IP are ignored from an untrusted peer", () => {
    expect(clientIp(h({ "x-forwarded-for": "1.1.1.1", "x-real-ip": "2.2.2.2" }), "203.0.113.7", OFF)).toBe("203.0.113.7");
  });
  test("Cloudflare peer with the flag off: header ignored", () => {
    expect(clientIp(h({ "cf-connecting-ip": "9.9.9.9" }), "172.70.1.2", OFF)).toBe("172.70.1.2");
  });
  test("Cloudflare IPv4 peer with the flag on: header honoured", () => {
    const on = { trustCloudflare: true, trustedProxies: [] };
    expect(clientIp(h({ "cf-connecting-ip": "9.9.9.9" }), "172.70.1.2", on)).toBe("9.9.9.9");
    expect(clientIp(h({ "cf-connecting-ip": "9.9.9.9" }), "::ffff:104.16.5.5", on)).toBe("9.9.9.9");
  });
  test("Cloudflare IPv6 peer with the flag on: header honoured", () => {
    const on = { trustCloudflare: true, trustedProxies: [] };
    expect(clientIp(h({ "cf-connecting-ip": "2001:db8::1" }), "2606:4700:10::6816:1", on)).toBe("2001:db8::1");
    expect(clientIp(h({ "cf-connecting-ip": "9.9.9.9" }), "2001:db8::5", on)).toBe("2001:db8::5");
  });
  test("trusted proxy (local tunnel) with the flag on: header honoured", () => {
    const opts = clientIpOptionsFromEnv({ REDX_TRUST_CLOUDFLARE: "true", REDX_TRUSTED_PROXIES: "127.0.0.1, 10.0.0.0/8" });
    expect(clientIp(h({ "cf-connecting-ip": "9.9.9.9" }), "127.0.0.1", opts)).toBe("9.9.9.9");
    expect(clientIp(h({ "cf-connecting-ip": "9.9.9.9" }), "10.4.5.6", opts)).toBe("9.9.9.9");
    expect(clientIp(h({ "cf-connecting-ip": "9.9.9.9" }), "11.0.0.1", opts)).toBe("11.0.0.1");
  });
  test("garbage cf-connecting-ip is not used as a bucket key", () => {
    const on = { trustCloudflare: true, trustedProxies: [] };
    expect(clientIp(h({ "cf-connecting-ip": "x".repeat(500) }), "172.70.1.2", on)).toBe("172.70.1.2");
  });
  test("trusted proxy: X-Forwarded-For rightmost untrusted hop wins", () => {
    const opts = { trustCloudflare: false, trustedProxies: ["10.0.0.0/8"] };
    expect(clientIp(h({ "x-forwarded-for": "6.6.6.6, 5.5.5.5, 10.0.0.2" }), "10.0.0.1", opts)).toBe("5.5.5.5");
  });
  test("env defaults are off", () => {
    expect(clientIpOptionsFromEnv({})).toEqual({ trustCloudflare: false, trustedProxies: [] });
  });
});
