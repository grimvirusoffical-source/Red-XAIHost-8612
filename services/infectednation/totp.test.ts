import { describe, expect, test } from "bun:test";
import { base32Decode, base32Encode, hotp, isSealed, openSecret, otpauthUri, sealSecret, sealSecretWith, totp, totpMasterSecret, TotpKeyMissing, verifyTotp } from "./totp";

// RFC 6238 Appendix B (8 digits). Seeds are ASCII "1234567890" repeated to the hash length.
const seed = (n: number) => Buffer.from("1234567890".repeat(7).slice(0, n));
const VEC: [number, string, string, string][] = [
  [59, "94287082", "46119246", "90693936"],
  [1111111109, "07081804", "68084774", "25091201"],
  [1111111111, "14050471", "67062674", "99943326"],
  [1234567890, "89005924", "91819424", "93441116"],
  [2000000000, "69279037", "90698825", "38618901"],
  [20000000000, "65353130", "77737706", "47863826"],
];

describe("RFC 6238 Appendix B", () => {
  for (const [t, s1, s256, s512] of VEC) {
    test(`T=${t}`, () => {
      expect(totp(seed(20), t, 8, "sha1")).toBe(s1);
      expect(totp(seed(32), t, 8, "sha256")).toBe(s256);
      expect(totp(seed(64), t, 8, "sha512")).toBe(s512);
    });
  }
});

test("RFC 4226 Appendix D HOTP", () => {
  const want = ["755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"];
  want.forEach((w, c) => expect(hotp(seed(20), c)).toBe(w));
});

test("base32 RFC 4648 vectors + round trip", () => {
  expect(base32Encode(Buffer.from("foobar"))).toBe("MZXW6YTBOI");
  expect(base32Decode("MZXW6YTBOI").toString()).toBe("foobar");
  expect(base32Decode("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ").equals(seed(20))).toBe(true);
  expect(() => base32Decode("abc1")).toThrow();
});

test("verify: skew window, wrong code, replay", () => {
  const k = seed(20), t = 1111111111;
  const step = Math.floor(t / 30);
  expect(verifyTotp(k, totp(k, t), t)).toBe(step);
  expect(verifyTotp(k, totp(k, t - 30), t)).toBe(step - 1);
  expect(verifyTotp(k, totp(k, t + 30), t)).toBe(step + 1);
  expect(verifyTotp(k, totp(k, t - 90), t)).toBeNull();
  expect(verifyTotp(k, "000000", t) === null || totp(k, t) === "000000").toBe(true);
  expect(verifyTotp(k, "12345a", t)).toBeNull();
  expect(verifyTotp(k, totp(k, t), t, step)).toBeNull(); // replay of an accepted step
});

test("otpauth URI", () => {
  const u = otpauthUri("InfectedNation", "jf", "GEZDGNBV");
  expect(u).toBe("otpauth://totp/InfectedNation:jf?secret=GEZDGNBV&issuer=InfectedNation&algorithm=SHA1&digits=6&period=30");
});

test("seal/open: random salt+nonce, tamper, wrong key, wrong account", () => {
  const m = "a-real-master-secret-0123456789";
  const raw = seed(20);
  const a = sealSecret(raw, m, "acct1"), b = sealSecret(raw, m, "acct1");
  expect(isSealed(a)).toBe(true);
  expect(a).not.toBe(b);
  expect(a.split(".")[1]).not.toBe(b.split(".")[1]);
  expect(a).not.toContain(base32Encode(raw));
  expect(openSecret(a, m, "acct1").equals(raw)).toBe(true);
  expect(() => openSecret(a, m + "x", "acct1")).toThrow();
  expect(() => openSecret(a, m, "acct2")).toThrow();
  const p = a.split("."); p[3] = Buffer.from(Buffer.from(p[3], "base64url").map((x, i) => (i === 0 ? x ^ 1 : x))).toString("base64url");
  expect(() => openSecret(p.join("."), m, "acct1")).toThrow();
});

test("no fallback key: fail closed", () => {
  expect(() => totpMasterSecret({})).toThrow(TotpKeyMissing);
  expect(() => totpMasterSecret({ CREDENTIAL_SECRET: "short" })).toThrow(TotpKeyMissing);
  expect(totpMasterSecret({ CREDENTIAL_SECRET: "0123456789abcdef" })).toBe("0123456789abcdef");
});

// Computed independently with the Red-SecretsXAI Go package (Argon2id -> HKDF-SHA256 -> ChaCha20-Poly1305).
const V2_KAT = "v2.CQkJCQkJCQkJCQkJCQkJCQ.BwcHBwcHBwcHBwcH.P1Xt-YnzlaJ0FXOwTgieniqFdos.kMJceiyV_Qq8lvbgreSScw";
// A real seed sealed in the refused AES-256-GCM "v1." form (same master, account and seed).
const V1_AES = "v1.CQkJCQkJCQkJCQkJCQkJCQ.BwcHBwcHBwcHBwcH.TvxFjPOHKcreAhvH9Q4rtsKkoiw.JAn9kU7GIwzrOq1kP8U9cw";

test("v2 seal known-answer vector matches the independent implementation", () => {
  const m = "a-real-master-secret-0123456789";
  expect(sealSecretWith(seed(20), m, "acct1", Buffer.alloc(16, 9), Buffer.alloc(12, 7))).toBe(V2_KAT);
  expect(openSecret(V2_KAT, m, "acct1").equals(seed(20))).toBe(true);
});

test("old AES v1 seeds are refused, never decrypted", () => {
  expect(isSealed(V1_AES)).toBe(false);
  expect(() => openSecret(V1_AES, "a-real-master-secret-0123456789", "acct1")).toThrow("not a sealed TOTP secret");
});
