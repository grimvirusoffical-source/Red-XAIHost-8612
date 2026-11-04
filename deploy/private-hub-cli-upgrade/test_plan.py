"""All observations below are synthetic fixtures, never live captures."""
from copy import deepcopy
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import plan

NOW = datetime(2026, 11, 4, 12, 0, tzinfo=timezone.utc)
DELETE = object()


def ident(path, kind="regular", uid=0, gid=0, mode="0644", hashed=True):
    item = dict(path=path, kind=kind, device=7, inode=101, uid=uid, gid=gid, mode=mode,
                size=128, mtimeNs=1000000, symlink=False)
    if hashed:
        item["sha256"] = "a" * 64
    return item


def observations(m, exposure="public-routed"):
    u, pid = m["unit"], 777
    uid, gid = u["requiredUid"], u["requiredGid"]
    support = "/var/lib/red-xai/example-support"
    config = dict(environmentFiles=[ident("/etc/red-xai/example.env", mode="0600")], cmsDatabase="website_cms",
                  cmsBoxPrefix="site-cms.red-xai.db", cmsDurability="group", cmsSingleWriter=True,
                  keyFile=ident("/etc/red-xai/example.key", uid=uid, gid=gid, mode="0600"),
                  supportDirectory=ident(support, "directory", uid, gid, "0700", False),
                  writerLockPid=pid, valuesOmitted=True)
    for field, suffix, kind, mode in (("mediaDirectory", "/media", "directory", "0700"),
                                     ("auditFile", "/audit.jsonl", "regular", "0600"),
                                     ("writerLock", "/writer.lock", "regular", "0600")):
        config[field] = ident(support + suffix, kind, uid, gid, mode, False)
    return dict(schema=1, observedAt=NOW.isoformat(),
        unit=dict(name=u["name"], type="simple", activeState="active", mainPid=pid, user="redx-site", group="redx-site",
                  workingDirectory=u["baseWorkingDirectory"], execStart=["/usr/bin/node", "server.mjs"],
                  effectivePropertiesSha256="b" * 64, fragment=ident("/etc/systemd/system/" + u["name"]),
                  dropIns=[ident("/etc/systemd/system/" + u["name"] + ".d/10-existing.conf")], newDropInAbsent=True),
        currentRuntime=dict(directory=ident(u["baseWorkingDirectory"], "directory", mode="0755", hashed=False),
                            runtimeFiles=deepcopy(m["baselineRuntimeFiles"]), browserAssetRevision=m["browserAssets"]["baseRevision"]),
        process=dict(pid=pid, startTicks=500, uids=[uid] * 4, gids=[gid] * 4),
        nodeExecutable=dict(identity=ident("/usr/bin/node", mode="0755"), version="v22.12.0"), configuration=config,
        listener=dict(address="127.0.0.1", port=14210, pid=pid),
        routing=dict(exposure=exposure, configuration=ident("/etc/cloudflared/config.yml", mode="0600"), inventorySha256="c" * 64),
        newRelease=dict(parent=ident("/opt", "directory", mode="0755", hashed=False), directoryAbsent=True))


def mutate(value, dotted, replacement):
    result = deepcopy(value)
    current = result
    parts = dotted.split(".")
    for part in parts[:-1]:
        current = current[int(part)] if type(current) is list else current[part]
    key = int(parts[-1]) if type(current) is list else parts[-1]
    if replacement is DELETE:
        del current[key]
    else:
        current[key] = replacement
    return result


class ProposalTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = Path(__file__).resolve().parent
        cls.manifest = plan.manifest()
        spec = importlib.util.spec_from_file_location("pinned_hub_fixture_builder", cls.directory.parent / "private-hub-ui-artifact" / "build.py")
        builder = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = builder
        spec.loader.exec_module(builder)
        cls.payloads = builder.bundle(builder.load_sources(cls.directory.parents[1]))
        cls.scratch = tempfile.TemporaryDirectory(prefix="redx-upgrade-plan-test-")
        cls.candidate = Path(cls.scratch.name).resolve() / "candidate"
        cls.candidate.mkdir()
        for name, data in cls.payloads.items():
            (cls.candidate / name).write_bytes(data)
        cls.checked = plan.artifacts(cls.candidate, cls.manifest)
        cls.observed = observations(cls.manifest)

    @classmethod
    def tearDownClass(cls):
        cls.scratch.cleanup()

    def preview(self, value=None):
        return plan.proposal(self.checked, self.manifest, self.observed if value is None else value, NOW)

    def test_exact_mixed_artifact_and_manifest_pins(self):
        self.assertEqual(plan.sha((self.directory / "manifest.json").read_bytes()), plan.MANIFEST_SHA256)
        self.assertEqual(self.checked["runtimeFilesVerified"], 22)
        self.assertEqual((self.checked["preservedBaseFiles"], self.checked["overlayFiles"]), (19, 3))
        self.assertEqual(self.checked["archiveSha256"], "56496195081d28be14b9be6d2fd79f77272de8280fcf67347f155544b94056c3")
        self.assertEqual(self.checked["candidateAssetRevision"], "3035a63b01e2519806befd2a835f54f8a91924a4aa890234672d804a4be0a1f4")
        self.assertFalse(self.checked["extractedToFilesystem"])

    def test_missing_evidence_never_assumes_privacy_or_produces_unit(self):
        result = plan.proposal(self.checked, self.manifest)
        self.assertEqual(result["operatorEvidenceStatus"], "UNVERIFIED")
        self.assertIsNone(result["unitProposal"])
        self.assertFalse(result["readyForDeployment"])
        self.assertNotIn("applicableApproval", result)

    def test_public_preview_only_two_properties_and_no_granted_actions(self):
        result = self.preview()
        changes = result["unitProposal"]["changes"]
        self.assertEqual([v["property"] for v in changes], ["WorkingDirectory", "ExecStart"])
        self.assertNotEqual(changes[0]["after"], self.manifest["unit"]["baseWorkingDirectory"])
        self.assertEqual(changes[1]["after"], ["/usr/bin/node", changes[0]["after"] + "/server.mjs"])
        self.assertIn("public-service restart approval", result["applicableApproval"])
        self.assertEqual(result["unitProposal"]["previewOnly"].splitlines(),
                         ["[Service]", "WorkingDirectory=" + changes[0]["after"], "ExecStart=", "ExecStart=" + " ".join(changes[1]["after"])])
        for key in ("readyForDeployment", "liveConfigurationRead", "credentialFilesOpened", "serviceCommandsExecuted",
                    "filesWritten", "approvalVerified", "backupRestoreProofVerified", "productionAcceptanceVerified"):
            self.assertIs(result[key], False)
        self.assertFalse(result["evidenceValidation"]["authenticityVerified"])
        self.assertFalse(result["evidenceValidation"]["liveIdentitiesRechecked"])

    def test_private_inventory_explicit_and_unknown_refused(self):
        value = observations(self.manifest, "private-loopback-only")
        self.assertIn("recheck actual exposure", self.preview(value)["applicableApproval"])
        self.assertFalse(self.preview(value)["readyForDeployment"])
        with self.assertRaises(plan.Refusal):
            self.preview(mutate(value, "routing.exposure", "unknown"))

    def test_full_canonical_observation_binding(self):
        expected = hashlib.sha256((json.dumps(self.observed, sort_keys=True, separators=(",", ":"), ensure_ascii=True) + "\n").encode("ascii")).hexdigest()
        self.assertEqual(self.preview()["evidenceValidation"]["observationSha256"], expected)
        self.assertEqual(self.preview(dict(reversed(list(self.observed.items()))))["evidenceValidation"]["observationSha256"], expected)
        for key, value in (("unit.effectivePropertiesSha256", "d" * 64), ("configuration.auditFile.mtimeNs", 2000000)):
            self.assertNotEqual(self.preview(mutate(self.observed, key, value))["evidenceValidation"]["observationSha256"], expected)

    def test_no_live_metadata_path_reads(self):
        with patch.object(plan.os, "open", side_effect=AssertionError("No live opens")), \
             patch.object(Path, "read_bytes", side_effect=AssertionError("No live reads")), \
             patch("builtins.open", side_effect=AssertionError("No live reads")):
            self.assertIsNotNone(self.preview()["unitProposal"])

    def test_stale_future_naive_non_utc_evidence_refused(self):
        for value in ((NOW - timedelta(seconds=301)).isoformat(), (NOW + timedelta(seconds=31)).isoformat(),
                      "2026-11-04T12:00:00", "2026-11-04T13:00:00+01:00", "UNVERIFIED"):
            with self.subTest(time=value), self.assertRaises(ValueError):
                self.preview(mutate(self.observed, "observedAt", value))

    def test_prefix_and_env_key_hash_requirements(self):
        for key, value in (("configuration.cmsBoxPrefix", DELETE), ("configuration.cmsBoxPrefix", "other.prefix"),
                           ("configuration.environmentFiles.0.sha256", DELETE), ("configuration.keyFile.sha256", DELETE),
                           ("configuration.environmentFiles.0.sha256", "invalid"), ("configuration.keyFile.sha256", "invalid")):
            with self.subTest(field=key), self.assertRaises(plan.Refusal):
                self.preview(mutate(self.observed, key, value))

    def test_unsupported_unit_process_and_node_shapes_refused(self):
        cases = [("unit.type", "oneshot"), ("unit.user", "root"), ("unit.name", "other.service"),
                 ("unit.workingDirectory", "/opt/other"), ("unit.execStart", ["/bin/sh", "-c", "node server.mjs"]),
                 ("unit.execStart", ["/usr/bin/node", "--inspect", "server.mjs"]), ("unit.mainPid", True),
                 ("process.pid", 778), ("process.uids", [986, 0, 986, 986]), ("process.gids", [976, 976, 976, 0]),
                 ("configuration.writerLockPid", 778), ("nodeExecutable.version", "v20.8.9")]
        for key, value in cases:
            with self.subTest(field=key), self.assertRaises(plan.Refusal):
                self.preview(mutate(self.observed, key, value))

    def test_baseline_and_unsafe_identity_or_state_refused(self):
        changed_hashes = {**self.manifest["baselineRuntimeFiles"], "app.js": "0" * 64}
        cases = [("currentRuntime.browserAssetRevision", self.manifest["browserAssets"]["candidateRevision"]),
                 ("currentRuntime.runtimeFiles", changed_hashes), ("currentRuntime.directory.symlink", True),
                 ("nodeExecutable.identity.path", "/usr/bin/node\nExecStart=/bad"),
                 ("nodeExecutable.identity.path", "/usr/bin/../bin/node"), ("nodeExecutable.identity.path", "/usr/bin/%n/node"),
                 ("configuration.keyFile.mode", "0644"), ("configuration.keyFile.uid", 0),
                 ("configuration.auditFile.path", "/var/lib/red-xai/other/audit.jsonl"),
                 ("configuration.cmsDatabase", "other"), ("configuration.cmsSingleWriter", False),
                 ("listener.address", "0.0.0.0"), ("listener.pid", 778), ("newRelease.directoryAbsent", False),
                 ("unit.newDropInAbsent", False)]
        for key, value in cases:
            with self.subTest(field=key), self.assertRaises(plan.Refusal):
                self.preview(mutate(self.observed, key, value))

    def test_uncertain_dropin_precedence_refused(self):
        value = deepcopy(self.observed)
        value["unit"]["dropIns"] = [ident("/etc/systemd/system/" + self.manifest["unit"]["name"] + ".d/zzzzz-later.conf")]
        with self.assertRaises(plan.Refusal):
            self.preview(value)
        value = deepcopy(self.observed)
        value["unit"]["dropIns"] *= 2
        with self.assertRaises(plan.Refusal):
            self.preview(value)

    def test_extra_values_duplicate_fields_and_incomplete_example_refused(self):
        value = deepcopy(self.observed)
        value["configuration"]["environmentValues"] = "BENIGN-SENTINEL-NOT-A-REAL-SECRET"
        with self.assertRaises(plan.Refusal):
            self.preview(value)
        with self.assertRaises(plan.Refusal):
            plan.json_object(b'{"schema":1,"schema":1}')
        with self.assertRaises(plan.Refusal):
            self.preview(json.loads((self.directory / "operator-evidence.example.json").read_bytes()))

    def test_self_consistent_artifact_metadata_tamper_refused(self):
        with tempfile.TemporaryDirectory(prefix="redx-upgrade-tamper-") as scratch:
            root = Path(scratch).resolve()
            for name, data in self.payloads.items():
                (root / name).write_bytes(data)
            proof = json.loads(self.payloads["provenance.json"])
            proof["runtimeFiles"]["catalog.json"]["sha256"] = "0" * 64
            (root / "provenance.json").write_text(json.dumps(proof))
            (root / "SHA256SUMS").write_text("self-consistent-edits-are-not-trusted")
            with self.assertRaises(plan.Refusal):
                plan.artifacts(root, self.manifest)

    def test_cli_readonly_and_no_apply_or_credential_filename(self):
        command = [sys.executable, "-I", "-B", str(self.directory / "plan.py"), "--artifact-directory", str(self.candidate)]
        before = {p.name: p.read_bytes() for p in self.candidate.iterdir()}
        result = subprocess.run(command, capture_output=True, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIsNone(json.loads(result.stdout)["unitProposal"])
        self.assertEqual(before, {p.name: p.read_bytes() for p in self.candidate.iterdir()})
        for flag in ("--apply", "--install", "--service", "--manifest"):
            result = subprocess.run([*command, flag, "x"], capture_output=True, check=False)
            with self.subTest(flag=flag):
                self.assertEqual(result.returncode, 2)
        result = subprocess.run([*command, "--evidence", str(self.candidate / "auth.json")], capture_output=True, check=False)
        self.assertEqual(result.returncode, 1)
        self.assertNotIn(b"auth.json", result.stderr)


if __name__ == "__main__":
    unittest.main()
