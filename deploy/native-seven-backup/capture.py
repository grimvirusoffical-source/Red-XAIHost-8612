#!/usr/bin/env python3
"""Native-only website DB capture and fresh offline restore; no service pause."""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import sys
import tarfile
import types
import uuid
import zlib

LIBRARY_SHA = 'e833b99c4ad283bc78f81bf8c17478bdfdd9f4322a6b98f63c536bca6b60faa3'
CLI = Path('/opt/redx-cms-maintenance-ba8f91f/redx-8512')
CLI_SHA = 'cfe3cc75b363e741f8b4594b38ab9a040b449fe7eac233d0ba3899fe1e596c19'
DATA = Path('/opt/redx-db-website-ec6e9798/data')
PROC = Path('/proc')
DB_NAME = re.compile(r'[a-z0-9][a-z0-9_-]{0,62}')
JSON_LIMIT = 64 * 1024 * 1024
EXTRA_UNITS = ('redx-site-cms-private.service', 'redxai-site-bridge.service', 'redxaihost-agent.service')
FIELDS = ('ActiveState', 'SubState', 'MainPID', 'ExecMainStartTimestampMonotonic', 'NRestarts')


class Refused(Exception):
    pass


def need(value, code):
    if not value:
        raise Refused(code)


def identity(s):
    return (s.st_dev, s.st_ino, s.st_size, s.st_mtime_ns,
            s.st_uid, s.st_gid, stat.S_IMODE(s.st_mode))


def trusted_parents(path):
    need(path.is_absolute() and '..' not in path.parts, 'absolute_path_required')
    for p in reversed(path.parents):
        s = p.lstat()
        need(stat.S_ISDIR(s.st_mode) and s.st_uid == 0 and not s.st_mode & 0o022,
             'untrusted_parent')


def regular_fd(path, uid, gid, private=True, allow_empty=False):
    before = path.lstat()
    need(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and before.st_uid == uid
         and before.st_gid == gid and not before.st_mode & 0o022 and (allow_empty or before.st_size > 0)
         and (not private or stat.S_IMODE(before.st_mode) == 0o600), 'file_metadata_refused')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        need(identity(os.fstat(fd)) == identity(before), 'file_changed_before_read')
    except Exception:
        os.close(fd)
        raise
    return fd, before


def read_private(path, limit):
    fd, before = regular_fd(path, 0, 0)
    try:
        need(before.st_size <= limit, 'private_file_size_limit')
        with os.fdopen(fd, 'rb', closefd=False) as source:
            raw = source.read(limit + 1)
        need(len(raw) == before.st_size and identity(os.fstat(fd)) == identity(before)
             and identity(path.lstat()) == identity(before), 'private_file_changed')
        return raw
    finally:
        os.close(fd)


def strict_json(raw):
    def pairs(items):
        value = {}
        for key, item in items:
            need(key not in value, 'duplicate_json_key')
            value[key] = item
        return value

    def invalid_constant(_):
        raise Refused('invalid_json_constant')

    return json.loads(raw.decode('utf-8', 'strict'), object_pairs_hook=pairs,
                      parse_constant=invalid_constant)


def expected_inventory(path):
    trusted_parents(path)
    raw = read_private(path, 1024 * 1024)
    value = strict_json(raw)
    need(isinstance(value, dict) and set(value) == {'schema', 'databases'}
         and type(value['schema']) is int and value['schema'] == 1, 'inventory_schema_refused')
    names = value['databases']
    need(isinstance(names, list) and 7 <= len(names) <= 10000
         and all(isinstance(n, str) and DB_NAME.fullmatch(n) for n in names)
         and len(set(names)) == len(names), 'inventory_names_refused')
    return {'databases': sorted(names), 'sha256': hashlib.sha256(raw).hexdigest()}


