import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import zlib

spec = importlib.util.spec_from_file_location("native_source_locator", Path(__file__).with_name("locate-native-source.py"))
loc = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = loc
spec.loader.exec_module(loc)


class FakeFile(io.BytesIO):
    def __init__(self, data, handle, reads):
        super().__init__(data)
        self.handle, self.reads = handle, reads

    def fileno(self):
        return self.handle

    def read(self, size=-1):
        result = super().read(size)
        self.reads.append((self.handle, len(result)))
        return result


class FakeFS:
    """Synthetic byte-bearing files prove privacy without visiting the host."""
    def __init__(self):
        self.nodes, self.opened, self.reads, self.enumerated = {}, [], [], 0
        self.directory_race = None
        self.file_race = None
        for root in loc.ROOTS:
            self.add(root, "directory")

    def add(self, path, kind="file", data=b"", device=1, links=1, declared_size=None):
        parent = path.rsplit("/", 1)[0]
        if parent and parent not in self.nodes:
            self.add(parent, "directory")
        mode = {"file": stat.S_IFREG, "directory": stat.S_IFDIR, "symlink": stat.S_IFLNK, "fifo": stat.S_IFIFO}[kind] | 0o600
        self.nodes[path] = {"kind": kind, "data": data,
            "info": SimpleNamespace(st_dev=device, st_ino=len(self.nodes)+1, st_size=len(data) if declared_size is None else declared_size,
                st_mtime_ns=123, st_mode=mode, st_nlink=links)}
        return path

    def root(self, path):
        node = self.nodes[path]
        if node["kind"] != "directory":
            raise OSError("PRIVATE ERROR TEXT MUST NOT APPEAR")
        return path

    def close(self, handle):
        pass

    def own_stat(self, handle):
        return copy.copy(self.nodes[handle]["info"])

    def stat(self, handle, name):
        path = handle + "/" + name
        if path not in self.nodes:
            raise FileNotFoundError()
        return self.own_stat(path)

    def names(self, handle):
        for path in self.nodes:
            if path.rsplit("/", 1)[0] == handle:
                self.enumerated += 1
                yield path.rsplit("/", 1)[1]

    def directory(self, handle, name):
        path = handle + "/" + name
        self.opened.append(path)
        if self.nodes[path]["kind"] != "directory":
            raise OSError("PRIVATE ERROR TEXT MUST NOT APPEAR")
        if self.directory_race == path:
            self.nodes[path]["info"].st_ino += 1000
        return path

    def file(self, handle, name):
        path = handle + "/" + name
        self.opened.append(path)
        if self.file_race == path:
            self.nodes[path]["info"].st_ino += 1000
        return FakeFile(self.nodes[path]["data"], path, self.reads)


