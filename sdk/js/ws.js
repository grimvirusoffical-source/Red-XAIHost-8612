'use strict';
// Minimal RFC 6455 client over Node's http/https upgrade. No dependencies.
// Only what the RedX subscribe endpoint needs: text messages (with
// continuation), ping/pong, close. Client frames are masked as required.

const http = require('node:http');
const https = require('node:https');
const crypto = require('node:crypto');
const { EventEmitter } = require('node:events');

const GUID = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';
const MAX_MESSAGE = 16 * 1024 * 1024;

function frame(opcode, payload) {
  const len = payload.length;
  let head;
  if (len < 126) head = Buffer.from([0x80 | opcode, 0x80 | len]);
  else if (len < 65536) { head = Buffer.alloc(4); head[0] = 0x80 | opcode; head[1] = 0x80 | 126; head.writeUInt16BE(len, 2); }
  else { head = Buffer.alloc(10); head[0] = 0x80 | opcode; head[1] = 0x80 | 127; head.writeBigUInt64BE(BigInt(len), 2); }
  const mask = crypto.randomBytes(4);
  const body = Buffer.alloc(len);
  for (let i = 0; i < len; i++) body[i] = payload[i] ^ mask[i & 3];
  return Buffer.concat([head, mask, body]);
}

class WebSocket extends EventEmitter {
  // events: 'message' (string), 'ping', 'close' ({code, reason}), 'error' (Error)
  constructor(socket) {
    super();
    this.socket = socket;
    this.buf = Buffer.alloc(0);
    this.frag = null; // { chunks, size }
    this.closed = false;
    this.closeSent = false;
    socket.on('data', (d) => this._onData(d));
    socket.on('error', (e) => this._finish(1006, e.message, e));
    socket.on('close', () => this._finish(1006, 'connection closed'));
    socket.setNoDelay(true);
  }

  _finish(code, reason, err) {
    if (this.closed) return;
    this.closed = true;
    clearTimeout(this._killTimer);
    if (err) this.emit('error', err);
    this.emit('close', { code, reason });
    this.socket.destroy();
  }

  _protocolError(msg) {
    this._sendClose(1002, msg);
    this._finish(1002, msg, new Error('websocket protocol error: ' + msg));
  }

  _onData(d) {
    this.buf = this.buf.length ? Buffer.concat([this.buf, d]) : d;
    for (;;) {
      if (this.closed) return;
      const b = this.buf;
      if (b.length < 2) return;
      const fin = (b[0] & 0x80) !== 0;
      if (b[0] & 0x70) return this._protocolError('reserved bits set');
      const op = b[0] & 0x0f;
      if (b[1] & 0x80) return this._protocolError('server frame is masked');
      let len = b[1] & 0x7f;
      let off = 2;
      if (len === 126) { if (b.length < 4) return; len = b.readUInt16BE(2); off = 4; }
      else if (len === 127) {
        if (b.length < 10) return;
        const big = b.readBigUInt64BE(2);
        if (big > BigInt(MAX_MESSAGE)) return this._protocolError('frame too large');
        len = Number(big); off = 10;
      }
      if (len > MAX_MESSAGE) return this._protocolError('frame too large');
      if (b.length < off + len) return;
      const payload = b.subarray(off, off + len);
      this.buf = b.subarray(off + len);
      if (op >= 0x8) { // control frame
        if (!fin || len > 125) return this._protocolError('bad control frame');
        if (op === 0x9) { this.emit('ping', payload); this._send(0xA, Buffer.from(payload)); }
        else if (op === 0xA) { /* pong: ignore */ }
        else if (op === 0x8) {
          const code = len >= 2 ? payload.readUInt16BE(0) : 1005;
          const reason = len > 2 ? payload.subarray(2).toString('utf8') : '';
          if (!this.closeSent) this._sendClose(code === 1005 ? 1000 : code, '');
          return this._finish(code, reason);
        } else return this._protocolError('unknown control opcode');
        continue;
      }
      if (op === 0x0) {
        if (!this.frag) return this._protocolError('unexpected continuation');
      } else if (op === 0x1 || op === 0x2) {
        if (this.frag) return this._protocolError('new message inside a fragmented one');
        this.frag = { chunks: [], size: 0, binary: op === 0x2 };
      } else return this._protocolError('unknown opcode');
      this.frag.size += len;
      if (this.frag.size > MAX_MESSAGE) return this._protocolError('message too large');
      this.frag.chunks.push(Buffer.from(payload));
      if (fin) {
        const { chunks, binary } = this.frag;
        this.frag = null;
        const whole = Buffer.concat(chunks);
        if (!binary) this.emit('message', whole.toString('utf8'));
      }
    }
  }

