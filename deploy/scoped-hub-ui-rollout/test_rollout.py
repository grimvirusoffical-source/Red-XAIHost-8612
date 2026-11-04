import argparse
from contextlib import ExitStack
import importlib.util
import os
from pathlib import Path
import re
import stat
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("scoped_ui_rollout", Path(__file__).with_name("rollout.py"))
rollout = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rollout)

CONFIG = b'''tunnel: synthetic-offline-fixture
credentials-file: /private/fixture-not-a-real-credential.json
ingress:
  - hostname: api.red-xai.com
    service: http://127.0.0.1:7790
  - hostname: red-xai.com
    path: '^/host(/.*)?$'
    service: http://127.0.0.1:14202
  - hostname: red-xai.com
    path: '^/(v1|ui)(/.*)?$'
    service: http://127.0.0.1:17790
  - hostname: red-xai.com
    path: '^/redxai-connectx?(/.*)?$'
    service: http://127.0.0.1:17774
  - hostname: red-xai.com
    path: '^/$'
    service: http://127.0.0.1:4210
  - hostname: red-xai.com
    service: http://127.0.0.1:4200
  - service: http_status:404
'''
INDEX = ('<html><script type="module" src="' + rollout.OLD_ASSET + '"></script><script src="/redxai-connect/ux.js"></script></html>').encode()


