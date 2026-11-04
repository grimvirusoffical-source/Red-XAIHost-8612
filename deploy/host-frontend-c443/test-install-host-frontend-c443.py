"""Pure/read-only tests run everywhere; real metadata/atomic drills require Linux root."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
import unittest

HERE=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('host_install',HERE/'install-host-frontend-c443.py')
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
ARCHIVE=HERE/'host-frontend-c443ce7.tgz'


class PublicArtifact(unittest.TestCase):
    def test_pinned_complete_payload(self):
        blobs=m.payload(ARCHIVE)
        self.assertEqual(set(blobs),set(m.FILES))
        self.assertEqual(hashlib.sha256(ARCHIVE.read_bytes()).hexdigest(),m.ARCHIVE_SHA)
        manifest=json.loads((HERE/'host-frontend-c443ce7-manifest.json').read_text())
        for name,data in blobs.items():
            self.assertEqual(manifest['files'][name],{'bytes':len(data),'sha256':m.sha(data)})

    def test_altered_archive_refused(self):
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'altered.tgz';p.write_bytes(ARCHIVE.read_bytes()+b'changed')
            with self.assertRaises(ValueError): m.payload(p)

    def test_symlink_file_refused(self):
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'file';p.write_bytes(b'x')
            link=Path(d)/'link'
            try: link.symlink_to(p)
            except OSError: self.skipTest('Symlink permission unavailable')
            with self.assertRaises(ValueError): m.read_regular(link)

    def test_index_guard_preserves_quick_login(self):
        good=b'<script src="'+m.QUICK+b'"></script><script src="'+m.OLD_MODULE+b'"></script>'
        m.require_index(good,m.sha(good))
        with self.assertRaises(ValueError): m.require_index(good,'0'*64)
        without=good.replace(m.QUICK,b'/wrong')
        with self.assertRaises(ValueError): m.require_index(without,m.sha(without))
        duplicate=good+good
        with self.assertRaises(ValueError): m.require_index(duplicate,m.sha(duplicate))


@unittest.skipUnless(sys.platform=='linux' and os.geteuid()==0,'Real application drill requires Linux root')
class LinuxAtomicDrill(unittest.TestCase):
    def setUp(self):
        self.scratch=tempfile.TemporaryDirectory()
        self.root=Path(self.scratch.name)
        self.origin=self.root/'dist';self.origin.mkdir()
        (self.origin/'assets').mkdir()
        self.old=b'<html><script src="'+m.QUICK+b'"></script><script src="'+m.OLD_MODULE+b'"></script></html>'
        self.index=self.origin/'index.html';self.index.write_bytes(self.old)
        os.chown(self.index,999,987);self.index.chmod(0o600)
        os.setxattr(self.index,'user.redx-test',b'preserved')
        self.backup=self.root/'private-backup'

    def tearDown(self): self.scratch.cleanup()

    def test_atomic_install_and_rollback_preserve_owner_mode_xattr(self):
        before=self.index.stat()
        r=m.install(self.origin,self.backup,ARCHIVE,m.sha(self.old),True)
        self.assertTrue(r['applied']);self.assertTrue(r['backupVerified'])
        self.assertEqual(self.index.read_bytes(),m.payload(ARCHIVE)['index.html'])
        self.assertNotEqual(self.index.stat().st_ino,before.st_ino)
        self.assertEqual((self.index.stat().st_uid,self.index.stat().st_gid,stat.S_IMODE(self.index.stat().st_mode)),(999,987,0o600))
        self.assertEqual(os.getxattr(self.index,'user.redx-test'),b'preserved')
        r=m.rollback(self.origin,self.backup,m.FILES['index.html'][1])
        self.assertTrue(r['rolledBack']);self.assertEqual(self.index.read_bytes(),self.old)
        self.assertEqual((self.index.stat().st_uid,self.index.stat().st_gid,stat.S_IMODE(self.index.stat().st_mode)),(999,987,0o600))
        self.assertEqual(os.getxattr(self.index,'user.redx-test'),b'preserved')

    def test_wrong_origin_and_public_collision_do_not_change_index(self):
        with self.assertRaises(ValueError): m.install(self.origin,self.backup,ARCHIVE,'0'*64,True)
        self.assertEqual(self.index.read_bytes(),self.old);self.assertFalse(self.backup.exists())
        p=self.origin/'assets/index-amJkAgmM.js';p.write_bytes(b'collision')
        with self.assertRaises(ValueError):m.install(self.origin,self.backup,ARCHIVE,m.sha(self.old),True)
        self.assertEqual(self.index.read_bytes(),self.old);self.assertFalse(self.backup.exists())

    def test_private_backup_collision_refused(self):
        self.backup.mkdir(mode=0o700)
        with self.assertRaises(ValueError):m.install(self.origin,self.backup,ARCHIVE,m.sha(self.old),True)
        self.assertEqual(self.index.read_bytes(),self.old)

    def test_rollback_refuses_concurrent_index_change(self):
        m.install(self.origin,self.backup,ARCHIVE,m.sha(self.old),True)
        self.index.write_bytes(self.index.read_bytes()+b'changed')
        with self.assertRaises(ValueError):m.rollback(self.origin,self.backup,m.FILES['index.html'][1])
        self.assertTrue(self.index.read_bytes().endswith(b'changed'))


if __name__=='__main__':unittest.main(verbosity=2)
