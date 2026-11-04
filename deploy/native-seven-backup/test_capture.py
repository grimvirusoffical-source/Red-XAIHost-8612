"""Synthetic source tests only; no services, network, daemon or real user data."""
import contextlib
import gzip
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import types
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('native_capture_under_test', HERE / 'capture.py')
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)
NAMES = [f'synthetic_{n}' for n in range(7)]
EXPECTED = {'databases': NAMES, 'sha256': hashlib.sha256(b'synthetic inventory').hexdigest()}
KEYS = {'version': 1, 'keys': [
    {'id': 'synthetic_b', 'hash': 'synthetic-hash', 'salt': 'synthetic-salt', 'm': 1,
     't': 1, 'p': 1, 'scope': 'read', 'db': NAMES[0], 'roles': ['reader'],
     'owner': 'synthetic-owner', 'created': '2026-01-01T00:00:00Z', 'rate_rps': 1.5},
    {'id': 'synthetic_a', 'hash': 'other-hash', 'salt': 'other-salt', 'm': 1,
     't': 1, 'p': 1, 'scope': 'write', 'db': NAMES[1], 'created': '2026-01-01T00:00:00Z'}]}


def pinned_source():
    path = HERE / 'cms_recovery.py'
    if not path.exists():
        path = HERE.parent / 'cms-recovery-tools' / 'cms_recovery.py'
    # Git checkouts on Windows use CRLF. Production library() intentionally does
    # not normalize; these tests load the pinned Git/LF bytes in memory only.
    raw = path.read_bytes().replace(b'\r\n', b'\n')
    if hashlib.sha256(raw).hexdigest() != c.LIBRARY_SHA:
        raise AssertionError('pinned source changed')
    return raw


def pinned_library():
    value = types.ModuleType('synthetic_pinned_library')
    exec(compile(pinned_source(), 'pinned-cms-recovery.py', 'exec'), value.__dict__)
    return value


def regular_portable(path, uid, gid, private=True, allow_empty=False):
    """Content-flow tests only. LinuxRootTests exercises the real guard."""
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1:
        raise c.Refused('synthetic_regular_file_required')
    fd = os.open(path, os.O_RDONLY | getattr(os, 'O_BINARY', 0))
    return fd, before


def archive(path, names=NAMES, keys=KEYS, extra=()):
    entries = [(f'dbs/{n}/', None) for n in names]
    entries += [(f'dbs/{n}/records.red-xai', f'synthetic records {n}'.encode()) for n in names]
    entries += [('keys.json', json.dumps(keys).encode()), ('ttl.json', b'[]'),
                ('tenants.json', b'{"synthetic":true}'), ('updates/signing.key', b'synthetic signing bytes'),
                ('updates/signing.key.meta', b'{"synthetic":1}')]
    with tarfile.open(path, 'w:gz', format=tarfile.USTAR_FORMAT) as value:
        for name, payload in [*entries, *extra]:
            item = tarfile.TarInfo(name)
            if payload is None:
                item.type, item.mode = tarfile.DIRTYPE, 0o700
                value.addfile(item)
            elif isinstance(payload, tarfile.TarInfo):
                value.addfile(payload)
            else:
                item.size, item.mode = len(payload), 0o600
                value.addfile(item, io.BytesIO(payload))


def synthetic_layout(path, native):
    """Keep pinned gzip/path checks; do not publish its historical real DB names."""
    assert native is True
    result = pinned_library().members(path, False)
    result['databases'] = sorted({PurePosixPath(n).parts[1] for n, _, _ in result['entries']
                                  if n.startswith('dbs/')})
    result['metadata'] = sorted(n for n, _, _ in result['entries'] if not n.startswith('dbs/'))
    return result