class RouteAndTransformTests(unittest.TestCase):
    def setUp(self):
        self.meta = rollout.manifest()

    def selected(self, path):
        return any(re.fullmatch(pattern, path) for pattern in rollout.route_patterns(self.meta))

    def test_only_exact_new_ui_pages_and_assets_are_selected(self):
        for path in ("/", "/host", "/db", "/signin", "/settings", "/api-keys", "/contact", "/products/software", "/products/cli/redxai-cli", "/api/quick-login", "/app.js", "/site/api.js", "/site-assets/" + "a" * 64 + "/site/api.js", "/api/site/posts/p_" + "a" * 32, "/api/site/media/" + "b" * 32):
            self.assertTrue(self.selected(path), path)
        for path in ("/signup", "/activate", "/tos", "/api/accounts/me", "/api/accounts/login", "/assets/index-native-navigation-8144e37d5067.js", "/host/", "/host/api/agent/heartbeat", "/host/api/rpc/nodes.list", "/host/assets/file.js", "/hostile", "/db/", "/v1/console/", "/ui", "/redxai-connect", "/redxai-connectx/authorize", "/vortex/", "/vortex/cloudpc/", "/unknown", "/api/site/unknown", "/products/unknown", "/site-assets/" + "a" * 64 + "/server.mjs", "/site-assets/not-a-hash/app.js", "/app.js/more"):
            self.assertFalse(self.selected(path), path)

    def test_all_pinned_store_products_and_runtime_browser_files_are_covered(self):
        self.assertEqual(len(self.meta["productPages"]), 11)
        for page in self.meta["productPages"]:
            self.assertTrue(self.selected(page))
        self.assertEqual(len(self.meta["runtimeFiles"]), 22)
        for name in self.meta["runtimeFiles"]:
            self.assertEqual(self.selected("/" + name), name in ("app.js", "styles.css", "catalog.json", "public-secrets-docs.json", "cli-installation.md") or name.startswith("site/"))

    def test_config_insertion_preserves_every_original_byte_and_only_main_hostname(self):
        for source in (CONFIG, CONFIG.replace(b"\n", b"\r\n")):
            candidate = rollout.transform_config(source, rollout.sha(source), self.meta)
            self.assertEqual(candidate.count(b"hostname: red-xai.com") - source.count(b"hostname: red-xai.com"), 5)
            self.assertEqual(candidate.count(b"hostname: api.red-xai.com"), 1)
            prefix, suffix = source.split(b"ingress:" + (b"\r\n" if b"\r\n" in source else b"\n"), 1)
            self.assertTrue(candidate.startswith(prefix + b"ingress:"))
            self.assertTrue(candidate.endswith(suffix))
            self.assertNotIn(b"path: '^/.*", candidate)
            self.assertRaises(rollout.Refused, rollout.transform_config, candidate, rollout.sha(candidate), self.meta)

    def test_hash_ambiguous_ingress_and_legacy_script_changes_refused(self):
        self.assertRaises(rollout.Refused, rollout.transform_config, CONFIG, "a" * 64, self.meta)
        for value in (b"ingress: []\n", CONFIG + b"ingress:\n", CONFIG.replace(b"ingress:\n", b"ingress: &unsafe\n")):
            self.assertRaises(rollout.Refused, rollout.transform_config, value, rollout.sha(value), self.meta)
        policy = CONFIG.replace(b"    service: http://127.0.0.1:4200", b"    originRequest:\n      access:\n        required: true\n    service: http://127.0.0.1:4200")
        self.assertRaisesRegex(rollout.Refused, "per_rule_policy", rollout.transform_config, policy, rollout.sha(policy), self.meta)
        output = rollout.transform_index(INDEX, rollout.sha(INDEX))
        self.assertEqual(output.replace(rollout.NEW_ASSET.encode(), rollout.OLD_ASSET.encode()), INDEX)
        for value in (INDEX + INDEX, INDEX.replace(b'type="module"', b'type="text/plain"'), INDEX.replace(b'type="module"', b'type="module" type="module"')):
            self.assertRaises(rollout.Refused, rollout.transform_index, value, rollout.sha(value))
        self.assertRaises(rollout.Refused, rollout.transform_asset, b"not-reviewed-public-bundle")

    def test_real_cloudflared_validation_and_route_output_are_required(self):
        calls = []
        def fake_command(args):
            calls.append(args)
            if args[-1] == "validate":
                return "Validating rules: OK\n"
            url = args[-1]
            host, path = url.split("://", 1)[1].split("/", 1)
            path = "/" + path.split("?", 1)[0]
            candidate = args[args.index("--config") + 1] == "candidate.yml"
            if host == "api.red-xai.com":
                port = 7790
            elif candidate and self.selected(path):
                port = 14210
            elif path == "/host" or path.startswith("/host/"):
                port = 14202
            elif re.match(r"^/(v1|ui)(/|$)", path):
                port = 17790
            elif re.match(r"^/redxai-connectx?(/|$)", path):
                port = 17774
            else:
                port = 4210 if path == "/" else 4200
            return "Matched rule #0\n  service: http://127.0.0.1:" + str(port) + "\n"
        with patch.object(rollout, "cloudflared", return_value="/fixture/cloudflared"), patch.object(rollout, "command", side_effect=fake_command):
            self.assertGreaterEqual(rollout.validate_routes(Path("original.yml"), Path("candidate.yml")), 40)
        self.assertEqual(calls[0][-1], "validate")
        self.assertFalse(any("restart" in args or "start" in args for args in calls))
        with patch.object(rollout, "cloudflared", return_value="/fixture/cloudflared"), patch.object(rollout, "command", return_value="unrecognized output"):
            self.assertRaises(rollout.Refused, rollout.ingress_service, Path("candidate.yml"), "https://red-xai.com/host")

    def test_sensitive_errors_do_not_use_external_exception_text(self):
        self.assertRaises(rollout.Refused, rollout.strict_json, b'{"a":1,"a":2}')
        self.assertRaises(rollout.Refused, rollout.strict_json, b'{"a":NaN}')
        source = Path(rollout.__file__).read_text()
        self.assertNotIn('"restart",', source)
        self.assertNotIn('"stop",', source)
        self.assertNotIn('"start",', source)
        self.assertNotIn("rmtree", source)

    def test_path_guard_rejects_symlink_unsafe_owner_and_writable_modes(self):
        node = unittest.mock.Mock()
        node.is_absolute.return_value = True
        node.resolve.return_value = node
        node.parents = ()
        with patch.object(rollout.os, "geteuid", return_value=0, create=True):
            for mode, uid in ((stat.S_IFLNK | 0o777, 0), (stat.S_IFDIR | 0o755, 1000), (stat.S_IFDIR | 0o775, 0)):
                node.lstat.return_value = SimpleNamespace(st_mode=mode, st_uid=uid)
                self.assertRaises(rollout.Refused, rollout.safe_path, node)

    def test_actual_primary_index_is_allowed_without_allowing_other_opt_paths(self):
        config = Path("/etc/cloudflared/config.yml")
        rollout.target_paths(config, Path("/opt/redx/lib/panel-dist/index.html"))
        rollout.target_paths(config, Path("/srv/red-xai/reviewed-origin/index.html"))
        for path in ("/opt/other/index.html", "/opt/redx/lib/other/index.html", "/opt/redx/lib/panel-dist/other.html", "/opt/redx/lib/panel-dist/../index.html"):
            self.assertRaises(rollout.Refused, rollout.target_paths, config, Path(path))


