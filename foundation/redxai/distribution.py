"""Signed .RXAI packages, safe staging, atomic version selection, and rollback.

Preview protocol: Ed25519 package signatures, NOT a full TUF implementation.
Trust keys must be supplied independently of the package being installed.
No package hook or downloaded source code is executed by this module.
"""
from __future__ import annotations
import base64
import hashlib
import json
import os
import platform
import re
import shutil
import stat
import tempfile
import time
import urllib.parse
import urllib.request
import uuid
import zipfile
from pathlib import Path, PurePosixPath
from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey, Ed25519PublicKey

MAX_TOTAL = 1024 * 1024 * 1024
MAX_FILE = 256 * 1024 * 1024
MAX_FILES = 20_000


def canonical(data) -> bytes:
    return json.dumps(data, sort_keys=True, ensure_ascii=False, separators=(',', ':')).encode()


def valid_path(name: str) -> bool:
    if not isinstance(name, str) or not name or len(name) > 240 or any(c in name for c in '\\<>:"|?*') or any(ord(c)<32 for c in name):
        return False
    path = PurePosixPath(name)
    if path.is_absolute() or any(p in ('','.', '..') for p in name.split('/')):
        return False
    reserved = {'CON','PRN','AUX','NUL',*[f'COM{i}' for i in range(1,10)],*[f'LPT{i}' for i in range(1,10)]}
    return all(not p.endswith((' ', '.')) and p.split('.')[0].upper() not in reserved for p in path.parts)


def version_tuple(value: str) -> tuple[int,int,int]:
    if not isinstance(value, str) or not re.fullmatch(r'(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)', value):
        raise ValueError('Version must be three non-negative integers, for example 0.2.0')
    return tuple(int(p) for p in value.split('.'))


def runtime_platform() -> tuple[str,str]:
    system = {'Windows':'windows','Darwin':'macos','Linux':'linux'}.get(platform.system(), platform.system().lower())
    architecture = {'x86_64':'x64','AMD64':'x64','arm64':'arm64','aarch64':'arm64'}.get(platform.machine(), platform.machine())
    return system, architecture


def create_package(source: Path, output: Path, app_id: str, version: str,
                   private_key: Ed25519PrivateKey, key_id: str,
                   target_os: str, architecture: str) -> dict:
    if not re.fullmatch(r'[a-z0-9][a-z0-9-]{2,79}', app_id): raise ValueError('Invalid application ID')
    version_tuple(version); files = []
    for path in sorted(source.rglob('*')):
        if path.is_symlink(): raise ValueError('Symbolic links are not supported in preview packages')
        if not path.is_file(): continue
        name = path.relative_to(source).as_posix()
        if not valid_path(name): raise ValueError('Unsafe file path')
        data = path.read_bytes()
        if len(data) > MAX_FILE: raise ValueError('Package file too large')
        files.append({'path': name,'size':len(data),'sha256':hashlib.sha256(data).hexdigest(),
                      'executable':bool(path.stat().st_mode & stat.S_IXUSR)})
    if len(files) > MAX_FILES or sum(f['size'] for f in files) > MAX_TOTAL: raise ValueError('Package exceeds limits')
    manifest = {'format':'RXAI','schema':1,'app_id':app_id,'version':version,
                'key_id':key_id,'os':target_os,'architecture':architecture,'files':files,
                'recommended':{'per_user':True,'launch_after_install':False,'auto_update':False},
                'custom_options':['installation_directory'], 'created_at':int(time.time())}
    raw = canonical(manifest)
    with zipfile.ZipFile(output,'w',zipfile.ZIP_DEFLATED) as archive:
        archive.writestr('manifest.json',raw)
        archive.writestr('manifest.sig',base64.b64encode(private_key.sign(raw)))
        for f in files: archive.write(source / f['path'], 'payload/' + f['path'])
    return manifest


