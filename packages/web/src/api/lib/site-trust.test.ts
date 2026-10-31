import { describe, expect, test } from "bun:test";
import { normalizeSiteTrust } from "./site-trust";

describe("website stay-signed-in choices", () => {
  test("matches the Red-XAI trust choices", () => {
    expect(normalizeSiteTrust({ mode: "no" })).toEqual({ mode: "no", remember: false, maxAgeSeconds: null });
    expect(normalizeSiteTrust({ mode: "5days" }).maxAgeSeconds).toBe(5 * 86400);
    expect(normalizeSiteTrust({ mode: "20days" }).maxAgeSeconds).toBe(20 * 86400);
    expect(normalizeSiteTrust({ mode: "30days" }).maxAgeSeconds).toBe(30 * 86400);
    expect(normalizeSiteTrust({ mode: "indefinitely" }).maxAgeSeconds).toBeGreaterThan(3650 * 86400);
  });

  test("bounds custom durations and defaults to 30 days", () => {
    expect(normalizeSiteTrust(undefined).maxAgeSeconds).toBe(30 * 86400);
    expect(normalizeSiteTrust({ mode: "custom", days: 14 }).maxAgeSeconds).toBe(14 * 86400);
    expect(() => normalizeSiteTrust({ mode: "custom", days: 0 })).toThrow();
    expect(() => normalizeSiteTrust({ mode: "custom", days: 3651 })).toThrow();
    expect(() => normalizeSiteTrust({ mode: "forever-ish" })).toThrow();
  });
});
