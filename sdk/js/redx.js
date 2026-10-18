'use strict';
// RedX client for Node >= 18 (global fetch). No dependencies.
//
//   const { RedX } = require('./redx');
//   const db = new RedX({ url: 'http://127.0.0.1:7777', key: process.env.REDX_KEY });
//   await db.set('players.red-xai.db:Player[ID=1]', { Name: 'Ann', Level: 5 });
//   const p = await db.get('players.red-xai.db:Player[ID=1]'); // { Level: 5, Name: 'Ann' }
//
// The API key is held in a private field: it is never part of an error
// message, JSON.stringify(client) or util.inspect(client).

const util = require('node:util');
const wsClient = require('./ws');
const { fromBox, BoxTextError } = require('./boxtext');

const DB_NAME = /^[a-z0-9][a-z0-9_-]{0,62}$/;
const KEY_SHAPE = /rx_live_[A-Za-z0-9]+/g;

class RedXError extends Error {
  /** @param {number} status HTTP status (0 = network/timeout/client-side error) */
  constructor(status, message, { retryAfter = null, body = null } = {}) {
    super(message);
    this.name = 'RedXError';
    this.status = status;
    this.retryAfter = retryAfter; // seconds, for 429
    this.body = body;
  }
  get unauthorized() { return this.status === 401; }
  get forbidden() { return this.status === 403; }
  get notFound() { return this.status === 404; }
  get rateLimited() { return this.status === 429; }
}

function scrub(s) { return String(s).replace(KEY_SHAPE, 'rx_live_[redacted]'); }

/** Escape a RedX path for use inside a URL path ([ ] : = etc. are percent-encoded). */
function escapePath(p) {
  if (typeof p !== 'string' || p === '') throw new TypeError('path must be a non-empty string');
  return encodeURIComponent(p);
}

class RedX {
  #key;
  /**
   * @param {object} o
   * @param {string} o.url      base URL of redxd, e.g. http://127.0.0.1:7777
   * @param {string} o.key      API key (rx_live_...)
   * @param {string} [o.db]     database name; omit for the host's default db
   * @param {number} [o.timeoutMs=30000]
   */
  constructor({ url, key, db, timeoutMs = 30000 } = {}) {
    if (!url) throw new TypeError('url is required');
    if (!key || typeof key !== 'string') throw new TypeError('key is required');
    if (db !== undefined && !DB_NAME.test(db)) throw new TypeError('invalid db name');
    const u = new URL(url);
    if (u.protocol !== 'http:' && u.protocol !== 'https:') throw new TypeError('url must be http(s)');
    this.baseUrl = u.origin;
    this.db = db;
    this.timeoutMs = timeoutMs;
    this.#key = key;
  }

  toJSON() { return { baseUrl: this.baseUrl, db: this.db }; }
  [util.inspect.custom]() { return `RedX { baseUrl: '${this.baseUrl}', db: ${JSON.stringify(this.db)} }`; }

