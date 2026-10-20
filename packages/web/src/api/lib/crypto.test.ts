import { expect, test } from "bun:test";
import { rsx } from "../../../../../vendor/rsx-sdk/rsx";
import {
  credentialSecret, CredentialSecretMissing, decryptJson, encryptJson, hashToken, isLegacyBlob,
  LegacyCredentialRefused, sealJsonWith,
} from "./crypto";

const S = "test-secret-" + rsx.random(8).toString("hex");
const FIX = "fixture-secret-0123456789";
// Computed independently with the Red-SecretsXAI Go package (Argon2id -> HKDF-SHA256 -> ChaCha20-Poly1305).
const V3_KAT = "v3.CQkJCQkJCQkJCQkJCQkJCQ.BwcHBwcHBwcHBwcH.9_CrzcdIfzuY36CYjaI.ga7wkeuTohYVsn69maJF_w";
// Real blobs in the refused AES-256-GCM formats, same secret and plaintext {"token":"gh"}.
const V1_AES = "BwcHBwcHBwcHBwcH.t2b5FdkoW2MLuvSzPCU.xbR0-xl0OGHamcscyPtOdA";
const V2_AES = "v2.CQkJCQkJCQkJCQkJCQkJCQ.BwcHBwcHBwcHBwcH.iWBwVTb0EL2AjNTUhfo.U35moHs09A-vVm-h1wO80w";

test("no hardcoded fallback: missing, short or the old dev value fail closed", () => {
  expect(() => credentialSecret({})).toThrow(CredentialSecretMissing);
  expect(() => credentialSecret({ CREDENTIAL_SECRET: "redxaihost-dev" })).toThrow(CredentialSecretMissing);
  expect(() => credentialSecret({ BETTER_AUTH_SECRET: "short" })).toThrow(CredentialSecretMissing);
  expect(credentialSecret({ BETTER_AUTH_SECRET: S })).toBe(S);
});

test("v3 known-answer vector matches the independent implementation and opens", () => {
  expect(sealJsonWith({ token: "gh" }, FIX, Buffer.alloc(16, 9), Buffer.alloc(12, 7))).toBe(V3_KAT);
  expect(decryptJson(V3_KAT, FIX)).toEqual({ token: "gh" });
});

test("v3: random salt and nonce per record, round trip, tamper and wrong key rejected", () => {
  const a = encryptJson({ apiKey: "sk-1" }, S), b = encryptJson({ apiKey: "sk-1" }, S);
  const pa = a.split("."), pb = b.split(".");
  expect(pa[0]).toBe("v3");
  expect(pa[1]).not.toBe(pb[1]);
  expect(pa[2]).not.toBe(pb[2]);
  expect(decryptJson(a, S)).toEqual({ apiKey: "sk-1" });
  expect(decryptJson(a, S + "x")).toBeNull();
  for (const i of [1, 2, 3, 4]) {
    const q = [...pa]; const x = Buffer.from(q[i], "base64url"); x[0] ^= 1; q[i] = x.toString("base64url");
    expect(decryptJson(q.join("."), S)).toBeNull();
  }
  expect(decryptJson("v3.x.y", S)).toBeNull();
  expect(isLegacyBlob(a)).toBe(false);
});

test("old AES blobs (v1 and v2) are refused, never decrypted", () => {
  for (const blob of [V1_AES, V2_AES]) {
    expect(isLegacyBlob(blob)).toBe(true);
    expect(() => decryptJson(blob, FIX)).toThrow(LegacyCredentialRefused);
  }
});

test("node token hash is SHA-256 hex", () => {
  expect(hashToken("abc")).toBe("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
});
