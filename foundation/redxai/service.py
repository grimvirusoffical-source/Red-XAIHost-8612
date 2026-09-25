"""Authenticated local API used by both native applications.

Run: python -m redxai.service --port 46321
Public, multi-tenant hosting is intentionally not enabled by this preview.
"""
from __future__ import annotations
import argparse
import base64
import collections
import http.server
import json
import os
import secrets
import socket
import threading
import time
import urllib.parse
import urllib.request
from pathlib import Path
from .format import export_snapshot,import_snapshot
from .hosting import HostManager
from .language import LanguageError,parse
from .security import private_file
from .store import Store, StoreError, Unauthorized,Forbidden,Missing,Conflict,data_root

VERSION='0.2.0-preview'
MAX_REQUEST=4*1024*1024


class LocalServer(http.server.ThreadingHTTPServer):
    daemon_threads=True
    allow_reuse_address=True
    def __init__(self,port:int=46321,root:Path|None=None):
        self.store=Store(root);self.host=HostManager(self.store.root)
        instance=self.store.root/'instance.id'
        try:private_file(instance,secrets.token_hex(16).encode())
        except FileExistsError:pass
        self.instance_id=instance.read_text();self.started=time.monotonic()
        self.semaphore=threading.BoundedSemaphore(24)
        self.rates=collections.defaultdict(collections.deque);self.rate_lock=threading.Lock()
        super().__init__(('127.0.0.1',port),Handler)

    def process_request(self,request,client_address):
        if not self.semaphore.acquire(blocking=False):
            request.sendall(b'HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')
            self.shutdown_request(request);return
        try:super().process_request(request,client_address)
        except BaseException:self.semaphore.release();raise

    def process_request_thread(self,request,client_address):
        try:super().process_request_thread(request,client_address)
        finally:self.semaphore.release()

    def limited(self,bucket:str,limit:int=10,seconds:int=60):
        now=time.monotonic()
        with self.rate_lock:
            events=self.rates[bucket]
            while events and events[0]<now-seconds:events.popleft()
            if len(events)>=limit:return True
            events.append(now);return False

    def server_close(self):
        self.host.close();super().server_close()


