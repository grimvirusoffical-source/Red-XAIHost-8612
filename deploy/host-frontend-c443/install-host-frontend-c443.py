#!/usr/bin/env python3
"""Guarded Host frontend-only install/rollback. No network or service actions.

Inspect by default. Apply and rollback require Linux and an independently pinned
current origin index SHA. Original assets and backend files are never removed.
"""
import argparse
import base64
import hashlib
import io
import json
import os
from pathlib import Path
import re
import stat
import sys
import tarfile
import uuid

ORIGIN = Path('/opt/redx/staging-host-8f60582c/packages/web/dist')
BACKUP_PARENT = Path('/srv/red-xai/backups')
ARCHIVE_SHA = '00413c39f00ddca778bb53bf72b72c37676743e5b298a076f2489c02b90803bd'
FILES = {
    'assets/index-Cc6VrOll.css': (38503, 'b2c89925b797ecdd585bfc0066df42ea917c660c33e966f8639c1fa63eacae1e'),
    'assets/index-amJkAgmM.js': (430435, '3ff28dcd4b718fddc57a61c6971aae515b2bd50b034f2e6b22380d0cd0e516a0'),
    'favicon.svg': (272, 'd20ddcf6fcfe34c722dfaead8cfcf53e311701495e2ceecc7942339a253dcd08'),
    'index.html': (789, 'f339b0a65e9d15541c97213f48334bc30f5e5991b04d9ed6bab80abdf58f1503'),
}
OLD_MODULE = b'/host/assets/index-no-vendor-dc980958bb5f.js'
NEW_MODULE = b'/host/assets/index-amJkAgmM.js'
QUICK = b'/redxai-connect/ux.js'


def sha(data):
    return hashlib.sha256(data).hexdigest()


def fingerprint(s):
    return (s.st_dev, s.st_ino, s.st_size, s.st_mtime_ns,
            s.st_uid, s.st_gid, stat.S_IMODE(s.st_mode))


def canonical_directory(p):
    if p.resolve(strict=True) != p or not p.is_dir():
        raise ValueError('Canonical existing directory required')


def read_regular(p, limit=2*1024*1024):
    canonical_directory(p.parent)
    before = p.lstat()
    if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1 or before.st_size > limit:
        raise ValueError('Bounded single regular file required')
    fd = os.open(p, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0))
    with os.fdopen(fd, 'rb') as f:
        if fingerprint(os.fstat(f.fileno())) != fingerprint(before):
            raise ValueError('File identity changed')
        data = f.read(limit+1)
    if len(data) > limit or fingerprint(p.lstat()) != fingerprint(before):
        raise ValueError('File changed')
    return data, before


def xattrs(p):
    return {n:os.getxattr(p,n,follow_symlinks=False)
            for n in os.listxattr(p,follow_symlinks=False)}


