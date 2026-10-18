'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const util = require('node:util');
const { RedX, RedXError, fromBox, escapePath } = require('../redx');
const { startHost } = require('./harness');

const P1 = 'players.red-xai.db:Player[ID=1]';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
function waitFor(cond, ms = 5000) {
  return new Promise((resolve, reject) => {
    const t0 = Date.now();
    (function tick() {
      if (cond()) return resolve();
      if (Date.now() - t0 > ms) return reject(new Error('timed out waiting'));
      setTimeout(tick, 20);
    })();
  });
}

let host, admin, rw, ro, scoped;

test.before(async () => {
  host = await startHost();
  admin = new RedX({ url: host.url, key: host.adminKey });
  const mk = async (o) => new RedX({ url: host.url, key: (await admin.createKey({ db: 'default', ...o })).key });
  rw = await mk({ scope: 'write', label: 'sdk-rw', rateRps: 1000, rateBurst: 1000 });
  ro = await mk({ scope: 'read', label: 'sdk-ro', rateRps: 1000, rateBurst: 1000 });
  scoped = await mk({ scope: 'write', prefix: 'sess.', label: 'sdk-prefix' });
});
test.after(async () => { if (host) await host.stop(); });

test('box text decoder', () => {
  assert.deepEqual(fromBox('"a\\"b\\u00e9"'), 'a"bé');
  assert.deepEqual(fromBox('-1.5e2'), -150);
  assert.deepEqual(fromBox('NELL'), null);
  assert.deepEqual(fromBox(';["a", 2, TRUE]:'), ['a', 2, true]);
  assert.deepEqual(fromBox('[ {A[]} = 1, {B[ID=7]} = [ {C[]} = ;[]: ] ]'), { A: 1, 'B[ID=7]': { C: [] } });
  const p = fromBox('[ {__proto__[]} = 1 ]');
  assert.equal(Object.getPrototypeOf(p), Object.prototype);
  assert.equal(Object.keys(p)[0], '__proto__');
  for (const bad of ['', '[', '"x', '[ {A[]} 1 ]', 'WAT', '1 2', ';[1,]:']) assert.throws(() => fromBox(bad), /offset/, bad);
  assert.throws(() => fromBox(';['.repeat(5000)), /deeply/);
});

test('path escaping', () => {
  assert.equal(escapePath('f.red-xai.db:Box[ID=1].Name'), 'f.red-xai.db%3ABox%5BID%3D1%5D.Name');
  assert.throws(() => escapePath(''), TypeError);
});

test('constructor validation and key secrecy', () => {
  assert.throws(() => new RedX({ url: 'http://x' }), TypeError);
  assert.throws(() => new RedX({ key: 'k' }), TypeError);
  assert.throws(() => new RedX({ url: 'ftp://x', key: 'k' }), TypeError);
  assert.throws(() => new RedX({ url: 'http://x', key: 'k', db: '../x' }), TypeError);
  for (const s of [JSON.stringify(rw), util.inspect(rw, { showHidden: true }), String(rw), JSON.stringify(Object.getOwnPropertyNames(rw))]) {
    assert.ok(!/rx_live_/.test(s));
  }
});

test('health and stats', async () => {
  const h = await new RedX({ url: host.url, key: 'rx_live_x' }).health();
  assert.equal(h.status, 'ok');
  const s = await rw.stats();
  assert.ok(s.dbs && s.dbs.default);
});

