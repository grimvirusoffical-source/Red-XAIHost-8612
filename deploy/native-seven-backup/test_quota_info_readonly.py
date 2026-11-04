"""Mocked quota boundary tests only: never load libc or call quotactl."""
from contextlib import ExitStack, redirect_stdout
import ctypes as C
import errno
import importlib.util
import io
import json
from pathlib import Path, PurePosixPath
import stat
import types
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('quota_info_under_test',
                                             Path(__file__).with_name('quota_info_readonly.py'))
q = importlib.util.module_from_spec(spec)
spec.loader.exec_module(q)  # Guarded import performs no query.


class QuotaBoundaryTests(unittest.TestCase):
    def fixture(self, errors=(0, errno.ESRCH, errno.EPERM), different_fs=False, alias=False):
        stack = ExitStack()
        self.addCleanup(stack.close)
        nodes = {}
        for fd, inode in ((10, 21), (11, 22), (12, 23), (15, 21), (16, 22), (17, 23)):
            nodes[fd] = types.SimpleNamespace(st_dev=17, st_ino=inode)
        if different_fs:
            nodes[11].st_dev = 18
        block = types.SimpleNamespace(st_dev=99, st_ino=30, st_rdev=17, st_uid=0,
                                      st_mode=(stat.S_IFLNK if alias else stat.S_IFBLK) | 0o600)
        nodes[14] = block
        calls = []

        def fake_query(operation, source, identity, buffer):
            calls.append((operation, source, identity))
            self.assertEqual(C.string_at(buffer, 128), bytes(128))
            error = errors[len(calls) - 1]
            C.set_errno(error)
            return -1 if error else 0

        function = mock.Mock(side_effect=fake_query)
        libc = types.SimpleNamespace(quotactl=function)
        patches = (
            mock.patch.object(q.sys, 'platform', 'linux'),
            mock.patch.object(q.os, 'geteuid', return_value=0, create=True),
            mock.patch.object(q.os, 'O_PATH', 0x200000, create=True),
            mock.patch.object(q.os, 'O_NOFOLLOW', 0x20000, create=True),
            mock.patch.object(q.os, 'O_CLOEXEC', 0x80000, create=True),
            mock.patch.object(q, 'directory', side_effect=iter((10, 11, 12, 13, 15, 16, 17))),
            mock.patch.object(q.os, 'open', return_value=14),
            mock.patch.object(q.os, 'fstat', side_effect=lambda fd: nodes[fd]),
            mock.patch.object(q.os, 'stat', return_value=block),
            mock.patch.object(q.os, 'close'),
            mock.patch.object(q, 'root_source', return_value='/dev/synthetic0'),
            mock.patch.object(q.C, 'CDLL', return_value=libc),
        )
        handles = [stack.enter_context(patch) for patch in patches]
        return calls, function, handles[-1], handles[-3]

    def test_only_getinfo_three_types_zero_id_explicit_abi_and_numeric_output(self):
        calls, function, loader, close = self.fixture()
        result = q.query_info()
        self.assertEqual([op & 0xffffffff for op, _, _ in calls],
                         [0x80000500, 0x80000501, 0x80000502])
        self.assertEqual([identity for _, _, identity in calls], [0, 0, 0])
        self.assertEqual([source for _, source, _ in calls], [b'/dev/synthetic0'] * 3)
        self.assertEqual(function.argtypes, (C.c_int, C.c_char_p, C.c_int, C.c_void_p))
        self.assertIs(function.restype, C.c_int)
        loader.assert_called_once_with(None, use_errno=True)
        self.assertEqual(result, {'USRQUOTA': {'rc': 0, 'errno': 0},
                                  'GRPQUOTA': {'rc': -1, 'errno': errno.ESRCH},
                                  'PRJQUOTA': {'rc': -1, 'errno': errno.EPERM}})
        self.assertEqual(sorted(call.args[0] for call in close.call_args_list), list(range(10, 18)))
        self.assertNotIn('synthetic0', json.dumps(result))

    def test_other_filesystem_refuses_before_loading_libc(self):
        calls, _, loader, _ = self.fixture(different_fs=True)
        with self.assertRaises(RuntimeError):
            q.query_info()
        self.assertEqual(calls, [])
        loader.assert_not_called()

    def test_symlink_device_alias_refuses_before_loading_libc(self):
        calls, _, loader, _ = self.fixture(alias=True)
        with self.assertRaises(RuntimeError):
            q.query_info()
        self.assertEqual(calls, [])
        loader.assert_not_called()

    def test_root_source_requires_one_matching_ext4_direct_unescaped_device(self):
        valid = b'10 1 8:1 / / rw - ext4 /dev/synthetic0 rw\n'
        samples = ((valid, True), (valid.replace(b'ext4', b'xfs'), False),
                   (valid.replace(b'8:1', b'8:2'), False), (valid + valid, False),
                   (valid.replace(b'/dev/synthetic0', b'/dev/synthetic\\040root'), False),
                   (valid.replace(b'/dev/synthetic0', b'UUID=synthetic'), False))
        for raw, passes in samples:
            with self.subTest(passes=passes), \
                    mock.patch('builtins.open', side_effect=lambda *a, **k: io.BytesIO(raw)), \
                    mock.patch.object(q, 'Path', PurePosixPath), \
                    mock.patch.object(q.os, 'major', return_value=8, create=True), \
                    mock.patch.object(q.os, 'minor', return_value=1, create=True):
                if passes:
                    self.assertEqual(q.root_source(17), '/dev/synthetic0')
                else:
                    with self.assertRaises(RuntimeError):
                        q.root_source(17)

    def test_failure_redacts_all_paths_and_has_no_quota_status_claim(self):
        output = io.StringIO()
        with mock.patch.object(q, 'query_info', side_effect=RuntimeError('/private/synthetic')), \
                redirect_stdout(output):
            self.assertEqual(q.main(), 2)
        self.assertEqual(json.loads(output.getvalue()), {'rc': None, 'errno': None})
        self.assertNotIn('private', output.getvalue())


if __name__ == '__main__':
    unittest.main()