class Handler(http.server.BaseHTTPRequestHandler):
    server_version='RedXAI/0.2'
    def log_message(self,*_):pass  # Never log request bodies or Authorization headers.
    def setup(self):
        super().setup();self.connection.settimeout(15)
    def send_json(self,status:int,data):
        body=json.dumps(data,ensure_ascii=False,separators=(',',':')).encode()
        self.send_response(status)
        self.send_header('Content-Type','application/json; charset=utf-8')
        self.send_header('Content-Length',str(len(body)))
        self.send_header('Cache-Control','no-store')
        self.send_header('X-Content-Type-Options','nosniff')
        self.send_header('Content-Security-Policy',"default-src 'none'; frame-ancestors 'none'")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):self.dispatch()
    def do_POST(self):self.dispatch()
    def do_OPTIONS(self):self.send_json(403,{'error':'Browser cross-origin access is disabled'})

    def dispatch(self):
        try:
            if self.headers.get('Host','').split(':')[0] not in ('127.0.0.1','localhost'):
                raise Forbidden('This preview service accepts localhost only')
            path=urllib.parse.urlsplit(self.path).path
            data={}
            if self.command=='POST':
                if self.headers.get('X-Red-XAI')!='1':raise Forbidden('Native client request header required')
                origin=self.headers.get('Origin')
                if origin and origin not in (f'http://127.0.0.1:{self.server.server_port}',f'http://localhost:{self.server.server_port}'):
                    raise Forbidden('Cross-origin request rejected')
                if self.headers.get('Transfer-Encoding'):raise StoreError('Chunked request bodies are not supported')
                try:length=int(self.headers.get('Content-Length','-1'))
                except ValueError:raise StoreError('Invalid content length')
                if length<0:raise StoreError('Content length required')
                if length>MAX_REQUEST:return self.send_json(413,{'error':'Request body exceeds 4 MiB'})
                data=json.loads(self.rfile.read(length))
                if not isinstance(data,dict):raise StoreError('JSON object required')
            store=self.server.store
            if path=='/health' and self.command=='GET':
                return self.send_json(200,{'ok':True,'service':'Red-XAI Local Host','version':VERSION,
                    'instance_id':self.server.instance_id,'setup_needed':store.setup_needed(),
                    'scope':'loopback-only','uptime_seconds':round(time.monotonic()-self.server.started,2)})
            if path in ('/v1/setup','/v1/login','/v1/register'):
                if self.command!='POST':return self.send_json(405,{'error':'POST required'})
                if self.server.limited('auth',12):return self.send_json(429,{'error':'Too many authentication attempts; retry in a minute'})
                if path=='/v1/setup':result=store.bootstrap(data['setup_token'],data['registration'])
                elif path=='/v1/login':result=store.login(data['email'],data['password'])
                else:result=store.register(data['registration'])
                return self.send_json(200,result)
            authorization=self.headers.get('Authorization','')
            principal=store.principal(authorization[7:] if authorization.startswith('Bearer ') else '')
            result=self.route(path,data,principal)
            self.send_json(200,result)
        except StoreError as exc:self.send_json(exc.status,{'error':str(exc)})
        except (LanguageError,ValueError,KeyError,TypeError,UnicodeDecodeError) as exc:
            self.send_json(400,{'error':str(exc)[:400]})
        except (BrokenPipeError,ConnectionResetError,socket.timeout):pass
        except Exception:
            self.send_json(500,{'error':'Operation failed; no partial transaction was committed'})

    def route(self,path,data,p):
        store=self.server.store;method=self.command
        if path=='/v1/databases':
            return store.list_databases(p) if method=='GET' else store.create_database(p,data['name'],data.get('project','Default'))
        if path=='/v1/validate' and method=='POST':
            store.session(p);doc=parse(data['source']);return {'valid':True,'objects':doc.index()}
        if path=='/v1/owner' and method=='GET':return store.owner_dashboard(p)
        if path=='/v1/owner/approve' and method=='POST':return store.approve_local_user(p,data['user_id'])
        if path=='/v1/keys/revoke' and method=='POST':return store.revoke_key(p,data['reference'])
        if path=='/v1/logout' and method=='POST':return store.revoke_key(p,p['key_ref'])
        if path.startswith('/v1/db/'):
            parts=path.split('/');uid=parts[3];action=parts[4] if len(parts)>4 else ''
            if not action and method=='GET':return store.read_database(p,uid)
            if action=='source' and method=='POST':return store.save_source(p,uid,data['source'],data['revision'])
            if action=='edit' and method=='POST':return store.edit(p,uid,data)
            if action=='keys' and method=='POST':return store.mint_key(p,uid,data.get('read_only',False),data.get('box_path'),data.get('days',30))
            if action=='history' and method=='GET':return store.history(p,uid)
            if action=='restore' and method=='POST':return store.restore(p,uid,data['target_revision'],data['revision'])
            if action=='delete' and method=='POST':return store.delete(p,uid,data['revision'])
            if action=='find' and method=='POST':
                rows=store.read_database(p,uid,False)['objects']
                allowed={'kind','name','local_id','global_id'}
                if not data or not set(data)<=allowed:raise StoreError('Find accepts kind, name, local_id, global_id')
                return [r for r in rows if all(r.get(k)==v for k,v in data.items())]
            if action=='export' and method=='POST':
                store.session(p);doc=store.read_database(p,uid)
                exported=export_snapshot(doc['source'],doc['name'],data.get('passphrase'))
                return {'name':doc['name'],'data_base64':base64.b64encode(exported).decode(),
                        'encrypted':data.get('passphrase') is not None,'api_keys_included':False}
            if action=='import' and method=='POST':
                store.session(p)
                payload=base64.b64decode(data['data_base64'],validate=True)
                imported=import_snapshot(payload,data.get('passphrase'))
                return store.save_source(p,uid,imported['source'],data['revision'])
        if path.startswith('/v1/host'):
            store.session(p)
            if p['role']!='owner':raise Forbidden('Local host owner required')
            if path=='/v1/host/stats' and method=='GET':
                import psutil
                memory=psutil.virtual_memory();disk=psutil.disk_usage(store.root)
                return {'cpu_percent':psutil.cpu_percent(interval=0.1),'memory_used_bytes':memory.used,
                        'memory_total_bytes':memory.total,'disk_free_bytes':disk.free,
                        'projects':len(self.server.host.projects),'running':len(self.server.host.servers),
                        'node_count':1,'multi_node_scheduler':'not_implemented',
                        'cloudflared_detected':bool(__import__('shutil').which('cloudflared'))}
            if path=='/v1/host/projects':
                return self.server.host.list() if method=='GET' else self.server.host.add(data['name'],data['directory'])
            if path=='/v1/host/start' and method=='POST':return self.server.host.start(data['project_id'])
            if path=='/v1/host/stop' and method=='POST':return self.server.host.stop(data['project_id'])
            if path=='/v1/host/cloudflare/zones' and method=='POST':
                api_token=data.get('api_token','')
                if not isinstance(api_token,str) or not 16<=len(api_token)<=4096:raise StoreError('Enter a scoped Cloudflare API token')
                result=[]
                for page in range(1,21):
                    request=urllib.request.Request(f'https://api.cloudflare.com/client/v4/zones?per_page=50&page={page}',headers={'Authorization':'Bearer '+api_token})
                    try:
                        with urllib.request.urlopen(request,timeout=20) as response:
                            info=json.loads(response.read(2*1024*1024))
                    except Exception as exc:raise StoreError('Cloudflare connection failed; check token scopes and network access') from exc
                    if not info.get('success'):raise StoreError('Cloudflare rejected the request')
                    result.extend({'id':z['id'],'name':z['name'],'status':z['status']} for z in info['result'])
                    if page>=info.get('result_info',{}).get('total_pages',1):break
                return {'zones':result,'token_persisted':False,'dns_changes_made':False}
        raise Missing('API route not found')


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--port',type=int,default=46321)
    parser.add_argument('--data-dir',type=Path)
    args=parser.parse_args()
    server=LocalServer(args.port,args.data_dir)
    print(json.dumps({'service':'Red-XAI Local Host','version':VERSION,'address':f'http://127.0.0.1:{server.server_port}',
                     'setup_needed':server.store.setup_needed()}),flush=True)
    try:server.serve_forever()
    except KeyboardInterrupt:pass
    finally:server.server_close()

if __name__=='__main__':main()
