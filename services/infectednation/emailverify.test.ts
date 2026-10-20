import { expect, test } from "bun:test";
import { emailVerificationEnabled, newVerifyToken, parseVerifyToken, sendMail, tokenMatches, verifyMail } from "./emailverify";

test("flag defaults to off", () => {
  expect(emailVerificationEnabled({})).toBe(false);
  expect(emailVerificationEnabled({ INFECTEDNATION_EMAIL_VERIFICATION: "off" })).toBe(false);
  expect(emailVerificationEnabled({ INFECTEDNATION_EMAIL_VERIFICATION: "yes please" })).toBe(false);
  expect(emailVerificationEnabled({ INFECTEDNATION_EMAIL_VERIFICATION: "ON" })).toBe(true);
});

test("tokens: random, hashed, parse, constant-time match", () => {
  const a = newVerifyToken("nat_abc"), b = newVerifyToken("nat_abc");
  expect(a.token).not.toBe(b.token);
  const p = parseVerifyToken(a.token)!;
  expect(p.accountId).toBe("nat_abc");
  expect(a.hash).toMatch(/^[0-9a-f]{64}$/);
  expect(a.token.includes(a.hash)).toBe(false);
  expect(tokenMatches(p.secret, a.hash)).toBe(true);
  expect(tokenMatches(p.secret, b.hash)).toBe(false);
  expect(tokenMatches(p.secret, "abcd")).toBe(false);
  for (const bad of ["", "nat_abc", ".x", "nat abc." + "A".repeat(43), "nat_abc." + "A".repeat(42), 5, null]) expect(parseVerifyToken(bad)).toBeNull();
});

test("link carries the token in the fragment; transport refuses plain http to remote hosts", async () => {
  const m = verifyMail("a@b.c", "nat_x." + "A".repeat(43), { INFECTEDNATION_VERIFY_URL: "https://id.example.com/verify?x=1" });
  expect(m.link.startsWith("https://id.example.com/verify#token=")).toBe(true);
  expect(await sendMail(m, {})).toBe(false);
  expect(await sendMail(m, { INFECTEDNATION_MAIL_WEBHOOK: "http://example.com/hook" })).toBe(false);
});