  /** A client for another database using the same key. */
  withDb(db) { return new RedX({ url: this.baseUrl, key: this.#key, db, timeoutMs: this.timeoutMs }); }

  #dbPrefix() { return this.db ? `/v1/db/${this.db}` : '/v1'; }

  async #request(method, path, { body, headers = {}, query, accept } = {}) {
    let url = this.baseUrl + path;
    if (query) {
      const q = new URLSearchParams();
      for (const [k, v] of Object.entries(query)) if (v !== undefined && v !== null) q.set(k, String(v));
      const s = q.toString();
      if (s) url += '?' + s;
    }
    const h = { Authorization: `Bearer ${this.#key}`, ...headers };
    if (accept) h.Accept = accept;
    let res;
    try {
      res = await fetch(url, { method, headers: h, body, signal: AbortSignal.timeout(this.timeoutMs), redirect: 'error' });
    } catch (e) {
      const timedOut = e && (e.name === 'TimeoutError' || e.name === 'AbortError');
      throw new RedXError(0, timedOut ? `request timed out after ${this.timeoutMs}ms` : scrub(`request failed: ${e && e.cause ? e.cause.message || e.cause : e.message}`));
    }
    const text = await res.text();
    if (!res.ok) {
      let msg = `HTTP ${res.status}`;
      let parsed = null;
      try { parsed = JSON.parse(text); if (parsed && parsed.error) msg = String(parsed.error); } catch { /* not JSON */ }
      const ra = res.headers.get('retry-after');
      throw new RedXError(res.status, scrub(msg), { retryAfter: ra ? Number(ra) : null, body: parsed });
    }
    return { text, res };
  }

  async #json(method, path, opts) {
    const { text } = await this.#request(method, path, opts);
    try { return JSON.parse(text); } catch { throw new RedXError(0, 'server sent a non-JSON response'); }
  }

  /** GET /healthz (needs no key). Resolves { status, node, role, version }. */
  async health() {
    let res;
    try { res = await fetch(this.baseUrl + '/healthz', { signal: AbortSignal.timeout(this.timeoutMs) }); }
    catch (e) { throw new RedXError(0, scrub(`request failed: ${e.message}`)); }
    if (!res.ok) throw new RedXError(res.status, `HTTP ${res.status}`);
    return res.json();
  }

  stats() { return this.#json('GET', '/v1/stats'); }

  /** Databases the key can see. Resolves the host's list response. */
  databases() { return this.#json('GET', '/v1/dbs'); }

  /** Raw Box text of the value at `path` (exactly what the host stores/prints). */
  async getBox(path) {
    const { text } = await this.#request('GET', `${this.#dbPrefix()}/path/${escapePath(path)}`, { accept: 'text/x-redx-box' });
    return text;
  }

  /** The value at `path` as a plain JS value (see boxtext.js for the mapping). Throws RedXError(404) if absent. */
  async get(path) {
    const j = await this.#json('GET', `${this.#dbPrefix()}/path/${escapePath(path)}`);
    if (typeof j.value !== 'string') throw new RedXError(0, 'unexpected response shape');
    try { return fromBox(j.value); }
    catch (e) { if (e instanceof BoxTextError) throw new RedXError(0, `cannot decode value: ${e.message}`); throw e; }
  }

  /** Like get(), but resolves `fallback` (default null) when the path does not exist. */
  async getOr(path, fallback = null) {
    try { return await this.get(path); } catch (e) { if (e instanceof RedXError && e.status === 404) return fallback; throw e; }
  }

  /**
   * Write a JSON-compatible value (objects become boxes, null becomes NELL).
   * @param {{ttl?: number, sliding?: boolean, checkpoint?: number}} [opts] seconds
   */
  set(path, value, opts = {}) {
    const body = { value };
    if (opts.ttl !== undefined) body.ttl_seconds = opts.ttl;
    if (opts.sliding !== undefined) body.sliding = !!opts.sliding;
    if (opts.checkpoint !== undefined) body.checkpoint_seconds = opts.checkpoint;
    return this.#json('PUT', `${this.#dbPrefix()}/path/${escapePath(path)}`, {
      body: JSON.stringify(body), headers: { 'Content-Type': 'application/json' },
    });
  }

  /** Write raw Box text (see GRAMMAR.md) at `path`. Same options as set(). */
  setBox(path, boxText, opts = {}) {
    return this.#json('PUT', `${this.#dbPrefix()}/path/${escapePath(path)}`, {
      body: boxText, headers: { 'Content-Type': 'text/x-redx-box' },
      query: { ttl: opts.ttl, sliding: opts.sliding === undefined ? undefined : !!opts.sliding, checkpoint: opts.checkpoint },
    });
  }

  /** Delete the value at `path`. */
  delete(path) { return this.#json('DELETE', `${this.#dbPrefix()}/path/${escapePath(path)}`); }

  /**
   * Run a RedX path query (POST /v1/query), e.g. `players.red-xai.db:Player[ID=*].Name`.
   * NOTE: this is the host's path-pattern query; use sql() for the SQL-like language (both stay).
   * Resolves { count, results: string[] (Box text), values: any[] (decoded) }.
   */
  async query(q) {
    if (typeof q !== 'string' || q === '') throw new TypeError('query must be a non-empty string');
    const j = await this.#json('POST', `${this.#dbPrefix()}/query`, {
      body: JSON.stringify({ q }), headers: { 'Content-Type': 'application/json' },
    });
    const results = Array.isArray(j.results) ? j.results : [];
    const values = results.map((r) => { try { return fromBox(r); } catch { return r; } });
    return { count: j.count, results, values };
  }

  /**
   * Run a RXQL statement (POST /v1/sql), e.g. `SELECT Name FROM players.Player WHERE Level >= ?`.
   * Values go in `params` (never concatenated into the text). Resolves the host's result object:
   * { kind, columns, rows, count, affected, paths, access, plan }. Rows hold decoded JSON values.
   */
  async sql(q, params = []) {
    if (typeof q !== 'string' || q === '') throw new TypeError('sql must be a non-empty string');
    if (!Array.isArray(params)) throw new TypeError('params must be an array');
    return this.#json('POST', `${this.#dbPrefix()}/sql`, {
      body: JSON.stringify({ q, params }), headers: { 'Content-Type': 'application/json' },
    });
  }

  /** Create an API key (needs an admin key). The returned `key` is shown once; store it. */
  createKey({ db, scope, prefix, label, ttl, rateRps, rateBurst } = {}) {
    const body = { db: db ?? this.db ?? 'default', scope };
    if (prefix !== undefined) body.prefix = prefix;
    if (label !== undefined) body.label = label;
    if (ttl !== undefined) body.ttl = ttl;
    if (rateRps !== undefined) body.rate_rps = rateRps;
    if (rateBurst !== undefined) body.rate_burst = rateBurst;
    return this.#json('POST', '/v1/keys', { body: JSON.stringify(body), headers: { 'Content-Type': 'application/json' } });
  }

  /** Revoke a key by id (admin). */
  revokeKey(id) { return this.#json('DELETE', `/v1/keys/${encodeURIComponent(id)}`); }

  /**
   * Subscribe to changes under `pathPrefix` (WebSocket, Node only).
   * handler receives { path, op: 'set'|'delete'|'expire', value, at, data }
   * where `value` is Box text and `data` its decoded form (undefined if it
   * could not be decoded). Resolves a Subscription once the upgrade succeeded;
   * rejects with RedXError (401/403/429...) if refused.
   * @param {(ev: object) => void} handler
   * @param {{onError?: (e: Error) => void, onClose?: (c: {code:number, reason:string}) => void}} [opts]
   */
  async subscribe(pathPrefix, handler, { onError, onClose } = {}) {
    if (typeof handler !== 'function') throw new TypeError('handler must be a function');
    const wsUrl = new URL(`${this.baseUrl}${this.#dbPrefix()}/subscribe`);
    wsUrl.searchParams.set('path', pathPrefix);
    let ws;
    try {
      ws = await wsClient.connect(wsUrl.toString(), { Authorization: `Bearer ${this.#key}` }, { timeoutMs: this.timeoutMs });
    } catch (e) {
      let msg = e.message, body = null;
      try { body = JSON.parse(e.body); if (body && body.error) msg = body.error; } catch { /* keep */ }
      const ra = e.headers && e.headers['retry-after'];
      throw new RedXError(e.status || 0, scrub(msg), { retryAfter: ra ? Number(ra) : null, body });
    }
    const closed = new Promise((resolve) => {
      ws.on('close', (c) => { if (onClose) onClose(c); resolve(c); });
    });
    ws.on('error', (e) => { if (onError) onError(e); });
    ws.on('message', (text) => {
      let ev;
      try { ev = JSON.parse(text); } catch (e) { if (onError) onError(new Error('bad event JSON')); return; }
      try { ev.data = typeof ev.value === 'string' && ev.op === 'set' ? fromBox(ev.value) : undefined; } catch { ev.data = undefined; }
      try { handler(ev); } catch (e) { if (onError) onError(e); }
    });
    return { close: () => ws.close(1000, 'bye'), closed, ping: () => ws.ping() };
  }
}

module.exports = { RedX, RedXError, BoxTextError, fromBox, escapePath };
