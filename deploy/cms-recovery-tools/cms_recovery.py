#!/usr/bin/env python3
"""Private CMS capture/restore; inspection by default, root --apply required.

Uses unmodified native CLI/daemon backup IPC. No API credentials, live engine
open, force operation, permission repair, public routing or artifact upload.
"""
import argparse
import datetime as dt
import gzip
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import socket
import stat
import subprocess
import sys
import tarfile
import uuid

REV = '8512e1704e589d24535e08082c3ab9f832d7655d'
NATIVE = Path('/opt/redx-db-website-ec6e9798/redxd-8512e17')
NATIVE_SHA = 'd99a44f1c41f87dc8f9571c6ebb4daec34948a70fb145b81bf0e134bf5403748'
SITE = Path('/opt/redx-site-c443ce7/website/red-xai.com')
SUPPORT = Path('/var/lib/red-xai-site')
KEY = Path('/etc/red-xai/site-cms.key')
HUB = 'redx-site-cms-private.service'
DB_UNIT = 'redx-db-website.service'
UNITS = ('redx-panel.service', 'redx-hub.service', 'redx-redxd.service', DB_UNIT,
         'redx-host-website.service', 'cloudflared-redx.service', 'redx-mailrelay.service')
BACKUPS = Path('/srv/red-xai/backups')
FIXTURES = Path('/srv/red-xai/cms-restore-fixtures')
ENV = {'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8'}
MODULES = {'redx-cms-store.mjs': '4c5b9187010fee5a9b384722c0dd7ab2e5bf825e2ec1811f6fac5a936d031d5d',
           'site-backend.mjs': '6095d8cb2047dba867444ebbea8a44a5d4919f447baa871301020a3095249437'}
LIMIT = 4 * 1024**3


class Refused(Exception):
    pass


def need(ok, code):
    if not ok:
        raise Refused(code)


def safe(value):
    p = Path(value)
    need(p.is_absolute() and re.fullmatch(r'/[A-Za-z0-9_./-]+', str(p))
         and '..' not in p.parts, 'absolute_safe_path_required')
    for q in (*reversed(p.parents), p):
        try:
            need(not stat.S_ISLNK(q.lstat().st_mode), 'symlink_refused')
        except FileNotFoundError:
            pass
    return p


def sha(p):
    with Path(p).open('rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest()


def code_file(p, expected=None, executable=False):
    p = safe(p)
    for parent in p.parents:
        s = parent.lstat()
        need(s.st_uid == 0 and s.st_mode & 0o022 == 0, 'untrusted_code_parent')
    s = p.lstat()
    need(stat.S_ISREG(s.st_mode) and s.st_nlink == 1 and s.st_uid == 0
         and s.st_mode & 0o022 == 0, 'root_owned_regular_code_required')
    if executable:
        need(s.st_mode & 0o111, 'executable_required')
    if expected is not None:
        need(re.fullmatch(r'[a-f0-9]{64}', expected) and sha(p) == expected, 'code_hash_mismatch')
    return s


def private_dir(p, uid=0, gid=None):
    p = safe(p)
    s = p.lstat()
    need(stat.S_ISDIR(s.st_mode) and s.st_uid == uid and stat.S_IMODE(s.st_mode) == 0o700,
         'private_directory_required')
    if gid is not None:
        need(s.st_gid == gid, 'directory_group_mismatch')
    return p


def sync_dir(p):
    if os.name != 'nt':
        fd = os.open(p, os.O_RDONLY | getattr(os, 'O_DIRECTORY', 0))
        try:
            os.fsync(fd)
        finally:
            os.close(fd)


def write(p, data, mode=0o600):
    fd = os.open(p, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, 'O_NOFOLLOW', 0), mode)
    try:
        if os.name != 'nt':
            os.fchmod(fd, mode)  # Only this newly created exclusive file.
        with os.fdopen(fd, 'wb', closefd=False) as f:
            f.write(data)
            f.flush()
            os.fsync(fd)
    finally:
        os.close(fd)
    sync_dir(Path(p).parent)


def encoded(v):
    return (json.dumps(v, sort_keys=True, separators=(',', ':')) + '\n').encode()


def run(argv, timeout=60):
    r = subprocess.run([str(x) for x in argv], stdin=subprocess.DEVNULL,
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=ENV,
                       timeout=timeout, check=False)
    need(r.returncode == 0, 'subprocess_failed_private_output_withheld')
    return r.stdout.decode('utf-8', 'strict')