def library(path):
    need(path.name == 'cms_recovery.py', 'library_name_refused')
    trusted_parents(path)
    fd, before = regular_fd(path, 0, 0, private=False)
    try:
        with os.fdopen(fd, 'rb', closefd=False) as source:
            raw = source.read(1024 * 1024 + 1)
        need(len(raw) <= 1024 * 1024 and identity(os.fstat(fd)) == identity(before)
             and identity(path.lstat()) == identity(before), 'library_changed')
    finally:
        os.close(fd)
    need(hashlib.sha256(raw).hexdigest() == LIBRARY_SHA, 'library_pin_mismatch')
    module = types.ModuleType('pinned_native_backup_library')
    module.__file__ = str(path)
    exec(compile(raw, str(path), 'exec'), module.__dict__)
    original_run = module.run

    def limited_run(args, timeout=60):
        values = [str(x) for x in args]
        need(allowed_command(values, module), 'unexpected_command_refused')
        return original_run(values, timeout=timeout)

    module.run = limited_run
    return module


def bundle_path(path, r):
    return path.parent == r.BACKUPS and re.fullmatch(r'native-seven-\d{8}T\d{6}Z-[a-f0-9]{32}', path.name)


def allowed_command(v, r):
    if len(v) == 4 and v[:2] == ['/usr/bin/systemctl', 'show']:
        return v[2] in (*r.UNITS, *EXTRA_UNITS) and v[3].startswith('--property=') and set(
            v[3].removeprefix('--property=').split(',')) <= set(FIELDS) | {'User'}
    if v == ['/usr/sbin/runuser', '-u', 'redx-db-site', '--', '/usr/bin/test', '-x', str(CLI)]:
        return True
    prefix = ['/usr/sbin/runuser', '-u', 'redx-db-site', '--', '/usr/bin/env', '-i',
              'PATH=/usr/bin:/bin', str(CLI), 'backup', '--offline', '--data-dir', str(DATA), '--out']
    if len(v) == len(prefix) + 1 and v[:-1] == prefix:
        p = Path(v[-1])
        return p.parent == DATA and re.fullmatch(r'\.native-seven-[a-f0-9]{32}\.tgz', p.name) is not None
    if len(v) == 5 and v[:2] == [str(CLI), 'restore'] and v[3] == '--data-dir':
        archive, dest = Path(v[2]), Path(v[4])
        return archive.name == 'native.tgz' and dest.name == 'restored' and archive.parent == dest.parent and bool(bundle_path(dest.parent, r))
    return False


def services(r):
    values = {u: r.props(u, FIELDS) for u in (*r.UNITS, *EXTRA_UNITS)}
    need(all(set(v) == set(FIELDS) and v['ActiveState'] == 'active' and v['SubState'] == 'running'
             and v['MainPID'].isdigit() and int(v['MainPID']) > 1 for v in values.values()),
         'monitored_service_not_running')
    return values


def lock_owner_matches(text, pid, info):
    owners = []
    for line in text.splitlines():
        fields = line.split()
        if len(fields) != 8 or fields[1:4] != ['FLOCK', 'ADVISORY', 'WRITE']:
            continue  # Includes blocked waiters ("->"), POSIX and OFD locks.
        device = fields[5].split(':')
        if len(device) != 3:
            continue
        try:
            key = (int(device[0], 16), int(device[1], 16), int(device[2]))
        except ValueError:
            continue
        if key == (os.major(info.st_dev), os.minor(info.st_dev), info.st_ino):
            owners.append(fields)
    return len(owners) == 1 and owners[0][4] == str(pid) and owners[0][6:] == ['0', 'EOF']


def native_identity(r, native):
    selected = r.native_identity(DATA)  # Includes fresh nonblocking flock refusal.
    path = DATA / 'redxd.lock'
    fd, before = regular_fd(path, native.pw_uid, native.pw_gid, private=False, allow_empty=True)
    try:
        proc = PROC / str(selected['pid'])
        need(proc.stat().st_uid == native.pw_uid, 'native_process_user_mismatch')
        matches = []
        for entry in (proc / 'fd').iterdir():
            try:
                s = entry.stat()
            except FileNotFoundError:
                continue
            if (s.st_dev, s.st_ino) == (before.st_dev, before.st_ino):
                matches.append(entry)
        need(matches, 'native_lock_open_inode_mismatch')
        with (PROC / 'locks').open('r', encoding='ascii') as locks:
            rows = locks.read(4 * 1024 * 1024 + 1)
        need(len(rows) <= 4 * 1024 * 1024 and lock_owner_matches(rows, selected['pid'], before),
             'native_lock_owner_mismatch')
        need(identity(os.fstat(fd)) == identity(before) and identity(path.lstat()) == identity(before)
             and r.native_identity(DATA) == selected, 'native_lock_identity_changed')
        return dict(selected, lockDevice=before.st_dev, lockInode=before.st_ino)
    finally:
        os.close(fd)