def sync_dir(p):
    fd = os.open(p, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def exclusive(p, data, mode=0o600):
    fd = os.open(p, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    with os.fdopen(fd, 'wb') as f:
        f.write(data); f.flush(); os.fsync(f.fileno())


def payload(archive):
    blob, _ = read_regular(archive, 1024*1024)
    if sha(blob) != ARCHIVE_SHA:
        raise ValueError('Pinned archive required')
    out = {}
    with tarfile.open(fileobj=io.BytesIO(blob),mode='r:gz') as tar:
        for member in tar:
            if (not member.isfile() or member.name not in FILES or member.name in out
                    or member.size != FILES[member.name][0]):
                raise ValueError('Exact public archive members required')
            stream = tar.extractfile(member)
            data = stream.read(member.size+1)
            if len(data) != member.size or sha(data) != FILES[member.name][1]:
                raise ValueError('Pinned public member required')
            out[member.name] = data
    if set(out) != set(FILES):
        raise ValueError('Complete public archive required')
    if any(re.search(rb'runable|runnable|lilstts|data-runable|analytics-consent\.js|Made with',b,re.I) for b in out.values()):
        raise ValueError('Vendor surface refused')
    if out['index.html'].count(QUICK) != 1 or out['index.html'].count(NEW_MODULE) != 1:
        raise ValueError('Reviewed runtime links required')
    return out


def require_index(data, expected, candidate=False):
    if not re.fullmatch('[a-f0-9]{64}',expected) or sha(data) != expected:
        raise ValueError('Origin index digest mismatch')
    module = NEW_MODULE if candidate else OLD_MODULE
    if data.count(module) != 1 or data.count(QUICK) != 1:
        raise ValueError('Reviewed Host/Quick Login index required')
    if not candidate and NEW_MODULE in data:
        raise ValueError('Candidate already installed')


def set_metadata(p, uid, gid, mode, attrs):
    os.chown(p,uid,gid,follow_symlinks=False)
    os.chmod(p,mode,follow_symlinks=False)
    for n in os.listxattr(p,follow_symlinks=False):
        if n not in attrs:
            os.removexattr(p,n,follow_symlinks=False)
    for n,v in attrs.items():
        os.setxattr(p,n,v,follow_symlinks=False)
    s=p.lstat()
    if (s.st_uid,s.st_gid,stat.S_IMODE(s.st_mode)) != (uid,gid,mode) or xattrs(p) != attrs:
        raise ValueError('Access metadata preservation failed')
    fd=os.open(p,os.O_RDONLY|os.O_NOFOLLOW)
    try: os.fsync(fd)
    finally: os.close(fd)


def install(origin, backup, archive, expected, apply=False):
    canonical_directory(origin); canonical_directory(origin/'assets')
    original, meta=read_regular(origin/'index.html')
    require_index(original,expected)
    data=payload(archive)
    for name, blob in data.items():
        if name=='index.html': continue
        p=origin/name
        if p.exists() or p.is_symlink():
            current,s=read_regular(p)
            if current!=blob or stat.S_IMODE(s.st_mode)&0o444!=0o444:
                raise ValueError('Existing public asset collision')
    report={'ok':True,'sourceRevision':'c443ce7eb01c0a33261c049bc7071dbc90fd46d4',
            'originSha256':sha(original),'candidateSha256':FILES['index.html'][1],
            'archiveSha256':ARCHIVE_SHA,'publicFilesVerified':4,'applied':False,
            'quickLoginScriptPreserved':True,'backendChanged':False,'servicesRestarted':0,
            'oldAssetsRetainedForRollback':True,'allOldVendorBytesRemoved':False,'originalUid':meta.st_uid,
            'originalGid':meta.st_gid,'originalMode':oct(stat.S_IMODE(meta.st_mode))}
    if not apply: return report
    if sys.platform!='linux' or os.geteuid()!=0:
        raise ValueError('Linux root application required')
    canonical_directory(backup.parent)
    if backup.exists() or backup.is_symlink() or origin in backup.parents:
        raise ValueError('Exclusive private backup path required')
    attrs=xattrs(origin/'index.html')
    os.mkdir(backup,0o700)
    exclusive(backup/'index.html',original)
    record={'schema':1,'origin':str(origin),'sha256':sha(original),
            'candidateSha256':FILES['index.html'][1],
            'uid':meta.st_uid,'gid':meta.st_gid,'mode':stat.S_IMODE(meta.st_mode),
            'xattrs':{n:base64.b64encode(v).decode('ascii') for n,v in attrs.items()}}
    exclusive(backup/'metadata.json',json.dumps(record,sort_keys=True).encode())
    sync_dir(backup); sync_dir(backup.parent)
    saved,_=read_regular(backup/'index.html')
    if saved!=original: raise ValueError('Backup verification failed')
    for name,blob in data.items():
        if name=='index.html': continue
        dest=origin/name
        if dest.exists(): continue
        temp=dest.parent/('.'+dest.name+'.'+uuid.uuid4().hex)
        exclusive(temp,blob,0o644)
        os.chown(temp,meta.st_uid,meta.st_gid,follow_symlinks=False)
        os.chmod(temp,0o644,follow_symlinks=False)
        fd=os.open(temp,os.O_RDONLY|os.O_NOFOLLOW)
        try: os.fsync(fd)
        finally: os.close(fd)
        os.link(temp,dest,follow_symlinks=False)
        temp.unlink(); sync_dir(dest.parent)
    stage=origin/('.index-c443ce7-'+uuid.uuid4().hex)
    exclusive(stage,data['index.html'])
    set_metadata(stage,meta.st_uid,meta.st_gid,stat.S_IMODE(meta.st_mode),attrs)
    current,current_meta=read_regular(origin/'index.html')
    if current!=original or fingerprint(current_meta)!=fingerprint(meta) or xattrs(origin/'index.html')!=attrs:
        raise ValueError('Origin changed before atomic switch')
    for name,blob in data.items():
        if name!='index.html' and read_regular(origin/name)[0]!=blob:
            raise ValueError('New asset changed before atomic switch')
    staged,stage_meta=read_regular(stage)
    if staged!=data['index.html'] or xattrs(stage)!=attrs:
        raise ValueError('Index stage changed before atomic switch')
    os.replace(stage,origin/'index.html'); sync_dir(origin)
    final,final_meta=read_regular(origin/'index.html')
    if (final!=data['index.html'] or (final_meta.st_uid,final_meta.st_gid,stat.S_IMODE(final_meta.st_mode))
            !=(meta.st_uid,meta.st_gid,stat.S_IMODE(meta.st_mode)) or xattrs(origin/'index.html')!=attrs):
        raise ValueError('Installed index verification failed')
    return {**report,'applied':True,'backupVerified':True,'accessMetadataPreserved':True}


def rollback(origin,backup,expected):
    if sys.platform!='linux' or os.geteuid()!=0:
        raise ValueError('Linux root rollback required')
    canonical_directory(origin); canonical_directory(backup)
    bmeta=backup.lstat()
    if bmeta.st_uid!=0 or stat.S_IMODE(bmeta.st_mode)!=0o700:
        raise ValueError('Private root backup required')
    record_raw,_=read_regular(backup/'metadata.json',65536)
    record=json.loads(record_raw)
    if record.get('schema')!=1 or record.get('origin')!=str(origin) or record.get('candidateSha256')!=FILES['index.html'][1]:
        raise ValueError('Reviewed backup metadata required')
    original,_=read_regular(backup/'index.html')
    require_index(original,record['sha256'])
    current,meta=read_regular(origin/'index.html')
    require_index(current,expected,True)
    if expected!=FILES['index.html'][1]: raise ValueError('Candidate digest required')
    attrs={n:base64.b64decode(v,validate=True) for n,v in record['xattrs'].items()}
    uid,gid,mode=record['uid'],record['gid'],record['mode']
    if (uid,gid,mode)!=(meta.st_uid,meta.st_gid,stat.S_IMODE(meta.st_mode)) or xattrs(origin/'index.html')!=attrs:
        raise ValueError('Origin access metadata changed after install')
    stage=origin/('.index-rollback-'+uuid.uuid4().hex)
    exclusive(stage,original); set_metadata(stage,uid,gid,mode,attrs)
    now,now_meta=read_regular(origin/'index.html')
    if now!=current or fingerprint(now_meta)!=fingerprint(meta) or xattrs(origin/'index.html')!=attrs:
        raise ValueError('Origin changed before rollback')
    os.replace(stage,origin/'index.html'); sync_dir(origin)
    final,final_meta=read_regular(origin/'index.html')
    if (final!=original or (final_meta.st_uid,final_meta.st_gid,stat.S_IMODE(final_meta.st_mode))!=(uid,gid,mode)
            or xattrs(origin/'index.html')!=attrs):
        raise ValueError('Rollback readback failed')
    return {'ok':True,'rolledBack':True,'originSha256':record['sha256'],
            'backendChanged':False,'servicesRestarted':0,'newAssetsRetained':True}


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive',type=Path)
    parser.add_argument('--expected-origin-sha256',required=True)
    parser.add_argument('--backup-name',default='host-frontend-c443ce7-20261103')
    action=parser.add_mutually_exclusive_group()
    action.add_argument('--apply',action='store_true')
    action.add_argument('--rollback',action='store_true')
    args=parser.parse_args()
    try:
        if not re.fullmatch(r'host-frontend-c443ce7-[A-Za-z0-9-]{1,40}',args.backup_name):
            raise ValueError('Reviewed backup name required')
        backup=BACKUP_PARENT/args.backup_name
        result=rollback(ORIGIN,backup,args.expected_origin_sha256) if args.rollback else install(ORIGIN,backup,args.archive.absolute(),args.expected_origin_sha256,args.apply)
    except Exception:
        # Do not print raw exceptions, config, filenames from payloads or env.
        print(json.dumps({'ok':False,'error':'host_frontend_install_refused','inspectExistingIndexAndBackupBeforeRetry':True}))
        return 1
    print(json.dumps(result,sort_keys=True)); return 0


if __name__=='__main__':
    raise SystemExit(main())
