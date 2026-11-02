import { test } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import { createWebsiteGateway, localOrigin } from './website-gateway.mjs';

async function listening(server) { server.listen(0, '127.0.0.1'); await once(server, 'listening'); return `http://127.0.0.1:${server.address().port}`; }
async function close(server) { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }

test('keeps accounts, Store and News on the original backend and isolates the Host mount', async () => {
  const legacy = http.createServer((req, res) => { res.setHeader('set-cookie', ['legacy=a; HttpOnly', 'preference=b']); res.end('legacy:' + req.url); });
  const host = http.createServer((req, res) => { res.statusCode = req.headers.cookie === 'synthetic_session=valid' ? 200 : 401; res.end('host:' + req.url); });
  const legacyOrigin = await listening(legacy), hostOrigin = await listening(host);
  const gateway = createWebsiteGateway({ legacyOrigin, hostOrigin, allowedHosts: ['red-xai.test'] });
  const origin = await listening(gateway);
  try {
    for (const path of ['/', '/api/accounts/me', '/store', '/news', '/settings', '/hostile']) {
      const response = await fetch(origin + path, { headers: { host: 'red-xai.test' } });
      assert.equal(await response.text(), 'legacy:' + path);
      assert.equal(response.headers.getSetCookie().length, 2);
    }
    const denied = await fetch(origin + '/host/api/rpc/projects/list', { headers: { host: 'red-xai.test' } });
    assert.equal(denied.status, 401);
    const allowed = await fetch(origin + '/host/api/rpc/projects/list', { headers: { host: 'red-xai.test', cookie: 'synthetic_session=valid' } });
    assert.equal(allowed.status, 200);
    assert.equal(await allowed.text(), 'host:/host/api/rpc/projects/list');
    const wrongHost = await fetch(origin + '/host', { headers: { host: 'untrusted.test' } });
    assert.equal(wrongHost.status, 400);
  } finally { await Promise.all([close(gateway), close(host), close(legacy)]); }
});

test('preserves streamed request bodies and does not turn caller headers into identity', async () => {
  const backend = http.createServer(async (req, res) => { let body = ''; for await (const chunk of req) body += chunk; res.end(JSON.stringify({ body, cookie: req.headers.cookie, origin: req.headers.origin })); });
  const backendOrigin = await listening(backend);
  const gateway = createWebsiteGateway({ legacyOrigin: backendOrigin, hostOrigin: backendOrigin, allowedHosts: ['red-xai.test'] });
  const origin = await listening(gateway);
  try {
    const response = await fetch(origin + '/host/api/accounts/login', { method: 'POST', headers: { host: 'red-xai.test', origin: 'https://red-xai.test', cookie: 'synthetic_session=valid', 'content-type': 'application/json' }, body: '{"synthetic":true}' });
    assert.deepEqual(await response.json(), { body: '{"synthetic":true}', cookie: 'synthetic_session=valid', origin: 'https://red-xai.test' });
  } finally { await Promise.all([close(gateway), close(backend)]); }
});

test('rejects public, credential-bearing and non-HTTP backend origins', () => {
  for (const value of ['https://127.0.0.1:4200', 'http://example.com', 'http://user:password@127.0.0.1', 'http://127.0.0.1/path', 'http://127.0.0.1?key=value']) assert.throws(() => localOrigin(value));
  assert.equal(localOrigin('http://127.0.0.1:4200').port, '4200');
});

test('preserves authenticated upgrade streams', async () => {
  const sockets = new Set();
  const backend = http.createServer();
  backend.on('upgrade', (request, socket, head) => {
    sockets.add(socket);
    socket.on('close', () => sockets.delete(socket));
    assert.equal(request.headers.cookie, 'synthetic_session=valid');
    socket.write('HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: synthetic-stream\r\n\r\n');
    if (head.length) socket.write(head);
    socket.on('data', data => socket.write(data));
  });
  const backendOrigin = await listening(backend);
  const gateway = createWebsiteGateway({ legacyOrigin: backendOrigin, hostOrigin: backendOrigin, allowedHosts: ['red-xai.test'] });
  const origin = await listening(gateway);
  let client, timer;
  try {
    const upgraded = await new Promise((resolve, reject) => {
      const request = http.request(origin + '/host/live', { headers: { host: 'red-xai.test', connection: 'Upgrade', upgrade: 'synthetic-stream', cookie: 'synthetic_session=valid' } });
      request.on('upgrade', (response, socket, head) => resolve({ response, socket, head }));
      request.on('response', response => { response.resume(); reject(new Error('Upgrade rejected')); });
      request.on('error', reject);
      timer = setTimeout(() => { request.destroy(); reject(new Error('Upgrade timed out')); }, 5000);
      request.end();
    });
    clearTimeout(timer);
    client = upgraded.socket;
    assert.equal(upgraded.response.statusCode, 101);
    const echoed = new Promise((resolve, reject) => {
      client.once('data', data => resolve(data.toString()));
      client.once('error', reject);
      timer = setTimeout(() => reject(new Error('Stream timed out')), 5000);
    });
    client.write('synthetic-frame');
    assert.equal(await echoed, 'synthetic-frame');
  } finally {
    clearTimeout(timer);
    client?.destroy();
    for (const socket of sockets) socket.destroy();
    await Promise.all([close(gateway), close(backend)]);
  }
});