def props(unit, fields):
    return dict(x.split('=', 1) for x in run(['/usr/bin/systemctl', 'show', unit,
                '--property=' + ','.join(fields)]).splitlines() if '=' in x)


def services():
    fields = ('ActiveState', 'SubState', 'MainPID', 'ExecMainStartTimestampMonotonic', 'NRestarts')
    return {u: props(u, fields) for u in UNITS}


def native_identity(data):
    p = props(DB_UNIT, ('ActiveState', 'SubState', 'MainPID', 'User'))
    need(p.get('ActiveState') == 'active' and p.get('SubState') == 'running'
         and p.get('User') == 'redx-db-site', 'native_service_mismatch')
    pid = int(p.get('MainPID', '0'))
    need(pid > 1, 'native_pid_missing')
    proc = Path('/proc') / str(pid)
    need((proc / 'exe').resolve() == NATIVE and sha(proc / 'exe') == NATIVE_SHA,
         'running_native_executable_mismatch')
    start = (proc / 'stat').read_text().rsplit(')', 1)[1].split()[19]
    targets = []
    for fd in (proc / 'fd').iterdir():
        try:
            targets.append(os.readlink(fd))
        except FileNotFoundError:
            pass
    need(str(data / 'redxd.lock') in targets, 'native_data_lock_fd_mismatch')
    import fcntl
    with (data / 'redxd.lock').open('rb') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            pass
        else:
            fcntl.flock(lock, fcntl.LOCK_UN)
            raise Refused('live_native_lock_not_held')
    return {'pid': pid, 'startTicks': start, 'sha256': NATIVE_SHA}


def stopped():
    p = props(HUB, ('ActiveState', 'MainPID', 'Result'))
    need(p.get('ActiveState') == 'inactive' and p.get('MainPID') == '0'
         and p.get('Result') == 'success', 'private_gateway_not_gracefully_stopped')
    with socket.socket() as s:
        s.settimeout(2)
        need(s.connect_ex(('127.0.0.1', 14210)) == 111, 'gateway_listener_or_unknown')
    need(not (SUPPORT / 'writer.lock').exists(), 'cms_writer_lock_present')


def inventory(root):
    root = Path(root)
    s = root.lstat()
    need(stat.S_ISDIR(s.st_mode) and not stat.S_ISLNK(s.st_mode)
         and stat.S_IMODE(s.st_mode) == 0o700, 'support_root_not_private')
    files, dirs = {}, []
    for p in sorted(root.rglob('*')):
        rel = p.relative_to(root).as_posix()
        s = p.lstat()
        need(rel != 'writer.lock' and not stat.S_ISLNK(s.st_mode), 'support_link_or_lock')
        need(s.st_mode & 0o077 == 0, 'support_member_not_private')
        if stat.S_ISDIR(s.st_mode):
            dirs.append(rel)
            continue
        need(stat.S_ISREG(s.st_mode) and s.st_nlink == 1, 'support_special_or_hardlink')
        fd = os.open(p, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0))
        try:
            before = os.fstat(fd)
            with os.fdopen(fd, 'rb', closefd=False) as f:
                digest = hashlib.file_digest(f, 'sha256').hexdigest()
            after = os.fstat(fd)
            shape = lambda v: (v.st_dev, v.st_ino, v.st_size, v.st_mtime_ns)
            need(shape(s) == shape(before) == shape(after), 'support_changed_during_hash')
        finally:
            os.close(fd)
        files[rel] = {'sha256': digest, 'bytes': s.st_size, 'mode': stat.S_IMODE(s.st_mode)}
    need({'audit.jsonl', 'maintenance-probe-v1.json'}.issubset(files) and 'media' in dirs,
         'maintenance_evidence_missing')
    return {'files': files, 'directories': dirs}


def support_archive(root, out, expected):
    with Path(out).open('xb') as f:
        os.chmod(out, 0o600)
        with tarfile.open(fileobj=f, mode='w:gz', dereference=False) as tar:
            for rel in expected['directories']:
                info = tarfile.TarInfo(rel + '/')
                info.type, info.mode = tarfile.DIRTYPE, 0o700
                tar.addfile(info)
            for rel, record in expected['files'].items():
                fd = os.open(Path(root) / rel, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0))
                with os.fdopen(fd, 'rb') as source:
                    s = os.fstat(source.fileno())
                    need(stat.S_ISREG(s.st_mode) and s.st_nlink == 1 and s.st_size == record['bytes'],
                         'support_changed_before_archive')
                    info = tarfile.TarInfo(rel)
                    info.size, info.mode = s.st_size, record['mode']
                    tar.addfile(info, source)
        f.flush()
        os.fsync(f.fileno())
    sync_dir(Path(out).parent)
    need(inventory(root) == expected, 'support_changed_during_archive')
    verify_support_archive(out, expected)


