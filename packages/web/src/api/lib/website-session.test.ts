import { expect, test } from "bun:test";
import { websiteIdentity } from "./website-session";

const env = { REDX_WEBSITE_SESSION: "true", REDX_WEBSITE_OWNER_ACCOUNT_IDS: "account-owner" };
const now = 1700000000000;
const profile = (id = "account-a") => ({ account: { id, email: "person@example.invalid", username: "Person", roles: [] as string[] }, expiresMs: now + 10000, amr: ["pwd"] });
const headers = (cookie = "redx_session=synthetic") => new Headers({ cookie });

test("website sessions require explicit opt-in and the exact original cookie", async () => {
  let calls = 0;
  const request = async () => { calls++; return Response.json(profile()); };
  expect(await websiteIdentity(headers(), { env: {}, fetch: request, now })).toBeNull();
  for (const cookie of ["", "redx-host.session_token=synthetic", "redx_session=", "redx_session=a; redx_session=b"]) {
    expect(await websiteIdentity(headers(cookie), { env, fetch: request, now })).toBeNull();
  }
  expect(calls).toBe(0);
});

test("only the website cookie is sent to the fixed private authority", async () => {
  const incoming = headers("other=synthetic; redx_session=synthetic; redx-host.session_token=synthetic-host");
  incoming.set("authorization", "Bearer synthetic-forged");
  incoming.set("x-redx-account-id", "account-owner");
  const identity = await websiteIdentity(incoming, { env, now, fetch: async (url, init) => {
    expect(url).toBe("http://127.0.0.1:4200/api/accounts/me");
    const sent = new Headers(init.headers);
    expect([...sent.keys()].sort()).toEqual(["accept", "cookie"]);
    expect(sent.get("cookie")).toBe("redx_session=synthetic");
    expect(init.redirect).toBe("manual");
    expect(init.cache).toBe("no-store");
    return Response.json(profile());
  } });
  expect(identity?.id).toBe("account-a");
  expect(identity?.owner).toBe(false);
});

test("website owner elevation requires immutable ID, authoritative role and MFA together", async () => {
  for (const [id, roles, amr, expected] of [
    ["account-owner", ["owner"], ["pwd", "otp"], true],
    ["account-owner", ["owner"], ["pwd", "call"], true],
    ["account-owner", ["owner"], ["pwd"], false],
    ["account-owner", [], ["pwd", "otp"], false],
    ["account-other", ["owner"], ["pwd", "otp"], false],
  ] as const) {
    const json = { ...profile(id), account: { ...profile(id).account, roles }, amr };
    expect((await websiteIdentity(headers(), { env, now, fetch: async () => Response.json(json) }))?.owner).toBe(expected);
  }
});

test("missing, expired or ambiguous identities and reserved tenant IDs fail closed", async () => {
  for (const json of [null, {}, { ...profile(), expiresMs: now }, { ...profile(), expiresMs: String(now + 10000) },
    { ...profile(), account: { email: "person@example.invalid" } }, profile("owner"), profile("__unassigned__"),
    { ...profile(), amr: [] }, { ...profile(), active: false },
    ...["disabled", "banned", "deleted"].map(key => ({ ...profile(), account: { ...profile().account, [key]: true } })),
    ...[{ status: "suspended" }, { state: "pending" }, { emailVerified: false }].map(flags => ({ ...profile(), account: { ...profile().account, ...flags } }))]) {
    expect(await websiteIdentity(headers(), { env, now, fetch: async () => Response.json(json) })).toBeNull();
  }
});

test("authority failures never redirect, accept a profile from an error response or authenticate offline", async () => {
  for (const status of [301, 401, 403, 429, 500]) {
    expect(await websiteIdentity(headers(), { env, now, fetch: async () => Response.json(profile(), { status }) })).toBeNull();
  }
  expect(await websiteIdentity(headers(), { env, now, fetch: async () => { throw new Error("offline"); } })).toBeNull();
  expect(await websiteIdentity(headers(), { env, now, fetch: async () => new Response("bad JSON") })).toBeNull();
});

test("each request rechecks revocation and concurrent users cannot share identities", async () => {
  let revoked = false;
  let calls = 0;
  const request = async (_url: string, init: RequestInit) => {
    calls++;
    const cookie = new Headers(init.headers).get("cookie");
    if (revoked) return new Response(null, { status: 401 });
    return Response.json(profile(cookie === "redx_session=a" ? "account-a" : "account-b"));
  };
  const result = await Promise.all(["a", "b"].map(value => websiteIdentity(headers(`redx_session=${value}`), { env, now, fetch: request })));
  expect(result.map(value => value?.id)).toEqual(["account-a", "account-b"]);
  revoked = true;
  expect(await websiteIdentity(headers("redx_session=a"), { env, now, fetch: request })).toBeNull();
  expect(calls).toBe(3);
});
