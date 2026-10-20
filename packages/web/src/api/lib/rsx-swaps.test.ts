import { expect, test } from "bun:test";
// RFC 4231 test case 2 and an exact recomputation, so the swapped HMACs are checked against real vectors.
const JEFE = "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843";
process.env.BETTER_AUTH_SECRET ||= "Jefe-but-longer-than-16";
const { rsx, ctEqStr } = await import("../../../../../vendor/rsx-sdk/rsx");
const { signatureMatches } = await import("../webhooks");
const bundle = await import("./bundle-storage");
const { newId, slugify } = await import("./ids");

test("rsx HMAC-SHA256 matches RFC 4231 case 2", () => {
  expect(rsx.hmac(Buffer.from("Jefe"), Buffer.from("what do ya want for nothing?")).toString("hex")).toBe(JEFE);
});

test("GitHub webhook signature: exact header accepted, any change refused", () => {
  const body = "what do ya want for nothing?";
  expect(signatureMatches("Jefe", body, `sha256=${JEFE}`)).toBe(true);
  expect(signatureMatches("Jefe", body, `sha256=${JEFE.slice(0, -1)}0`)).toBe(false);
  expect(signatureMatches("Jefe", body + " ", `sha256=${JEFE}`)).toBe(false);
  expect(signatureMatches("Jefe", body, JEFE)).toBe(false);
  expect(signatureMatches("Jefe", body, undefined)).toBe(false);
});

test("signed bundle URLs: HMAC over method, key, expiry; tamper and expiry refused", () => {
  const key = "bundles/b.zip", exp = Date.now() + 60_000;
  const sig = bundle.sign("GET", key, exp);
  const secret = process.env.CREDENTIAL_SECRET || process.env.BETTER_AUTH_SECRET!;
  expect(sig).toBe(rsx.hmac(Buffer.from(secret), Buffer.from(`GET\n${key}\n${exp}`)).toString("hex"));
  expect(bundle.verify("GET", key, exp, sig)).toBe(true);
  expect(bundle.verify("PUT", key, exp, sig)).toBe(false);
  expect(bundle.verify("GET", key, exp + 1, sig)).toBe(false);
  expect(bundle.verify("GET", key, Date.now() - 1, bundle.sign("GET", key, Date.now() - 1))).toBe(false);
});

test("ids use the rsx RNG with the same shapes", () => {
  expect(newId("prj")).toMatch(/^prj_[A-Za-z0-9_-]{12}$/);
  expect(newId("prj")).not.toBe(newId("prj"));
  expect(slugify("!!!")).toMatch(/^project-[0-9a-f]{6}$/);
  expect(ctEqStr("abc", "abc")).toBe(true);
  expect(ctEqStr("abc", "abd")).toBe(false);
  expect(ctEqStr("abc", "abcd")).toBe(false);
});

test("crypto boundary: runtime panel and Nation code uses no node:crypto or Bun.password", async () => {
  const { Glob } = await import("bun");
  const root = new URL("../../../../../", import.meta.url).pathname;
  const hits: string[] = [];
  for (const pattern of ["packages/web/src/**/*.ts", "services/infectednation/*.ts", "shared/**/*.ts", "scripts/selfhost.mjs"]) {
    for await (const f of new Glob(pattern).scan({ cwd: root })) {
      if (/\.test\.ts$/.test(f)) continue;
      const src = await Bun.file(root + f).text();
      if (/from\s+["'](node:)?crypto["']|Bun\.password|crypto\.subtle|createHmac|randomBytes|timingSafeEqual/.test(src)) hits.push(f);
    }
  }
  expect(hits).toEqual([]);
});
