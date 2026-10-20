import { expect, test } from "bun:test";
import { createCipheriv, randomBytes, scryptSync } from "node:crypto";
import { credentialSecret, CredentialSecretMissing, decryptJson, encryptJson, isLegacyBlob } from "./crypto";

const S = "test-secret-" + randomBytes(8).toString("hex");

test("no hardcoded fallback: missing, short or the old dev value fail closed", () => {
  expect(() => credentialSecret({})).toThrow(CredentialSecretMissing);
  expect(() => credentialSecret({ CREDENTIAL_SECRET: "redxaihost-dev" })).toThrow(CredentialSecretMissing);
  expect(() => credentialSecret({ BETTER_AUTH_SECRET: "short" })).toThrow(CredentialSecretMissing);
  expect(credentialSecret({ BETTER_AUTH_SECRET: S })).toBe(S);
});

test("v2: random salt and IV per record, round trip, tamper and wrong key rejected", () => {
  const a = encryptJson({ apiKey: "sk-1" }, S), b = encryptJson({ apiKey: "sk-1" }, S);
  const pa = a.split("."), pb = b.split(".");
  expect(pa[0]).toBe("v2");
  expect(pa[1]).not.toBe(pb[1]);
  expect(pa[2]).not.toBe(pb[2]);
  expect(decryptJson(a, S)).toEqual({ apiKey: "sk-1" });
  expect(decryptJson(a, S + "x")).toBeNull();
  const ct = Buffer.from(pa[3], "base64url"); ct[0] ^= 1; pa[3] = ct.toString("base64url");
  expect(decryptJson(pa.join("."), S)).toBeNull();
  expect(isLegacyBlob(a)).toBe(false);
});

test("legacy v1 blobs (fixed salt) still decrypt", () => {
  const key = scryptSync(S, "redxaihost.credentials.v1", 32), iv = randomBytes(12);
  const c = createCipheriv("aes-256-gcm", key, iv);
  const enc = Buffer.concat([c.update(Buffer.from(JSON.stringify({ token: "gh" }))), c.final()]);
  const blob = [iv, enc, c.getAuthTag()].map((x) => x.toString("base64url")).join(".");
  expect(isLegacyBlob(blob)).toBe(true);
  expect(decryptJson(blob, S)).toEqual({ token: "gh" });
});
