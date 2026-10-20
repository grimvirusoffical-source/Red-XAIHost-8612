// Real HTTP: a local listener stands in for a remote redxd reached over plain http.
import { afterAll, expect, test } from "bun:test";
import { keySafeOver, redxdbFrom } from "./redxdb";

const seen: (string | null)[] = [];
const srv = Bun.serve({ port: 0, hostname: "0.0.0.0", fetch(r) { seen.push(r.headers.get("authorization")); return Response.json({ dbs: [] }); } });
afterAll(() => srv.stop(true));

test("key only over https or loopback http", () => {
  expect(keySafeOver("https://db.example.com")).toBe(true);
  expect(keySafeOver("http://127.0.0.1:7777")).toBe(true);
  expect(keySafeOver("http://localhost:7777")).toBe(true);
  expect(keySafeOver("http://[::1]:7777")).toBe(true);
  expect(keySafeOver("http://10.0.0.5:7777")).toBe(false);
  expect(keySafeOver("http://db.example.com")).toBe(false);
  expect(keySafeOver("http://127.0.0.1.nip.io")).toBe(false);
});

test("non-loopback http: key is never sent", async () => {
  // 127.0.0.2 is not on the allow-list's exact-loopback name, but use a LAN-style name to be explicit:
  const c = redxdbFrom({ url: `http://lan-host.invalid:${srv.port}`, key: "rx_live_secret" });
  await expect(c.listDatabases()).rejects.toMatchObject({ code: "insecure_url" });
  expect(seen.length).toBe(0);
});

test("loopback http still works and sends the key", async () => {
  const c = redxdbFrom({ url: `http://127.0.0.1:${srv.port}`, key: "rx_live_secret" });
  expect((await c.listDatabases()).databases).toEqual([]);
  expect(seen.at(-1)).toBe("Bearer rx_live_secret");
});
