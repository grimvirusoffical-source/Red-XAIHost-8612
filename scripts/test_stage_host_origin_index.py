import importlib.util
from pathlib import Path
import os
import shutil
import stat
import sys
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("host_index_patch", Path(__file__).with_name("stage-host-origin-index.py"))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


def fixture(src="/host/assets/index-kxtL07zC.js"):
    return ("<!doctype html><html><head><title>Red-XAI Host</title>"
            '<script type="module" crossorigin src="' + src + '"></script>'
            '<script defer="" src="/analytics-consent.js" data-hostname="localhost" '
            'data-url="https://r.lilstts.com/events" data-debug="localhost"></script>'
            '</head><body><div id="root">Account workspace 🧪</div>'
            '<script src="/redxai-connect/ux.js" defer></script>'
            '<script src="/unrelated-application.js"></script></body></html>').encode()


class TransformTests(unittest.TestCase):
    def test_exact_asset_and_loader_change_preserves_remaining_bytes(self):
        for src in ("/host/assets/index-kxtL07zC.js", "/assets/index-kxtL07zC.js"):
            original = fixture(src)
            result, report = MODULE.transform(original, MODULE.digest(original), src)
            loader = ('<script defer="" src="/analytics-consent.js" data-hostname="localhost" '
                      'data-url="https://r.lilstts.com/events" data-debug="localhost"></script>').encode()
            expected = original.replace(src.encode(), src.replace(MODULE.OLD_NAME, MODULE.NEW_NAME).encode()).replace(loader, b"")
            self.assertEqual(result, expected)
            self.assertEqual(report["assetReferencesChanged"], 1)
            self.assertEqual(report["analyticsLoadersRemoved"], 1)
            self.assertTrue(report["quickLoginScriptPreserved"])

    def test_missing_duplicate_or_inline_script_sets_are_refused(self):
        original = fixture()
        loader = b'<script src="/analytics-consent.js" data-url="https://r.lilstts.com/events"></script>'
        candidates = [original + loader, original + b'<script type="module" src="/host/assets/index-kxtL07zC.js"></script>',
                      original.replace(b'/analytics-consent.js', b'/different.js'),
                      original.replace(b'https://r.lilstts.com/events', b'https://unrelated.invalid/events'),
                      original.replace(b'/redxai-connect/ux.js', b'/different-quick.js'),
                      original.replace(b'data-debug="localhost"></script>', b'data-debug="localhost">alert(1)</script>'),
                      original.replace(b'crossorigin src=', b'type="module" crossorigin src=')]
        for value in candidates:
            with self.assertRaises(ValueError):
                MODULE.transform(value, MODULE.digest(value), "/host/assets/index-kxtL07zC.js")

    def test_changed_hash_and_unreviewed_asset_path_are_refused(self):
        original = fixture()
        for checksum, src in [("0" * 64, "/host/assets/index-kxtL07zC.js"),
                              (MODULE.digest(original), "https://untrusted.invalid/index-kxtL07zC.js"),
                              (MODULE.digest(original).upper(), "/host/assets/index-kxtL07zC.js")]:
            with self.assertRaises(ValueError):
                MODULE.transform(original, checksum, src)


