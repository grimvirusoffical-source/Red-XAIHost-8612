import { expect, test } from "bun:test";
import { hashPassword as upstreamScrypt } from "better-auth/crypto";
import { rsx } from "../../../../../vendor/rsx-sdk/rsx";
import { hashPanelPassword, verifyPanelPassword } from "./password";

// Reference vector of the phc-winner-argon2 CLI (echo -n password | argon2 somesalt -id -t 2 -m 16 -p 1 -l 32 -e),
// also accepted by Bun's own Argon2 implementation.
const REF = "$argon2id$v=19$m=65536,t=2,p=1$c29tZXNhbHQ$CTFhFdXPJO1aFaMaO6Mm5c8y7cJHAph8ArZWb2GRPPc";

test("panel hashes are Argon2id PHC strings and verify", async () => {
  const h = await hashPanelPassword("correct horse battery");
  expect(h.startsWith("$argon2id$v=19$m=65536,t=3,p=1$")).toBe(true);
  expect(await verifyPanelPassword({ hash: h, password: "correct horse battery" })).toBe(true);
  expect(await verifyPanelPassword({ hash: h, password: "correct horse batterY" })).toBe(false);
});

test("reference PHC vector verifies", async () => {
  expect(rsx.phcVerifyPassword("password", REF)).toBe(true);
  expect(await verifyPanelPassword({ hash: REF, password: "password" })).toBe(true);
  expect(await verifyPanelPassword({ hash: REF, password: "passwore" })).toBe(false);
});

test("rsx PHC strings and Bun's Argon2id agree both ways", async () => {
  const h = await hashPanelPassword("cross check");
  expect(await Bun.password.verify("cross check", h)).toBe(true);
  const b = await Bun.password.hash("cross check", { algorithm: "argon2id" });
  expect(await verifyPanelPassword({ hash: b, password: "cross check" })).toBe(true);
});

test("pre-switch Better Auth scrypt hashes still verify; garbage is refused", async () => {
  const old = await upstreamScrypt("old owner password");
  expect(await verifyPanelPassword({ hash: old, password: "old owner password" })).toBe(true);
  expect(await verifyPanelPassword({ hash: old, password: "old owner passworD" })).toBe(false);
  expect(await verifyPanelPassword({ hash: "$argon2id$garbage", password: "x" })).toBe(false);
  expect(await verifyPanelPassword({ hash: "plain", password: "plain" })).toBe(false);
});