def restore_fixture(source, dest):
    """Reconstruct only our synthetic fixtures, not an alternate native restore."""
    (dest / 'dbs').mkdir(mode=0o700, exist_ok=True)
    mapping = {'ttl.json': 'cluster/ttl.json'}
    with tarfile.open(source, 'r:gz') as value:
        for item in value:
            if item.name == 'keys.json':
                keys = json.load(value.extractfile(item))
                keys['keys'] = list(reversed(keys['keys'] or []))
                payload = json.dumps(keys, indent=1).encode()
                rel = 'keys.red-xai-keys'
            elif item.isdir():
                (dest / item.name).mkdir(mode=0o700, parents=True, exist_ok=True)
                continue
            else:
                rel = mapping.get(item.name, item.name)
                payload = value.extractfile(item).read()
            target = dest / rel
            target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            target.write_bytes(payload)
            if os.name != 'nt':
                target.chmod(0o600)
    for name in ('redxd.lock', 'keys.red-xai-keys.lock'):
        target = dest / name
        target.touch(mode=0o600)


class PortableTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.lib = pinned_library()
        self.lib.members = synthetic_layout

    def test_pin_and_command_boundary(self):
        self.assertEqual(hashlib.sha256(pinned_source()).hexdigest(), c.LIBRARY_SHA)
        r = types.SimpleNamespace(BACKUPS=Path('/srv/red-xai/backups'), UNITS=('synthetic.service',))
        bundle = r.BACKUPS / ('native-seven-20260101T000000Z-' + 'a' * 32)
        restore = [str(c.CLI), 'restore', str(bundle / 'native.tgz'), '--data-dir', str(bundle / 'restored')]
        self.assertTrue(c.allowed_command(restore, r))
        for bad in ([*restore, '--force'], [str(c.CLI), 'restore', 'native.tgz', '--data-dir', str(c.DATA)],
                    ['/usr/bin/systemctl', 'stop', 'synthetic.service'], ['/bin/sh', '-c', 'anything'],
                    ['/usr/bin/systemctl', 'show', 'unlisted.service', '--property=MainPID']):
            with self.subTest(command=bad[0]):
                self.assertFalse(c.allowed_command(bad, r))
        self.assertTrue(c.allowed_command(['/usr/bin/systemctl', 'show', 'synthetic.service',
                                            '--property=' + ','.join(c.FIELDS)], r))

    def test_library_executes_only_exact_verified_bytes_and_guards_runner(self):
        path = self.root / 'cms_recovery.py'
        path.write_bytes(pinned_source())
        with mock.patch.object(c, 'trusted_parents'), mock.patch.object(c, 'regular_fd', regular_portable):
            loaded = c.library(path)
            with self.assertRaises(c.Refused):
                loaded.run(['/bin/sh'])
            path.write_bytes(pinned_source() + b'# changed\n')
            with self.assertRaises(c.Refused):
                c.library(path)

    def test_private_inventory_schema_count_names_duplicates_and_read_binding(self):
        path = self.root / 'inventory.json'
        valid = json.dumps({'schema': 1, 'databases': NAMES}).encode()
        bad = [b'{"schema":1,"schema":1,"databases":[]}', b'{"schema":NaN,"databases":[]}',
               json.dumps({'schema': True, 'databases': NAMES}).encode(),
               json.dumps({'schema': 1, 'databases': NAMES[:6]}).encode(),
               json.dumps({'schema': 1, 'databases': NAMES + [NAMES[0]]}).encode(),
               json.dumps({'schema': 1, 'databases': NAMES[:-1] + ['../escape']}).encode(),
               json.dumps({'schema': 1, 'databases': NAMES, 'extra': True}).encode()]
        with mock.patch.object(c, 'trusted_parents'), mock.patch.object(c, 'regular_fd', regular_portable):
            path.write_bytes(valid)
            self.assertEqual(c.expected_inventory(path)['databases'], NAMES)
            self.assertEqual(c.expected_inventory(path)['sha256'], hashlib.sha256(valid).hexdigest())
            for raw in bad:
                with self.subTest(raw=raw[:40]):
                    path.write_bytes(raw)
                    with self.assertRaises((c.Refused, ValueError)):
                        c.expected_inventory(path)

    def test_seven_required_and_additional_database_preserved(self):
        path = self.root / 'native.tgz'
        for names, passes in ((NAMES[:5], False), (NAMES, True), (NAMES + ['synthetic_extra'], True)):
            archive(path, names)
            if passes:
                self.assertEqual(c.coverage(self.lib, path, EXPECTED)['databases'], sorted(names))
            else:
                with self.assertRaises(c.Refused):
                    c.coverage(self.lib, path, EXPECTED)

    def test_archive_crc_paths_links_duplicates_and_trailers_refused(self):
        path = self.root / 'native.tgz'
        link = tarfile.TarInfo('dbs/synthetic_0/link')
        link.type, link.linkname = tarfile.SYMTYPE, '/outside'
        for extra in ([('../escape', b'x')], [('keys.json', b'{}')], [('ignored', link)]):
            archive(path, extra=extra)
            with self.assertRaises(Exception):
                c.coverage(self.lib, path, EXPECTED)
        archive(path)
        good = path.read_bytes()
        broken = bytearray(good)
        broken[-8] ^= 1  # gzip CRC, not just a tar header.
        raw = gzip.decompress(good)
        candidates = [bytes(broken), good[:-4], good + gzip.compress(b'non-tar payload'),
                      gzip.compress(raw + b'nonzero trailing tar payload')]
        for data in candidates:
            path.write_bytes(data)
            with self.assertRaises(Exception):
                c.coverage(self.lib, path, EXPECTED)
        path.write_bytes(good)
        c.coverage(self.lib, path, EXPECTED)

    def test_full_restore_metadata_and_keys_and_extra_database(self):
        path, dest = self.root / 'native.tgz', self.root / 'restored'
        names = NAMES + ['synthetic_extra']
        archive(path, names)
        dest.mkdir()
        restore_fixture(path, dest)
        r = types.SimpleNamespace(private_dir=mock.Mock(), sync_dir=mock.Mock())
        with mock.patch.object(c, 'regular_fd', regular_portable), mock.patch.object(c.os, 'fsync'):
            result = c.restored_bytes(r, path, dest, sorted(names))
        self.assertEqual(result['keyRecordCount'], 2)
        self.assertIn('dbs/synthetic_extra/records.red-xai', result['files'])
        self.assertIn('cluster/ttl.json', result['files'])
        self.assertIn('updates/signing.key.meta', result['files'])

    def test_restore_missing_corrupt_extra_metadata_and_owner_field_refused(self):
        path, dest = self.root / 'native.tgz', self.root / 'restored'
        archive(path)
        r = types.SimpleNamespace(private_dir=mock.Mock(), sync_dir=mock.Mock())
        for rel in ('cluster/ttl.json', 'tenants.json', 'updates/signing.key', 'updates/signing.key.meta',
                    'dbs/synthetic_0/records.red-xai', 'keys.red-xai-keys'):
            for missing in (True, False):
                with self.subTest(file=rel, missing=missing):
                    if dest.exists():
                        shutil.rmtree(dest)
                    dest.mkdir()
                    restore_fixture(path, dest)
                    if missing:
                        (dest / rel).unlink()
                    elif rel == 'keys.red-xai-keys':
                        keys = json.loads((dest / rel).read_bytes())
                        keys['keys'][0]['owner'] = 'other-synthetic-owner'
                        (dest / rel).write_text(json.dumps(keys))
                    else:
                        (dest / rel).write_bytes(b'corrupted')
                    with mock.patch.object(c, 'regular_fd', regular_portable), mock.patch.object(c.os, 'fsync'):
                        with self.assertRaises(Exception):
                            c.restored_bytes(r, path, dest, NAMES)
        shutil.rmtree(dest)
        dest.mkdir()
        restore_fixture(path, dest)
        (dest / 'unarchived').write_bytes(b'extra')
        with mock.patch.object(c, 'regular_fd', regular_portable), mock.patch.object(c.os, 'fsync'):
            with self.assertRaises(Exception):
                c.restored_bytes(r, path, dest, NAMES)

    def test_key_records_compare_all_fields_and_reject_duplicate_ids(self):
        changed = json.loads(json.dumps(KEYS))
        for field in ('hash', 'salt', 'roles', 'owner'):
            changed['keys'][0][field] = ['different'] if field == 'roles' else 'different'
            self.assertNotEqual(c.key_records(json.dumps(changed).encode()), c.key_records(json.dumps(KEYS).encode()))
            changed = json.loads(json.dumps(KEYS))
        changed['keys'].append(changed['keys'][0])
        with self.assertRaises(c.Refused):
            c.key_records(json.dumps(changed).encode())

    def test_lock_owner_rejects_other_pid_waiters_and_duplicate_owners(self):
        info = types.SimpleNamespace(st_dev=999, st_ino=1234)
        row = '7: FLOCK ADVISORY WRITE 321 08:01:1234 0 EOF\n'
        with mock.patch.object(c.os, 'major', return_value=8, create=True), \
                mock.patch.object(c.os, 'minor', return_value=1, create=True):
            self.assertTrue(c.lock_owner_matches(row, 321, info))
            for bad in (row.replace('321', '322'), row.replace('FLOCK', 'POSIX'),
                        row.replace('FLOCK', '-> FLOCK'), row + row, row.replace('1234', '1235')):
                self.assertFalse(c.lock_owner_matches(bad, 321, info))

    def test_service_baseline_requires_all_expected_properties(self):
        value = dict(zip(c.FIELDS, ('active', 'running', '321', '1000', '0')))
        r = types.SimpleNamespace(UNITS=('synthetic.service',), props=lambda *a: dict(value))
        self.assertEqual(c.services(r)['synthetic.service'], value)
        value['ActiveState'] = 'inactive'
        with self.assertRaises(c.Refused):
            c.services(r)

    def test_main_withholds_private_error_and_unsupported_platform_refuses(self):
        output = io.StringIO()
        args = ['capture.py', '--library-file', 'private.py', '--expected-inventory', 'private.json']
        with mock.patch.object(sys, 'argv', args), mock.patch.object(c.sys, 'platform', 'unsupported'), \
                contextlib.redirect_stdout(output):
            self.assertEqual(c.main(), 2)
        self.assertEqual(json.loads(output.getvalue()), {'ok': False, 'privateOutputWithheld': True,
                                                        'partialEvidencePreserved': True})
        self.assertNotIn('private.py', output.getvalue())
        output = io.StringIO()
        with mock.patch.object(sys, 'argv', args), mock.patch.object(c.sys, 'platform', 'linux'), \
                mock.patch.object(c.os, 'geteuid', return_value=0, create=True), \
                mock.patch.object(c, 'expected_inventory', side_effect=c.Refused('synthetic-private-detail')), \
                contextlib.redirect_stdout(output):
            self.assertEqual(c.main(), 2)
        self.assertFalse(json.loads(output.getvalue())['ok'])
        self.assertNotIn('synthetic-private-detail', output.getvalue())


class CaptureFlowTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.data, self.backups = self.root / 'data', self.root / 'backups'
        self.data.mkdir()
        self.backups.mkdir()
        self.source = self.root / 'synthetic.tgz'
        archive(self.source, NAMES + ['synthetic_extra'])
        self.r = pinned_library()
        self.r.BACKUPS, self.r.members = self.backups, synthetic_layout
        self.r.private_dir, self.r.sync_dir = mock.Mock(), mock.Mock()
        self.r.resumed_health = mock.Mock()
        def write(path, raw):
            with path.open('xb') as target:
                target.write(raw)

        self.r.write = write
        self.selected, self.baseline = {'pid': 321, 'startTicks': '1000'}, {'synthetic': {'MainPID': '321'}}
        self.native = types.SimpleNamespace(pw_uid=0, pw_gid=0)
        self.commands = []

        def run(args, timeout=60):
            values = [str(x) for x in args]
            self.assertTrue(c.allowed_command(values, self.r))
            self.commands.append(values)
            if 'backup' in values:
                Path(values[-1]).write_bytes(self.source.read_bytes())
            else:
                dest = Path(values[-1])
                self.assertEqual(list(dest.iterdir()), [])
                self.assertNotEqual(dest, self.data)
                restore_fixture(Path(values[2]), dest)

        def copy(r, source, dest, uid, gid):
            before = source.stat()
            with dest.open('xb') as out:
                out.write(source.read_bytes())
            return before, r.sha(dest)

        self.r.run = run
        for patcher in (mock.patch.object(c, 'DATA', self.data), mock.patch.object(c, 'trusted_parents'),
                        mock.patch.object(c, 'inspect', return_value=(self.selected, self.baseline, self.native)),
                        mock.patch.object(c, 'native_identity', return_value=self.selected),
                        mock.patch.object(c, 'services', return_value=self.baseline),
                        mock.patch.object(c, 'regular_fd', regular_portable),
                        mock.patch.object(c, 'copy_export', copy), mock.patch.object(c.os, 'fsync')):
            patcher.start()
            self.addCleanup(patcher.stop)

    def test_default_inspection_creates_nothing(self):
        result = c.capture(self.r, EXPECTED)
        self.assertTrue(result['inspectionOnly'])
        self.assertEqual(list(self.backups.iterdir()), [])
        self.assertEqual(list(self.data.iterdir()), [])
        self.assertEqual(self.commands, [])

    def test_capture_retains_export_exact_extra_inventory_and_private_receipt(self):
        result = c.capture(self.r, EXPECTED, True)
        self.assertTrue(result['ok'])
        self.assertTrue(result['temporaryExportRetained'])
        self.assertEqual(result['databaseCount'], 8)
        self.assertEqual(len(self.commands), 2)
        self.assertEqual(len(list(self.data.glob('.native-seven-*.tgz'))), 1)
        bundle = Path(result['bundleDirectory'])
        receipt = json.loads((bundle / 'receipt.json').read_bytes())
        self.assertEqual(receipt['databases'], NAMES + ['synthetic_extra'])
        self.assertEqual(receipt['expectedInventorySha256'], EXPECTED['sha256'])
        self.assertEqual(receipt['temporaryExportSha256'], self.r.sha(bundle / 'native.tgz'))
        for private in NAMES + ['synthetic-owner', 'synthetic-hash']:
            self.assertNotIn(private, json.dumps(result))
        self.assertFalse(receipt['cmsPairVerified'])
        self.assertFalse(receipt['offServerVerified'])

    def test_service_change_and_restore_failure_preserve_all_evidence(self):
        with mock.patch.object(c, 'services', return_value={'changed': True}):
            with self.assertRaises(c.Refused):
                c.capture(self.r, EXPECTED, True)
        self.assertEqual(len(self.commands), 1)
        self.assertEqual(len(list(self.data.glob('.native-seven-*.tgz'))), 1)
        with mock.patch.object(c, 'restored_bytes', side_effect=c.Refused('synthetic private failure')):
            with self.assertRaises(c.Refused):
                c.capture(self.r, EXPECTED, True)
        self.assertEqual(len(list(self.data.glob('.native-seven-*.tgz'))), 2)
        self.assertEqual(len(list(self.backups.rglob('native.tgz'))), 1)
        self.assertEqual(len(list(self.backups.rglob('receipt.json'))), 0)

    def test_fsync_failure_keeps_export_and_archive_without_success_receipt(self):
        with mock.patch.object(c.os, 'fsync', side_effect=OSError('synthetic fsync failure')):
            with self.assertRaises(OSError):
                c.capture(self.r, EXPECTED, True)
        self.assertEqual(len(list(self.data.glob('.native-seven-*.tgz'))), 1)
        self.assertEqual(len(list(self.backups.rglob('native.tgz'))), 1)
        self.assertEqual(len(list(self.backups.rglob('receipt.json'))), 0)

    def test_native_or_hub_identity_change_after_restore_refuses_receipt(self):
        with mock.patch.object(c, 'native_identity', side_effect=[self.selected, {'pid': 322}]):
            with self.assertRaises(c.Refused):
                c.capture(self.r, EXPECTED, True)
        with mock.patch.object(c, 'services', side_effect=[self.baseline, self.baseline, {'hubPid': 322}]):
            with self.assertRaises(c.Refused):
                c.capture(self.r, EXPECTED, True)
        self.assertEqual(len(list(self.data.glob('.native-seven-*.tgz'))), 2)
        self.assertEqual(len(list(self.backups.rglob('receipt.json'))), 0)