def members(path, native):
    total = 0
    with gzip.open(path, 'rb') as f:
        while block := f.read(1024 * 1024):
            total += len(block)
            need(total <= LIMIT, 'archive_expanded_limit')
    allowed = {'keys.json', 'ttl.json', 'tenants.json', 'updates/signing.key', 'updates/signing.key.meta'}
    seen, dbs, metadata, entries = set(), set(), [], []
    with tarfile.open(path, 'r:gz') as tar:
        for item in tar:
            name = item.name.rstrip('/')
            p = PurePosixPath(name)
            need(name and name == p.as_posix() and not p.is_absolute() and '..' not in p.parts and '\\' not in name
                 and ':' not in name and '\0' not in name and name not in seen
                 and (item.isdir() or item.isreg()), 'unsafe_archive_member')
            need(len(seen) < 100000, 'archive_entry_limit')
            seen.add(name)
            if native:
                if len(p.parts) >= 2 and p.parts[0] == 'dbs':
                    dbs.add(p.parts[1])
                else:
                    need(name in allowed and item.isreg(), 'unexpected_native_layout')
                    metadata.append(name)
            entries.append((name, item.isdir(), item.size))
    if native:
        need(len(dbs) >= 5 and {'website_cms', 'fl_studio_db'}.issubset(dbs)
             and {'keys.json', 'ttl.json'}.issubset(seen), 'native_database_coverage')
    return {'databases': sorted(dbs), 'metadata': sorted(metadata), 'entries': entries}


def verify_support_archive(archive, expected):
    entries = members(archive, False)['entries']
    need({x[0] for x in entries} == set(expected['files']) | set(expected['directories']),
         'support_archive_inventory_mismatch')
    with tarfile.open(archive, 'r:gz') as tar:
        for item in tar:
            name = item.name.rstrip('/')
            if item.isdir():
                need(name in expected['directories'], 'support_directory_type_mismatch')
            else:
                need(name in expected['files'], 'support_file_type_mismatch')
                record = expected['files'][name]
                with tar.extractfile(item) as f:
                    need(item.size == record['bytes'] and hashlib.file_digest(f, 'sha256').hexdigest() == record['sha256'],
                         'support_archive_payload_hash_mismatch')


def extract_support(archive, dest, expected):
    verify_support_archive(archive, expected)
    entries = members(archive, False)['entries']
    need({x[0] for x in entries} == set(expected['files']) | set(expected['directories']),
         'support_archive_inventory_mismatch')
    dest.mkdir(mode=0o700)
    with tarfile.open(archive, 'r:gz') as tar:
        for item in tar:
            name = item.name.rstrip('/')
            p = dest / name
            need(p.resolve().is_relative_to(dest.resolve()), 'support_extract_escape')
            if item.isdir():
                need(name in expected['directories'], 'support_directory_type_mismatch')
                p.mkdir(mode=0o700, parents=True, exist_ok=True)
            else:
                need(name in expected['files'], 'support_file_type_mismatch')
                p.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                with tar.extractfile(item) as f:
                    mode = expected['files'][name]['mode']
                    fd = os.open(p, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, 'O_NOFOLLOW', 0), mode)
                    with os.fdopen(fd, 'wb') as target:
                        if os.name != 'nt':
                            os.fchmod(target.fileno(), mode)
                        shutil.copyfileobj(f, target, 1024 * 1024)
                        target.flush()
                        os.fsync(target.fileno())
                    sync_dir(p.parent)
    need(inventory(dest) == expected, 'restored_support_hash_mismatch')


