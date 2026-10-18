'use strict';
// RFC 6455 framing tests against a scripted raw-socket server (no redxd needed).
const test = require('node:test');
const assert = require('node:assert/strict');
const net = require('node:net');
const crypto = require('node:crypto');
const { connect } = require('../ws');

const GUID = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';

function sf(op, payload, { fin = true } = {}) { // unmasked server frame
  const p = Buffer.from(payload);
  let h;
  if (p.length < 126) h = Buffer.from([(fin ? 0x80 : 0) | op, p.length]);
  else if (p.length < 65536) { h = Buffer.alloc(4); h[0] = (fin ? 0x80 : 0) | op; h[1] = 126; h.writeUInt16BE(p.length, 2); }
  else { h = Buffer.alloc(10); h[0] = (fin ? 0x80 : 0) | op; h[1] = 127; h.writeBigUInt64BE(BigInt(p.length), 2); }
  return Buffer.concat([h, p]);
}

function scriptedServer(onUpgraded, { badAccept = false } = {}) {
  return new Promise((resolve) => {
    const received = [];
    const srv = net.createServer((sock) => {
      let hs = '';
      let upgraded = false;
      let rest = Buffer.alloc(0);
      sock.on('data', (d) => {
        if (!upgraded) {
          hs += d.toString('latin1');
          const end = hs.indexOf('\r\n\r\n');
          if (end < 0) return;
          const key = /sec-websocket-key: (.+)\r\n/i.exec(hs)[1].trim();
          assert.match(hs, /authorization: Bearer test-token/i);
          const acc = badAccept ? 'nope' : crypto.createHash('sha1').update(key + GUID).digest('base64');
          sock.write(`HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${acc}\r\n\r\n`);
          upgraded = true;
          onUpgraded(sock);
          return;
        }
        rest = Buffer.concat([rest, d]);
        while (rest.length >= 6) { // parse masked client frames
          let len = rest[1] & 0x7f; let off = 2;
          if (len === 126) { len = rest.readUInt16BE(2); off = 4; }
          if (rest.length < off + 4 + len) break;
          assert.ok(rest[1] & 0x80, 'client frames must be masked');
          const mask = rest.subarray(off, off + 4);
          const body = Buffer.from(rest.subarray(off + 4, off + 4 + len)).map((b, i) => b ^ mask[i & 3]);
          received.push({ op: rest[0] & 0xf, body });
          rest = rest.subarray(off + 4 + len);
        }
      });
      sock.on('error', () => {});
    });
    srv.listen(0, '127.0.0.1', () => resolve({ srv, port: srv.address().port, received }));
  });
}

const H = { Authorization: 'Bearer test-token' };

test('fragmented text, 16-bit and 64-bit lengths, ping answered with pong, split TCP reads', async () => {
  const big = 'x'.repeat(70000);
  const mid = 'y'.repeat(300);
  const { srv, port, received } = await scriptedServer((sock) => {
    const all = Buffer.concat([
      sf(0x1, 'hel', { fin: false }), sf(0x9, 'pp'), sf(0x0, 'lo'),
      sf(0x1, mid), sf(0x1, big), sf(0x1, 'é✓'), sf(0xA, 'x'),
    ]);
    for (let i = 0; i < all.length; i += 777) setTimeout(() => sock.write(all.subarray(i, i + 777)), (i / 777) | 0);
  });
  const ws = await connect(`ws://127.0.0.1:${port}/x`, H);
  const msgs = [];
  ws.on('message', (m) => msgs.push(m));
  const pings = [];
  ws.on('ping', (p) => pings.push(p.toString()));
  while (msgs.length < 4) await new Promise((r) => setTimeout(r, 10));
  assert.deepEqual(msgs, ['hello', mid, big, 'é✓']);
  assert.deepEqual(pings, ['pp']);
  assert.ok(received.some((f) => f.op === 0xA && f.body.toString() === 'pp'));
  ws.close();
  await new Promise((r) => ws.on('close', r));
  srv.close();
});

test('server close frame is echoed and reported', async () => {
  const { srv, port, received } = await scriptedServer((sock) => {
    const p = Buffer.alloc(2 + 4); p.writeUInt16BE(1001, 0); p.write('away', 2);
    sock.write(sf(0x8, p));
  });
  const ws = await connect(`ws://127.0.0.1:${port}/x`, H);
  const c = await new Promise((r) => ws.on('close', r));
  assert.deepEqual(c, { code: 1001, reason: 'away' });
  await new Promise((r) => setTimeout(r, 50));
  assert.ok(received.some((f) => f.op === 0x8));
  srv.close();
});

test('protocol violations close the connection', async () => {
  for (const bad of [Buffer.from([0x81 | 0x40, 0x00]), Buffer.from([0x81, 0x80, 0, 0, 0, 0]), sf(0x0, 'orphan'), sf(0x9, 'z'.repeat(200)), Buffer.from([0x03, 0x00])]) {
    const { srv, port } = await scriptedServer((sock) => sock.write(bad));
    const ws = await connect(`ws://127.0.0.1:${port}/x`, H);
    ws.on('error', () => {});
    const c = await new Promise((r) => ws.on('close', r));
    assert.equal(c.code, 1002);
    srv.close();
  }
});

test('oversized frame header is refused without buffering', async () => {
  const h = Buffer.alloc(10); h[0] = 0x82; h[1] = 127; h.writeBigUInt64BE(1n << 40n, 2);
  const { srv, port } = await scriptedServer((sock) => sock.write(h));
  const ws = await connect(`ws://127.0.0.1:${port}/x`, H);
  ws.on('error', () => {});
  const c = await new Promise((r) => ws.on('close', r));
  assert.equal(c.code, 1002);
  srv.close();
});

test('bad Sec-WebSocket-Accept is rejected', async () => {
  const { srv, port } = await scriptedServer(() => {}, { badAccept: true });
  await assert.rejects(connect(`ws://127.0.0.1:${port}/x`, H), /Sec-WebSocket-Accept/);
  srv.close();
});

test('non-101 response rejects with status', async () => {
  const srv = net.createServer((s) => { s.on('data', () => s.end('HTTP/1.1 401 Unauthorized\r\nContent-Type: application/json\r\nContent-Length: 18\r\n\r\n{"error":"denied"}\n')); });
  await new Promise((r) => srv.listen(0, '127.0.0.1', r));
  await assert.rejects(connect(`ws://127.0.0.1:${srv.address().port}/x`, H), (e) => e.status === 401 && /denied/.test(e.body));
  srv.close();
});
