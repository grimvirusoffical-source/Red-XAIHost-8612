import http from 'node:http';
import { Transform } from 'node:stream';
import { SiteError, fail } from './site-backend.mjs';

export const LEGACY_PAGES = new Set(['/signup','/activate','/tos']);
export function requestURL(value,origin) {
  if(typeof value!=='string'||value.length>8192||!value.startsWith('/')||value.startsWith('//')||/[\\#\u0000-\u0020\u007f]/u.test(value))fail(400,'invalid_request');
  let url;try{url=new URL(value,origin);}catch{fail(400,'invalid_request');}
  // Route the exact request path. Do not classify a normalized path but then
  // send a different raw dot-segment/backslash path to another authority.
  if(value.split('?',1)[0]!==url.pathname)fail(400,'invalid_request');
  return url;
}
export function proxyTarget(url) {
  const p=url.pathname;
  if(LEGACY_PAGES.has(p)||(p.startsWith('/vortex/')&&p!=='/vortex/')||p.startsWith('/api/accounts/')||p.startsWith('/api/rpc/')||p.startsWith('/assets/'))return {port:4200,kind:'legacy'};
  if(['/redxai-connect','/redxai-connectx'].some(x=>p===x||p.startsWith(x+'/')))return {port:17774,kind:'quick'};
  if((p==='/host'&&url.searchParams.get('embed')==='site')||p.startsWith('/host/'))return {port:14202,kind:'host'};
  if(['/v1','/ui'].some(x=>p===x||p.startsWith(x+'/')))return {port:17790,kind:'db'};
  return null;
}
export function requireOrigin(req,origin) {
  if(req.headers.origin!==origin||req.headers['sec-fetch-site']==='cross-site')fail(403,'same_origin_required');
}
function cookieFor(req,kind) {
  const raw=req.headers.cookie||'';
  if(typeof raw!=='string'||raw.length>16384||/[\r\n\u0000]/u.test(raw))fail(400,'invalid_cookie');
  if(kind==='legacy')return raw;
  const names=kind==='quick'?new Set(['redx_session','__Host-redx_cli_browser']):new Set(['redx_session']);
  const selected=raw.split(';').map(x=>x.trim()).filter(x=>names.has(x.split('=',1)[0]));
  for(const name of names)if(selected.filter(x=>x.startsWith(name+'=')).length>1)fail(400,'invalid_cookie');
  if(selected.some(x=>!x.includes('=')||/[\u0000-\u0020\u007f]/u.test(x)))fail(400,'invalid_cookie');
  return selected.join('; ');
}
export function forwardHeaders(req,target,config,upgrade=false) {
  const origin=new URL(config.publicOrigin),out={host:origin.host};
  const names=['accept','content-type','range','if-none-match','if-modified-since','last-event-id','sec-fetch-site'];
  if(['legacy','host'].includes(target.kind))names.push('x-redx-flow','x-redx-session');
  if(target.kind==='quick')names.push('x-redxai-client-key');
  if(target.kind==='db')names.push('x-redx-sha256','x-redx-confirm','x-redx-wait-seq','x-redx-wait-replicas','x-hub-signature-256','x-github-delivery','x-github-event');
  for(const key of names) {
    const value=req.headers[key];if(typeof value==='string'&&value.length<=8192&&!/[\r\n\u0000]/u.test(value))out[key]=value;
  }
  const cookie=cookieFor(req,target.kind);if(cookie)out.cookie=cookie;
  if(req.headers.authorization!==undefined) {
    const auth=req.headers.authorization;
    if(typeof auth!=='string'||auth.length>8192||!/^Bearer [A-Za-z0-9_.~+\/-]+=*$/u.test(auth))fail(400,'invalid_authorization');
    out.authorization=auth;
  }
  if(typeof req.headers.origin==='string'&&req.headers.origin.length<=2048&&!/[\r\n\u0000]/u.test(req.headers.origin))out.origin=req.headers.origin;
  if(config.trustCloudflared&&['127.0.0.1','::1','::ffff:127.0.0.1'].includes(req.socket?.remoteAddress)) {
    const ip=req.headers['cf-connecting-ip'];if(typeof ip==='string'&&/^[0-9a-fA-F:.]{3,45}$/u.test(ip))out['cf-connecting-ip']=ip;
  }
  out['x-forwarded-proto']=origin.protocol.slice(0,-1);out['x-forwarded-host']=origin.host;
  if(upgrade) {
    out.connection='Upgrade';out.upgrade='websocket';
    for(const key of ['sec-websocket-key','sec-websocket-version','sec-websocket-protocol','sec-websocket-extensions']) {
      const value=req.headers[key];if(typeof value==='string'&&value.length<8192&&!/[\r\n\u0000]/u.test(value))out[key]=value;
    }
  }
  return out;
}
function copyHeaders(up,res) {
  const names=new Set(['content-type','content-length','content-encoding','cache-control','etag','last-modified','accept-ranges','content-range',
    'retry-after','www-authenticate','content-disposition','content-security-policy','x-frame-options','x-content-type-options','vary',
    'referrer-policy','permissions-policy','pragma','allow','x-redx-seq','x-redx-applied','x-redx-replicas-acked','x-redx-snapshot-offset','x-redx-replid','x-redx-encrypted','x-redx-signature','x-redx-key-id','x-redx-sha256',
    'access-control-allow-origin','access-control-allow-methods','access-control-allow-headers','access-control-allow-credentials','access-control-expose-headers','access-control-max-age']);
  for(const [name,value]of Object.entries(up.headers))if(names.has(name)&&value!==undefined)res.setHeader(name,value);
  if(up.headers['set-cookie'])res.setHeader('Set-Cookie',up.headers['set-cookie']);
  if(typeof up.headers.location==='string') {
    try{const u=new URL(up.headers.location,'http://127.0.0.1');if(['http:','https:'].includes(u.protocol)&&!u.username&&!u.password)
      res.setHeader('Location',/^https?:\/\//iu.test(up.headers.location)&&u.hostname==='127.0.0.1'?u.pathname+u.search+u.hash:up.headers.location);}catch{}
  }
}
export async function proxyHttp(req,res,target,config) {
  // Registration/account endpoints retain their original authority's protocol and CSRF checks.
  if(target.kind!=='legacy'&&!['GET','HEAD','OPTIONS'].includes(req.method)&&cookieFor(req,target.kind))requireOrigin(req,config.publicOrigin);
  const max=target.kind==='legacy'?65536:1024*1024*1024;
  if(req.headers['content-length']!==undefined&&(!/^\d+$/u.test(String(req.headers['content-length']))||Number(req.headers['content-length'])>max))fail(413,'body_too_large');
  const headers=forwardHeaders(req,target,config);if(req.headers['content-length']!==undefined)headers['content-length']=req.headers['content-length'];
  return new Promise((resolve,reject)=>{
    let sent=0,done=false;
    const finish=()=>{if(!done){done=true;resolve();}};
    const upstream=http.request({hostname:'127.0.0.1',port:target.port,path:req.url,method:req.method,headers},incoming=>{
      clearTimeout(timer);res.statusCode=incoming.statusCode||502;copyHeaders(incoming,res);
      incoming.on('error',()=>{res.destroy?.();finish();});incoming.on('end',finish);incoming.pipe(res);
    });
    const timer=setTimeout(()=>upstream.destroy(new Error('upstream timeout')),10000);timer.unref?.();
    upstream.on('error',()=>{clearTimeout(timer);if(res.headersSent){res.destroy?.();finish();}else if(!done){done=true;reject(new SiteError(503,'service_unavailable'));}});
    req.on('aborted',()=>upstream.destroy());res.on('close',()=>{if(!res.writableEnded)upstream.destroy();});
    const limiter=new Transform({transform(chunk,encoding,callback){sent+=chunk.length;callback(sent>max?new SiteError(413,'body_too_large'):null,chunk);}});
    limiter.on('error',e=>{upstream.destroy();if(!done){done=true;reject(e);}});
    req.pipe(limiter).pipe(upstream);
  });
}
export function attachUpgrades(server,config) {
  server.on('upgrade',(req,socket,head)=>{
    try {
      const target=proxyTarget(requestURL(req.url,config.publicOrigin));
      if(!target||!['host','db','quick'].includes(target.kind)||req.headers.upgrade?.toLowerCase()!=='websocket')throw Error();
      if(req.headers.origin&&req.headers.origin!==config.publicOrigin)throw Error();
      if(cookieFor(req,target.kind)&&req.headers.origin!==config.publicOrigin)throw Error();
      const up=http.request({hostname:'127.0.0.1',port:target.port,path:req.url,method:'GET',headers:forwardHeaders(req,target,config,true)});
      up.setTimeout(10000,()=>up.destroy());
      up.on('upgrade',(response,remote,remoteHead)=>{
        up.setTimeout(0);socket.write('HTTP/1.1 101 Switching Protocols\r\n');
        for(const [k,v]of Object.entries(response.headers))if(['upgrade','connection','sec-websocket-accept','sec-websocket-protocol','sec-websocket-extensions'].includes(k)&&typeof v==='string')socket.write(k+': '+v+'\r\n');
        socket.write('\r\n');if(remoteHead.length)socket.write(remoteHead);if(head.length)remote.write(head);socket.pipe(remote);remote.pipe(socket);
        socket.on('error',()=>remote.destroy());remote.on('error',()=>socket.destroy());
        socket.on('close',()=>remote.destroy());remote.on('close',()=>socket.destroy());
      });
      up.on('response',response=>{socket.end('HTTP/1.1 '+(response.statusCode||502)+' Rejected\r\nConnection: close\r\n\r\n');response.resume();});
      up.on('error',()=>socket.destroy());socket.on('close',()=>up.destroy());up.end();
    }catch{socket.end('HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n');}
  });
}