def probe_verify(a):
    value = json.loads(run(['/usr/sbin/runuser', '-u', 'redx-site', '--', '/usr/bin/env', '-i',
                           'PATH=/usr/bin:/bin', 'NODE_ENV=production', '/usr/bin/node', a.probe,
                           'verify', '--private-gateway-stopped']))
    need(value.get('ok') is True and value.get('writerLockReleased') is True
         and value.get('cmsRevisionReadback') == 4 and value.get('unpublishedDraft') is True
         and value.get('syntheticPreferenceKeys') == 2 and value.get('realWebpDecoded') is True
         and value.get('websiteOwnerApiVerified') is False
         and value.get('normalAccountIsolationVerified') is False
         and value.get('backupRestoreVerified') is False, 'service_fixture_verification_failed')
    return value


def preflight(a):
    need(sys.platform == 'linux' and os.geteuid() == 0, 'linux_root_required')
    import pwd
    native_user, site_user = pwd.getpwnam('redx-db-site'), pwd.getpwnam('redx-site')
    need((site_user.pw_uid, site_user.pw_gid) == (986, 976), 'site_identity_mismatch')
    code_file(NATIVE, NATIVE_SHA, True)
    code_file(a.cli, a.cli_sha256, True)
    code_file(a.probe, a.probe_sha256)
    for name, digest in MODULES.items():
        code_file(SITE / name, digest)
    data = safe(a.live_data_dir)
    s = data.lstat()
    need(stat.S_ISDIR(s.st_mode) and s.st_uid == native_user.pw_uid and s.st_mode & 0o077 == 0,
         'native_data_owner_or_mode')
    private_dir(SUPPORT, 986, 976)
    key = safe(KEY).lstat()  # Metadata only; this Python tool never opens the key.
    need(stat.S_ISREG(key.st_mode) and key.st_nlink == 1 and key.st_uid == 986
         and key.st_gid == 976 and stat.S_IMODE(key.st_mode) == 0o600, 'key_metadata_mismatch')
    hub = props(HUB, ('ActiveState', 'User', 'Group', 'WorkingDirectory', 'FragmentPath'))
    need(hub.get('ActiveState') in ('active', 'inactive') and hub.get('User') == 'redx-site'
         and hub.get('Group') == 'redx-site' and hub.get('WorkingDirectory') == str(SITE)
         and hub.get('FragmentPath') == '/etc/systemd/system/' + HUB, 'private_hub_mismatch')
    identity = native_identity(data)
    ipc = safe(data / 'backup')
    if ipc.exists():
        private_dir(ipc, native_user.pw_uid, native_user.pw_gid)
    elif a.ipc_user == 'root':
        raise Refused('root_cli_would_create_daemon_unreadable_ipc_directory')
    if a.ipc_user == 'native':
        run(['/usr/sbin/runuser', '-u', 'redx-db-site', '--', '/usr/bin/test', '-x', a.cli])
    return data, identity, hub


def resumed_health():
    import urllib.request
    p = props(HUB, ('ActiveState', 'SubState', 'MainPID'))
    need(p.get('ActiveState') == 'active' and p.get('SubState') == 'running'
         and int(p.get('MainPID', '0')) > 1, 'private_gateway_resume_failed')
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args, **kwargs):
            return None
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    with opener.open('http://127.0.0.1:14210/api/site/health', timeout=10) as response:
        value = json.loads(response.read(16385))
        need(response.status == 200 and value.get('ok') is True
             and value.get('cmsStorage') == 'native-redx-single-writer', 'private_gateway_health_failed')


