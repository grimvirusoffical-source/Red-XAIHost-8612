#!/usr/bin/env python3
"""Read-only website-native metadata estimate. No quota or capture readiness proof."""
import json
import os
from pathlib import Path
import re
import stat
import sys
import time

DATA = Path('/opt/redx-db-website-ec6e9798/data')
BACKUPS = Path('/srv/red-xai/backups')
METADATA = ('keys.red-xai-keys', 'cluster/ttl.json', 'tenants.json',
            'updates/signing.key', 'updates/signing.key.meta')
GIB = 1024 ** 3
DIRECTORY = os.O_RDONLY | getattr(os, 'O_DIRECTORY', 0) | getattr(os, 'O_NOFOLLOW', 0)


class Refused(Exception):
    pass


def need(value):
    if not value:
        raise Refused('metadata_preflight_refused_private_details_withheld')


def shape(s):
    return (s.st_dev, s.st_ino, s.st_mode, s.st_uid, s.st_gid, s.st_nlink,
            s.st_size, s.st_mtime_ns, s.st_ctime_ns)


class Result:
    __slots__ = ('_summary', '_names')

    def __init__(self, summary, names):
        self._summary, self._names = summary, tuple(names)

    def public_result(self):
        return json.loads(json.dumps(self._summary))

    def private_database_names(self):
        return self._names  # Explicit private operator use only; never print this.

    def __repr__(self):
        return json.dumps(self._summary, sort_keys=True)


def filesystem(fd):
    v = os.fstatvfs(fd)
    unit = v.f_frsize or v.f_bsize
    need(unit > 0 and v.f_bavail >= 0 and v.f_favail >= 0 and v.f_files > 0)
    return {'allocationUnit': unit, 'availableBytes': v.f_bavail * unit,
            'availableInodes': v.f_favail}


def capacity(files, dirs, native, backup, shared):
    unit = max(native['allocationUnit'], backup['allocationUnit'])
    c, r, reserve = 4 * GIB, 4 * GIB + GIB // 4 + (files + dirs + 64) * unit, 2 * GIB
    inode_reserve = 2 * (files + dirs + 64) + 1024
    native_bytes, backup_bytes = 2 * c + reserve, c + r + reserve
    shared_bytes = 3 * c + r + reserve  # Deliberately extra-conservative overlap allowance.
    sufficient = (native['availableBytes'] >= shared_bytes and
                  backup['availableBytes'] >= shared_bytes and
                  min(native['availableInodes'], backup['availableInodes']) >= files + dirs + 128 + inode_reserve)
    if not shared:
        sufficient = (native['availableBytes'] >= native_bytes and backup['availableBytes'] >= backup_bytes
                      and native['availableInodes'] >= 64 + inode_reserve
                      and backup['availableInodes'] >= files + dirs + 64 + inode_reserve)
    return {'archivePlanningBytes': c, 'restoredPlanningBytes': r, 'operationalReserveBytes': reserve,
            'nativeRequiredBytes': native_bytes, 'backupRequiredBytes': backup_bytes,
            'sharedRequiredBytes': shared_bytes if shared else None,
            'inodeReserve': inode_reserve, 'byteAndInodeEnvelopeSatisfied': sufficient}