def inspect_package(package: Path, trust_keys: dict[str, Ed25519PublicKey],
                    enforce_platform: bool=True) -> dict:
    with zipfile.ZipFile(package) as archive:
        names = archive.namelist()
        if len(names) > MAX_FILES+2 or len(names) != len(set(n.casefold() for n in names)):
            raise ValueError('Too many files or case-insensitive duplicate paths')
        if archive.getinfo('manifest.json').file_size > 4*1024*1024 or archive.getinfo('manifest.sig').file_size > 512:
            raise ValueError('Manifest too large')
        manifest = json.loads(archive.read('manifest.json'))
        if manifest.get('format') != 'RXAI' or manifest.get('schema') != 1: raise ValueError('Unsupported package format')
        app_id = manifest.get('app_id','')
        if not re.fullmatch(r'[a-z0-9][a-z0-9-]{2,79}',app_id): raise ValueError('Invalid application ID')
        version_tuple(manifest.get('version',''))
        key = trust_keys.get(manifest.get('key_id'))
        if key is None: raise ValueError('Publisher key is not independently trusted')
        try:
            key.verify(base64.b64decode(archive.read('manifest.sig'),validate=True), canonical(manifest))
        except (ValueError, InvalidSignature) as exc: raise ValueError('Package signature invalid') from exc
        if enforce_platform and (manifest['os'],manifest['architecture']) != runtime_platform():
            raise ValueError('Package does not match this operating system/architecture')
        files = manifest.get('files')
        if not isinstance(files,list) or len(files)>MAX_FILES: raise ValueError('Invalid file manifest')
        total, expected, declared = 0, {'manifest.json','manifest.sig'}, set()
        for f in files:
            if not isinstance(f,dict) or not isinstance(f.get('path'),str) or f['path'].casefold() in declared:
                raise ValueError('Invalid or duplicated manifest entry')
            declared.add(f['path'].casefold())
            if not isinstance(f.get('sha256'),str) or not re.fullmatch('[a-f0-9]{64}',f['sha256']):
                raise ValueError('Invalid SHA256 in manifest')
            if type(f.get('executable',False)) is not bool:raise ValueError('Invalid executable flag')
            if not valid_path(f['path']) or type(f['size']) is not int or not 0 <= f['size'] <= MAX_FILE:
                raise ValueError('Unsafe file manifest')
            name = 'payload/' + f['path']; expected.add(name); info = archive.getinfo(name)
            if info.file_size != f['size']: raise ValueError('File size mismatch')
            mode = (info.external_attr >> 16) & 0xFFFF
            if stat.S_ISLNK(mode) or (stat.S_IFMT(mode) not in (0,stat.S_IFREG)):
                raise ValueError('Special files and symbolic links are forbidden')
            total += info.file_size
            if total > MAX_TOTAL: raise ValueError('Uncompressed package exceeds limit')
            checksum = hashlib.sha256()
            with archive.open(name) as stream:
                remaining = f['size']
                while block := stream.read(min(1024*1024, remaining+1)):
                    remaining -= len(block)
                    if remaining < 0: raise ValueError('Expanded data exceeds declared length')
                    checksum.update(block)
            if remaining or checksum.hexdigest() != f['sha256']: raise ValueError('File checksum mismatch')
        if set(names) != expected: raise ValueError('Undeclared package file')
        return manifest


def atomic_json(path: Path, value: dict):
    temporary = path.with_name(path.name+'.tmp-'+uuid.uuid4().hex)
    with temporary.open('xb') as stream:
        stream.write(canonical(value)); stream.flush(); os.fsync(stream.fileno())
    os.replace(temporary,path)
    if os.name != 'nt':
        fd = os.open(path.parent, os.O_RDONLY)
        try: os.fsync(fd)
        finally: os.close(fd)