@unittest.skipUnless(sys.platform == 'linux' and hasattr(os, 'geteuid') and os.geteuid() == 0,
                     'Linux root required; Windows mocks do not attest POSIX safety')
class LinuxRootTests(unittest.TestCase):
    def setUp(self):
        # /tmp is intentionally not trusted by the production ancestor guard.
        self.temp = tempfile.TemporaryDirectory(prefix='.native-capture-test-', dir='/root')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.r = pinned_library()

    def test_real_private_inventory_parent_mode_symlink_and_hardlink_guards(self):
        path = self.root / 'expected.json'
        path.write_text(json.dumps({'schema': 1, 'databases': NAMES}))
        path.chmod(0o600)
        self.assertEqual(c.expected_inventory(path)['databases'], NAMES)
        path.chmod(0o640)
        with self.assertRaises(c.Refused):
            c.expected_inventory(path)
        path.chmod(0o600)
        link = self.root / 'link.json'
        link.symlink_to(path)
        with self.assertRaises(c.Refused):
            c.expected_inventory(link)
        os.link(path, self.root / 'hardlink.json')
        with self.assertRaises(c.Refused):
            c.expected_inventory(path)
        self.root.chmod(0o770)
        with self.assertRaises(c.Refused):
            c.trusted_parents(path)

    def test_real_copy_is_durable_exclusive_private_and_rejects_changed_export(self):
        source, dest = self.root / 'export.tgz', self.root / 'copy.tgz'
        source.write_bytes(b'synthetic archive bytes')
        source.chmod(0o600)
        before, digest = c.copy_export(self.r, source, dest, 0, 0)
        self.assertEqual(digest, self.r.sha(dest))
        self.assertEqual(stat.S_IMODE(dest.stat().st_mode), 0o600)
        with self.assertRaises(FileExistsError):
            c.copy_export(self.r, source, dest, 0, 0)
        c.unchanged_export(source, before, 0, 0, digest)
        source.rename(self.root / 'original.tgz')
        source.write_bytes(b'synthetic archive bytes')
        source.chmod(0o600)
        with self.assertRaises(c.Refused):
            c.unchanged_export(source, before, 0, 0, digest)
        source.unlink()
        source.symlink_to(self.root / 'original.tgz')
        with self.assertRaises(c.Refused):
            c.copy_export(self.r, source, self.root / 'other.tgz', 0, 0)

    def test_real_restore_payload_modes_ownership_and_fsync(self):
        path, dest = self.root / 'archive.tgz', self.root / 'restored'
        archive(path)
        dest.mkdir(mode=0o700)
        restore_fixture(path, dest)
        result = c.restored_bytes(self.r, path, dest, NAMES)
        self.assertEqual(result['keyRecordCount'], 2)
        target = dest / 'cluster' / 'ttl.json'
        target.chmod(0o644)
        with self.assertRaises(c.Refused):
            c.restored_bytes(self.r, path, dest, NAMES)
        target.chmod(0o600)
        os.chown(target, 65534, 65534)
        with self.assertRaises(c.Refused):
            c.restored_bytes(self.r, path, dest, NAMES)

    def test_real_flock_pid_and_open_inode_binding(self):
        import select
        lock = self.root / 'redxd.lock'
        lock.touch(mode=0o600)
        code = ('import fcntl,sys; f=open(sys.argv[1], "rb"); '
                'fcntl.flock(f, fcntl.LOCK_EX); print("ready",flush=True); sys.stdin.read()')
        child = subprocess.Popen([sys.executable, '-I', '-B', '-c', code, str(lock)],
                                 stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            self.assertTrue(select.select([child.stdout], [], [], 5)[0], 'synthetic child readiness timeout')
            self.assertEqual(child.stdout.readline(), b'ready\n')
            native = types.SimpleNamespace(pw_uid=0, pw_gid=0)
            selected = {'pid': child.pid, 'startTicks': 'synthetic'}
            r = types.SimpleNamespace(native_identity=lambda _: selected)
            with mock.patch.object(c, 'DATA', self.root):
                result = c.native_identity(r, native)
                self.assertEqual(result['lockInode'], lock.stat().st_ino)
                lock.rename(self.root / 'old.lock')
                lock.touch(mode=0o600)
                with self.assertRaises(c.Refused):
                    c.native_identity(r, native)
        finally:
            child.communicate(timeout=5)


if __name__ == '__main__':
    unittest.main()