def _scan(data, backups, uid, gid, seconds=20, depth_limit=64, entry_limit=90000):
    started, opened, chains = time.monotonic(), [], []

    def tick():
        need(time.monotonic() - started <= seconds)

    def directory(s, owner, group, private):
        need(stat.S_ISDIR(s.st_mode) and s.st_uid == owner and
             (group is None or s.st_gid == group) and not s.st_mode & 0o022)
        if private:
            need(stat.S_IMODE(s.st_mode) == 0o700)

    def chain(path, owner, group):
        need(path.is_absolute() and '..' not in path.parts)
        fd = os.open('/', DIRECTORY)
        opened.append(fd)
        root = os.fstat(fd)
        directory(root, 0, None, False)
        chains.append((None, '/', fd, shape(root)))
        for index, name in enumerate(path.parts[1:]):
            tick()
            before = os.stat(name, dir_fd=fd, follow_symlinks=False)
            last = index == len(path.parts) - 2
            directory(before, owner if last else 0, group if last else None, last)
            child = os.open(name, DIRECTORY, dir_fd=fd)
            opened.append(child)
            need(shape(os.fstat(child)) == shape(before))
            chains.append((fd, name, child, shape(before)))
            fd = child
        return fd

    def anchored():
        for parent, name, fd, expected in chains:
            tick()
            need(shape(os.fstat(fd)) == expected)
            current = os.stat('/', follow_symlinks=False) if parent is None else os.stat(
                name, dir_fd=parent, follow_symlinks=False)
            need(shape(current) == expected)

    try:
        data_fd, backup_fd = chain(data, uid, gid), chain(backups, 0, 0)
        device = os.fstat(data_fd).st_dev

        def observe():
            records, names, totals = {}, [], {'files': 0, 'directories': 0, 'logicalBytes': 0, 'framingBytes': 1024}

            def record(key, s, regular):
                tick()
                need(len(records) + 1 < entry_limit and s.st_dev == device and s.st_uid == uid and s.st_gid == gid)
                if regular:
                    need(stat.S_ISREG(s.st_mode) and stat.S_IMODE(s.st_mode) == 0o600 and s.st_nlink == 1)
                    totals['files'] += 1
                    totals['logicalBytes'] += s.st_size
                    totals['framingBytes'] += (-s.st_size) % 512
                else:
                    directory(s, uid, gid, True)
                    totals['directories'] += 1
                records[key] = shape(s)
                totals['framingBytes'] += 2048 + 2 * len('/'.join(key).encode('utf-8'))

            def visit(parent, name, key, depth):
                tick()
                need(depth <= depth_limit)
                before = os.stat(name, dir_fd=parent, follow_symlinks=False)
                regular = stat.S_ISREG(before.st_mode)
                record(key, before, regular)
                if not regular:
                    fd = os.open(name, DIRECTORY, dir_fd=parent)
                    try:
                        need(shape(os.fstat(fd)) == shape(before))
                        # A fresh directory description avoids readdir offset reuse between passes.
                        reader = os.open('.', DIRECTORY, dir_fd=fd)
                        try:
                            with os.scandir(reader) as entries:
                                for entry in entries:
                                    if key == ('dbs',):
                                        need(re.fullmatch(r'[a-z0-9][a-z0-9_-]{0,62}', entry.name))
                                        child = os.stat(entry.name, dir_fd=fd, follow_symlinks=False)
                                        directory(child, uid, gid, True)
                                        names.append(entry.name)
                                    visit(fd, entry.name, (*key, entry.name), depth + 1)
                        finally:
                            os.close(reader)
                        need(shape(os.fstat(fd)) == shape(before))
                    finally:
                        os.close(fd)
                need(shape(os.stat(name, dir_fd=parent, follow_symlinks=False)) == shape(before))

            visit(data_fd, 'dbs', ('dbs',), 0)
            need(7 <= len(names) <= 10000 and len(set(names)) == len(names))
            for rel in METADATA:
                parts, fd, parent = Path(rel).parts, data_fd, None
                try:
                    if len(parts) == 2:
                        try:
                            before = os.stat(parts[0], dir_fd=data_fd, follow_symlinks=False)
                        except FileNotFoundError:
                            records[('optional-parent', parts[0])] = None
                            continue
                        if ('metadata-parent', parts[0]) not in records:
                            record(('metadata-parent', parts[0]), before, False)
                        parent = fd = os.open(parts[0], DIRECTORY, dir_fd=data_fd)
                        need(shape(os.fstat(fd)) == shape(before))
                    try:
                        before_file = os.stat(parts[-1], dir_fd=fd, follow_symlinks=False)
                    except FileNotFoundError:
                        need(rel != 'keys.red-xai-keys')
                        records[('optional-file', rel)] = None
                        continue
                    record(('metadata-file', rel), before_file, True)
                    need(shape(os.stat(parts[-1], dir_fd=fd, follow_symlinks=False)) == shape(before_file))
                finally:
                    if parent is not None:
                        try:
                            need(shape(os.fstat(parent)) == shape(before) and shape(os.stat(
                                parts[0], dir_fd=data_fd, follow_symlinks=False)) == shape(before))
                        finally:
                            os.close(parent)
            tick()
            need(len(records) < entry_limit)
            return records, tuple(sorted(names)), totals

        first, names, totals = observe()
        anchored()
        second, later_names, later_totals = observe()
        anchored()
        need(first == second and names == later_names and totals == later_totals)
        estimate = totals['logicalBytes'] + totals['framingBytes'] + GIB // 4
        need(estimate < 3 * GIB)  # Keep a full GiB below the post-write expanded limit.
        native_fs, backup_fs = filesystem(data_fd), filesystem(backup_fd)
        shared = device == os.fstat(backup_fd).st_dev
        tick()
        summary = dict(totals, databaseCount=len(names), observedPaddedEstimateBytes=estimate,
                       metadataStableAcrossTwoPasses=True, sharedFilesystem=shared,
                       nativeFilesystem=native_fs, backupFilesystem=backup_fs,
                       quotaEvidenceVerified=False, operatorQuotaReviewRequired=True,
                       capacityReserved=False, nativeWriterSizeCapped=False,
                       applicationOrWalConsistencyVerified=False)
        summary.update(capacity(totals['files'], totals['directories'], native_fs, backup_fs, shared))
        return Result(summary, names)
    finally:
        for fd in reversed(opened):
            os.close(fd)


def scan():
    try:
        need(sys.platform == 'linux' and os.geteuid() == 0)
        import pwd
        native = pwd.getpwnam('redx-db-site')
        return _scan(DATA, BACKUPS, native.pw_uid, native.pw_gid)
    except Exception:
        raise Refused('metadata_preflight_refused_private_details_withheld') from None


def main():
    try:
        print(scan())
        return 0
    except Exception:
        print('{"metadataPreflightCompleted":false,"privateDetailsWithheld":true,"quotaEvidenceVerified":false}')
        return 2


if __name__ == '__main__':
    raise SystemExit(main())