def inspect(r):
    import pwd
    native = pwd.getpwnam('redx-db-site')
    r.code_file(CLI, CLI_SHA, True)
    r.code_file(r.NATIVE, r.NATIVE_SHA, True)
    r.safe(DATA)
    r.private_dir(DATA, native.pw_uid, native.pw_gid)
    if (DATA / 'backup').exists():
        r.private_dir(DATA / 'backup', native.pw_uid, native.pw_gid)
    r.run(['/usr/sbin/runuser', '-u', 'redx-db-site', '--', '/usr/bin/test', '-x', CLI])
    selected = native_identity(r, native)
    baseline = services(r)
    r.resumed_health()  # Read-only existing HTTP health; never starts/stops a service.
    need(native_identity(r, native) == selected and services(r) == baseline, 'live_process_changed')
    return selected, baseline, native


def copy_export(r, source, dest, uid, gid):
    fd, original = regular_fd(source, uid, gid)
    digest = hashlib.sha256()
    try:
        need(original.st_size <= r.LIMIT, 'native_export_size_limit')
        target = os.open(dest, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        try:
            os.fchmod(target, 0o600)
            with os.fdopen(target, 'wb', closefd=False) as output:
                remaining = original.st_size
                while remaining:
                    chunk = os.read(fd, min(remaining, 1024 * 1024))
                    need(chunk, 'native_export_short_read')
                    digest.update(chunk)
                    output.write(chunk)
                    remaining -= len(chunk)
                need(os.read(fd, 1) == b'', 'native_export_grew')
                output.flush()
            os.fsync(target)
        finally:
            os.close(target)
        need(identity(os.fstat(fd)) == identity(original) and identity(source.lstat()) == identity(original),
             'native_export_changed')
    finally:
        os.close(fd)
    r.sync_dir(dest.parent)
    need(r.sha(dest) == digest.hexdigest(), 'durable_copy_hash_mismatch')
    return original, digest.hexdigest()


def strict_framing(archive, limit):
    end = 0
    with tarfile.open(archive, 'r:gz') as native:
        for item in native:
            end = max(end, item.offset_data + ((item.size + 511) // 512) * 512)
    decoder = zlib.decompressobj(16 + zlib.MAX_WBITS)
    size = 0
    with archive.open('rb') as source:
        while chunk := source.read(64 * 1024):
            need(not decoder.eof, 'multiple_gzip_members_or_trailing_bytes')
            while chunk:
                decoded = decoder.decompress(chunk, 1024 * 1024)
                tail = decoded[max(0, end - size):]
                need(not any(tail), 'nonzero_tar_trailer')
                size += len(decoded)
                need(size <= limit and not decoder.unused_data, 'gzip_size_or_trailer_refused')
                chunk = decoder.unconsumed_tail
    need(decoder.eof and size >= end + 1024 and size % 512 == 0, 'tar_or_gzip_framing_refused')


def coverage(r, archive, expected):
    result = r.members(archive, True)
    names = result['databases']
    need(all(isinstance(n, str) and DB_NAME.fullmatch(n) for n in names)
         and len(set(names)) == len(names) and set(expected['databases']) <= set(names),
         'expected_database_coverage_missing')
    strict_framing(archive, r.LIMIT)
    return result


def unchanged_export(path, expected, uid, gid, digest):
    fd, before = regular_fd(path, uid, gid)
    try:
        need(identity(before) == identity(expected), 'export_changed_cleanup_refused')
        with os.fdopen(fd, 'rb', closefd=False) as source:
            need(hashlib.file_digest(source, 'sha256').hexdigest() == digest,
                 'export_changed_cleanup_refused')
        need(identity(os.fstat(fd)) == identity(expected) and identity(path.lstat()) == identity(expected),
             'export_changed_cleanup_refused')
    finally:
        os.close(fd)


def key_records(raw):
    value = strict_json(raw)
    need(isinstance(value, dict) and set(value) == {'version', 'keys'}
         and type(value['version']) is int and value['version'] == 1
         and (value['keys'] is None or isinstance(value['keys'], list)), 'key_store_schema_refused')
    records = {}
    for record in value['keys'] or []:
        need(isinstance(record, dict) and isinstance(record.get('id'), str)
             and record['id'] and record['id'] not in records, 'key_record_identity_refused')
        records[record['id']] = record
    return records


def restored_bytes(r, archive, dest, databases):
    root = dest / 'dbs'
    children = list(root.iterdir())
    need(all(p.is_dir() and not p.is_symlink() for p in children)
         and sorted(p.name for p in children) == databases, 'restored_database_inventory_mismatch')
    empty = {'bytes': 0, 'sha256': hashlib.sha256(b'').hexdigest()}
    expected = {'redxd.lock': empty, 'keys.red-xai-keys.lock': empty}
    directories = {'dbs'}
    metadata = {'ttl.json': 'cluster/ttl.json', 'tenants.json': 'tenants.json',
                'updates/signing.key': 'updates/signing.key',
                'updates/signing.key.meta': 'updates/signing.key.meta'}
    keys = None
    with tarfile.open(archive, 'r:gz') as native:
        for item in native:
            name = item.name.rstrip('/')
            if item.isdir():
                directories.add(name)
                continue
            with native.extractfile(item) as original:
                if name == 'keys.json':
                    need(item.size <= JSON_LIMIT, 'key_store_size_limit')
                    keys = key_records(original.read(JSON_LIMIT + 1))
                    continue
                target = name if name.startswith('dbs/') else metadata[name]
                expected[target] = {'bytes': item.size,
                                    'sha256': hashlib.file_digest(original, 'sha256').hexdigest()}
    raw_keys = read_private(dest / 'keys.red-xai-keys', JSON_LIMIT)
    need(keys is not None and key_records(raw_keys) == keys, 'restored_key_records_mismatch')
    # Serialization differs from keys.json; bind the verified records to these bytes.
    expected['keys.red-xai-keys'] = {'bytes': len(raw_keys), 'sha256': hashlib.sha256(raw_keys).hexdigest()}
    for rel in (*expected, *tuple(directories)):
        directories.update(p.as_posix() for p in Path(rel).parents if p != Path('.'))
    actual_files, actual_dirs, verified = set(), set(), {}
    r.private_dir(dest, 0, 0)
    for p in dest.rglob('*'):
        s = p.lstat()
        rel = p.relative_to(dest).as_posix()
        if stat.S_ISDIR(s.st_mode):
            r.private_dir(p, 0, 0)
            actual_dirs.add(rel)
            continue
        need(rel in expected, 'restored_unexpected_file')
        fd, before = regular_fd(p, 0, 0, allow_empty=True)
        try:
            with os.fdopen(fd, 'rb', closefd=False) as restored:
                value = {'bytes': before.st_size, 'sha256': hashlib.file_digest(restored, 'sha256').hexdigest()}
            need(value == expected[rel], 'restored_payload_mismatch')
            os.fsync(fd)
            need(identity(os.fstat(fd)) == identity(before) and identity(p.lstat()) == identity(before),
                 'restored_file_changed')
        finally:
            os.close(fd)
        actual_files.add(rel)
        verified[rel] = value
    need(actual_files == set(expected) and actual_dirs == directories, 'restored_payload_inventory_mismatch')
    for rel in sorted(directories, key=lambda p: len(Path(p).parts), reverse=True):
        r.sync_dir(dest / rel)
    r.sync_dir(dest)
    return {'files': verified, 'directories': sorted(directories), 'keyRecordCount': len(keys)}


def capture(r, expected, apply=False):
    selected, baseline, native = inspect(r)
    if not apply:
        return {'ok': True, 'inspectionOnly': True, 'expectedDatabaseCount': len(expected['databases']),
                'serviceChanges': False}
    trusted_parents(r.BACKUPS)
    r.private_dir(r.BACKUPS, 0, 0)
    bundle = r.BACKUPS / ('native-seven-' + dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ-') + uuid.uuid4().hex)
    bundle.mkdir(mode=0o700)
    r.sync_dir(r.BACKUPS)
    r.private_dir(bundle, 0, 0)
    export = DATA / ('.native-seven-' + uuid.uuid4().hex + '.tgz')
    need(not export.exists() and not export.is_symlink(), 'native_export_already_exists')
    r.run(['/usr/sbin/runuser', '-u', 'redx-db-site', '--', '/usr/bin/env', '-i', 'PATH=/usr/bin:/bin',
           CLI, 'backup', '--offline', '--data-dir', DATA, '--out', export], timeout=180)
    need(native_identity(r, native) == selected and services(r) == baseline, 'live_changed_during_backup')
    archive = bundle / 'native.tgz'
    original, archive_sha = copy_export(r, export, archive, native.pw_uid, native.pw_gid)
    layout = coverage(r, archive, expected)
    restored = bundle / 'restored'
    restored.mkdir(mode=0o700)
    r.sync_dir(bundle)
    need(not list(restored.iterdir()), 'restore_destination_not_empty')
    r.run([CLI, 'restore', archive, '--data-dir', restored], timeout=180)
    reconstructed = restored_bytes(r, archive, restored, layout['databases'])
    need(r.sha(archive) == archive_sha and native_identity(r, native) == selected and services(r) == baseline,
         'live_or_archive_changed_during_restore')
    r.resumed_health()
    need(native_identity(r, native) == selected and services(r) == baseline, 'live_changed_after_health')
    unchanged_export(export, original, native.pw_uid, native.pw_gid, archive_sha)
    receipt = {'schema': 1, 'kind': 'native-only-seven-database-recovery', 'nativeRevision': r.REV,
               'nativeBinarySha256': r.NATIVE_SHA, 'nativeCliSha256': CLI_SHA, 'librarySha256': LIBRARY_SHA,
               'nativeProcess': selected, 'services': baseline, 'archiveSha256': archive_sha,
               'archiveBytes': archive.stat().st_size, 'databases': layout['databases'],
               'expectedInventorySha256': expected['sha256'], 'expectedDatabases': expected['databases'],
               'nativeMetadata': layout['metadata'], 'offlineRestoreInventoryMatched': True,
               'restoredFiles': reconstructed, 'allDatabasePayloadBytesMatched': True,
               'nativeMetadataBytesMatched': True, 'keyRecordsMatched': True,
               'temporaryExportRetained': True, 'temporaryExport': str(export),
               'temporaryExportIdentity': identity(original), 'temporaryExportSha256': archive_sha,
               'allUserRecordsSemanticallyVerified': False,
               'cmsSupportCaptured': False, 'cmsPairVerified': False, 'offServerVerified': False,
               'serviceChanges': False, 'liveDataRestored': False}
    r.write(bundle / 'receipt.json', r.encoded(receipt))
    # DATA belongs to the native daemon. No atomic compare-inode unlink exists here;
    # retain even a verified export rather than risk removing a replacement entry.
    return {'ok': True, 'bundleDirectory': str(bundle), 'receiptSha256': r.sha(bundle / 'receipt.json'),
            'databaseCount': len(layout['databases']), 'allDatabasePayloadBytesMatched': True,
            'nativeMetadataBytesMatched': True, 'keyRecordsMatched': True, 'temporaryExportRetained': True,
            'offlineRestoreInventoryMatched': True, 'serviceChanges': False, 'liveDataRestored': False,
            'cmsPairVerified': False, 'offServerVerified': False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--library-file', type=Path, required=True)
    parser.add_argument('--expected-inventory', type=Path, required=True)
    parser.add_argument('--capture', action='store_true')
    args = parser.parse_args()
    os.umask(0o077)
    try:
        need(sys.platform == 'linux' and os.geteuid() == 0, 'linux_root_required')
        expected = expected_inventory(args.expected_inventory)
        r = library(args.library_file)
        print(json.dumps(capture(r, expected, args.capture), sort_keys=True))
        return 0
    except Exception:
        print('{"ok":false,"privateOutputWithheld":true,"partialEvidencePreserved":true}')
        return 2


if __name__ == '__main__':
    raise SystemExit(main())
