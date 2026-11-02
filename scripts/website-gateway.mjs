import http from 'node:http';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';

const hopHeaders = ['connection', 'keep-alive', 'proxy-authenticate', 'proxy-authorization', 'te', 'trailer', 'transfer-encoding', 'upgrade'];

export function localOrigin(value) {
  const url = new URL(value);
  if (url.protocol !== 'http:' || !['127.0.0.1', '[::1]'].includes(url.hostname) || url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
    throw new Error('Gateway backends must be explicit loopback HTTP origins.');
  }
  return url;
}

function forwardedHeaders(headers, upgrade = false) {
  const next = { ...headers };
  const connectionHeaders = String(headers.connection || '').split(',').map(x => x.trim().toLowerCase());
  for (const name of [...hopHeaders, ...connectionHeaders]) delete next[name];
  if (upgrade) {
    next.connection = 'Upgrade';
    next.upgrade = headers.upgrade;
  }
  return next;
}

export function createWebsiteGateway({ legacyOrigin, hostOrigin, mount = '/host', allowedHosts = ['red-xai.com', 'www.red-xai.com'] }) {
  const legacy = localOrigin(legacyOrigin), host = localOrigin(hostOrigin);
  if (!/^(?:\/[A-Za-z0-9_-]+)+$/.test(mount)) throw new Error('Invalid Host mount.');
  const allowed = new Set(allowedHosts.map(x => x.toLowerCase()));

  function target(request) {
    if (!request.url?.startsWith('/') || request.url.startsWith('//')) return null;
    const name = String(request.headers.host || '').toLowerCase();
    if (!allowed.has(name)) return null;
    const path = new URL(request.url, 'http://gateway.invalid').pathname;
    return path === mount || path.startsWith(mount + '/') ? host : legacy;
  }

  const server = http.createServer((request, response) => {
    const backend = target(request);
    if (!backend) { response.writeHead(400); response.end('Invalid request.'); return; }
    const upstream = http.request(backend, {
      method: request.method, path: request.url, headers: forwardedHeaders(request.headers),
    }, incoming => {
      response.writeHead(incoming.statusCode || 502, forwardedHeaders(incoming.headers));
      incoming.on('error', () => response.destroy());
      incoming.pipe(response);
    });
    upstream.on('error', () => {
      if (!response.headersSent) response.writeHead(502, { 'content-type': 'text/plain', 'cache-control': 'no-store' });
      response.end('Service temporarily unavailable.');
    });
    request.on('aborted', () => upstream.destroy());
    response.on('close', () => { if (!response.writableFinished) upstream.destroy(); });
    request.pipe(upstream);
  });

  // Preserve existing desktop/live-stream upgrades; authentication remains at
  // the destination service. This gateway never creates an identity or token.
  server.on('upgrade', (request, socket, head) => {
    const backend = target(request);
    if (!backend) { socket.end('HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n'); return; }
    const upstream = http.request(backend, {
      method: request.method, path: request.url, headers: forwardedHeaders(request.headers, true),
    });
    upstream.on('upgrade', (incoming, remote, remoteHead) => {
      const headerLines = [];
      for (let i = 0; i < incoming.rawHeaders.length; i += 2) headerLines.push(incoming.rawHeaders[i] + ': ' + incoming.rawHeaders[i + 1]);
      socket.write(`HTTP/1.1 ${incoming.statusCode} ${incoming.statusMessage}\r\n${headerLines.join('\r\n')}\r\n\r\n`);
      if (remoteHead.length) socket.write(remoteHead);
      if (head.length) remote.write(head);
      socket.on('error', () => remote.destroy());
      remote.on('error', () => socket.destroy());
      socket.on('close', () => remote.destroy());
      remote.on('close', () => socket.destroy());
      socket.pipe(remote).pipe(socket);
    });
    upstream.on('response', incoming => {
      socket.end(`HTTP/1.1 ${incoming.statusCode || 502} Rejected\r\nConnection: close\r\n\r\n`);
      incoming.resume();
    });
    upstream.on('error', () => socket.destroy());
    socket.on('close', () => upstream.destroy());
    upstream.end();
  });
  server.on('clientError', (_error, socket) => socket.end('HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n'));
  return server;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const port = Number(process.env.PORT || 14220);
  if (!Number.isInteger(port) || port < 1024 || port > 65535) throw new Error('Invalid private gateway port.');
  const legacyOrigin = process.env.REDX_LEGACY_ORIGIN || 'http://127.0.0.1:14210';
  const hostOrigin = process.env.REDX_HOST_ORIGIN || 'http://127.0.0.1:14200';
  if ([localOrigin(legacyOrigin), localOrigin(hostOrigin)].some(url => Number(url.port || 80) === port)) throw new Error('Gateway cannot proxy to itself.');
  const server = createWebsiteGateway({
    legacyOrigin, hostOrigin,
    mount: process.env.REDX_HOST_MOUNT || '/host',
    allowedHosts: (process.env.REDX_SITE_HOSTS || 'red-xai.com,www.red-xai.com').split(',').map(x => x.trim()).filter(Boolean),
  });
  server.listen(port, '127.0.0.1', () => console.log(`Red-XAI website gateway listening on loopback:${port}`));
  for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => server.close(() => process.exit(0)));
}
