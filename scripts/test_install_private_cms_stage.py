import datetime as dt
from contextlib import ExitStack
import importlib.util
import json
import io
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import types
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("private_stage", HERE / "install-private-cms-stage.py")
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class StageSafety(unittest.TestCase):
    def test_runtime_probe_loads_actual_pinned_package_by_supported_entry(self):
        site = Path(os.environ["REDX_STAGE_TEST_SITE"]).resolve()
        package = json.loads((site / "node_modules/sharp/package.json").read_bytes())
        self.assertEqual(package["version"], "0.35.5")
        self.assertTrue((site / "node_modules/sharp" / package["main"]).is_file())
        self.assertFalse((site / "node_modules/sharp/lib/index.js").exists())
        source = m.runtime_probe_source(site)
        self.assertIn("createRequire(entry)('sharp')", source)
        node = shutil.which("node")
        self.assertIsNotNone(node)
        env = {key: value for key, value in os.environ.items()
               if key.upper() in {"PATH", "SYSTEMROOT", "WINDIR", "TEMP", "TMP"}}
        run = subprocess.run([node, "--input-type=module", "-e", source],
                             capture_output=True, env=env, timeout=30, check=False)
        self.assertEqual(run.returncode, 0, "Real pinned package failed its declared-entry runtime probe")
        self.assertEqual(run.stdout, b"")

    def test_runtime_probe_failure_occurs_before_env_unit_or_service_mutation(self):
        # Drive actual orchestration to a failing service-user probe. Linux and
        # systemd inspection is mocked; backups stay inside this temp directory.
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            state = root / "state"
            state.mkdir()
            backups = root / "backups"
            backups.mkdir()
            site = root / "release/site"
            site.mkdir(parents=True)
            config = root / "config"
            config.mkdir()
            real_path = Path
            proc = types.SimpleNamespace(read_text=lambda: "systemd\n")
            def path_factory(value):
                if value == "/proc/1/comm":
                    return proc
                if value == "/srv/red-xai/backups":
                    return backups
                return real_path(value)
            def props(unit, names):
                if unit == m.OLD_UNIT:
                    return {"ActiveState":"active", "MainPID":"555", "WorkingDirectory":str(m.OLD_SITE),
                            "DynamicUser":"yes", "FragmentPath":"/run/systemd/transient/fixture.service"}
                return {"LoadState":"not-found"}
            def commands(args, **kwargs):
                if args[:2] == ["/usr/bin/node", "--version"]:
                    return "v20.20.2\n"
                if args[:2] == ["/usr/bin/node", "--help"]:
                    return "--env-file\n"
                if args[:2] == ["/usr/bin/systemctl", "cat"]:
                    return "[Service]\nWorkingDirectory=" + str(m.OLD_SITE) + "\n"
                if args[0] == "/usr/sbin/runuser":
                    raise m.Refused("command_failed:runuser")
                return ""
            args = types.SimpleNamespace(source_archive="fixture.tgz", durability_observation="observation.json",
                                         apply=True, single_writer_reviewed=True, owner_stdin=True, owner_hex=False)
            fake_pwd = types.SimpleNamespace(getpwnam=lambda _: types.SimpleNamespace(
                pw_uid=986, pw_gid=976, pw_shell="/usr/sbin/nologin"))
            patches = [patch.object(m.sys, "platform", "linux"),
                       patch.object(m.os, "geteuid", return_value=0, create=True),
                       patch.dict(sys.modules, {"pwd":fake_pwd}),
                       patch.object(m, "Path", side_effect=path_factory),
                       patch.object(m, "inspect_path", return_value=types.SimpleNamespace(st_size=51, st_nlink=1)),
                       patch.object(m, "verify_archive", return_value=300),
                       patch.object(m, "check_durability", return_value="source-derived-group-default"),
                       patch.object(m, "customer_snapshot", return_value={}),
                       patch.object(m, "properties", side_effect=props),
                       patch.object(m, "check_listener"),
                       patch.object(m, "metadata", return_value={"ok":True,"cmsConfigured":False,"cmsStorage":"unconfigured"}),
                       patch.object(m, "STATE", state), patch.object(m, "SITE", site),
                       patch.object(m, "ENV_FILE", config / "site.env"),
                       patch.object(m, "UNIT_PATH", config / "new.service"),
                       patch.object(m, "OLD_FALLBACK", config / "old-fallback.service"),
                       patch.object(m.sys, "stdin", io.StringIO("fixture-owner\n")),
                       patch.object(m, "backup_support"),
                       patch.object(m, "release_permissions", return_value=0)]
            with ExitStack() as stack:
                for item in patches:
                    stack.enter_context(item)
                writes = stack.enter_context(patch.object(m, "atomic_private"))
                calls = stack.enter_context(patch.object(m, "command", side_effect=commands))
                with self.assertRaisesRegex(m.Refused, "command_failed:runuser"):
                    m.install(args)
                self.assertFalse(any(call.args[0][0:2] == ["/usr/bin/systemctl", "stop"] for call in calls.call_args_list))
                targets = [call.args[0] for call in writes.call_args_list]
                self.assertNotIn(config / "site.env", targets)
                self.assertNotIn(config / "new.service", targets)
                self.assertFalse((config / "site.env").exists())
                self.assertFalse((config / "new.service").exists())

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
