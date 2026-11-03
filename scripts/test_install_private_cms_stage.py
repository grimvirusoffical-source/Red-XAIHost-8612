import datetime as dt
import importlib.util
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("private_stage", HERE / "install-private-cms-stage.py")
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class StageSafety(unittest.TestCase):
    def test_actual_pinned_archive_matches_extracted_source_and_tamper_refused(self):
        archive = Path(os.environ.get("REDX_STAGE_TEST_ARCHIVE", HERE / "hub-c443ce7-source.tgz"))
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            with tarfile.open(archive) as source:
                source.extractall(root, filter="data")
            release = root / m.ARCHIVE_ROOT
            self.assertGreaterEqual(m.verify_archive(archive, release), 200)
            server = release / "website/red-xai.com/server.mjs"
            server.write_bytes(server.read_bytes() + b"\n// altered\n")
            with self.assertRaisesRegex(m.Refused, "release_file_mismatch"):
                m.verify_archive(archive, release)

    def test_archive_digest_is_required_before_source_inspection(self):
        with tempfile.TemporaryDirectory() as temp:
            archive = Path(temp) / "false.tgz"
            archive.write_bytes(b"not the reviewed source")
            with self.assertRaisesRegex(m.Refused, "archive_digest_mismatch"):
                m.verify_archive(archive, Path(temp) / "absent")

    def test_owner_cannot_inject_dotenv_or_systemd_configuration(self):
        for owner in ["", "a\nNODE_OPTIONS=--inspect", "a,b", "a\x00", "a #unsafe", 'a"b', "a\\b"]:
            with self.subTest(owner=owner):
                with self.assertRaises(m.Refused):
                    m.environment_bytes(owner)
        encoded = m.environment_bytes("fixture-owner_0123-abc").decode()
        self.assertEqual(len(encoded.splitlines()), 7)
        self.assertIn("REDX_SITE_CMS_DURABILITY=group\n", encoded)
        self.assertNotIn("rx_live_", encoded)

    def test_unit_uses_clean_environment_private_paths_and_no_boot_enable(self):
        unit = m.unit_bytes().decode()
        self.assertIn("ExecStart=/usr/bin/env -i PATH=/usr/bin:/bin NODE_ENV=production /usr/bin/node --env-file=/etc/red-xai/site.env", unit)
        self.assertIn("ReadWritePaths=/var/lib/red-xai-site", unit)
        self.assertIn("WorkingDirectory=/opt/redx-site-c443ce7/website/red-xai.com", unit)
        self.assertNotIn("[Install]", unit)
        self.assertNotIn("ExecStartPre=", unit)
        self.assertNotIn("cloudflared", unit)

    def test_durability_duplicate_unknown_and_stale_inputs_refused(self):
        now = dt.datetime(2026, 11, 3, 22, 0, tzinfo=dt.timezone.utc)
        data = {"source":"owner-authenticated-native-config", "observedUtc":now.isoformat(),
                "dbRevision":m.NATIVE_REVISION, "rawEngineDurability":""}
        with tempfile.TemporaryDirectory() as temp, patch.object(m, "inspect_path"):
            path = Path(temp) / "observation.json"
            path.write_text(json.dumps(data))
            self.assertEqual(m.check_durability(path, now), "source-derived-group-default")
            for changes in [{"rawEngineDurability":None}, {"rawEngineDurability":"async"},
                            {"dbRevision":"wrong"}, {"extra":True},
                            {"observedUtc":(now-dt.timedelta(hours=1)).isoformat()}]:
                path.write_text(json.dumps({**data, **changes}))
                with self.assertRaises(m.Refused):
                    m.check_durability(path, now)
            path.write_text(json.dumps(data)[:-1] + ',"rawEngineDurability":""}')
            with self.assertRaisesRegex(m.Refused, "duplicate_durability_field"):
                m.check_durability(path, now)

    def test_private_listener_requires_single_loopback_bind_and_exact_pid(self):
        good = 'LISTEN 0 511 127.0.0.1:14210 0.0.0.0:* users:(("node",pid=555,fd=20))\n'
        with patch.object(m, "command", return_value=good):
            m.check_listener("555")
            with self.assertRaises(m.Refused):
                m.check_listener("55")
        for bad in [good.replace("127.0.0.1:14210", "0.0.0.0:14210"), good+good, ""]:
            with patch.object(m, "command", return_value=bad):
                with self.assertRaises(m.Refused):
                    m.check_listener("555")

    def test_existing_writer_lock_blocks_support_backup_without_removal(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            state = root / "support"
            state.mkdir()
            lock = state / "writer.lock"
            lock.write_text("synthetic process evidence")
            with patch.object(m, "STATE", state):
                with self.assertRaisesRegex(m.Refused, "existing_writer_lock_refused"):
                    m.backup_support(root / "backup.tgz")
            self.assertEqual(lock.read_text(), "synthetic process evidence")
            self.assertFalse((root / "backup.tgz").exists())

    def test_only_fixed_private_stage_is_ever_stopped_and_never_native_key_read(self):
        import ast
        tree = ast.parse((HERE / "install-private-cms-stage.py").read_text())
        stops = []
        for node in ast.walk(tree):
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id == "command" and node.args and isinstance(node.args[0], ast.List):
                values = node.args[0].elts
                if len(values) > 2 and isinstance(values[1], ast.Constant) and values[1].value == "stop":
                    stops.append(values[2].id)
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) and isinstance(node.func.value, ast.Name) and node.func.value.id == "KEY":
                self.assertNotIn(node.func.attr, ["open", "read_bytes", "read_text", "write_bytes", "write_text", "unlink"])
        self.assertEqual(sorted(stops), ["OLD_UNIT", "UNIT"])


if __name__ == "__main__":
    unittest.main()