def capture(a):
    data, identity, hub = preflight(a)
    baseline = services()
    if not a.apply:
        return {'ok': True, 'inspectionOnly': True, 'nativeRevision': REV,
                'nativeSha256': NATIVE_SHA, 'ipcUser': a.ipc_user, 'serviceChanges': False}
    need(a.exclusive_writer_reviewed, 'exclusive_writer_attestation_required')
    private_dir(BACKUPS)
    bundle = BACKUPS / ('cms-pair-' + dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ-') + uuid.uuid4().hex)
    bundle.mkdir(mode=0o700)
    should_resume = hub['ActiveState'] == 'active'
    try:
        if should_resume:
            # Arm restoration before the command: it may stop the unit and then fail.
            run(['/usr/bin/systemctl', 'stop', HUB])
        stopped()
        before = probe_verify(a)
        stopped()
        support = inventory(SUPPORT)
        begin = dt.datetime.now(dt.timezone.utc).isoformat()
        output = bundle / 'native.tgz'
        command = [a.cli, 'backup', '--offline', '--data-dir', data, '--out', output]
        temporary = None
        if a.ipc_user == 'native':
            temporary = data / ('.cms-capture-' + uuid.uuid4().hex + '.tgz')
            command[-1] = temporary
            command = ['/usr/sbin/runuser', '-u', 'redx-db-site', '--', '/usr/bin/env', '-i',
                       'PATH=/usr/bin:/bin', *command]
        need(native_identity(data) == identity, 'native_process_changed_before_capture')
        run(command, timeout=180)
        need(native_identity(data) == identity, 'native_process_changed_during_capture')
        if temporary is not None:
            s = temporary.lstat()
            need(stat.S_ISREG(s.st_mode) and s.st_nlink == 1 and stat.S_IMODE(s.st_mode) == 0o600,
                 'native_export_metadata_mismatch')
            with temporary.open('rb') as source, output.open('xb') as dest:
                shutil.copyfileobj(source, dest)
                dest.flush()
                os.fsync(dest.fileno())
            need(sha(output) == sha(temporary), 'private_native_copy_hash_mismatch')
            sync_dir(bundle)
            # Only this exclusive task-created export is removed, after durable copy.
            temporary.unlink()
            sync_dir(data)
        os.chmod(output, 0o600)
        sync_dir(bundle)
        layout = members(output, True)
        support_archive(SUPPORT, bundle / 'support.tgz', support)
        after = probe_verify(a)
        stopped()
        need(before == after and inventory(SUPPORT) == support, 'cms_pair_changed_during_capture')
        need(native_identity(data) == identity and services() == baseline,
             'unrelated_service_changed_during_capture')
        pair = {'schema': 1, 'kind': 'cms-native-support-pair', 'nativeRevision': REV,
                'nativeBinarySha256': NATIVE_SHA, 'nativeProcess': identity,
                'nativeCliSha256': a.cli_sha256, 'nativeCliProvenance': 'parent-reviewed-same-revision-build',
                'nativeCapture': 'unmodified-native-cli-offline-coordinated-daemon-ipc',
                'ipcUser': a.ipc_user, 'beginUtc': begin, 'endUtc': dt.datetime.now(dt.timezone.utc).isoformat(),
                'databases': layout['databases'], 'nativeMetadata': layout['metadata'],
                'support': support, 'probe': after, 'probeSha256': a.probe_sha256,
                'artifacts': {name: {'sha256': sha(bundle / name), 'bytes': (bundle / name).stat().st_size}
                              for name in ('native.tgz', 'support.tgz')},
                'fullAuditSha256': support['files']['audit.jsonl']['sha256'],
                'liveNativeRestarted': False, 'publicRoutingChanged': False}
        write(bundle / 'pair.json', encoded(pair))
    finally:
        # Never delete a writer lock or restore support over newer writes.
        if should_resume:
            need(not (SUPPORT / 'writer.lock').exists(), 'resume_refused_writer_lock_present')
            run(['/usr/bin/systemctl', 'start', HUB])
            resumed_health()
    need(services() == baseline, 'unrelated_service_changed_after_resume')
    need(native_identity(data) == identity, 'native_process_changed_after_resume')
    return {'ok': True, 'pairDirectory': str(bundle), 'pairSha256': sha(bundle / 'pair.json'),
            'databaseCount': len(pair['databases']), 'flStudioPreserved': True,
            'fullAuditPaired': True, 'gatewayResumed': should_resume,
            'nativeProcessUnchanged': True}


def load_pair(bundle, digest):
    private_dir(bundle)
    code_file(bundle / 'pair.json', digest)
    pair = json.loads((bundle / 'pair.json').read_text())
    need(pair.get('schema') == 1 and pair.get('kind') == 'cms-native-support-pair'
         and pair.get('nativeRevision') == REV and pair.get('nativeBinarySha256') == NATIVE_SHA,
         'pair_manifest_mismatch')
    for name in ('native.tgz', 'support.tgz'):
        record = pair['artifacts'][name]
        code_file(bundle / name, record['sha256'])
        need((bundle / name).stat().st_size == record['bytes'], 'artifact_size_mismatch')
    need(members(bundle / 'native.tgz', True)['databases'] == pair['databases'],
         'pair_database_inventory_mismatch')
    return pair


def unit_text(fixture):
    fixture = safe(fixture)
    binds = ['/usr:/usr']
    for name in ('/lib', '/lib64', '/etc/ld.so.cache'):
        if Path(name).exists():
            binds.append(str(Path(name).resolve()) + ':' + name)
    binds += [SITE.as_posix() + ':/site', (fixture / 'tools').as_posix() + ':/tools', KEY.as_posix() + ':/credential']
    return ('[Unit]\nDescription=Isolated CMS restored-copy verification\n\n[Service]\nType=oneshot\n'
            'User=redx-site\nGroup=redx-site\nUMask=0077\nRootDirectory=' + (fixture / 'jail').as_posix() + '\n'
            'WorkingDirectory=/work\nExecStart=/usr/bin/env -i PATH=/usr/bin:/bin NODE_ENV=production /usr/bin/node /tools/restore-driver.mjs\n'
            'BindReadOnlyPaths=' + ' '.join(binds) + '\nBindPaths=' + (fixture / 'work').as_posix() + ':/work\n'
            'PrivateNetwork=yes\nPrivateTmp=yes\nPrivateDevices=yes\nProtectSystem=strict\nProtectHome=yes\nInaccessiblePaths=-/proc -/sys\n'
            'ReadWritePaths=/work\nNoNewPrivileges=yes\nCapabilityBoundingSet=\nRestrictSUIDSGID=yes\n'
            'ProtectKernelTunables=yes\nProtectKernelModules=yes\nProtectControlGroups=yes\n'
            'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6\nIPAddressDeny=any\nIPAddressAllow=localhost\n'
            'MemoryMax=1G\nTasksMax=128\nStandardOutput=null\nStandardError=null\nTimeoutStartSec=120\nTimeoutStopSec=20\nKillMode=control-group\n')


def restore(a):
    need(sys.platform == 'linux' and os.geteuid() == 0, 'linux_root_required')
    bundle = safe(a.pair)
    pair = load_pair(bundle, a.pair_sha256)
    code_file(a.cli, a.cli_sha256, True)
    need(a.cli_sha256 == pair['nativeCliSha256'], 'restore_cli_differs_from_capture')
    code_file(NATIVE, NATIVE_SHA, True)
    code_file(a.probe, pair['probeSha256'])
    for name, digest in MODULES.items():
        code_file(SITE / name, digest)
    driver = Path(__file__).with_name('restore-driver.mjs')
    code_file(driver, a.driver_sha256)
    key = safe(KEY).lstat()  # No plaintext key read/copy by this coordinator.
    need(stat.S_ISREG(key.st_mode) and key.st_nlink == 1 and key.st_uid == 986
         and key.st_gid == 976 and stat.S_IMODE(key.st_mode) == 0o600, 'key_metadata_mismatch')
    if not a.apply:
        return {'ok': True, 'inspectionOnly': True, 'pairVerified': True, 'serviceChanges': False}
    if not FIXTURES.exists():
        parent = safe(FIXTURES.parent).lstat()
        need(stat.S_ISDIR(parent.st_mode) and parent.st_uid == 0 and parent.st_mode & 0o022 == 0,
             'fixture_parent_not_root_protected')
        FIXTURES.mkdir(mode=0o700)
    private_dir(FIXTURES)
    fixture = FIXTURES / ('cms-restore-' + uuid.uuid4().hex)
    fixture.mkdir(mode=0o700)
    work, tools, jail = fixture / 'work', fixture / 'tools', fixture / 'jail'
    work.mkdir(mode=0o700)
    tools.mkdir(mode=0o755)
    jail.mkdir(mode=0o755)
    os.chmod(tools, 0o755)
    os.chmod(jail, 0o755)
    for name in ('work', 'site', 'tools', 'usr', 'lib', 'lib64', 'etc', 'tmp', 'var', 'var/tmp', 'proc', 'sys', 'dev'):
        (jail / name).mkdir(mode=0o755)
        os.chmod(jail / name, 0o755)
    write(jail / 'credential', b'')
    write(jail / 'etc/ld.so.cache', b'', 0o644)
    run([a.cli, 'restore', bundle / 'native.tgz', '--data-dir', work / 'native'], timeout=180)
    dbs = work / 'native/dbs'
    need(sorted(p.name for p in dbs.iterdir() if p.is_dir()) == pair['databases'],
         'restored_database_inventory_mismatch')
    extract_support(bundle / 'support.tgz', work / 'support', pair['support'])
    config = ('data_dir="/work/native"\nlisten="127.0.0.1:17791"\nadvertise="http://127.0.0.1:17791"\n'
              'node_id="cms-restore-drill"\nrole="leader"\npeers=[]\ntls="off"\nbootstrap="off"\n'
              'engine_durability="group"\naudit_log="/work/native-fixture-audit.jsonl"\n'
              'accounts_issuer=""\naccounts_events_poll_seconds=0\ngithub_repo=""\ngithub_repos=[]\nhosted_apps=[]\n')
    write(work / 'fixture.toml', config.encode())
    pair['restoreDriverSha256'] = a.driver_sha256
    pair['moduleSha256'] = MODULES
    write(work / 'expected.json', encoded(pair))
    for source, name, digest in ((NATIVE, 'redxd', NATIVE_SHA),
                                  (Path(a.probe), 'probe.mjs', pair['probeSha256']),
                                  (driver, 'restore-driver.mjs', a.driver_sha256)):
        write(tools / name, source.read_bytes(), 0o555 if name == 'redxd' else 0o444)
        need(sha(tools / name) == digest, 'fixture_code_copy_mismatch')
    # Resolve bounds before changing ownership of NEW fixture copies only.
    need(fixture.resolve().parent == FIXTURES.resolve() and fixture.name.startswith('cms-restore-'),
         'fixture_bounds_mismatch')
    for p in [work, *work.rglob('*')]:
        need(not p.is_symlink(), 'restored_symlink_refused')
        os.chown(p, 986, 976)
    unit = 'redx-cms-restore-' + uuid.uuid4().hex + '.service'
    unit_path = fixture / unit
    write(unit_path, unit_text(fixture).encode())
    run(['/usr/bin/systemd-analyze', 'verify', unit_path])
    runtime = Path('/run/systemd/system') / unit
    baseline = services()
    write(runtime, unit_path.read_bytes(), 0o644)
    try:
        run(['/usr/bin/systemctl', 'daemon-reload'])
        run(['/usr/bin/systemctl', 'start', unit], timeout=150)
        p = props(unit, ('ActiveState', 'MainPID', 'Result', 'ExecMainStatus'))
        need(p.get('ActiveState') == 'inactive' and p.get('MainPID') == '0'
             and p.get('Result') == 'success' and p.get('ExecMainStatus') == '0', 'fixture_unit_failed')
    finally:
        # These operations can target only this exclusive newly created unit.
        run(['/usr/bin/systemctl', 'stop', unit])
        runtime.unlink()
        sync_dir(runtime.parent)
        run(['/usr/bin/systemctl', 'daemon-reload'])
    need(services() == baseline, 'live_service_changed_during_restore')
    result_path = work / 'restore-result.json'
    s = result_path.lstat()
    need(stat.S_ISREG(s.st_mode) and s.st_nlink == 1 and s.st_mode & 0o077 == 0,
         'fixture_result_not_private_regular')
    value = json.loads(result_path.read_text())
    need(value.get('ok') is True and value.get('backupRestoreVerified') is True
         and value.get('fullSupportMatched') is True and value.get('daemonStopped') is True,
         'restored_application_verification_failed')
    return {'ok': True, 'fixtureDirectory': str(fixture), 'resultSha256': sha(result_path),
            'backupRestoreVerified': True, 'fullSupportMatched': True, 'liveServicesUnchanged': True,
            'fixtureStopped': True, 'artifactsRetained': True}


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='action', required=True)
    cap = sub.add_parser('capture')
    cap.add_argument('--live-data-dir', required=True)
    cap.add_argument('--ipc-user', choices=('native', 'root'), default='native')
    cap.add_argument('--exclusive-writer-reviewed', action='store_true')
    rest = sub.add_parser('restore')
    rest.add_argument('--pair', required=True)
    rest.add_argument('--pair-sha256', required=True)
    rest.add_argument('--driver-sha256', required=True)
    for p in (cap, rest):
        p.add_argument('--cli', required=True)
        p.add_argument('--cli-sha256', required=True)
        p.add_argument('--probe', required=True)
        if p is cap:
            p.add_argument('--probe-sha256', required=True)
        p.add_argument('--apply', action='store_true')
    a = parser.parse_args()
    try:
        print(json.dumps(capture(a) if a.action == 'capture' else restore(a), sort_keys=True))
    except Exception as exc:
        code = str(exc) if isinstance(exc, Refused) else 'operation_failed_private_details_withheld'
        print(json.dumps({'ok': False, 'code': code, 'partialEvidenceRetained': True,
                          'privateOutputWithheld': True}), file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
