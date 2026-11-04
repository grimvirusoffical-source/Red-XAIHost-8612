"""Local synthetic archive, state-machine and unit checks. Never Linux main."""
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import tarfile
import tempfile
import types
import unittest
from unittest.mock import Mock, patch

SPEC = importlib.util.spec_from_file_location('cms_recovery', Path(__file__).with_name('cms_recovery.py'))
M = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(M)


class RecoveryTests(unittest.TestCase):
    def test_reviewed_pins_are_full_lowercase_sha256_and_native_matches_grouped_evidence(self):
        # Independent transcription of the parent's eight 8-hex evidence groups.
        # This proves pin representation, not a server/VCS provenance claim.
        grouped = ('d99a44f1', 'c41f87dc', '8f9571c6', 'ebb4daec',
                   '34948a70', 'fb145b81', 'bf0e134b', 'f5403748')
        self.assertEqual(M.NATIVE_SHA, ''.join(grouped))
        for label, digest in [('native', M.NATIVE_SHA), *M.MODULES.items()]:
            with self.subTest(pin=label):
                self.assertRegex(digest, r'\A[a-f0-9]{64}\Z')
                self.assertEqual(len(bytes.fromhex(digest)), 32)

    def test_code_hash_guard_refuses_bad_format_before_hashing_and_still_requires_exact_bytes(self):
        # Exercise the actual guard with synthetic metadata, without opening
        # production files or assuming Windows can validate root ownership.
        parent = Mock()
        parent.lstat.return_value = types.SimpleNamespace(st_uid=0, st_mode=stat.S_IFDIR | 0o755)
        code = Mock()
        code.parents = (parent,)
        code.lstat.return_value = types.SimpleNamespace(st_uid=0, st_mode=stat.S_IFREG | 0o755, st_nlink=1)
        for malformed in ['a' * 63, 'a' * 65, 'A' * 64, 'g' * 64, 'a' * 64 + '\n']:
            with self.subTest(format=repr(malformed)), patch.object(M, 'safe', return_value=code), \
                    patch.object(M, 'sha') as hash_bytes:
                with self.assertRaisesRegex(M.Refused, 'code_hash_mismatch'):
                    M.code_file('/reviewed/synthetic-code', malformed, True)
                hash_bytes.assert_not_called()
        with patch.object(M, 'safe', return_value=code), patch.object(M, 'sha', return_value='b' * 64):
            with self.assertRaisesRegex(M.Refused, 'code_hash_mismatch'):
                M.code_file('/reviewed/synthetic-code', 'a' * 64, True)
        with patch.object(M, 'safe', return_value=code), patch.object(M, 'sha', return_value='a' * 64):
            self.assertEqual(M.code_file('/reviewed/synthetic-code', 'a' * 64, True), code.lstat.return_value)

    def archive(self, root, entries):
        target = root / 'input.tgz'
        with tarfile.open(target, 'w:gz') as tar:
            for name, kind, data in entries:
                info = tarfile.TarInfo(name)
                info.type = kind
                info.size = len(data) if kind == tarfile.REGTYPE else 0
                if kind == tarfile.SYMTYPE:
                    info.linkname = '/outside'
                tar.addfile(info, io.BytesIO(data) if info.size else None)
        return target

    def native_entries(self):
        return [(f'dbs/{name}/', tarfile.DIRTYPE, b'') for name in
                ('default', 'fl_studio_db', 'website_cms', 'fixture_a', 'fixture_b')] + [
                    ('keys.json', tarfile.REGTYPE, b'{"version":1,"keys":[]}'),
                    ('ttl.json', tarfile.REGTYPE, b'[]')]

    def test_native_keeps_all_five_and_optional_secret_metadata_without_printing(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            entries = self.native_entries() + [('updates/signing.key', tarfile.REGTYPE, b'private-fixture-only')]
            v = M.members(self.archive(root, entries), True)
            self.assertEqual(len(v['databases']), 5)
            self.assertIn('fl_studio_db', v['databases'])
            self.assertIn('updates/signing.key', v['metadata'])
            self.assertNotIn('private-fixture-only', repr(v))

    def test_missing_database_never_selectively_accepts(self):
        with tempfile.TemporaryDirectory() as tmp:
            entries = [x for x in self.native_entries() if 'fl_studio_db' not in x[0]]
            with self.assertRaises(M.Refused):
                M.members(self.archive(Path(tmp), entries), True)

    def test_traversal_links_duplicates_and_unknown_roots_rejected(self):
        for bad in [('../escape', tarfile.REGTYPE, b'x'), ('/absolute', tarfile.REGTYPE, b'x'),
                    ('dbs/link', tarfile.SYMTYPE, b''), ('keys.json', tarfile.REGTYPE, b'x'),
                    ('config.toml', tarfile.REGTYPE, b'fixture')]:
            with self.subTest(bad=bad[0]), tempfile.TemporaryDirectory() as tmp:
                with self.assertRaises(M.Refused):
                    M.members(self.archive(Path(tmp), self.native_entries() + [bad]), True)

    def test_full_gzip_crc_is_checked(self):
        with tempfile.TemporaryDirectory() as tmp:
            a = self.archive(Path(tmp), self.native_entries())
            data = bytearray(a.read_bytes())
            data[-8] ^= 1
            a.write_bytes(data)
            with self.assertRaises((M.Refused, OSError)):
                M.members(a, True)

    def test_support_roundtrip_matches_full_audit_not_only_probe_prefix(self):
        if os.name == 'nt':
            self.skipTest('POSIX support mode semantics require Linux; no main is run')
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / 'support'
            root.mkdir(mode=0o700)
            (root / 'media').mkdir(mode=0o700)
            for name, data in [('audit.jsonl', b'prefix\nsuffix\n'), ('maintenance-probe-v1.json', b'{}'), ('media/fixture.webp', b'RIFFfixtureWEBP')]:
                M.write(root / name, data)
            expected = M.inventory(root)
            archive = Path(tmp) / 'support.tgz'
            M.support_archive(root, archive, expected)
            restored = Path(tmp) / 'restored'
            M.extract_support(archive, restored, expected)
            self.assertEqual(M.inventory(restored), expected)
            (restored / 'audit.jsonl').write_bytes(b'prefix\nchanged\n')
            self.assertNotEqual(M.inventory(restored), expected)

    def test_native_export_command_has_no_force_key_or_http(self):
        source = Path(M.__file__).read_text()
        self.assertIn("'backup', '--offline', '--data-dir'", source)
        self.assertNotIn("'--force'", source)
        self.assertNotIn('Authorization', source)
        self.assertNotIn('REDX_KEY', source)

    def test_unit_is_one_namespace_and_rootdirectory_without_live_support_bind(self):
        with patch.object(M, 'safe', side_effect=lambda p: Path(p)):
            text = M.unit_text(Path('/protected/cms-restore-fixture'))
        for required in ['PrivateNetwork=yes', 'RootDirectory=/protected/cms-restore-fixture/jail',
                         'ReadWritePaths=+/work', 'InaccessiblePaths=+/proc +/sys',
                         'bootstrap', 'NoNewPrivileges=yes']:
            if required == 'bootstrap':
                continue  # The generated TOML carries bootstrap=off, not the unit.
            self.assertIn(required, text)
        self.assertIn('site-cms.key:/credential', text)
        self.assertNotIn('/var/lib/red-xai-site:', text)
        self.assertNotIn('site.env', text)
        self.assertNotIn('cloudflared', text)

    def test_capture_failure_still_resumes_only_previously_active_hub(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            a = types.SimpleNamespace(apply=True, exclusive_writer_reviewed=True)
            calls = []
            with patch.object(M, 'preflight', return_value=(root, {}, {'ActiveState': 'active'})), \
                    patch.object(M, 'services', return_value={}), patch.object(M, 'private_dir'), \
                    patch.object(M, 'BACKUPS', root), patch.object(M, 'SUPPORT', root), \
                    patch.object(M, 'run', side_effect=lambda argv, **kw: calls.append(argv)), \
                    patch.object(M, 'stopped', side_effect=M.Refused('fixture-stop-check')), \
                    patch.object(M, 'resumed_health'):
                with self.assertRaises(M.Refused):
                    M.capture(a)
            self.assertEqual(calls, [['/usr/bin/systemctl', 'stop', M.HUB], ['/usr/bin/systemctl', 'start', M.HUB]])

    def test_failed_stop_is_also_armed_for_restart(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            a = types.SimpleNamespace(apply=True, exclusive_writer_reviewed=True)
            calls = []
            def command(argv, **kw):
                calls.append(argv)
                if argv[1] == 'stop':
                    raise M.Refused('post-stop-command-failure')
            with patch.object(M, 'preflight', return_value=(root, {}, {'ActiveState': 'active'})), \
                    patch.object(M, 'services', return_value={}), patch.object(M, 'private_dir'), \
                    patch.object(M, 'BACKUPS', root), patch.object(M, 'SUPPORT', root), \
                    patch.object(M, 'run', side_effect=command), patch.object(M, 'resumed_health'):
                with self.assertRaises(M.Refused):
                    M.capture(a)
            self.assertEqual(calls[-1], ['/usr/bin/systemctl', 'start', M.HUB])

    def test_inspection_does_not_stop_write_or_seed(self):
        a = types.SimpleNamespace(apply=False, ipc_user='native')
        with patch.object(M, 'preflight', return_value=(Path('/fixture'), {}, {'ActiveState': 'active'})), \
                patch.object(M, 'services', return_value={}), patch.object(M, 'run') as command:
            result = M.capture(a)
        self.assertTrue(result['inspectionOnly'])
        command.assert_not_called()


if __name__ == '__main__':
    unittest.main()