test('set / get / getBox / delete round trip', async () => {
  const v = { Name: 'ann "the" \\ ünï', Level: 5, Ratio: 0.25, Tags: ['a', 'b'], Stats: { Gold: 3 }, Ok: true, Gone: null };
  const r = await rw.set(P1, v);
  assert.equal(r.ok, true);
  assert.deepEqual(await rw.get(P1), v);
  assert.equal(await rw.get(P1 + '.Name'), v.Name);
  assert.equal(await rw.get(P1 + '.Stats.Gold'), 3);
  assert.match(await rw.getBox(P1), /\{Name\[\]\} = "ann/);
  await rw.setBox('players.red-xai.db:Player[ID=2]', '[ {Name[]} = "bob", {Level[]} = 9 ]');
  assert.deepEqual(await rw.get('players.red-xai.db:Player[ID=2]'), { Level: 9, Name: 'bob' });
  await rw.delete(P1);
  await assert.rejects(rw.get(P1), (e) => e instanceof RedXError && e.status === 404 && e.notFound);
  assert.equal(await rw.getOr(P1, 'none'), 'none');
});

test('query (host path query)', async () => {
  for (let i = 10; i < 13; i++) await rw.set(`q.red-xai.db:Row[ID=${i}]`, { Name: `n${i}` });
  const r = await rw.query('q.red-xai.db:Row[ID=*].Name');
  assert.equal(r.count, 3);
  assert.deepEqual(r.values.slice().sort(), ['n10', 'n11', 'n12']);
  await assert.rejects(rw.query(''), TypeError);
});

test('ttl: sliding option accepted, volatile file expires', async () => {
  const p = 'sess.red-xai-db-qs:Q[ID=1].Otp';
  await rw.set(p, 'abc', { ttl: 1 });
  assert.equal(await rw.get(p), 'abc');
  await rw.setBox('sess.red-xai-db-qs:Q[ID=2].Otp', '"slide"', { ttl: 30, sliding: true });
  assert.equal(await rw.get('sess.red-xai-db-qs:Q[ID=2].Otp'), 'slide');
  await sleep(2600);
  // an expired or never-written path is always 404 (never 200 with NELL)
  assert.equal(await rw.getOr(p, 'x'), 'x');
  assert.equal(await rw.getOr('sess.red-xai-db-qs:Q[ID=77].Otp', 'x'), 'x');
  await assert.rejects(rw.get(p), (e) => e instanceof RedXError && e.status === 404);
  assert.equal(await rw.getOr('sess.red-xai-db-qs:Q[ID=2].Otp', 'gone'), 'slide'); // 30s sliding: still alive
});

test('sql: select with params, write, errors; query() still works', async () => {
  await rw.set('sqlt.red-xai.db:P[ID=1]', { Name: 'Ann', Level: 30 });
  await rw.set('sqlt.red-xai.db:P[ID=2]', { Name: 'Bob', Level: 5 });
  const r = await rw.sql('SELECT Name FROM sqlt.P WHERE Level > ? ORDER BY Name', [10]);
  assert.equal(r.kind, 'select');
  assert.equal(r.count, 1);
  assert.deepEqual(r.columns, ['Name']);
  const w = await rw.sql('UPDATE sqlt.P SET Level = ? WHERE _id = ?', [6, 2]);
  assert.equal(w.affected, 1);
  assert.equal(await rw.get('sqlt.red-xai.db:P[ID=2].Level'), 6);
  await assert.rejects(rw.sql('SELEKT'), (e) => e instanceof RedXError && e.status === 400);
  await assert.rejects(ro.sql('DELETE FROM sqlt.P WHERE _id = 1'), (e) => e.status === 403);
  assert.equal((await rw.query('sqlt.red-xai.db:P[ID=*].Name')).count, 2);
  await assert.rejects(rw.sql(''), TypeError);
});

test('auth: invalid key, read-only key, prefix key', async () => {
  const bad = new RedX({ url: host.url, key: 'rx_live_' + 'A'.repeat(43) });
  const errs = [];
  for (const f of [() => bad.get(P1), () => bad.stats(), () => bad.set(P1, 1)]) {
    await assert.rejects(f(), (e) => { errs.push(e.message); return e.status === 401 && e.unauthorized; });
  }
  assert.ok(errs.every((m) => !m.includes('A'.repeat(20))));
  await assert.rejects(ro.set(P1, { Name: 'x' }), (e) => e.status === 403 && e.forbidden);
  await assert.rejects(ro.delete(P1), (e) => e.status === 403);
  await assert.rejects(rw.createKey({ scope: 'read' }), (e) => e.status === 403);
  await rw.set(P1, { Name: 'ro-can-read' });
  assert.equal(await ro.get(P1 + '.Name'), 'ro-can-read');
  // prefix-restricted key: inside prefix ok, outside refused, no /v1/query
  await scoped.set('sess.red-xai-db-qs:Z[ID=1].V', 'ok', { ttl: 60 });
  await assert.rejects(scoped.set(P1, 1), (e) => e.status === 403);
  await assert.rejects(scoped.query('sess.red-xai-db-qs:Z[ID=*]'), (e) => e.status === 403);
  // key never appears in an error (including the transport-level one)
  const dead = new RedX({ url: 'http://127.0.0.1:1', key: host.adminKey, timeoutMs: 2000 });
  await assert.rejects(dead.stats(), (e) => e.status === 0 && !e.message.includes(host.adminKey));
});

test('rate limit: 429 with retryAfter', async () => {
  const slow = new RedX({ url: host.url, key: (await admin.createKey({ db: 'default', scope: 'read', rateRps: 1, rateBurst: 1 })).key });
  let hit = null;
  for (let i = 0; i < 10 && !hit; i++) {
    try { await slow.stats(); } catch (e) { hit = e; }
  }
  assert.ok(hit, 'expected a 429');
  assert.equal(hit.status, 429);
  assert.ok(hit.rateLimited);
  assert.ok(hit.retryAfter >= 0);
});

test('databases and withDb', async () => {
  const d = await admin.databases();
  assert.ok(JSON.stringify(d).includes('default'));
  assert.equal(rw.withDb('default').db, 'default');
  // a key bound to "default" must not reach another db
  await assert.rejects(rw.withDb('other').get(P1), (e) => e.status === 403 || e.status === 404);
});

test('subscribe: set, delete, expire events; clean close', async () => {
  const events = [];
  const errors = [];
  const sub = await ro.subscribe('subs.red-xai-db-qs:*', (e) => events.push(e), { onError: (e) => errors.push(e) });
  const a = 'subs.red-xai-db-qs:S[ID=1].V';
  await rw.set(a, 'one', { ttl: 60 });
  await waitFor(() => events.some((e) => e.op === 'set' && e.path === a));
  const ev = events.find((e) => e.op === 'set');
  assert.equal(ev.value, '"one"');
  assert.equal(ev.data, 'one');
  assert.ok(ev.at);
  await rw.delete(a);
  await waitFor(() => events.some((e) => e.op === 'delete' && e.path === a));
  await rw.set('subs.red-xai-db-qs:S[ID=2].V', 'short', { ttl: 1 });
  await waitFor(() => events.some((e) => e.op === 'expire'), 8000);
  // events outside the prefix are not delivered
  const n = events.length;
  await rw.set(P1, { Name: 'elsewhere' });
  await sleep(300);
  assert.equal(events.length, n);
  sub.ping();
  const closed = sub.closed;
  sub.close();
  const c = await closed;
  assert.equal(c.code, 1000);
  assert.deepEqual(errors, []);
});

test('subscribe: refused with bad key; handler errors reach onError', async () => {
  const bad = new RedX({ url: host.url, key: 'rx_live_' + 'B'.repeat(43) });
  await assert.rejects(bad.subscribe('x', () => {}), (e) => e instanceof RedXError && e.status === 401 && !e.message.includes('BBBB'));
  const errors = [];
  const sub = await ro.subscribe('subs2.red-xai-db-qs:*', () => { throw new Error('boom'); }, { onError: (e) => errors.push(e.message) });
  await rw.set('subs2.red-xai-db-qs:S[ID=1].V', 'x', { ttl: 60 });
  await waitFor(() => errors.includes('boom'));
  sub.close();
  await sub.closed;
});

test('subscribe: a bare file name is an error, not a silent close', async () => {
  await assert.rejects(rw.subscribe('players', () => {}), (e) => e instanceof RedXError && e.status === 400);
  await assert.rejects(rw.subscribe('players.red-xai.db', () => {}), (e) => e instanceof RedXError && e.status === 400);
});

test('subscribe: server shutdown surfaces as close', async () => {
  // a second, disposable host so killing it does not disturb the other tests
  const h2 = await startHost();
  const a2 = new RedX({ url: h2.url, key: h2.adminKey });
  let closed = null;
  await a2.subscribe('x.red-xai.db:*', () => {}, { onClose: (c) => { closed = c; }, onError: () => {} });
  await h2.stop();
  await waitFor(() => closed !== null);
  assert.ok(closed.code === 1006 || closed.code === 1001);
});
