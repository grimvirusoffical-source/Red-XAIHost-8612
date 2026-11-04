#!/usr/bin/env python3
"""Q_GETINFO-only observation; not per-user quota headroom or capture readiness.

Controller review is required. The mount namespace and /dev tree must remain
trusted and stable. Direct ext4 root-device sources only; aliases refuse.
No query executes on import. No quota buffer contents or paths are printed.

ABI/constants: https://man7.org/linux/man-pages/man2/Q_GETINFO.2const.html
https://raw.githubusercontent.com/torvalds/linux/master/include/uapi/linux/quota.h
"""
import ctypes as C
import json
import os
from pathlib import Path
import re
import stat
import sys
from contextlib import ExitStack

DATA = Path('/opt/redx-db-website-ec6e9798/data')
BACKUPS = Path('/srv/red-xai/backups')
Q_GETINFO = 0x800005
TYPES = (('USRQUOTA', 0), ('GRPQUOTA', 1), ('PRJQUOTA', 2))


def need(ok):
    if not ok:
        raise RuntimeError('quota_observation_refused_private_details_withheld')


def directory(path):
    need(path.is_absolute() and '..' not in path.parts)
    flags = os.O_PATH | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC
    fd = os.open('/', flags)
    try:
        for part in path.parts[1:]:
            nxt = os.open(part, flags, dir_fd=fd)
            os.close(fd)
            fd = nxt
        return fd
    except BaseException:
        os.close(fd)
        raise


def root_source(dev):
    with open('/proc/self/mountinfo', 'rb') as source:
        raw = source.read(1024 * 1024 + 1)
    need(len(raw) <= 1024 * 1024)
    matches = []
    for line in raw.decode('ascii', 'strict').splitlines():
        left, sep, right = line.partition(' - ')
        before, after = left.split(), right.split()
        if sep and len(before) >= 6 and before[4] == '/':
            pair = tuple(map(int, before[2].split(':')))
            if pair == (os.major(dev), os.minor(dev)):
                need(len(after) >= 3 and after[0] == 'ext4')
                matches.append(after[1])
    need(len(matches) == 1)
    value = matches[0]
    need(re.fullmatch(r'/dev/[A-Za-z0-9_./-]+', value)
         and '..' not in Path(value).parts and str(Path(value)) == value)
    return value


def query_info():
    need(sys.platform == 'linux' and os.geteuid() == 0 and C.sizeof(C.c_int) == 4)
    with ExitStack() as stack:
        paths = (Path('/'), DATA, BACKUPS)
        fds = []
        for path in paths:
            fd = directory(path)
            stack.callback(os.close, fd)
            fds.append(fd)
        identities = [(os.fstat(fd).st_dev, os.fstat(fd).st_ino) for fd in fds]
        dev = identities[0][0]
        need(all(item[0] == dev for item in identities))
        source = root_source(dev)
        parent = directory(Path(source).parent)
        stack.callback(os.close, parent)
        block = os.open(Path(source).name, os.O_PATH | os.O_NOFOLLOW | os.O_CLOEXEC,
                        dir_fd=parent)
        stack.callback(os.close, block)
        before = os.fstat(block)
        need(stat.S_ISBLK(before.st_mode) and before.st_uid == 0 and before.st_rdev == dev)
        libc = C.CDLL(None, use_errno=True)
        query = libc.quotactl
        query.argtypes = (C.c_int, C.c_char_p, C.c_int, C.c_void_p)
        query.restype = C.c_int
        results = {}
        for name, quota_type in TYPES:
            # if_dqinfo is u64,u64,u32,u32 (24 bytes). Do not parse its contents.
            buffer = C.create_string_buffer(128)
            operation = C.c_int((Q_GETINFO << 8) | (quota_type & 0xff)).value
            C.set_errno(0)
            rc = query(operation, os.fsencode(source), 0, C.cast(buffer, C.c_void_p))
            error = C.get_errno() if rc == -1 else 0
            need(rc in (-1, 0))
            results[name] = {'rc': rc, 'errno': error}
        need(root_source(dev) == source)
        after = os.stat(source, follow_symlinks=False)
        need((after.st_dev, after.st_ino, after.st_rdev, stat.S_IFMT(after.st_mode)) ==
             (before.st_dev, before.st_ino, before.st_rdev, stat.S_IFMT(before.st_mode)))
        for path, expected in zip(paths, identities):
            check = directory(path)
            try:
                info = os.fstat(check)
                need((info.st_dev, info.st_ino) == expected)
            finally:
                os.close(check)
        return results


def main():
    # rc=0 means query completion, not sufficient quota or inactive enforcement.
    # Only ESRCH on a supported, verified target means inactive type accounting.
    output, exit_code = {'rc': None, 'errno': None}, 2
    try:
        output, exit_code = query_info(), 0
    except Exception:
        pass
    print(json.dumps(output, sort_keys=True))
    return exit_code


if __name__ == '__main__':
    raise SystemExit(main())
