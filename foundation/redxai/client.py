"""Small documented Python SDK. API credentials stay in process memory."""
from __future__ import annotations
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from .store import data_root


class APIError(RuntimeError):
    def __init__(self,status:int,message:str):self.status=status;super().__init__(message)


class Client:
    def __init__(self,base_url='http://127.0.0.1:46321',token=''):
        parsed=urllib.parse.urlsplit(base_url)
        if parsed.username or parsed.password:raise ValueError('Do not place credentials in URLs')
        if parsed.scheme!='https' and not (parsed.scheme=='http' and parsed.hostname in ('localhost','127.0.0.1')):
            raise ValueError('Remote API connections require HTTPS')
        self.base_url=base_url.rstrip('/');self.token=token
    def request(self,path:str,data=None):
        if not path.startswith('/v1/') and path!='/health':raise ValueError('Invalid API route')
        headers={'X-Red-XAI':'1','Content-Type':'application/json'}
        if self.token:headers['Authorization']='Bearer '+self.token
        request=urllib.request.Request(self.base_url+path,headers=headers,
            data=json.dumps(data,ensure_ascii=False).encode() if data is not None else None)
        try:
            with urllib.request.urlopen(request,timeout=30) as response:return json.load(response)
        except urllib.error.HTTPError as exc:
            try:message=json.load(exc).get('error','Request failed')
            except Exception:message='Request failed'
            raise APIError(exc.code,message) from None
    def databases(self):return self.request('/v1/databases')
    def create(self,name,project='Default'):return self.request('/v1/databases',{'name':name,'project':project})
    def read(self,uid):return self.request('/v1/db/'+uid)
    def find(self,uid,**selectors):return self.request('/v1/db/'+uid+'/find',selectors)
    def save(self,uid,source,revision):return self.request('/v1/db/'+uid+'/source',{'source':source,'revision':revision})
    def edit(self,uid,revision,operation,path=None,**kwargs):
        return self.request('/v1/db/'+uid+'/edit',{'revision':revision,'operation':operation,'path':path or [],**kwargs})


def ensure_service(port=46321) -> Client:
    client=Client(f'http://127.0.0.1:{port}')
    def verify():
        response=client.request('/health')
        identity=data_root()/'instance.id'
        if not identity.exists() or response.get('instance_id')!=identity.read_text():
            raise RuntimeError('An unrecognized service owns the local port. Stop it before pairing.')
        return client
    try:return verify()
    except urllib.error.URLError:pass
    args=([sys.executable,'--service'] if getattr(sys,'frozen',False) else [sys.executable,'-m','redxai.service'])+['--port',str(port)]
    options={'stdin':subprocess.DEVNULL,'stdout':subprocess.DEVNULL,'stderr':subprocess.DEVNULL}
    if os.name=='nt':options['creationflags']=subprocess.CREATE_NO_WINDOW | subprocess.DETACHED_PROCESS
    else:options['start_new_session']=True
    subprocess.Popen(args,**options)
    for _ in range(50):
        time.sleep(.1)
        try:return verify()
        except urllib.error.URLError:continue
    raise RuntimeError('Local Host service did not start. Check dependencies and whether port 46321 is in use.')
