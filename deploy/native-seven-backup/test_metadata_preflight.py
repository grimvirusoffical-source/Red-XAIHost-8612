"""Synthetic metadata-only fixtures; never inspect production data or run capture."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('metadata_preflight_under_test', HERE / 'metadata_preflight.py')
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
NAMES = [f'synthetic_{i}' for i in range(7)]


class SummaryTests(unittest.TestCase):
    def test_capacity_is_conservative_and_quota_independent(self):
        fs = {'allocationUnit': 4096, 'availableBytes': 100 * m.GIB, 'availableInodes': 1000000}
        result = m.capacity(7, 8, fs, fs, True)
        self.assertEqual(result['sharedRequiredBytes'], 3 * 4 * m.GIB + result['restoredPlanningBytes'] + 2 * m.GIB)
        self.assertTrue(result['byteAndInodeEnvelopeSatisfied'])
        poor = dict(fs, availableInodes=1)
        self.assertFalse(m.capacity(7, 8, poor, fs, True)['byteAndInodeEnvelopeSatisfied'])
        self.assertFalse(m.capacity(7, 8, fs, dict(fs, availableBytes=0), False)['byteAndInodeEnvelopeSatisfied'])

    def test_result_repr_and_public_copy_never_include_private_names(self):
        result = m.Result({'databaseCount': 7, 'quotaEvidenceVerified': False}, NAMES)
        self.assertEqual(result.private_database_names(), tuple(NAMES))
        view = result.public_result()
        view['databaseCount'] = 99
        self.assertEqual(result.public_result()['databaseCount'], 7)
        for name in NAMES:
            self.assertNotIn(name, repr(result))
        self.assertNotIn('ready', repr(result).lower())

    def test_public_errors_withhold_os_paths_and_private_names(self):
        text = io.StringIO()
        with mock.patch.object(m, 'scan', side_effect=OSError('synthetic-private-name')), contextlib.redirect_stdout(text):
            self.assertEqual(m.main(), 2)
        self.assertFalse(json.loads(text.getvalue())['metadataPreflightCompleted'])
        self.assertNotIn('synthetic-private-name', text.getvalue())


@unittest.skipUnless(sys.platform == 'linux' and hasattr(os, 'geteuid') and os.geteuid() == 0,
                     'Linux root required for real directory-descriptor/ownership fixtures')
class PosixMetadataTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='.metadata-preflight-test-', dir='/root')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.data, self.backups = self.root / 'data', self.root / 'backups'
        self.data.mkdir(mode=0o700)
        self.backups.mkdir(mode=0o700)
        self.dbs = self.data / 'dbs'
        self.dbs.mkdir(mode=0o700)
        for name in NAMES:
            directory = self.dbs / name
            directory.mkdir(mode=0o700)
            self.file(directory / 'records', b'synthetic')
        for rel in m.METADATA:
            path = self.data / rel
            path.parent.mkdir(mode=0o700, exist_ok=True)
            self.file(path, b'synthetic metadata')

    @staticmethod
    def file(path, content):
        path.write_bytes(content)
        path.chmod(0o600)

    def scan(self, **kw):
        return m._scan(self.data, self.backups, 0, 0, **kw)

    def test_two_pass_scan_never_opens_file_contents_or_writes(self):
        original = os.open

        def directory_only(path, flags, *args, **kw):
            self.assertTrue(flags & os.O_DIRECTORY)
            self.assertFalse(flags & (os.O_WRONLY | os.O_RDWR | os.O_CREAT | os.O_TRUNC))
            return original(path, flags, *args, **kw)

        with mock.patch.object(m.os, 'open', side_effect=directory_only), \
                mock.patch('builtins.open', side_effect=AssertionError('file content open forbidden')):
            result = self.scan()
        summary = result.public_result()
        self.assertEqual(result.private_database_names(), tuple(NAMES))
        self.assertEqual(summary['databaseCount'], 7)
        self.assertTrue(summary['metadataStableAcrossTwoPasses'])
        self.assertFalse(summary['quotaEvidenceVerified'])
        self.assertFalse(summary['capacityReserved'])
        self.assertFalse(summary['applicationOrWalConsistencyVerified'])

    def test_links_modes_and_hardlinks_refused_without_repair(self):
        target = self.dbs / NAMES[0] / 'records'
        target.chmod(0o644)
        with self.assertRaises(m.Refused):
            self.scan()
        self.assertEqual(target.stat().st_mode & 0o777, 0o644)
        target.chmod(0o600)
        link = self.dbs / NAMES[0] / 'hardlink'
        os.link(target, link)
        with self.assertRaises(m.Refused):
            self.scan()
        link.unlink()
        link.symlink_to(target)
        with self.assertRaises(m.Refused):
            self.scan()

    def test_mutation_between_passes_is_refused(self):
        original, visits = os.scandir, []
        inode = self.dbs.stat().st_ino

        def change_at_second_pass(fd):
            if os.fstat(fd).st_ino == inode:
                visits.append(True)
                if len(visits) == 2:
                    (self.dbs / NAMES[0] / 'records').write_bytes(b'changed synthetic size')
            return original(fd)

        with mock.patch.object(m.os, 'scandir', side_effect=change_at_second_pass):
            with self.assertRaises(m.Refused):
                self.scan()

    def test_time_depth_entry_limits_and_near_limit_logical_size_refused(self):
        for limits in ({'seconds': 0}, {'depth_limit': 0}, {'entry_limit': 8}):
            with self.subTest(limits=limits), self.assertRaises(m.Refused):
                self.scan(**limits)
        # Sparse synthetic fixture: capacity must count logical, not allocated bytes.
        with (self.dbs / NAMES[0] / 'records').open('r+b') as output:
            output.truncate(3 * m.GIB)
        with self.assertRaises(m.Refused):
            self.scan()

    def test_optional_absence_is_supported_but_keystore_is_required(self):
        for rel in m.METADATA[1:]:
            (self.data / rel).unlink()
        self.assertEqual(self.scan().public_result()['databaseCount'], 7)
        (self.data / m.METADATA[0]).unlink()
        with self.assertRaises(m.Refused):
            self.scan()


if __name__ == '__main__':
    unittest.main()