@unittest.skipUnless(sys.platform == "linux", "real atomic fsync/xattr fixture requires Linux")
class LinuxFileTransactionTests(unittest.TestCase):
    def fixture(self, directory):
        directory = Path(directory)
        stage = directory / "stage"
        origin = directory / "origin"
        stage.mkdir(mode=0o700)
        (origin / "assets").mkdir(parents=True)
        config = directory / "config.yml"
        index = origin / "index.html"
        original_asset = origin / rollout.OLD_ASSET.lstrip("/")
        config.write_bytes(CONFIG)
        index.write_bytes(INDEX)
        original_asset.write_bytes(b"synthetic original fixture")
        for file in (config, index, original_asset):
            file.chmod(0o600 if file == config else 0o644)
            os.setxattr(file, "user.redx_ui_fixture", b"preserve-this-fixture-attribute")
        files = {"original-config.yml": CONFIG, "original-index.html": INDEX, "candidate-config.yml": b"synthetic candidate config\n", "candidate-index.html": b"synthetic candidate index\n", "candidate-asset.js": b"synthetic candidate asset\n"}
        state = {"config": str(config), "index": str(index), "configMetadata": rollout.fingerprint(config.stat()), "indexMetadata": rollout.fingerprint(index.stat()), "assetMetadata": rollout.fingerprint(original_asset.stat()), "configXattrs": rollout.xattrs(config), "indexXattrs": rollout.xattrs(index), "assetXattrs": rollout.xattrs(original_asset), "baseline": {"fixture": "unchanged"}}
        stack = ExitStack()
        self.addCleanup(stack.close)
        stack.enter_context(patch.object(rollout, "safe_path"))
        stack.enter_context(patch.object(rollout, "load_stage", return_value=(state, files)))
        stack.enter_context(patch.object(rollout, "runtime", return_value=True))
        stack.enter_context(patch.object(rollout, "baseline", return_value=state["baseline"]))
        stack.enter_context(patch.object(rollout, "validate_routes", return_value=45))
        stack.enter_context(patch.object(rollout, "verify_live_config"))
        stack.enter_context(patch.object(rollout, "BEFORE_SHA", rollout.sha(original_asset.read_bytes())))
        stack.enter_context(patch.object(rollout, "AFTER_SHA", rollout.sha(files["candidate-asset.js"])))
        stack.enter_context(patch.object(rollout, "cloudflared", return_value="/fixture/cloudflared"))
        command = stack.enter_context(patch.object(rollout, "command", return_value="validated"))
        return stage, config, index, state, files, command

    def test_real_file_apply_and_resumable_rollback_preserve_modes_without_restarts(self):
        with tempfile.TemporaryDirectory() as root:
            stage, config, index, state, files, command = self.fixture(root)
            result = rollout.apply_stage(stage, {})
            self.assertTrue(result["tunnelRestartRequired"])
            self.assertFalse(result["tunnelRestarted"])
            self.assertEqual(config.read_bytes(), files["candidate-config.yml"])
            self.assertEqual(index.read_bytes(), files["candidate-index.html"])
            self.assertEqual(stat.S_IMODE(config.stat().st_mode), 0o600)
            self.assertEqual(stat.S_IMODE(index.stat().st_mode), 0o644)
            self.assertEqual(os.getxattr(index, "user.redx_ui_fixture"), b"preserve-this-fixture-attribute")
            rollout.rollback_stage(stage)
            rollout.rollback_stage(stage)
            self.assertEqual(config.read_bytes(), CONFIG)
            self.assertEqual(index.read_bytes(), INDEX)
            self.assertEqual(os.getxattr(config, "user.redx_ui_fixture"), b"preserve-this-fixture-attribute")
            self.assertFalse(any("restart" in call.args[0] for call in command.call_args_list))

    def test_failure_after_each_atomic_promotion_restores_original_files(self):
        for target in ("index", "config"):
            with self.subTest(target=target), tempfile.TemporaryDirectory() as root:
                stage, config, index, state, files, command = self.fixture(root)
                actual = rollout.atomic
                failed = False
                def after_replace(file, *args):
                    nonlocal failed
                    actual(file, *args)
                    if file == (index if target == "index" else config) and not failed:
                        failed = True
                        raise OSError("synthetic post-replacement fsync failure")
                with patch.object(rollout, "atomic", side_effect=after_replace):
                    with self.assertRaisesRegex(rollout.Refused, "rollback_succeeded"):
                        rollout.apply_stage(stage, {})
                self.assertEqual(config.read_bytes(), CONFIG)
                self.assertEqual(index.read_bytes(), INDEX)
                self.assertEqual(stat.S_IMODE(config.stat().st_mode), 0o600)

    def test_concurrent_unknown_change_is_not_overwritten_by_failure_recovery(self):
        with tempfile.TemporaryDirectory() as root:
            stage, config, index, state, files, command = self.fixture(root)
            def concurrent(file, *args):
                index.write_bytes(b"unknown intervening operator change")
                raise OSError("synthetic interruption")
            with patch.object(rollout, "atomic", side_effect=concurrent):
                with self.assertRaisesRegex(rollout.Refused, "needs_manual_review"):
                    rollout.apply_stage(stage, {})
            self.assertEqual(index.read_bytes(), b"unknown intervening operator change")
            self.assertEqual(config.read_bytes(), CONFIG)

    def test_rollback_rechecks_unknown_change_during_cloudflared_validation(self):
        with tempfile.TemporaryDirectory() as root:
            stage, config, index, state, files, command = self.fixture(root)
            rollout.apply_stage(stage, {})
            def changed_during_validation(args):
                index.write_bytes(b"intervening operator change during validation")
                return "validated"
            command.side_effect = changed_during_validation
            with self.assertRaisesRegex(rollout.Refused, "before_promotion"):
                rollout.rollback_stage(stage)
            self.assertEqual(index.read_bytes(), b"intervening operator change during validation")

    def test_staged_file_and_self_consistent_receipt_tampering_are_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            stage = root / "scoped-hub-ui-fixture"
            stage.mkdir(mode=0o700)
            meta = rollout.manifest()
            files = {"original-config.yml": CONFIG, "original-index.html": INDEX, "candidate-config.yml": rollout.transform_config(CONFIG, rollout.sha(CONFIG), meta), "candidate-index.html": rollout.transform_index(INDEX, rollout.sha(INDEX)), "candidate-asset.js": b"synthetic candidate fixture"}
            state = {"schema": 1, "runtimeRevision": meta["runtimeRevision"], "config": "/etc/cloudflared/config.yml", "index": "/srv/red-xai/fixture/index.html"}
            pairs = (("original-config.yml", "originalConfigSha256"), ("original-index.html", "originalIndexSha256"), ("candidate-config.yml", "candidateConfigSha256"), ("candidate-index.html", "candidateIndexSha256"), ("candidate-asset.js", "candidateAssetSha256"))
            for name, key in pairs:
                state[key] = rollout.sha(files[name])
                rollout.exclusive(stage / name, files[name])
            rollout.exclusive(stage / "receipt.json", rollout.json_bytes(state))
            with patch.object(rollout, "BACKUPS", root), patch.object(rollout, "safe_path"), patch.object(rollout, "AFTER_SHA", state["candidateAssetSha256"]):
                rollout.load_stage(stage)
                candidate = stage / "candidate-config.yml"
                candidate.write_bytes(files["candidate-config.yml"] + b"# unauthorized staged edit\n")
                self.assertRaisesRegex(rollout.Refused, "digest_mismatch", rollout.load_stage, stage)
                state["candidateConfigSha256"] = rollout.sha(candidate.read_bytes())
                (stage / "receipt.json").write_bytes(rollout.json_bytes(state))
                self.assertRaisesRegex(rollout.Refused, "not_reviewed_transform", rollout.load_stage, stage)


if __name__ == "__main__":
    unittest.main()
