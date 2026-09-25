"""Single-node static hosting preview. No untrusted commands or workloads execute."""
from __future__ import annotations
import functools
import http.server
import json
import mimetypes
import os
import shutil
import threading
import urllib.parse
from pathlib import Path
from .distribution import atomic_json

DENIED = {'.env','.git','.ssh','.aws','master.key','setup.token','redxai.sqlite3'}


class StaticHandler(http.server.BaseHTTPRequestHandler):
    server_version='RedXAIStatic/0.2'
    def log_message(self,*_): pass
    def do_GET(self):
        try:
            name=urllib.parse.unquote(urllib.parse.urlsplit(self.path).path).lstrip('/') or 'index.html'
            parts=Path(name).parts
            if any(p in ('..',) or p.startswith('.') or p.casefold() in DENIED for p in parts):
                self.send_error(403);return
            root=self.server.document_root
            target=root/name
            if not target.resolve().is_relative_to(root) or any(p.is_symlink() for p in [target,*target.parents] if p != root):
                self.send_error(403);return
            if target.is_dir(): target=target/'index.html'
            if not target.is_file(): self.send_error(404);return
            if target.stat().st_size>128*1024*1024:self.send_error(413);return
            self.send_response(200)
            self.send_header('Content-Type',mimetypes.guess_type(target.name)[0] or 'application/octet-stream')
            self.send_header('Content-Length',str(target.stat().st_size))
            self.send_header('X-Content-Type-Options','nosniff');self.end_headers()
            with target.open('rb') as f:shutil.copyfileobj(f,self.wfile)
        except (OSError,ValueError):
            self.send_error(400)


class HostManager:
    def __init__(self,root:Path):
        self.root=root;self.path=root/'projects.json';self._lock=threading.RLock();self.servers={}
        self.projects=json.loads(self.path.read_text()) if self.path.exists() else []

    def list(self):
        with self._lock:
            return [{**p,'running':p['id'] in self.servers,
                     'url':f"http://127.0.0.1:{self.servers[p['id']].server_port}" if p['id'] in self.servers else None}
                    for p in self.projects]

    def add(self,name:str,directory:str):
        import uuid
        path=Path(directory).expanduser().resolve()
        if not 1<=len(name)<=80 or not path.is_dir():raise ValueError('Choose a project name and existing static-site directory')
        if path==Path.home() or path==Path(path.anchor) or path==self.root or self.root.is_relative_to(path):
            raise ValueError('Choose only a dedicated website folder, not a home/system/data directory')
        project={'id':uuid.uuid4().hex,'name':name,'directory':str(path),'kind':'static','auto_start':False}
        with self._lock:
            self.projects.append(project);atomic_json(self.path,self.projects)
        return project

    def start(self,project_id:str):
        with self._lock:
            if project_id in self.servers:return self.list()
            project=next((p for p in self.projects if p['id']==project_id),None)
            if not project:raise ValueError('Project not found')
            root=Path(project['directory']).resolve()
            if not root.is_dir():raise ValueError('Project directory is unavailable')
            # Preview binds loopback. Cloudflare may tunnel an explicitly selected site.
            server=http.server.ThreadingHTTPServer(('127.0.0.1',0),StaticHandler)
            server.daemon_threads=True;server.document_root=root
            self.servers[project_id]=server
            threading.Thread(target=server.serve_forever,daemon=True).start()
            return self.list()

    def stop(self,project_id:str):
        with self._lock:server=self.servers.pop(project_id,None)
        if server:server.shutdown();server.server_close()
        return self.list()

    def close(self):
        for pid in list(self.servers):self.stop(pid)
