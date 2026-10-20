import { expect, test } from "bun:test";
import { makeColseal, SealError } from "../../../../../shared/colseal-core";

// Known-answer vectors computed independently in Python (argon2-cffi Argon2id, pyca/cryptography
// HKDF-SHA256, HMAC-SHA256 and ChaCha20-Poly1305), same construction as shared/colseal-core.ts.
const KAT_SECRET = "colseal-kat-secret-0123";
const kat = makeColseal("katdomain", () => KAT_SECRET);
const D1_KAT = "d1.D98XEVuvgQcR4cCKwYRaQsyFWF407aFmsUNTiTVauVbmVNFrVl_ferN-laA"; // accounts.email, "kat@example.test"
const B1_KAT = "b1.6505607ecf29a119d56913522462cfadab5e0410895939037f0574f95dfc0ac3"; // sessions.token_hash, "tok-kat-0001"
const S1_KAT = "s1.AAECAwQFBgcICQoLqVTffuGjOqqntU0PrHahi4PRSvHrkzzASoMV"; // accounts.phone, row acc_kat, nonce 00..0b

test("known-answer vectors match the independent implementation", () => {
  expect(kat.sealDet("accounts.email", "kat@example.test")).toBe(D1_KAT);
  expect(kat.openDet("accounts.email", D1_KAT)).toBe("kat@example.test");
  expect(kat.blindIndex("sessions.token_hash", "tok-kat-0001")).toBe(B1_KAT);
  expect(kat.open("accounts.phone", "acc_kat", S1_KAT)).toBe("+1 555 0100");
});

test("s1 round trip, fresh nonce each seal, unicode and long values", () => {
  for (const v of ["x", "Ünïcødé ✓ 名前", "a".repeat(100_000)]) {
    const a = kat.seal("t.c", "row1", v), b = kat.seal("t.c", "row1", v);
    expect(a).not.toBe(b);
    expect(a).not.toContain(Buffer.from(v).toString("base64url").slice(0, 8));
    expect(kat.open("t.c", "row1", a)).toBe(v);
  }
});

test("tampering any byte of an s1 or d1 blob is rejected", () => {
  for (const [blob, openIt] of [
    [kat.seal("t.c", "row1", "secret value"), (b: string) => kat.open("t.c", "row1", b)],
    [kat.sealDet("t.d", "lookup value"), (b: string) => kat.openDet("t.d", b)],
  ] as const) {
    const raw = Buffer.from(blob.slice(3), "base64url");
    for (let i = 0; i < raw.length; i++) {
      const t = Buffer.from(raw); t[i] ^= 0x01;
      expect(() => openIt(blob.slice(0, 3) + t.toString("base64url"))).toThrow(SealError);
    }
    expect(() => openIt(blob.slice(0, 3) + raw.subarray(0, 20).toString("base64url"))).toThrow(SealError);
  }
});

test("AD binding: a value moved to another row, column or table does not open", () => {
  const blob = kat.seal("accounts.phone", "acc_A", "+1 555 0100");
  expect(() => kat.open("accounts.phone", "acc_B", blob)).toThrow(SealError);          // row swap
  expect(() => kat.open("accounts.stage_name", "acc_A", blob)).toThrow(SealError);     // column swap
  expect(() => kat.open("sessions.phone", "acc_A", blob)).toThrow(SealError);          // table swap
  const det = kat.sealDet("accounts.email", "a@example.test");
  expect(() => kat.openDet("oauth_identities.email", det)).toThrow(SealError);
  expect(() => kat.open("accounts.email", "", det)).toThrow(SealError);                // format confusion
});

test("deterministic lookup: equal plaintext gives equal blobs per column, not across columns", () => {
  expect(kat.sealDet("u.email", "a@x.test")).toBe(kat.sealDet("u.email", "a@x.test"));
  expect(kat.sealDet("u.email", "a@x.test")).not.toBe(kat.sealDet("u.email", "b@x.test"));
  expect(kat.sealDet("u.email", "a@x.test").slice(3, 19)).not.toBe(kat.sealDet("v.email", "a@x.test").slice(3, 19));
});

test("a d1 blob re-encrypted under a forged IV is refused (synthetic IV re-checked)", () => {
  // Swap in the ciphertext of another plaintext but keep the first blob's IV: the AEAD tag fails, and an
  // attacker without the key cannot produce a matching tag, so only the IV check could otherwise be bypassed.
  const a = Buffer.from(kat.sealDet("t.d", "aaaa").slice(3), "base64url");
  const b = Buffer.from(kat.sealDet("t.d", "bbbb").slice(3), "base64url");
  const mixed = Buffer.concat([a.subarray(0, 12), b.subarray(12)]);
  expect(() => kat.openDet("t.d", "d1." + mixed.toString("base64url"))).toThrow(SealError);
});

test("keys: separate per column and per purpose; a different master secret opens nothing", () => {
  const other = makeColseal("katdomain", () => KAT_SECRET + "x");
  expect(() => other.open("accounts.phone", "acc_kat", S1_KAT)).toThrow(SealError);
  expect(() => other.openDet("accounts.email", D1_KAT)).toThrow(SealError);
  expect(other.blindIndex("sessions.token_hash", "tok-kat-0001")).not.toBe(B1_KAT);
  expect(kat.blindIndex("api_keys.key_hash", "tok-kat-0001")).not.toBe(B1_KAT);
  const otherDomain = makeColseal("otherdomain", () => KAT_SECRET);
  expect(otherDomain.blindIndex("sessions.token_hash", "tok-kat-0001")).not.toBe(B1_KAT);
  // The index key is not the encryption key: the d1 IV is HMAC(idxKey, ctx||0||pt), the b1 index HMAC(idxKey, pt).
  expect(kat.blindIndex("accounts.email", "kat@example.test").slice(3, 27)).not.toBe(Buffer.from(D1_KAT.slice(3), "base64url").subarray(0, 12).toString("hex"));
});
