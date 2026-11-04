"""Synthetic tests only: no real unit, account, state, listener or credentials."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("quicklogin_rollout", HERE / "rollout.py")
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)
RESTORE_PROBE = r.restore_probe
LINUX_ROOT = sys.platform.startswith("linux") and hasattr(os, "geteuid") and os.geteuid() == 0


class SourceContracts(unittest.TestCase):
    def test_pinned_immutable_browser_manifest(self):
        # Git may expand LF to CRLF in a Windows working tree. Deployment uses
        # the immutable GitHub artifact bytes; never normalize in rollout.py.
        raw = (HERE.parent / "quicklogin-ui-5c54aa9/manifest.json").read_bytes().replace(b"\r\n", b"\n")
        self.assertEqual(r.sha(raw), r.MANIFEST_SHA)
        meta = r.strict_json(raw)
        self.assertEqual(set(meta["originalRuntimeFiles"]), r.RUNTIME_NAMES)
        self.assertEqual(meta["originalRuntimeFiles"]["server.cjs"]["sha256"], r.BACKEND_SHA)
        for name, expected in meta["files"].items():
            data = (HERE.parent / "quicklogin-ui-5c54aa9" / name).read_bytes().replace(b"\r\n", b"\n")
            self.assertEqual((len(data), r.sha(data)), (expected["bytes"], expected["sha256"]))

    def test_schema_refuses_empty_unknown_and_duplicate_keys(self):
        for raw in (b'{}', b'{"schema":1,"apps":[],"grants":[]}',
                    b'{"schema":2,"apps":{},"grants":[]}',
                    b'{"schema":2,"schema":2,"apps":[],"grants":[]}'):
            with self.subTest(raw=raw), self.assertRaises(r.Refused):
                r.state_json(raw)
        self.assertEqual(r.state_json(b'{"schema":2,"grants":[],"apps":[]}')["schema"], 2)

    def test_state_exception_is_one_exact_directory(self):
        private = types.SimpleNamespace(st_mode=stat.S_IFDIR | 0o700, st_uid=981, st_gid=981)
        r.dir_policy(r.STATE.parent, private)
        for path in (r.STATE.parent.parent, r.STATE.parent / "nested", r.BASE):
            with self.subTest(path=path), self.assertRaises(r.Refused):
                r.dir_policy(path, private)

    def test_no_mutating_service_command(self):
        source = (HERE / "rollout.py").read_text()
        self.assertEqual(source.count('["/usr/bin/systemctl",'), 1)
        self.assertIn('["/usr/bin/systemctl", "show",', source)
        self.assertNotIn("shutil", source)
        self.assertNotIn("shell=True", source)

    def test_cli_errors_never_echo_external_exception(self):
        output = io.StringIO()
        with patch.object(sys, "argv", ["rollout.py"]), patch.object(sys, "platform", "linux"), \
             patch.object(os, "geteuid", return_value=0, create=True), \
             patch.object(r, "inspect", side_effect=RuntimeError("SYNTHETIC_PRIVATE_VALUE")), \
             contextlib.redirect_stdout(output):
            self.assertEqual(r.main(), 2)
        self.assertNotIn("SYNTHETIC_PRIVATE_VALUE", output.getvalue())
        self.assertEqual(json.loads(output.getvalue())["code"], "guarded_operation_failed")

    def test_maintenance_ack_is_required_before_mutation(self):
        with patch.object(sys, "argv", ["rollout.py", "--promote-from", "/untrusted"]), \
             patch.object(sys, "platform", "linux"), patch.object(os, "geteuid", return_value=0, create=True), \
             patch.object(r, "promote") as promote, contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(r.main(), 2)
            promote.assert_not_called()

    def test_unknown_environment_files_and_startup_refused_before_procfs(self):
        props = {"LoadState": "loaded", "User": "redxai", "Group": "redxai", "WorkingDirectory": str(r.CURRENT),
                 "FragmentPath": str(r.FRAGMENT), "UnitFileState": "enabled", "EnvironmentFiles": "unexpected"}
        with patch.object(r, "properties", return_value=props):
            for absent in (False, True):
                if absent:
                    del props["EnvironmentFiles"]
                with self.subTest(absent=absent), self.assertRaisesRegex(r.Refused, "environment_files_require_review"):
                    r.selected_configuration(None)
            props["EnvironmentFiles"] = ""
            props["ExecStart"] = "unexpected"
            with self.assertRaisesRegex(r.Refused, "startup_shape_requires_review"):
                r.selected_configuration(None)

    def test_properties_requests_all_selected_fields_and_retains_explicit_empty_values(self):
        for raw in (b"EnvironmentFiles=\nEnvironment=\nDropInPaths=\n", b"Environment=\nDropInPaths=\n"):
            with self.subTest(display=raw), patch.object(r, "command", side_effect=[raw, b"a(sb) 0\n"]) as command:
                self.assertEqual(r.properties(), {"EnvironmentFiles": "", "Environment": "", "DropInPaths": ""})
            self.assertEqual(command.call_count, 2)
            self.assertEqual(command.call_args_list[1].args[0], ["/usr/bin/busctl", "get-property",
                "org.freedesktop.systemd1", "/org/freedesktop/systemd1/unit/redxai_2dsite_2dbridge_2eservice",
                "org.freedesktop.systemd1.Service", "EnvironmentFiles"])
        args = command.call_args_list[0].args[0]
        self.assertEqual(args[:4], ["/usr/bin/systemctl", "show", r.UNIT, "--all"])
        self.assertEqual(len(args), 5)
        self.assertEqual(args[4], "--property=LoadState,ActiveState,MainPID,User,Group,WorkingDirectory,FragmentPath,UnitFileState,Environment,EnvironmentFiles,ExecStart,DropInPaths,NRestarts,ExecMainStartTimestampMonotonic")

    def test_omitted_display_needs_exact_typed_empty_not_missing_wrong_type_or_nonempty(self):
        for typed in (b"", b"as 0\n", b"a(sb) 1 \"/synthetic\" false\n", b"a(sb) 0\nextra\n"):
            with self.subTest(typed=typed), patch.object(r, "command", side_effect=[b"Environment=\n", typed]):
                with self.assertRaisesRegex(r.Refused, "environment_files_typed_empty_required"):
                    r.properties()

    def test_typed_query_failure_and_contradictory_display_refuse(self):
        with patch.object(r, "command", side_effect=[b"Environment=\n", r.Refused("command_failed")]):
            with self.assertRaisesRegex(r.Refused, "command_failed"):
                r.properties()
        for raw in (b"EnvironmentFiles=/synthetic\n", b"EnvironmentFile=/synthetic (ignore_errors=no)\n"):
            with self.subTest(display=raw), patch.object(r, "command", side_effect=[raw, b"a(sb) 0\n"]):
                with self.assertRaisesRegex(r.Refused, "environment_files_display_requires_review"):
                    r.properties()


@unittest.skipUnless(LINUX_ROOT, "Requires Linux root for real UID, dirfd, fsync and symlink tests")
class LinuxTransactions(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="redx-quicklogin-guard-test-", dir="/srv")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        os.chmod(self.root, 0o700)
        values = {"BASE": self.root / "application", "BACKUPS": self.root / "backups",
                  "TOOLS": self.root / "tools", "STATE": self.root / "state/app-grants.json",
                  "LOCK": self.root / "deployment.lock", "NODE": self.root / "node",
                  "FRAGMENT": self.root / "unit.service"}
        values.update(CURRENT=values["BASE"] / "current", ORIGINAL=values["BASE"] / "releases/old",
                      CANDIDATE=values["BASE"] / "releases/new")
        for name, value in values.items():
            manager = patch.object(r, name, value)
            manager.start()
            self.addCleanup(manager.stop)
        for path in (r.BASE, r.BASE / "releases", r.ORIGINAL, r.ORIGINAL / "web", r.BACKUPS, r.TOOLS, r.STATE.parent):
            path.mkdir(mode=0o755)
            os.chmod(path, 0o755)
        os.chown(r.STATE.parent, 981, 981)
        os.chmod(r.STATE.parent, 0o700)
        self.write_state({"schema": 2, "grants": [], "apps": []})
        self.original = {name: ("synthetic original " + name + "\n").encode() for name in r.RUNTIME_NAMES}
        for name, data in self.original.items():
            self.write(r.ORIGINAL / name, data)
        self.write(r.NODE, b"synthetic interpreter fixture\n", 0o755)
        self.write(r.FRAGMENT, b"synthetic nonsecret unit\n")
        r.CURRENT.symlink_to(r.ORIGINAL)
        self.artifact = r.TOOLS / "artifact"
        self.artifact.mkdir(mode=0o755)
        (self.artifact / "web").mkdir(mode=0o755)
        (self.artifact / "test").mkdir(mode=0o755)
        assets = {"web/app.js": b"synthetic new script\n", "web/index.html": b"synthetic new page\n",
                  "test/approval-ui.test.cjs": b"synthetic tests\n", "README.md": b"synthetic documentation\n"}
        self.meta = {"sourceRevision": r.SOURCE_REVISION,
                     "originalRuntimeFiles": {name: {"bytes": len(data), "sha256": r.sha(data)} for name, data in self.original.items()},
                     "files": {name: {"bytes": len(data), "sha256": r.sha(data)} for name, data in assets.items()}}
        raw = r.encoded(self.meta)
        self.write(self.artifact / "manifest.json", raw)
        for name, data in assets.items():
            self.write(self.artifact / name, data)
        for name, value in (("MANIFEST_SHA", r.sha(raw)), ("BACKEND_SHA", r.sha(self.original["server.cjs"]))):
            manager = patch.object(r, name, value)
            manager.start()
            self.addCleanup(manager.stop)
        self.config = {"files": {str(r.FRAGMENT): r.sha(r.FRAGMENT.read_bytes())}, "syntheticIdentity": "same"}
        self.baseline = {"cwd": str(r.ORIGINAL), "node": r.identity(r.NODE.stat()), "MainPID": "123"}
        for name, impl in (("selected_configuration", self.configuration), ("loopback", lambda *a, **k: None),
                           ("restore_probe", self.probe)):
            manager = patch.object(r, name, side_effect=impl)
            manager.start()
            self.addCleanup(manager.stop)
        self.probed = []
        self.tree = r.Tree()
        self.addCleanup(self.tree.close)

    def write(self, path, data, mode=0o644):
        path.write_bytes(data)
        os.chmod(path, mode)

    def write_state(self, value):
        self.write(r.STATE, r.encoded(value), 0o600)
        os.chown(r.STATE, 981, 981)

    def configuration(self, *args, **kwargs):
        return self.config, self.baseline, {str(r.FRAGMENT): r.FRAGMENT.read_bytes()}

    def probe(self, tree, backend, copied):
        self.assertNotEqual(copied, r.STATE)
        self.assertEqual(r.sha(tree.read(backend, private=True)[0]), r.BACKEND_SHA)
        r.state_json(tree.read(copied, private=True)[0])
        self.probed.append(copied)

    def stage(self):
        return Path(r.stage(self.tree, self.artifact)["stagedDirectory"])

    def assert_state(self, data):
        self.assertEqual(r.STATE.read_bytes(), data)
        self.assertEqual((r.STATE.stat().st_uid, r.STATE.stat().st_gid, stat.S_IMODE(r.STATE.stat().st_mode)), (981, 981, 0o600))

    def test_inspect_is_read_only_and_never_constructs(self):
        before = r.STATE.read_bytes()
        r.inspect(self.tree, self.artifact)
        self.assertEqual(list(r.BACKUPS.iterdir()), [])
        self.assertFalse(r.CANDIDATE.exists())
        self.assertEqual(self.probed, [])
        self.assert_state(before)

    def test_stage_private_backups_and_only_two_new_assets(self):
        before = r.STATE.read_bytes()
        original_link = r.CURRENT.lstat()
        directory = self.stage()
        for path in (directory, directory / "original", directory / "original/web", directory / "unit"):
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o700)
        for path in directory.rglob("*"):
            if path.is_file():
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        for name, data in self.original.items():
            self.assertEqual((r.ORIGINAL / name).read_bytes(), data)
            self.assertEqual((directory / "original" / name).read_bytes(), data)
            self.assertEqual(stat.S_IMODE((r.CANDIDATE / name).stat().st_mode), 0o644)
            expected = (self.artifact / name).read_bytes() if name in ("web/app.js", "web/index.html") else data
            self.assertEqual((r.CANDIDATE / name).read_bytes(), expected)
        self.assertEqual(r.CURRENT.lstat().st_ino, original_link.st_ino)
        self.assert_state(before)
        self.assertEqual(len(self.probed), 1)

    def test_promote_and_rollback_never_restore_older_state(self):
        directory = self.stage()
        self.write_state({"schema": 2, "grants": [], "apps": [{"synthetic": "new"}]})
        advanced = r.STATE.read_bytes()
        result = r.promote(self.tree, directory)
        self.assertTrue(result["serviceRestartRequired"])
        self.assertTrue(result["stateAdvancedSinceStage"])
        self.assertEqual(r.CURRENT.readlink(), r.CANDIDATE)
        self.assert_state(advanced)
        result = r.rollback(self.tree, directory)
        self.assertFalse(result["liveStateRestored"])
        self.assertEqual(r.CURRENT.readlink(), r.ORIGINAL)
        self.assert_state(advanced)
        r.rollback(self.tree, directory)  # Only our recorded restored link is idempotent.
        self.assertFalse(r.LOCK.exists())

    def test_missing_state_is_never_silently_created(self):
        r.STATE.unlink()
        with self.assertRaises(FileNotFoundError):
            r.stage(self.tree, self.artifact)
        self.assertFalse(r.STATE.exists())
        self.assertFalse(r.CANDIDATE.exists())

    def test_private_state_permissions_and_owner_are_enforced(self):
        for mode, uid in ((0o644, 981), (0o600, 0)):
            os.chmod(r.STATE, mode)
            os.chown(r.STATE, uid, 981)
            with self.subTest(mode=mode, uid=uid), self.assertRaises(r.Refused):
                self.tree.read(r.STATE, state=True)

    def test_state_symlink_is_refused(self):
        real = r.STATE.with_name("saved.json")
        r.STATE.rename(real)
        r.STATE.symlink_to(real)
        with self.assertRaises(r.Refused):
            self.tree.read(r.STATE, state=True)

    def test_pinned_ancestor_swap_cannot_redirect_read_or_write(self):
        self.tree.directory(r.BACKUPS)
        moved = r.BACKUPS.with_name("backups-moved")
        r.BACKUPS.rename(moved)
        r.BACKUPS.mkdir(mode=0o755)
        with self.assertRaisesRegex(r.Refused, "directory_identity_changed"):
            self.tree.exclusive(r.BACKUPS / "redirected", b"synthetic")
        self.assertFalse((r.BACKUPS / "redirected").exists())
        self.assertFalse((moved / "redirected").exists())

    def test_service_owned_directory_elsewhere_is_refused(self):
        other = self.root / "other"
        other.mkdir(mode=0o700)
        os.chown(other, 981, 981)
        with self.assertRaisesRegex(r.Refused, "root_directory_metadata"):
            self.tree.directory(other)

    def test_candidate_tamper_refused_before_link_change(self):
        directory = self.stage()
        self.write(r.CANDIDATE / "web/app.js", b"tampered fixture\n")
        with self.assertRaisesRegex(r.Refused, "runtime_file_mismatch"):
            r.promote(self.tree, directory)
        self.assertEqual(r.CURRENT.readlink(), r.ORIGINAL)

    def test_manifest_tamper_refused_before_private_backup(self):
        self.write(self.artifact / "manifest.json", b'{"synthetic":"tampered"}')
        with self.assertRaisesRegex(r.Refused, "artifact_manifest_mismatch"):
            self.stage()
        self.assertEqual(list(r.BACKUPS.iterdir()), [])

    def test_preexisting_candidate_never_overwritten(self):
        r.CANDIDATE.mkdir(mode=0o755)
        marker = r.CANDIDATE / "review-me"
        self.write(marker, b"preserve fixture")
        with self.assertRaisesRegex(r.Refused, "candidate_already_exists"):
            self.stage()
        self.assertEqual(marker.read_bytes(), b"preserve fixture")

    def test_changed_process_refused_since_stage(self):
        directory = self.stage()
        self.baseline["MainPID"] = "456"
        with self.assertRaisesRegex(r.Refused, "service_changed_since_stage"):
            r.promote(self.tree, directory)
        self.assertEqual(r.CURRENT.readlink(), r.ORIGINAL)

    def test_stale_lock_is_preserved(self):
        self.write(r.LOCK, b"synthetic stale lock", 0o600)
        with self.assertRaises(FileExistsError):
            self.stage()
        self.assertEqual(r.LOCK.read_bytes(), b"synthetic stale lock")

    def replace_external(self, target):
        r.CURRENT.rename(self.root / ("held-link-" + str(len(list(self.root.glob("held-link-*"))))))
        r.CURRENT.symlink_to(target)
        return r.identity(r.CURRENT.lstat())

    def test_same_target_new_link_inode_refused_since_stage(self):
        directory = self.stage()
        external = self.replace_external(r.ORIGINAL)
        with self.assertRaisesRegex(r.Refused, "current_link_changed_since_stage"):
            r.promote(self.tree, directory)
        self.assertEqual(r.identity(r.CURRENT.lstat()), external)

    def test_intervening_candidate_before_our_rename_is_preserved(self):
        directory = self.stage()
        actual = self.tree.replace_link
        external = []
        def intervene(expected, target, transaction, arm=None):
            external.append(self.replace_external(r.CANDIDATE))
            return actual(expected, target, transaction, arm=arm)
        with patch.object(self.tree, "replace_link", side_effect=intervene):
            with self.assertRaisesRegex(r.Refused, "manual_review_lock_retained"):
                r.promote(self.tree, directory)
        self.assertEqual(r.identity(r.CURRENT.lstat()), external[0])
        self.assertEqual(r.CURRENT.readlink(), r.CANDIDATE)
        self.assertTrue(r.LOCK.exists())

    def test_intervening_original_before_our_rename_is_preserved(self):
        directory = self.stage()
        actual = self.tree.replace_link
        external = []
        def intervene(expected, target, transaction, arm=None):
            external.append(self.replace_external(r.ORIGINAL))
            return actual(expected, target, transaction, arm=arm)
        with patch.object(self.tree, "replace_link", side_effect=intervene):
            with self.assertRaisesRegex(r.Refused, "manual_review_lock_retained"):
                r.promote(self.tree, directory)
        self.assertEqual(r.identity(r.CURRENT.lstat()), external[0])
        self.assertTrue(r.LOCK.exists())

    def test_post_rename_fsync_fault_restores_only_our_link(self):
        directory = self.stage()
        before = r.STATE.read_bytes()
        fsync, replace = os.fsync, os.replace
        renamed = [False]
        def rename(*args, **kwargs):
            result = replace(*args, **kwargs)
            renamed[0] = True
            return result
        def sync(fd):
            if renamed[0]:
                renamed[0] = False
                raise OSError("synthetic fsync fault")
            return fsync(fd)
        # Only the first rename faults: recovery's fsync is permitted.
        with patch.object(os, "replace", side_effect=rename) as replacing, patch.object(os, "fsync", side_effect=sync):
            def once(*args, **kwargs):
                result = replace(*args, **kwargs)
                if replacing.call_count == 1:
                    renamed[0] = True
                return result
            replacing.side_effect = once
            with self.assertRaisesRegex(r.Refused, "original_link_restored"):
                r.promote(self.tree, directory)
        self.assertEqual(r.CURRENT.readlink(), r.ORIGINAL)
        self.assert_state(before)
        self.assertFalse(r.LOCK.exists())

    def test_promotion_proof_write_failure_rolls_code_back(self):
        directory = self.stage()
        actual = self.tree.exclusive
        def fail_proof(path, *args, **kwargs):
            if path.name == "promotion.json":
                raise OSError("synthetic receipt fault")
            return actual(path, *args, **kwargs)
        with patch.object(self.tree, "exclusive", side_effect=fail_proof):
            with self.assertRaisesRegex(r.Refused, "original_link_restored"):
                r.promote(self.tree, directory)
        self.assertEqual(r.CURRENT.readlink(), r.ORIGINAL)

    def test_durable_arm_receipt_precedes_link_mutation(self):
        directory = self.stage()
        actual = self.tree.exclusive
        observed = []
        def check_arm(path, data, *args, **kwargs):
            if path.name == "promotion.arm.json":
                value = r.strict_json(data)
                self.assertFalse(value["armed"])
                self.assertEqual(r.CURRENT.readlink(), r.ORIGINAL)
                temps = list(r.CURRENT.parent.glob(".current.quicklogin-*"))
                self.assertEqual(len(temps), 1)
                self.assertEqual(value["created"][:2], r.identity(temps[0].lstat())[:2])
                observed.append(value)
            return actual(path, data, *args, **kwargs)
        with patch.object(self.tree, "exclusive", side_effect=check_arm):
            r.promote(self.tree, directory)
        self.assertEqual(len(observed), 1)
        proof = r.strict_json((directory / "promotion.json").read_bytes())
        self.assertEqual(observed[0]["created"][:2], proof["link"]["metadata"][:2])
        r.rollback(self.tree, directory)
        arm = r.strict_json((directory / "rollback.arm.json").read_bytes())
        proof = r.strict_json((directory / "rollback.json").read_bytes())
        self.assertEqual(arm["created"][:2], proof["link"]["metadata"][:2])

    def test_arm_receipt_failure_never_mutates_current(self):
        directory = self.stage()
        original = r.identity(r.CURRENT.lstat())
        actual = self.tree.exclusive
        def fail_arm(path, *args, **kwargs):
            if path.name == "promotion.arm.json":
                raise OSError("synthetic arm receipt failure")
            return actual(path, *args, **kwargs)
        with patch.object(self.tree, "exclusive", side_effect=fail_arm):
            with self.assertRaisesRegex(r.Refused, "original_link_restored"):
                r.promote(self.tree, directory)
        self.assertEqual(r.identity(r.CURRENT.lstat()), original)

    def test_unrecognized_post_rename_link_is_preserved_and_locked(self):
        directory = self.stage()
        actual = self.tree.replace_link
        external = []
        def intervene(expected, target, transaction, arm=None):
            result = actual(expected, target, transaction, arm=arm)
            external.append(self.replace_external(self.root / "unknown-release"))
            raise OSError("synthetic post-rename fault")
        with patch.object(self.tree, "replace_link", side_effect=intervene):
            with self.assertRaisesRegex(r.Refused, "manual_review_lock_retained"):
                r.promote(self.tree, directory)
        self.assertEqual(r.identity(r.CURRENT.lstat()), external[0])
        self.assertTrue(r.LOCK.exists())

    def test_rollback_does_not_clobber_external_candidate_inode(self):
        directory = self.stage()
        r.promote(self.tree, directory)
        external = self.replace_external(r.CANDIDATE)
        with self.assertRaisesRegex(r.Refused, "manual_review_lock_retained"):
            r.rollback(self.tree, directory)
        self.assertEqual(r.identity(r.CURRENT.lstat()), external)
        self.assertTrue(r.LOCK.exists())

    def test_rollback_proof_write_failure_retains_review_lock(self):
        directory = self.stage()
        r.promote(self.tree, directory)
        actual = self.tree.exclusive
        def fail_proof(path, *args, **kwargs):
            if path.name == "rollback.json":
                raise OSError("synthetic proof failure")
            return actual(path, *args, **kwargs)
        with patch.object(self.tree, "exclusive", side_effect=fail_proof):
            with self.assertRaisesRegex(r.Refused, "manual_review_lock_retained"):
                r.rollback(self.tree, directory)
        self.assertEqual(r.CURRENT.readlink(), r.ORIGINAL)
        self.assertTrue(r.LOCK.exists())

    def test_file_attributes_preserved_and_tamper_refused(self):
        os.setxattr(r.ORIGINAL / "web/app.js", "user.redx-fixture", b"synthetic attribute")
        directory = self.stage()
        self.assertEqual(os.getxattr(r.CANDIDATE / "web/app.js", "user.redx-fixture"), b"synthetic attribute")
        os.setxattr(r.CANDIDATE / "web/app.js", "user.redx-fixture", b"tampered")
        with self.assertRaisesRegex(r.Refused, "runtime_attributes_changed"):
            r.promote(self.tree, directory)

    def test_probe_pins_backend_and_rejects_changed_copy(self):
        directory = self.stage()
        backend = directory / "original/server.cjs"
        copied = directory / "state.snapshot.json"
        self.write(backend, b"unexpected synthetic backend", 0o600)
        with patch.object(r.subprocess, "run") as child:
            with self.assertRaisesRegex(r.Refused, "probe_backend_mismatch"):
                RESTORE_PROBE(self.tree, backend, copied)
            child.assert_not_called()
        self.write(backend, self.original["server.cjs"], 0o600)
        def changing_copy(*args, **kwargs):
            self.write(copied, r.encoded({"schema": 2, "grants": [], "apps": [{"changed": True}]}), 0o600)
            return types.SimpleNamespace(returncode=0, stdout=b"RESTORE_COPY_OK", stderr=b"")
        with patch.object(r.subprocess, "run", side_effect=changing_copy):
            with self.assertRaisesRegex(r.Refused, "private_copy_changed_by_constructor"):
                RESTORE_PROBE(self.tree, backend, copied)


if __name__ == "__main__":
    unittest.main()