def install(package: Path, destination: Path, trust_keys: dict[str, Ed25519PublicKey],
            progress=None, allow_downgrade=False) -> dict:
    manifest = inspect_package(package,trust_keys)
    destination = destination.expanduser().resolve()
    destination.mkdir(parents=True,exist_ok=True)
    app = destination/manifest['app_id']; app.mkdir(exist_ok=True)
    if app.is_symlink(): raise ValueError('Application directory cannot be a symbolic link')
    lock = app/'.install.lock'
    try:
        descriptor = os.open(lock,os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600)
    except FileExistsError as exc: raise ValueError('Another installation is running or a stale lock needs inspection') from exc
    os.close(descriptor)
    stage = None
    try:
        pointer = app/'current.json'
        if pointer.is_symlink() or (app/'releases').is_symlink():raise ValueError('Installation metadata cannot be a symbolic link')
        current = json.loads(pointer.read_text()) if pointer.exists() else None
        if current and not allow_downgrade and version_tuple(manifest['version']) < version_tuple(current['highest_version']):
            raise ValueError('Rollback/replay protection: package older than the highest installed version')
        total = sum(f['size'] for f in manifest['files'])
        if shutil.disk_usage(destination).free < total + 16*1024*1024: raise ValueError('Insufficient disk space')
        stage = Path(tempfile.mkdtemp(prefix='.stage-',dir=app))
        with zipfile.ZipFile(package) as archive:
            for i,f in enumerate(manifest['files']):
                target = stage/f['path']; target.parent.mkdir(parents=True,exist_ok=True)
                with archive.open('payload/'+f['path']) as source, target.open('xb') as output:
                    remaining=f['size']; digest=hashlib.sha256()
                    while block:=source.read(min(1024*1024,remaining+1)):
                        remaining-=len(block)
                        if remaining<0:raise ValueError('Payload changed during installation')
                        digest.update(block);output.write(block)
                    if remaining or digest.hexdigest()!=f['sha256']:raise ValueError('Payload changed during installation')
                    output.flush(); os.fsync(output.fileno())
                if f.get('executable') and os.name!='nt': target.chmod(0o755)
                if progress: progress((i+1)/max(1,len(manifest['files'])))
        release_name = manifest['version']+'-'+uuid.uuid4().hex[:12]
        releases = app/'releases'; releases.mkdir(exist_ok=True)
        release_path = releases/release_name
        os.replace(stage,release_path); stage = None
        highest = max([manifest['version'],current['highest_version']] if current else [manifest['version']],key=version_tuple)
        atomic_json(pointer,{'schema':1,'app_id':manifest['app_id'],'version':manifest['version'],
                             'release':release_name,'previous':current,'highest_version':highest})
        return {'app_id':manifest['app_id'],'version':manifest['version'],'release':str(release_path),
                'activation':'version pointer switched; application processes were not forcibly restarted'}
    finally:
        if stage is not None: shutil.rmtree(stage,ignore_errors=True)
        lock.unlink(missing_ok=True)


def rollback(destination: Path, app_id: str) -> dict:
    if not re.fullmatch(r'[a-z0-9][a-z0-9-]{2,79}',app_id): raise ValueError('Invalid application ID')
    app = destination.expanduser().resolve()/app_id; pointer=app/'current.json'
    descriptor = os.open(app/'.install.lock',os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600); os.close(descriptor)
    try:
        current=json.loads(pointer.read_text()); previous=current.get('previous')
        if not previous or not (app/'releases'/previous['release']).is_dir(): raise ValueError('No previous installed version available')
        previous['highest_version']=current['highest_version']; atomic_json(pointer,previous)
        return previous
    finally: (app/'.install.lock').unlink(missing_ok=True)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError('Redirects are disabled for package downloads; use the final HTTPS URL')


def download_https(url: str, output: Path, max_bytes=MAX_TOTAL) -> None:
    parsed=urllib.parse.urlparse(url)
    if parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.password:
        raise ValueError('Use an HTTPS URL without embedded credentials')
    opener=urllib.request.build_opener(NoRedirect())
    created=False
    try:
        with opener.open(url,timeout=30) as response, output.open('xb') as stream:
            created=True;count=0
            while block:=response.read(1024*1024):
                count+=len(block)
                if count>max_bytes: raise ValueError('Download exceeds allowed size')
                stream.write(block)
            stream.flush(); os.fsync(stream.fileno())
    except BaseException:
        if created:output.unlink(missing_ok=True)
        raise


def load_trust(path: Path) -> dict[str,Ed25519PublicKey]:
    document=json.loads(path.read_text())
    return {key_id:Ed25519PublicKey.from_public_bytes(base64.b64decode(encoded,validate=True))
            for key_id,encoded in document.items()}