class FileTests(unittest.TestCase):
    def setUp(self):
        self.temp_root = Path(tempfile.gettempdir()).resolve()
        self.directory = Path(tempfile.mkdtemp(prefix="redx-host-index-", dir=self.temp_root)).resolve()
        self.public = self.directory / "public"
        self.private = self.directory / "private"
        self.public.mkdir()
        self.private.mkdir()
        self.index = self.public / "index.html"
        self.assets = self.public / "assets"
        self.assets.mkdir()
        self.asset = self.assets / MODULE.NEW_NAME
        self.asset_bytes = b"/* synthetic reviewed public artifact */"
        self.asset.write_bytes(self.asset_bytes)
        os.chmod(self.asset, 0o644)
        self.asset_hash_patch = patch.object(MODULE, "EXPECTED_ASSET_SHA256", MODULE.digest(self.asset_bytes))
        self.asset_hash_patch.start()
        self.backup = self.private / "index.backup"
        self.staged = self.public / ".index-host-vendor-stage.html"
        self.original = fixture()
        self.index.write_bytes(self.original)
        self.sha = MODULE.digest(self.original)

    def tearDown(self):
        self.asset_hash_patch.stop()
        # Verify the resolved target before any recursive Windows cleanup.
        target = self.directory.resolve()
        if target.parent != self.temp_root or not target.name.startswith("redx-host-index-"):
            raise RuntimeError("Refused cleanup outside guarded temporary directory")
        shutil.rmtree(target)

    def test_staging_creates_verified_exclusive_backup_without_switching_origin(self):
        result = MODULE.stage(self.index, self.sha, "/host/assets/index-kxtL07zC.js", self.backup, self.staged)
        self.assertEqual(self.index.read_bytes(), self.original)
        self.assertEqual(self.backup.read_bytes(), self.original)
        expected, _ = MODULE.transform(self.original, self.sha, "/host/assets/index-kxtL07zC.js")
        self.assertEqual(self.staged.read_bytes(), expected)
        self.assertTrue(result["backupVerified"])
        self.assertFalse(result["applied"])
        with self.assertRaises(ValueError):
            MODULE.stage(self.index, self.sha, "/host/assets/index-kxtL07zC.js", self.backup, self.staged)

    def test_bad_hash_and_public_backup_are_refused_before_writes(self):
        for checksum, destination in [("0" * 64, self.backup), (self.sha, self.public / "index.backup")]:
            with self.assertRaises(ValueError):
                MODULE.stage(self.index, checksum, "/host/assets/index-kxtL07zC.js", destination, self.staged)
            self.assertFalse(self.staged.exists())
            self.assertFalse(self.backup.exists())
            self.assertEqual(self.index.read_bytes(), self.original)

    def test_unreviewed_new_asset_is_refused_before_writes(self):
        self.asset.write_bytes(b"changed artifact")
        with self.assertRaises(ValueError):
            MODULE.stage(self.index, self.sha, "/host/assets/index-kxtL07zC.js", self.backup, self.staged)
        self.assertFalse(self.backup.exists())
        self.assertFalse(self.staged.exists())
        self.assertEqual(self.index.read_bytes(), self.original)

    def test_concurrent_origin_change_is_detected_and_not_overwritten(self):
        original_reader = MODULE.read_regular
        reads = 0
        def changed_reader(path):
            nonlocal reads
            if Path(path) == self.index:
                reads += 1
                if reads == 2:
                    self.index.write_bytes(b"changed by a different writer")
            return original_reader(path)
        with patch.object(MODULE, "read_regular", changed_reader), self.assertRaises(ValueError):
            MODULE.stage(self.index, self.sha, "/host/assets/index-kxtL07zC.js", self.backup, self.staged)
        self.assertEqual(self.index.read_bytes(), b"changed by a different writer")
        self.assertEqual(self.backup.read_bytes(), self.original)

    @unittest.skipUnless(sys.platform == "linux", "Linux-only atomic application")
    def test_linux_atomic_application_preserves_mode_and_backup(self):
        os.chmod(self.index, 0o644)
        result = MODULE.stage(self.index, self.sha, "/host/assets/index-kxtL07zC.js", self.backup, self.staged, apply=True)
        self.assertTrue(result["applied"])
        self.assertEqual(MODULE.digest(self.index.read_bytes()), result["outputSha256"])
        self.assertEqual(stat.S_IMODE(self.index.stat().st_mode), 0o644)
        self.assertEqual(stat.S_IMODE(self.backup.stat().st_mode), 0o600)
        self.assertFalse(self.staged.exists())
        self.assertEqual(self.backup.read_bytes(), self.original)


if __name__ == "__main__":
    unittest.main()
