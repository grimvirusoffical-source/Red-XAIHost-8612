#!/usr/bin/env python3
"""Capture one explicitly selected Host SQLite ledger, never a full recovery."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import posixpath
import re
try:
    import sqlite3
except ImportError:
    sqlite3 = None
import stat
import sys
import time

SCHEMA = 'redx.host-sqlite-backup.v1'
MAX_JSON = 16384
PAGES = 64
MAX_BATCH = PAGES * 65536


class Refusal(Exception):
    def __init__(self, code):
        self.code = code


def require(condition, code):
    if not condition:
        raise Refusal(code)


def runtime_info():
    return {'python': '.'.join(map(str, sys.version_info[:3])), 'sqlite': sqlite3.sqlite_version if sqlite3 is not None else 'unavailable'}


def exact(value, keys):
    require(isinstance(value, dict) and set(value) == set(keys), 'invalid_expectations')


def integer(value, minimum=0, maximum=(1 << 64) - 1):
    require(type(value) is int and minimum <= value <= maximum, 'invalid_expectations')


def canonical_text(value):
    require(isinstance(value, str) and 0 < len(value) <= 4096, 'invalid_expectations')
    require(value.startswith('/') and posixpath.normpath(value) == value and not value.startswith('//'), 'invalid_expectations')
    require(not any(ord(c) < 32 or ord(c) == 127 for c in value), 'invalid_expectations')
    try:
        value.encode('utf-8', 'strict')
    except UnicodeError:
        raise Refusal('invalid_expectations') from None


def identity(value, path=True):
    exact(value, ['path', 'device', 'inode'] if path else ['number', 'device', 'inode'])
    integer(value['device']); integer(value['inode'], 1)
    if path:
        canonical_text(value['path'])
    else:
        integer(value['number'], 0, 1048576)


def parse_expectations(payload):
    require(isinstance(payload, bytes) and 0 < len(payload) <= MAX_JSON, 'invalid_expectations')
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, 'invalid_expectations')
            result[key] = value
        return result
    try:
        value = json.loads(payload.decode('utf-8'), object_pairs_hook=unique,
                           parse_constant=lambda _: (_ for _ in ()).throw(Refusal('invalid_expectations')))
    except (ValueError, UnicodeError, RecursionError):
        raise Refusal('invalid_expectations') from None
    exact(value, ['version', 'source', 'host', 'reader'])
    require(type(value['version']) is int and value['version'] == 1, 'invalid_expectations')
    identity(value['source'])
    host = value['host']; exact(host, ['boot_id', 'pid', 'start_ticks', 'exe', 'cwd', 'ledger_fd'])
    require(isinstance(host['boot_id'], str) and re.fullmatch(r'[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}', host['boot_id']), 'invalid_expectations')
    integer(host['pid'], 1, 2147483647); integer(host['start_ticks'], 1)
    identity(host['exe']); identity(host['cwd']); identity(host['ledger_fd'], False)
    require((host['ledger_fd']['device'], host['ledger_fd']['inode']) == (value['source']['device'], value['source']['inode']), 'invalid_expectations')
    exact(value['reader'], ['python', 'sqlite'])
    require(all(isinstance(x, str) and re.fullmatch(r'\d+\.\d+\.\d+', x) for x in value['reader'].values()), 'invalid_expectations')
    return value


def read_bounded(path, limit):
    with open(path, 'rb') as stream:
        result = stream.read(limit + 1)
    require(len(result) <= limit, 'metadata_unavailable')
    return result


def load_expectations(path):
    canonical_text(path)
    require(os.path.realpath(path) == path, 'invalid_expectations')
    trusted_directories(posixpath.dirname(path), {0})
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        info = os.fstat(fd)
        require(stat.S_ISREG(info.st_mode) and info.st_uid == 0 and info.st_nlink == 1 and info.st_mode & 0o077 == 0 and 0 < info.st_size <= MAX_JSON, 'invalid_expectations')
        payload = os.read(fd, MAX_JSON + 1)
        after = os.fstat(fd)
        fields = ('st_dev', 'st_ino', 'st_size', 'st_mode', 'st_uid', 'st_gid', 'st_nlink', 'st_mtime_ns', 'st_ctime_ns')
        require(len(payload) == info.st_size and all(getattr(info, name) == getattr(after, name) for name in fields), 'invalid_expectations')
        return parse_expectations(payload), hashlib.sha256(payload).hexdigest()
    finally:
        os.close(fd)  # Before any source SQLite connection exists.


def parse_proc_start_ticks(text):
    require(isinstance(text, str) and len(text) <= 65536 and re.match(r'^\d+ \(', text), 'metadata_unavailable')
    end = text.rfind(')')
    fields = text[end + 1:].split() if end >= 0 else []
    require(len(fields) >= 20 and re.fullmatch(r'\d+', fields[19]), 'metadata_unavailable')
    return int(fields[19])


def matches(info, expected):
    return (info.st_dev, info.st_ino) == (expected['device'], expected['inode'])


def trusted_directories(path, owners):
    current = '/'
    for part in [''] + path.strip('/').split('/'):
        if part: current = posixpath.join(current, part)
        info = os.lstat(current)
        require(stat.S_ISDIR(info.st_mode) and info.st_uid in owners, 'unsafe_namespace')
        require(info.st_mode & 0o022 == 0 or (info.st_uid == 0 and info.st_mode & stat.S_ISVTX), 'unsafe_namespace')


def verify_sidecars(path, source_info):
    identities = {(source_info.st_dev, source_info.st_ino)}
    for suffix in ('-wal', '-shm', '-journal'):
        name = path + suffix
        try:
            info = os.lstat(name)
        except FileNotFoundError:
            continue
        require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and os.path.realpath(name) == name and info.st_uid in {0, source_info.st_uid} and info.st_mode & 0o022 == 0, 'unsafe_source_sidecar')
        value = (info.st_dev, info.st_ino)
        require(value not in identities, 'unsafe_source_sidecar'); identities.add(value)


def verify_identity(expected):
    source = expected['source']; host = expected['host']; path = source['path']
    require(os.path.realpath(path) == path, 'source_identity_changed')
    info = os.lstat(path)
    require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and matches(info, source), 'source_identity_changed')
    require(info.st_mode & 0o022 == 0, 'unsafe_namespace')
    parent_info = os.lstat(posixpath.dirname(path))
    require(stat.S_ISDIR(parent_info.st_mode) and parent_info.st_uid in {0, info.st_uid} and parent_info.st_mode & 0o022 == 0, 'unsafe_namespace')
    trusted_directories(posixpath.dirname(path), {0, info.st_uid})
    verify_sidecars(path, info)
    require(read_bounded('/proc/sys/kernel/random/boot_id', 128).decode('ascii').strip() == host['boot_id'], 'host_identity_changed')
    root = '/proc/' + str(host['pid'])
    require(parse_proc_start_ticks(read_bounded(root + '/stat', 65536).decode('utf-8')) == host['start_ticks'], 'host_identity_changed')
    for name, directory in [('exe', False), ('cwd', True)]:
        target = root + '/' + name; pin = host[name]; observed = os.stat(target)
        require(os.readlink(target) == pin['path'] and matches(observed, pin) and (stat.S_ISDIR(observed.st_mode) if directory else stat.S_ISREG(observed.st_mode)), 'host_identity_changed')
    target = root + '/fd/' + str(host['ledger_fd']['number'])
    require(os.readlink(target) == path and matches(os.stat(target), host['ledger_fd']), 'host_identity_changed')
    return info


def utc():
    return datetime.now(timezone.utc).isoformat()


def run(args):
    args.cleanup_warning = False
    require(sys.platform == 'linux' and hasattr(os, 'geteuid') and os.geteuid() == 0, 'linux_root_required')
    require(sys.version_info >= (3, 7) and sqlite3 is not None and sqlite3.sqlite_version_info >= (3, 22, 0), 'unsupported_reader')
    expected, expectation_hash = load_expectations(args.expectations)
    require(expected['host']['pid'] != os.getpid(), 'same_process_refused')
    require(runtime_info() == expected['reader'], 'reader_version_mismatch')
    try:
        maximum, reserve = int(args.max_output_bytes), int(args.reserve_free_bytes)
        duration = float(args.deadline_seconds)
    except (ValueError, OverflowError):
        raise Refusal('invalid_arguments') from None
    require(65536 <= maximum <= (1 << 40) and 0 <= reserve <= (1 << 50) and math.isfinite(duration) and 0 < duration <= 900, 'invalid_arguments')
    output = args.output_dir; canonical_text(output)
    require(os.path.realpath(output) == output and not os.path.lexists(output), 'output_unavailable')
    parent = os.path.dirname(output); parent_info = os.stat(parent)
    require(stat.S_ISDIR(parent_info.st_mode) and parent_info.st_uid == 0 and parent_info.st_mode & 0o022 == 0, 'output_unavailable')
    trusted_directories(parent, {0})
    deadline = time.monotonic() + duration
    def check_deadline():
        require(time.monotonic() < deadline, 'deadline_exceeded')
    def space(path, needed):
        vfs = os.statvfs(path)
        require(vfs.f_bavail * vfs.f_frsize >= needed and vfs.f_favail > 8, 'insufficient_space')
    verify_identity(expected); check_deadline(); space(parent, 2 * maximum + reserve + MAX_BATCH)
    if not args.capture:
        return 'inspected'
    parent_fd = os.open(parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    bundle_fd = anchor = artifact_fd = None
    source = target = validation = None
    created = completed = False; bundle_identity = None
    stage = 'bundle'; failure_code = 'operation_failed'
    old_umask = os.umask(0o077)
    def bundle_check():
        trusted_directories(output, {0})
        require(created and os.path.realpath(output) == output and matches(os.lstat(output), bundle_identity) and matches(os.fstat(bundle_fd), bundle_identity), 'output_identity_changed')
        for info in (os.lstat(output), os.fstat(bundle_fd)):
            require(info.st_uid == 0 and stat.S_IMODE(info.st_mode) == 0o700, 'output_identity_changed')
    def private_write(name, payload):
        fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=bundle_fd)
        try:
            with os.fdopen(fd, 'wb', closefd=False) as stream:
                stream.write(payload); stream.flush(); os.fsync(fd)
        finally:
            os.close(fd)
    try:
        parent_pin = {'device': parent_info.st_dev, 'inode': parent_info.st_ino}
        require(matches(os.fstat(parent_fd), parent_pin) and matches(os.stat(parent), parent_pin), 'output_identity_changed')
        os.mkdir(os.path.basename(output), 0o700, dir_fd=parent_fd); created = True
        bundle_fd = os.open(os.path.basename(output), os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent_fd)
        observed = os.fstat(bundle_fd); bundle_identity = {'device': observed.st_dev, 'inode': observed.st_ino}
        require(observed.st_uid == 0 and stat.S_IMODE(observed.st_mode) == 0o700, 'output_unavailable')
        private_write('INCOMPLETE', b'redx-host-sqlite-backup-incomplete-v1\n'); os.fsync(bundle_fd); os.fsync(parent_fd)
        verify_identity(expected); check_deadline(); bundle_check()
        stage = 'source_open'
        anchor = os.open(expected['source']['path'], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        require(stat.S_ISREG(os.fstat(anchor).st_mode) and matches(os.fstat(anchor), expected['source']), 'source_identity_changed')
        source = sqlite3.connect(Path(expected['source']['path']).as_uri() + '?mode=ro&cache=private', uri=True, timeout=0.1, isolation_level=None)
        verify_identity(expected); check_deadline()
        stage = 'metadata'
        mode = source.execute('PRAGMA journal_mode').fetchone()[0]
        page_size = source.execute('PRAGMA page_size').fetchone()[0]
        initial_pages = source.execute('PRAGMA page_count').fetchone()[0]
        require(mode in {'wal', 'delete', 'truncate', 'persist'} and type(page_size) is int and 512 <= page_size <= 65536 and page_size & (page_size - 1) == 0, 'unsupported_source')
        require(type(initial_pages) is int and initial_pages >= 0 and initial_pages * page_size <= maximum, 'output_limit_exceeded')
        check_deadline(); stage = 'destination_open'
        artifact_fd = os.open('ledger.sqlite3', os.O_RDWR | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=bundle_fd)
        artifact_info = os.fstat(artifact_fd)
        require(not matches(artifact_info, expected['source']), 'output_unavailable')
        artifact = os.path.join(output, 'ledger.sqlite3')
        artifact_pin = {'device': artifact_info.st_dev, 'inode': artifact_info.st_ino}
        target = sqlite3.connect(Path(artifact).as_uri() + '?mode=rw&cache=private', uri=True, timeout=0.1, isolation_level=None)
        require(target.execute('PRAGMA journal_mode=DELETE').fetchone()[0] == 'delete', 'destination_journal_failed')
        target.execute('PRAGMA synchronous=FULL')
        require(not target.in_transaction, 'destination_transaction_open')
        iterations = 0
        def progress(status, remaining, total):
            nonlocal iterations
            iterations += 1; check_deadline(); bundle_check(); verify_identity(expected)
            require(status in (0, 101, 5, 6), 'backup_failed')  # OK, DONE, BUSY, LOCKED.
            require(total >= 0 and total * page_size <= maximum and os.fstat(artifact_fd).st_size <= maximum, 'output_limit_exceeded')
            require(matches(os.stat(artifact), artifact_pin) and matches(os.fstat(artifact_fd), artifact_pin), 'output_identity_changed')
            space(output, reserve + 2 * MAX_BATCH)
        stage = 'backup'
        started = utc(); source.backup(target, pages=PAGES, progress=progress, sleep=0.05); finished = utc()
        check_deadline(); verify_identity(expected); bundle_check()
        require(source.execute('PRAGMA page_size').fetchone()[0] == page_size and source.execute('PRAGMA journal_mode').fetchone()[0] == mode, 'source_metadata_changed')
        stage = 'finalize'
        require(target.execute('PRAGMA journal_mode=DELETE').fetchone()[0] == 'delete', 'destination_journal_failed')
        target.close(); target = None
        source.close(); source = None
        closed_anchor = anchor; anchor = None
        os.close(closed_anchor)  # After SQLite closes; never retry ambiguous close.
        require(all(not os.path.lexists(artifact + suffix) for suffix in ('-wal', '-shm', '-journal')), 'destination_sidecar_remaining')
        stage = 'validation'
        validation = sqlite3.connect(Path(artifact).as_uri() + '?mode=ro&cache=private', uri=True, timeout=0.1, isolation_level=None)
        validation.set_progress_handler(lambda: int(time.monotonic() >= deadline), 1000)
        require(validation.execute('PRAGMA journal_mode').fetchone()[0] == 'delete', 'destination_journal_failed')
        require(validation.execute('PRAGMA integrity_check').fetchmany(2) == [('ok',)], 'destination_integrity_failed')
        validation.close(); validation = None
        check_deadline(); verify_identity(expected); bundle_check()
        require(all(not os.path.lexists(artifact + suffix) for suffix in ('-wal', '-shm', '-journal')), 'destination_sidecar_remaining')
        stage = 'hash'
        info = os.fstat(artifact_fd)
        require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and info.st_uid == 0 and stat.S_IMODE(info.st_mode) == 0o600 and matches(info, artifact_pin) and matches(os.stat(artifact), artifact_pin), 'output_identity_changed')
        digest = hashlib.sha256(); size = 0; os.lseek(artifact_fd, 0, os.SEEK_SET)
        while True:
            check_deadline(); chunk = os.read(artifact_fd, 1024 * 1024)
            if not chunk: break
            size += len(chunk); require(size <= maximum, 'output_limit_exceeded'); digest.update(chunk)
        os.fsync(artifact_fd)
        after = os.fstat(artifact_fd)
        require(matches(after, artifact_pin) and after.st_size == size == info.st_size and (after.st_mtime_ns, after.st_ctime_ns) == (info.st_mtime_ns, info.st_ctime_ns) and matches(os.stat(artifact), artifact_pin), 'output_identity_changed')
        closed_artifact = artifact_fd; artifact_fd = None
        os.close(closed_artifact)  # All destination SQLite connections have closed.
        check_deadline(); verify_identity(expected); bundle_check(); space(output, reserve)
        final_artifact = os.lstat(artifact)
        require(matches(final_artifact, artifact_pin) and stat.S_ISREG(final_artifact.st_mode) and final_artifact.st_nlink == 1 and final_artifact.st_uid == 0 and stat.S_IMODE(final_artifact.st_mode) == 0o600 and final_artifact.st_size == size and (final_artifact.st_mtime_ns, final_artifact.st_ctime_ns) == (after.st_mtime_ns, after.st_ctime_ns), 'output_identity_changed')
        stage = 'publish'
        report = {'schema': SCHEMA, 'status': 'complete', 'scope': 'one-committed-sqlite-ledger', 'reader': runtime_info(), 'expectations_sha256': expectation_hash, 'source_identity': expected['source'], 'host_identity': expected['host'], 'backup_interval': {'started': started, 'finished': finished}, 'source_metadata': {'journal_mode': mode, 'page_size': page_size, 'initial_pages': initial_pages}, 'artifact': {'name': 'ledger.sqlite3', 'bytes': size, 'sha256': digest.hexdigest()}, 'iterations': iterations}
        report_bytes = (json.dumps(report, sort_keys=True, separators=(',', ':')) + '\n').encode('utf-8')
        private_write('report.json', report_bytes)
        private_write('COMPLETE', (hashlib.sha256(report_bytes).hexdigest() + '\n').encode('ascii')); os.fsync(bundle_fd)
        os.unlink('INCOMPLETE', dir_fd=bundle_fd); os.fsync(bundle_fd); completed = True
        return 'complete'
    except BaseException as error:
        failure_code = error.code if isinstance(error, Refusal) else 'interrupted' if isinstance(error, KeyboardInterrupt) else 'operation_failed'
        if time.monotonic() >= deadline: failure_code = 'deadline_exceeded'
        if failure_code == 'deadline_exceeded': raise Refusal(failure_code) from None
        raise
    finally:
        close_failed = False; source_closed = source is None; destination_closed = True
        for label, connection in [('validation', validation), ('target', target), ('source', source)]:
            if connection is not None:
                try:
                    connection.close()
                    if label == 'source': source_closed = True
                except Exception:
                    close_failed = True
                    if label != 'source': destination_closed = False
        # If SQLite close itself fails, leave its raw anchor to process exit.
        for fd, allowed in ((anchor, source_closed), (artifact_fd, destination_closed)):
            if fd is not None and allowed:
                try:
                    os.close(fd)
                except OSError:
                    close_failed = True  # Never retry an ambiguous raw close.
        if created and (not completed or close_failed) and bundle_fd is not None:
            try:
                os.unlink('COMPLETE', dir_fd=bundle_fd)
            except OSError:
                pass
            try:
                receipt = {'schema': SCHEMA, 'status': 'incomplete', 'stage': 'cleanup' if close_failed else stage, 'code': 'cleanup_failed' if close_failed else failure_code}
                private_write('failure.json', (json.dumps(receipt, sort_keys=True) + '\n').encode('ascii'))
            except OSError:
                pass
            try:
                try:
                    os.stat('INCOMPLETE', dir_fd=bundle_fd, follow_symlinks=False)
                except FileNotFoundError:
                    private_write('INCOMPLETE', b'redx-host-sqlite-backup-incomplete-v1\n')
                os.fsync(bundle_fd)
            except OSError:
                pass
        # Directory handles carry no SQLite payload locks. Once persistence and
        # markers are committed, directory-only close errors do not undo them.
        for fd in (bundle_fd, parent_fd):
            if fd is not None:
                try:
                    os.close(fd)
                except OSError:
                    args.cleanup_warning = True  # No ambiguous-close retry.
        os.umask(old_umask)
        if close_failed: raise Refusal('cleanup_failed')


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise Refusal('invalid_arguments')


def main(argv=None):
    args = None
    try:
        parser = Parser(description=__doc__, allow_abbrev=False)
        for name in ('expectations', 'output-dir', 'max-output-bytes', 'reserve-free-bytes', 'deadline-seconds'):
            parser.add_argument('--' + name, required=True)
        parser.add_argument('--capture', action='store_true')
        args = parser.parse_args(argv)
        status = run(args); code = 'ok'; result = 0
    except Refusal as error:
        status, code, result = 'refused', error.code, 1
    except KeyboardInterrupt:
        status, code, result = 'refused', 'interrupted', 1
    except Exception:
        status, code, result = 'refused', 'operation_failed', 1
    print(json.dumps({'schema': SCHEMA, 'status': status, 'code': code, 'cleanup_warning': bool(getattr(args, 'cleanup_warning', False))}, sort_keys=True))
    return result


if __name__ == '__main__':
    sys.exit(main())