  _send(op, payload) {
    if (this.closed || this.socket.destroyed) return;
    this.socket.write(frame(op, payload));
  }

  _sendClose(code, reason) {
    if (this.closeSent) return;
    this.closeSent = true;
    const r = Buffer.from(reason || '', 'utf8').subarray(0, 120);
    const p = Buffer.alloc(2 + r.length);
    p.writeUInt16BE(code, 0);
    r.copy(p, 2);
    this._send(0x8, p);
  }

  ping(data = Buffer.alloc(0)) { this._send(0x9, Buffer.from(data)); }

  /** Start a clean close; the socket is destroyed if the peer does not answer within 2s. */
  close(code = 1000, reason = '') {
    if (this.closed) return;
    this._sendClose(code, reason);
    this._killTimer = setTimeout(() => this._finish(code, reason), 2000);
    this._killTimer.unref();
  }
}

/**
 * Open a WebSocket. Resolves with a WebSocket once the 101 handshake is
 * verified; rejects with { status, body } if the server refused the upgrade.
 */
function connect(urlString, headers = {}, { timeoutMs = 30000 } = {}) {
  return new Promise((resolve, reject) => {
    const u = new URL(urlString);
    const lib = u.protocol === 'https:' || u.protocol === 'wss:' ? https : http;
    const key = crypto.randomBytes(16).toString('base64');
    const req = lib.request({
      protocol: u.protocol === 'wss:' ? 'https:' : u.protocol === 'ws:' ? 'http:' : u.protocol,
      hostname: u.hostname,
      port: u.port || undefined,
      path: u.pathname + u.search,
      method: 'GET',
      agent: false,
      timeout: timeoutMs,
      headers: {
        ...headers,
        Connection: 'Upgrade',
        Upgrade: 'websocket',
        'Sec-WebSocket-Key': key,
        'Sec-WebSocket-Version': '13',
      },
    });
    const fail = (e) => { req.destroy(); reject(e); };
    req.on('timeout', () => fail(new Error('websocket handshake timed out')));
    req.on('error', reject);
    req.on('response', (res) => { // not upgraded: collect a small body for the error
      const chunks = []; let n = 0;
      res.on('data', (c) => { n += c.length; if (n < 8192) chunks.push(c); });
      res.on('end', () => {
        const e = new Error(`websocket upgrade refused: HTTP ${res.statusCode}`);
        e.status = res.statusCode;
        e.body = Buffer.concat(chunks).toString('utf8');
        e.headers = res.headers;
        reject(e);
      });
    });
    req.on('upgrade', (res, socket, head) => {
      const want = crypto.createHash('sha1').update(key + GUID).digest('base64');
      if (res.headers['sec-websocket-accept'] !== want) {
        socket.destroy();
        return reject(new Error('websocket handshake: bad Sec-WebSocket-Accept'));
      }
      req.setTimeout(0);
      socket.setTimeout(0);
      const ws = new WebSocket(socket);
      resolve(ws);
      // bytes that arrived with the 101 response: deliver after the caller had a chance to attach listeners
      if (head && head.length) setImmediate(() => ws._onData(head));
    });
    req.end();
  });
}

module.exports = { connect, WebSocket, frame };
