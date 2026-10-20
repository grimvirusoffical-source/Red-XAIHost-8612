'use strict';
// WebAssembly backend. Same function names and results as the native addon.
//   createWasm({ threads })  threads: 'auto' (default), true, or false
// 'auto' picks the threaded build only when SharedArrayBuffer and Worker threads exist
// and RSX_WASM_THREADS is not "0"; otherwise the single-threaded build (Argon2id lanes
// run one after another, same output, same parameters).
const path = require('path');
const fs = require('fs');

function threadsAvailable() {
  if (process.env.RSX_WASM_THREADS === '0') return false;
  if (typeof SharedArrayBuffer === 'undefined') return false;
  try { require('worker_threads'); } catch (_) { return false; }
  return !process.versions.bun;   // Bun: the Emscripten pthread runtime is not supported; use single-threaded
}

function wasmDir() { return process.env.RSX_WASM_DIR || path.join(__dirname, 'wasm', 'dist'); }

class RsxError extends Error {
  constructor(op, rc) {
    const m = rc === -1 ? 'Wrong password or the data was changed' : rc === -2 ? 'Not a Red-SecretXAI blob' : 'operation failed';
    super(`${op}: ${m} (${rc})`); this.code = rc;
  }
}

async function createWasm(opts = {}) {
  const want = opts.threads === undefined ? 'auto' : opts.threads;
  const threads = want === 'auto' ? threadsAvailable() : !!want;
  const file = path.join(wasmDir(), threads ? 'rsx-mt.js' : 'rsx-st.js');
  if (!fs.existsSync(file)) throw new Error('WASM build missing: ' + file + ' (run wasm/build.sh)');
  const M = await require(file)();
  const U8 = () => M.HEAPU8;          // re-read: the heap view changes when memory grows
  const toBuf = (v, what, req = true) => {
    if (v === null || v === undefined) {
      if (req) throw new TypeError(what + ' must be a Buffer, not null');
      return Buffer.alloc(0);
    }
    if (!(v instanceof Uint8Array)) throw new TypeError(what + ': expected a Buffer or Uint8Array');
    return v;
  };
  const need = (b, n, what) => { if (b.length !== n) throw new RangeError(`${what} must be ${n} bytes`); };
  // scratch allocator: every call frees what it allocated (and wipes it)
  function withMem(fn) {
    const ptrs = [];
    const put = (b) => { const p = M._malloc(Math.max(b.length, 1)); U8().set(b, p); ptrs.push([p, Math.max(b.length, 1)]); return p; };
    const alloc = (n) => { const p = M._malloc(Math.max(n, 1)); U8().fill(0, p, p + Math.max(n, 1)); ptrs.push([p, Math.max(n, 1)]); return p; };
    const get = (p, n) => Buffer.from(U8().slice(p, p + n));
    const u32 = (p) => M.HEAPU32[p >>> 2];
    try { return fn({ put, alloc, get, u32 }); }
    finally { for (const [p, n] of ptrs) { U8().fill(0, p, p + n); M._free(p); } }
  }
  const pw = (v) => typeof v === 'string' ? Buffer.from(v, 'utf8') : toBuf(v, 'password');
  const digits = (d) => { d = d === undefined ? 6 : d; if (!(d >= 6 && d <= 8)) throw new RangeError('digits must be 6, 7 or 8'); return d; };
  const big = (v, d) => BigInt(v === undefined ? d : v);
  const blobOut = (fn, op) => withMem(({ alloc, get, u32 }) => {
    const pp = alloc(4), pl = alloc(4);
    const rc = fn(pp, pl);
    if (rc !== 0) return { rc };
    const out = get(u32(pp), u32(pl)); M._rsx_free(u32(pp));
    return { rc, out };
  });

  const api = {
    backend: threads ? 'wasm-mt' : 'wasm-st',
    abiVersion: () => M._rsx_abi_version(),
    sha256: (m) => withMem(({ put, alloc, get }) => { m = toBuf(m, 'msg', false); const o = alloc(32); M._rsx_sha256(put(m), m.length, o); return get(o, 32); }),
    hmac: (k, m) => withMem(({ put, alloc, get }) => { k = toBuf(k, 'key', false); m = toBuf(m, 'msg', false); const o = alloc(32); M._rsx_hmac_sha256(put(k), k.length, put(m), m.length, o); return get(o, 32); }),
    hkdf: (ikm, salt, info, L) => withMem(({ put, alloc, get }) => {
      ikm = toBuf(ikm, 'ikm', false); salt = toBuf(salt, 'salt', false); info = toBuf(info, 'info', false);
      const o = alloc(L); const rc = M._rsx_hkdf_sha256(put(ikm), ikm.length, put(salt), salt.length, put(info), info.length, o, L);
      if (rc) throw new RsxError('hkdf', rc); return get(o, L);
    }),
    aeadEncrypt: (key, nonce, aad, pt) => withMem(({ put, alloc, get }) => {
      key = toBuf(key, 'key'); nonce = toBuf(nonce, 'nonce'); aad = toBuf(aad, 'aad', false); pt = toBuf(pt, 'plaintext', false);
      need(key, 32, 'key'); need(nonce, 12, 'nonce');
      const ct = alloc(pt.length), tag = alloc(16);
      const rc = M._rsx_aead_encrypt(put(key), put(nonce), put(aad), aad.length, put(pt), pt.length, ct, tag);
      if (rc) throw new RsxError('aeadEncrypt', rc); return { ct: get(ct, pt.length), tag: get(tag, 16) };
    }),
    aeadDecrypt: (key, nonce, aad, ct, tag) => withMem(({ put, alloc, get }) => {
      key = toBuf(key, 'key'); nonce = toBuf(nonce, 'nonce'); aad = toBuf(aad, 'aad', false); ct = toBuf(ct, 'ct'); tag = toBuf(tag, 'tag');
      need(key, 32, 'key'); need(nonce, 12, 'nonce'); need(tag, 16, 'tag');
      const pt = alloc(ct.length);
      const rc = M._rsx_aead_decrypt(put(key), put(nonce), put(aad), aad.length, put(ct), ct.length, put(tag), pt);
      if (rc) throw new RsxError('aeadDecrypt', rc); return get(pt, ct.length);
    }),
    argon2Ext: (p, salt, secret, ad, t, m, lanes, outLen) => withMem(({ put, alloc, get }) => {
      p = toBuf(p, 'password', false); salt = toBuf(salt, 'salt', false); secret = toBuf(secret, 'secret', false); ad = toBuf(ad, 'ad', false);
      const o = alloc(outLen);
      const rc = M._rsx_argon2id_ext(put(p), p.length, put(salt), salt.length, put(secret), secret.length, put(ad), ad.length, t, m, lanes, o, outLen);
      if (rc) throw new RsxError('argon2', rc); return get(o, outLen);
    }),
    argon2: (p, salt, t, m, lanes) => withMem(({ put, alloc, get }) => {
      p = toBuf(p, 'password', false); salt = toBuf(salt, 'salt', false); const o = alloc(32);
      const rc = M._rsx_argon2id(put(p), p.length, put(salt), salt.length, t, m, lanes, o, 32);
      return { rc, out: get(o, 32) };
    }),
    ctEq: (a, b) => withMem(({ put }) => {
      a = toBuf(a, 'a'); b = toBuf(b, 'b'); if (a.length !== b.length) return false;
      return a.length === 0 || M._rsx_ct_eq(put(a), put(b), a.length) === 1;
    }),
    blake2b: (m, k, n = 64) => withMem(({ put, alloc, get }) => {
      m = toBuf(m, 'msg'); k = toBuf(k, 'key', false); const o = alloc(n);
      const rc = M._rsx_blake2b(put(m), m.length, k.length ? put(k) : 0, k.length, o, n);
      if (rc) throw new RsxError('blake2b', rc); return get(o, n);
    }),
    pbkdf2: (p, salt, iters, n) => withMem(({ put, alloc, get }) => {
      p = toBuf(p, 'password', false); salt = toBuf(salt, 'salt', false); const o = alloc(n);
      const rc = M._rsx_pbkdf2_sha256(put(p), p.length, put(salt), salt.length, iters, o, n);
      if (rc) throw new RsxError('pbkdf2', rc); return get(o, n);
    }),
    random: (n) => withMem(({ alloc, get }) => { const o = alloc(n); const rc = M._rsx_random(o, n); if (rc) throw new RsxError('random', rc); return get(o, n); }),
    open1: (p, blob) => { p = pw(p); blob = toBuf(blob, 'blob'); return withMem(({ put }) => { const a = put(p), b = put(blob); return blobOut((pp, pl) => M._rsx_open1(a, p.length, b, blob.length, pp, pl)); }); },
    open2: (p, sk, blob) => {
      p = pw(p); sk = toBuf(sk, 'serverKey'); blob = toBuf(blob, 'blob'); need(sk, 32, 'serverKey');
      return withMem(({ put }) => { const a = put(p), k = put(sk), b = put(blob); return blobOut((pp, pl) => M._rsx_open2(a, p.length, k, b, blob.length, pp, pl)); });
    },
    // ABI 2.13
    sha512: (m) => withMem(({ put, alloc, get }) => { m = toBuf(m, 'msg'); const o = alloc(64); M._rsx_sha512(put(m), m.length, o); return get(o, 64); }),
    sha1: (m) => withMem(({ put, alloc, get }) => { m = toBuf(m, 'msg'); const o = alloc(20); M._rsx_sha1(put(m), m.length, o); return get(o, 20); }),
    hmacSha1: (k, m) => withMem(({ put, alloc, get }) => { k = toBuf(k, 'key'); m = toBuf(m, 'msg'); const o = alloc(20); M._rsx_hmac_sha1(put(k), k.length, put(m), m.length, o); return get(o, 20); }),
    ed25519SeedKeypair: (seed) => withMem(({ put, alloc, get }) => {
      seed = toBuf(seed, 'seed'); need(seed, 32, 'seed'); const pub = alloc(32), priv = alloc(64);
      const rc = M._rsx_ed25519_seed_keypair(put(seed), pub, priv); if (rc) throw new RsxError('ed25519SeedKeypair', rc);
      return { publicKey: get(pub, 32), privateKey: get(priv, 64) };
    }),
    ed25519Keypair: () => withMem(({ alloc, get }) => {
      const pub = alloc(32), priv = alloc(64); const rc = M._rsx_ed25519_keypair(pub, priv);
      if (rc) throw new RsxError('ed25519Keypair', rc); return { publicKey: get(pub, 32), privateKey: get(priv, 64) };
    }),
    ed25519Sign: (k, m) => withMem(({ put, alloc, get }) => {
      k = toBuf(k, 'privateKey'); need(k, 64, 'privateKey'); m = toBuf(m, 'msg'); const sig = alloc(64);
      const rc = M._rsx_ed25519_sign(put(k), put(m), m.length, sig); if (rc) throw new RsxError('ed25519Sign', rc); return get(sig, 64);
    }),
    ed25519Verify: (pk, m, sig) => withMem(({ put }) => {
      pk = toBuf(pk, 'publicKey'); need(pk, 32, 'publicKey'); m = toBuf(m, 'msg'); sig = toBuf(sig, 'signature'); need(sig, 64, 'signature');
      const rc = M._rsx_ed25519_verify(put(pk), put(m), m.length, put(sig));
      if (rc === -1) return false; if (rc) throw new RsxError('ed25519Verify', rc); return true;
    }),
    hotp: (k, counter, d) => withMem(({ put, alloc, u32 }) => {
      k = toBuf(k, 'key'); d = digits(d); const c = alloc(4);
      const rc = M._rsx_hotp(put(k), k.length, big(counter, 0), d, c); if (rc) throw new RsxError('hotp', rc); return u32(c);
    }),
    totp: (k, t, d, step, t0) => withMem(({ put, alloc, u32 }) => {
      k = toBuf(k, 'key'); d = digits(d); const c = alloc(4);
      const rc = M._rsx_totp(put(k), k.length, big(t, 0), big(t0, 0), big(step, 30), d, c); if (rc) throw new RsxError('totp', rc); return u32(c);
    }),
    totpVerify: (k, code, t, d, step, t0, skew) => withMem(({ put }) => {
      k = toBuf(k, 'key'); d = digits(d);
      const rc = M._rsx_totp_verify(put(k), k.length, code >>> 0, big(t, 0), big(t0, 0), big(step, 30), d, skew === undefined ? 1 : skew);
      if (rc === -1) return false; if (rc) throw new RsxError('totpVerify', rc); return true;
    }),
    phcEncode: (t, m, p, salt, hash) => withMem(({ put, alloc, get, u32 }) => {
      salt = toBuf(salt, 'salt'); hash = toBuf(hash, 'hash'); const o = alloc(512), n = alloc(4);
      const rc = M._rsx_phc_encode(t, m, p, put(salt), salt.length, put(hash), hash.length, o, 512, n);
      if (rc) throw new RsxError('phcEncode', rc); return get(o, u32(n)).toString('latin1');
    }),
    phcParse: (s) => withMem(({ put, alloc, get, u32 }) => {
      if (typeof s !== 'string') throw new TypeError('phc must be a string');
      const b = Buffer.from(s, 'latin1'); const t = alloc(4), m = alloc(4), p = alloc(4), sa = alloc(256), sl = alloc(4), h = alloc(256), hl = alloc(4);
      const rc = M._rsx_phc_parse(put(b), b.length, t, m, p, sa, 256, sl, h, 256, hl);
      if (rc) throw new RsxError('phcParse', rc);
      return { t: u32(t), mKib: u32(m), p: u32(p), salt: get(sa, u32(sl)), hash: get(h, u32(hl)) };
    }),
    phcHashPassword: (password, t = 3, m = 65536, p = 1) => withMem(({ put, alloc, get, u32 }) => {
      const b = pw(password); const o = alloc(512), n = alloc(4);
      const rc = M._rsx_phc_hash_password(put(b), b.length, t, m, p, o, 512, n);
      if (rc) throw new RsxError('phcHashPassword', rc); return get(o, u32(n)).toString('latin1');
    }),
    phcVerifyPassword: (password, s) => withMem(({ put }) => {
      if (typeof s !== 'string') throw new TypeError('phc must be a string');
      const b = pw(password), sb = Buffer.from(s, 'latin1');
      const rc = M._rsx_phc_verify_password(put(b), b.length, put(sb), sb.length);
      if (rc === -1) return false; if (rc) throw new RsxError('phcVerifyPassword', rc); return true;
    }),
  };

  // RSX3 sessions (keymap file on the real filesystem)
  api.Session = class Session {
    constructor(password, keymapPath) {
      const p = pw(password);
      this._s = withMem(({ put, alloc }) => {
        const err = alloc(4); const s = M._rsx_session_open(put(p), p.length, put(Buffer.from(keymapPath + '\0', 'utf8')), err);
        if (!s) throw new RsxError('session open', M.HEAP32[err >>> 2]); return s;
      });
    }
    _live() { if (!this._s) throw new Error('session is closed'); return this._s; }
    seal3(aad, pt) {
      aad = toBuf(aad, 'aad', false); pt = toBuf(pt, 'plaintext'); const s = this._live();
      const r = withMem(({ put }) => { const a = put(aad), b = put(pt); return blobOut((pp, pl) => M._rsx_seal3(s, a, aad.length, b, pt.length, pp, pl)); });
      if (r.rc) throw new RsxError('seal3', r.rc); return r.out;
    }
    open3(aad, blob) {
      aad = toBuf(aad, 'aad', false); blob = toBuf(blob, 'blob'); const s = this._live();
      const r = withMem(({ put }) => { const a = put(aad), b = put(blob); return blobOut((pp, pl) => M._rsx_open3(s, a, aad.length, b, blob.length, pp, pl)); });
      if (r.rc) throw new RsxError('open3', r.rc); return r.out;
    }
    close() { if (this._s) { M._rsx_session_close(this._s); this._s = 0; } }
  };
  return api;
}

module.exports = { createWasm, threadsAvailable };