class LocatorTests(unittest.TestCase):
    def test_exact_go_filenames_only_and_optional_old_adapter(self):
        for path in ("x/host/acct/service.go", "host/accounts_native.go", "cmd/redx-mailrelay/main.go", "cmd/redx-gdg-accounts/main.go"):
            self.assertIsNotNone(loc.source_kind(tuple(path.split("/"))))
        for path in ("x/service.go", "host/acct/deeper/service.go", "cmd/redx-mailrelay/other.go", "host/accounts.go", "host/acct/service.go.bak", "host/Acct/secret.go"):
            self.assertIsNone(loc.source_kind(tuple(path.split("/"))))
        self.assertEqual(loc.source_kind(("host", "accounts.go"), True), "legacy_host_accounts_optional")

    def test_privacy_pruning_and_metadata_markers_without_contents(self):
        fs = FakeFS()
        secret = b'const password = "SYNTHETIC-PRIVATE-MARKER"'
        source = fs.add('/root/repo/host/acct/service.go', data=secret)
        fs.add('/root/repo/go.mod', data=b'PRIVATE MODULE CONTENTS')
        fs.add('/root/repo/.git/config', data=b'PRIVATE VCS CONTENTS')
        fs.add('/root/repo/.env', data=b'PRIVATE CREDENTIAL CONTENTS')
        fs.add('/root/repo/other/service.go', data=secret)
        for directory in loc.PRUNE:
            fs.add('/root/repo/'+directory+'/host/acct/crypto.go', data=secret)
        fs.add('/root/link', kind='symlink', data=b'/etc/private')
        fs.add('/root/repo/host/accounts_native.go', kind='symlink', data=b'/etc/private')
        report = loc.scan(loc.Limits(), fs=fs)
        self.assertEqual([x['path'] for x in report['matches']], [source])
        self.assertEqual(report['matches'][0]['closestGoModDirectory'], '/root/repo')
        self.assertEqual(report['matches'][0]['closestRepositoryDirectory'], '/root/repo')
        self.assertFalse(fs.reads)
        self.assertFalse(any('/.git' in x or '/.ssh' in x or '/data/' in x for x in fs.opened))
        encoded = json.dumps(report)
        for value in ('SYNTHETIC-PRIVATE-MARKER', 'PRIVATE MODULE', 'PRIVATE VCS', 'PRIVATE CREDENTIAL', '/etc/private'):
            self.assertNotIn(value, encoded)
        self.assertGreater(report['stats']['policyPruned'], 0)
        self.assertEqual(report['stats']['symlinksPruned'], 2)
        self.assertFalse(report['coverage']['sourceAbsenceProven'])
        self.assertFalse(report['coverage']['sourceParityProven'])

    def test_optional_hash_is_bounded_and_never_returns_source(self):
        fs = FakeFS()
        a = fs.add('/root/repo/host/acct/one.go', data=b'abc')
        b = fs.add('/root/repo/host/acct/two.go', data=b'def')
        report = loc.scan(loc.Limits(total_hash_bytes=4), sha256=True, fs=fs)
        bypath = {x['path']: x for x in report['matches']}
        self.assertEqual(bypath[a]['hash'], {'status':'verified', 'sha256':hashlib.sha256(b'abc').hexdigest(), 'bytes':3})
        self.assertEqual(bypath[b]['hash']['status'], 'skipped_total_hash_cap')
        self.assertEqual(report['stats']['hashBytesRead'], 3)
        self.assertNotIn('abc', json.dumps(report))
        fs = FakeFS()
        fs.add('/root/repo/host/acct/big.go', data=b'abc')
        fs.add('/root/repo/host/acct/hard.go', data=b'a', links=2)
        report = loc.scan(loc.Limits(file_bytes=2), sha256=True, fs=fs)
        self.assertEqual([x['hash']['status'] for x in report['matches']], ['skipped_file_size_cap','refused_hardlink'])
        self.assertFalse(fs.reads)

    def test_growth_truncation_and_replacement_never_publish_a_digest(self):
        for data, advertised in ((b'growme', 2), (b'x', 4)):
            fs = FakeFS()
            fs.add('/root/host/acct/file.go', data=data, declared_size=advertised)
            report = loc.scan(loc.Limits(file_bytes=4), sha256=True, fs=fs)
            self.assertEqual(report['matches'][0]['hash']['status'], 'refused_changed_file')
            self.assertNotIn('sha256', report['matches'][0]['hash'])
            self.assertLessEqual(report['stats']['hashBytesRead'], 4)
        fs = FakeFS()
        fs.file_race = fs.add('/root/host/acct/file.go', data=b'private')
        report = loc.scan(loc.Limits(), sha256=True, fs=fs)
        self.assertEqual(report['matches'][0]['hash']['status'], 'refused_changed_file')
        self.assertFalse(fs.reads)

    def test_entry_match_depth_and_clock_budgets_are_global_and_explicit(self):
        fs = FakeFS()
        for i in range(100):
            fs.add('/root/junk'+str(i))
        report = loc.scan(loc.Limits(entries=3), fs=fs)
        self.assertEqual(fs.enumerated, 3)
        self.assertEqual(report['stats']['entries'], 3)
        self.assertEqual(report['coverage']['stopReasons'], ['entry_budget'])
        self.assertEqual(report['roots'][1]['status'], 'not_started_budget')
        fs = FakeFS()
        fs.add('/root/host/accounts_native.go')
        fs.add('/opt/host/accounts_native.go')
        report = loc.scan(loc.Limits(matches=1), fs=fs)
        self.assertEqual(len(report['matches']), 1)
        self.assertEqual(report['coverage']['stopReasons'], ['match_budget'])
        report = loc.scan(loc.Limits(depth=0), fs=fs)
        self.assertEqual(report['stats']['depthPruned'], 2)
        self.assertFalse(report['matches'])
        ticks = iter(x/10 for x in range(1000))
        report = loc.scan(loc.Limits(seconds=1), fs=fs, clock=lambda:next(ticks))
        self.assertEqual(report['coverage']['stopReasons'], ['time_budget'])
        for result in (report, loc.scan(loc.Limits(), fs=FakeFS())):
            self.assertFalse(result['coverage']['sourceAbsenceProven'])
            self.assertFalse(result['coverage']['exhaustiveFilesystemSearch'])

    def test_directory_swap_and_mount_boundaries_are_not_entered(self):
        fs = FakeFS()
        fs.add('/root/swapped/host/acct/secret.go')
        fs.directory_race = '/root/swapped'
        fs.add('/root/mount', kind='directory', device=2)
        fs.add('/root/mount/host/acct/secret.go', device=2)
        report = loc.scan(loc.Limits(), fs=fs)
        self.assertFalse(report['matches'])
        self.assertEqual(report['stats']['crossFilesystemPruned'], 1)
        self.assertEqual(report['stats']['metadataErrors'], 1)
        self.assertNotIn('/root/mount', fs.opened)
        self.assertFalse(report['coverage']['traversalFinishedWithinPolicy'])

    def test_gitfile_symlink_common_directory_and_alternates_never_probe(self):
        for marker, kind in (('.git','file'), ('.git','symlink'), ('.git/commondir','file'), ('.git/config','symlink'), ('.git/objects','symlink'), ('.git/objects/info/alternates','file'), ('.git/objects/info/http-alternates','file')):
            fs = FakeFS()
            fs.add('/root/repo/'+marker, kind=kind, data=b'PRIVATE POINTER')
            fs.add('/root/repo/host/acct/file.go')
            with self.subTest(marker=marker, kind=kind):
                report = loc.scan(loc.Limits(), vcs=True, fs=fs, git_probe=lambda *args:self.fail('external pointer was probed'))
                self.assertEqual(report['stats']['gitRepositoriesProbed'], 0)
                self.assertNotIn('PRIVATE POINTER', json.dumps(report))
                self.assertFalse(fs.reads)

    def test_git_repo_cap_and_nearest_module_do_not_search_ancestors_outside_root(self):
        fs = FakeFS()
        for repo in ('one','two'):
            fs.add('/root/'+repo+'/.git', kind='directory')
            fs.add('/root/'+repo+'/go.mod')
            fs.add('/root/'+repo+'/nested/go.mod')
            fs.add('/root/'+repo+'/nested/host/acct/file.go')
        calls = []
        report = loc.scan(loc.Limits(git_repositories=1), vcs=True, fs=fs, git_probe=lambda *args:calls.append(args) or {})
        self.assertEqual(len(calls), 1)
        self.assertEqual(report['matches'][0]['closestGoModDirectory'], '/root/one/nested')
        self.assertEqual(report['repositories'][1]['status'], 'not_probed_repository_cap')

    def test_git_only_checks_fixed_commit_objects_with_sanitized_environment(self):
        calls = []
        def run(argv, **kwargs):
            calls.append((argv, kwargs))
            return SimpleNamespace(returncode=0 if len(calls)==1 else 128)
        with patch.object(loc.subprocess, 'run', run), patch.dict(os.environ, {'HOME':'PRIVATE-HOME','GIT_DIR':'PRIVATE-OVERRIDE','GIT_SSH_COMMAND':'PRIVATE-COMMAND'}):
            result = loc.probe_git(7, 100, clock=lambda:99.9)
        self.assertEqual(len(calls), 3)
        self.assertEqual(next(iter(result.values()))['status'], 'locally_available_commit_object')
        for argv, kwargs in calls:
            self.assertEqual(argv[:4], ['/usr/bin/git','--git-dir=/proc/self/fd/7','cat-file','-e'])
            self.assertIn(argv[4][:-9], loc.REFERENCES.values())
            self.assertEqual(kwargs['env']['GIT_ALLOW_PROTOCOL'], '')
            self.assertEqual(kwargs['env']['GIT_NO_LAZY_FETCH'], '1')
            self.assertNotIn('HOME', kwargs['env'])
            self.assertNotIn('GIT_DIR', kwargs['env'])
            self.assertNotIn('GIT_SSH_COMMAND', kwargs['env'])
            self.assertLessEqual(kwargs['timeout'], .101)
            self.assertEqual(kwargs['stdout'], subprocess.DEVNULL)
            self.assertEqual(kwargs['stderr'], subprocess.DEVNULL)
        with patch.object(loc.subprocess, 'run', side_effect=OSError('PRIVATE ERROR')):
            result = loc.probe_git(7, 100, clock=lambda:99)
        self.assertNotIn('PRIVATE ERROR', json.dumps(result))
        self.assertTrue(all(x['status']=='not_confirmed' for x in result.values()))

    def test_limits_and_invalid_arguments_never_echo_caller_text(self):
        self.assertTrue(loc.Limits().valid())
        for changes in ({'entries':200001},{'seconds':31},{'depth':33},{'matches':129},{'file_bytes':(5<<20)+1},{'total_hash_bytes':(16<<20)+1},{'git_repositories':9}):
            self.assertFalse(loc.Limits(**changes).valid())
        stdout = io.StringIO()
        with patch.object(loc.sys, 'platform', 'linux'), patch.object(loc,'private_stdout',return_value=True), patch.object(loc.sys,'stdout',stdout), patch.object(loc,'supervised_scan',side_effect=AssertionError('must not scan')):
            self.assertEqual(loc.main(['--root','PRIVATE-CREDENTIAL-TEXT']), 2)
        self.assertNotIn('PRIVATE-CREDENTIAL-TEXT', stdout.getvalue())
        self.assertFalse(json.loads(stdout.getvalue())['coverage']['sourceAbsenceProven'])

    def test_private_stdout_and_json_path_escaping(self):
        info = SimpleNamespace(st_mode=stat.S_IFREG|0o600, st_nlink=1, st_uid=986)
        with patch.object(os,'geteuid',return_value=986,create=True), patch.object(os,'fstat',return_value=info):
            self.assertTrue(loc.private_stdout())
            for field, value in (('st_mode',stat.S_IFREG|0o644),('st_nlink',2),('st_uid',0),('st_mode',stat.S_IFIFO|0o600)):
                bad = copy.copy(info)
                setattr(bad,field,value)
                with patch.object(os,'fstat',return_value=bad):
                    self.assertFalse(loc.private_stdout())
        fs = FakeFS()
        path = fs.add('/root/quoted"\nname/host/acct/service.go')
        encoded = json.dumps(loc.scan(loc.Limits(),fs=fs),ensure_ascii=True,separators=(',',':'))
        self.assertNotIn('\n',encoded)
        self.assertEqual(json.loads(encoded)['matches'][0]['path'],path)

    @unittest.skipUnless(sys.platform.startswith('linux'), 'Linux fork supervisor fixture')
    def test_supervisor_timeout_discards_partial_results_without_running_a_walk(self):
        started = time.monotonic()
        with patch.object(loc,'scan',side_effect=lambda **kwargs:time.sleep(10)):
            report = loc.supervised_scan(loc.Limits(seconds=1))
        self.assertLess(time.monotonic()-started,4)
        self.assertEqual(report['matches'],[])
        self.assertEqual(report['coverage']['stopReasons'],['worker_timeout_or_failure'])
        self.assertTrue(report['coverage']['partialWorkerResultsDiscarded'])
        self.assertFalse(report['coverage']['sourceAbsenceProven'])

    @unittest.skipUnless(sys.platform.startswith('linux') and Path('/usr/bin/git').is_file(), 'Linux isolated cat-file fixture')
    def test_real_cat_file_uses_only_a_synthetic_local_commit_object(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root/'objects'/'info').mkdir(parents=True)
            (root/'refs'/'heads').mkdir(parents=True)
            (root/'HEAD').write_bytes(b'ref: refs/heads/fixture\n')
            (root/'config').write_bytes(b'[core]\nrepositoryformatversion = 0\nbare = true\n')
            body = b'tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nauthor Fixture <fixture@example.invalid> 0 +0000\ncommitter Fixture <fixture@example.invalid> 0 +0000\n\nfixture\n'
            raw = b'commit '+str(len(body)).encode()+b'\0'+body
            oid = hashlib.sha1(raw).hexdigest()
            (root/'objects'/oid[:2]).mkdir()
            (root/'objects'/oid[:2]/oid[2:]).write_bytes(zlib.compress(raw))
            fd = loc.NativeFS().root(directory)
            try:
                with patch.dict(loc.REFERENCES,{'fixture':oid},clear=True):
                    result = loc.probe_git(fd,time.monotonic()+1)
                self.assertEqual(result['fixture']['status'],'locally_available_commit_object')
            finally:
                os.close(fd)

    @unittest.skipUnless(sys.platform.startswith('linux'), 'Linux descriptor/no-follow fixture')
    def test_native_fd_backend_refuses_real_symlinks_and_reads_only_the_fixture(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root/'host'/'acct').mkdir(parents=True)
            (root/'host'/'acct'/'service.go').write_bytes(b'synthetic Go source')
            (root/'link').symlink_to('/etc')
            fs = loc.NativeFS()
            descriptor = fs.root(directory)
            try:
                self.assertTrue(stat.S_ISLNK(fs.stat(descriptor,'link').st_mode))
                with self.assertRaises(OSError):
                    fs.directory(descriptor,'link')
                with self.assertRaises(OSError):
                    fs.file(descriptor,'link')
                self.assertIn('host', list(fs.names(descriptor)))
                self.assertTrue(stat.S_ISDIR(fs.own_stat(descriptor).st_mode))
            finally:
                fs.close(descriptor)


if __name__ == '__main__':
    unittest.main()
