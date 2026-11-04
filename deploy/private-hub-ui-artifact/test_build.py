from __future__ import annotations

from dataclasses import replace
import binascii
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import stat
import struct
import subprocess
import sys
import tarfile
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import build as candidate


def changed_blob(original: candidate.Blob, data: bytes, *, mode: str | None = None) -> candidate.Blob:
    oid = hashlib.sha1(b"blob " + str(len(data)).encode("ascii") + b"\0" + data).hexdigest()
    return candidate.Blob(mode or original.mode, oid, data)


class PinnedCandidateTests(unittest.TestCase):
    def test_git_object_reads_cannot_lazy_fetch_replace_or_prompt(self) -> None:
        result = SimpleNamespace(returncode=0, stdout=b"fixture", stderr=b"")
        with patch.object(candidate.subprocess, "run", return_value=result) as run:
            self.assertEqual(candidate.git(Path.cwd(), "rev-parse", "HEAD"), b"fixture")
        arguments, options = run.call_args
        self.assertEqual(arguments[0][:4], ["git", "--no-lazy-fetch", "--no-replace-objects", "-C"])
        self.assertEqual(options["env"]["GIT_TERMINAL_PROMPT"], "0")
        self.assertIs(options["check"], False)

    @classmethod
    def setUpClass(cls) -> None:
        cls.repository = candidate.repository_root(Path(__file__).resolve().parents[2])
        cls.sources = candidate.load_sources(cls.repository)
        cls.payloads = candidate.bundle(cls.sources)

    def source_with(self, *, side: str, name: str, data: bytes | None = None,
                    mode: str | None = None) -> candidate.Sources:
        files = dict(getattr(self.sources, side))
        original = files[name]
        files[name] = changed_blob(original, data if data is not None else original.data, mode=mode)
        return replace(self.sources, **{side: files})

    def test_exact_source_provenance_and_deployment_flags(self) -> None:
        provenance = json.loads(self.payloads["provenance.json"])
        self.assertEqual(provenance["baseRevision"], "c443ce7eb01c0a33261c049bc7071dbc90fd46d4")
        self.assertEqual(provenance["overlayRevision"], "ce4408632446225462a4f9bdce7f365b55822fe2")
        overlays = {"catalog.json", "cli-installation.md", "site/catalog.js"}
        self.assertEqual(set(provenance["runtimeFiles"]), set(candidate.RUNTIME_FILES))
        self.assertEqual(set(provenance["overlayFiles"]), overlays)
        self.assertEqual(sum(v["sourceRevision"] == candidate.BASE_REVISION
                             for v in provenance["runtimeFiles"].values()), 19)
        self.assertEqual(sum(v["sourceRevision"] == candidate.OVERLAY_REVISION
                             for v in provenance["runtimeFiles"].values()), 3)
        self.assertTrue(all(value is False for value in provenance["deployment"].values()))
        self.assertFalse(provenance["browserAssets"]["oldRevisionRetentionAcrossRestartVerified"])
        self.assertNotIn(str(self.repository), self.payloads["provenance.json"].decode())

    def test_archive_bytes_match_git_sources_and_have_no_extra_paths(self) -> None:
        with tarfile.open(fileobj=io.BytesIO(self.payloads["runtime.tar.gz"]), mode="r:gz") as archive:
            members = archive.getmembers()
            self.assertEqual([m.name for m in members], ["runtime/" + p for p in sorted(candidate.RUNTIME_FILES)])
            self.assertEqual(len(members), 22)
            for member in members:
                with self.subTest(member=member.name):
                    name = member.name.removeprefix("runtime/")
                    expected = self.sources.target[name] if name in candidate.OVERLAY_HASHES else self.sources.base[name]
                    self.assertEqual(archive.extractfile(member).read(), expected.data)
                    self.assertTrue(member.isfile())
                    self.assertFalse(member.linkname)
                    self.assertEqual((member.uid, member.gid, member.mtime, member.mode), (0, 0, 0, 0o644))
                    self.assertEqual((member.uname, member.gname, member.pax_headers), ("", "", {}))
                    self.assertNotIn("..", Path(member.name).parts)
                    self.assertFalse(member.name.startswith("/"))
            # The package retains the baseline metadata, not the target test-script edit.
            self.assertEqual(archive.extractfile("runtime/package.json").read(), self.sources.base["package.json"].data)

    def test_determinism_and_gzip_metadata(self) -> None:
        self.assertEqual(candidate.bundle(self.sources), self.payloads)
        archive = self.payloads["runtime.tar.gz"]
        self.assertEqual(archive[:3], b"\x1f\x8b\x08")
        self.assertEqual(archive[3], 0)  # No embedded filename or host-specific metadata.
        self.assertEqual(archive[4:8], b"\0\0\0\0")
        self.assertEqual(archive[9], 255)
        candidate.verify_bundle(self.payloads, self.sources)

    def test_detached_checksums_bind_archive_and_provenance(self) -> None:
        lines = self.payloads["SHA256SUMS"].decode("ascii").splitlines()
        self.assertEqual(len(lines), 2)
        for line in lines:
            digest, name = line.split("  ")
            self.assertEqual(digest, hashlib.sha256(self.payloads[name]).hexdigest())
        metadata = json.loads(self.payloads["provenance.json"])
        self.assertEqual(metadata["runtimeArchive"]["sha256"], hashlib.sha256(self.payloads["runtime.tar.gz"]).hexdigest())
        self.assertEqual(metadata["runtimeArchive"]["size"], len(self.payloads["runtime.tar.gz"]))

    def test_reviewed_outer_pins_match_all_three_output_files(self) -> None:
        expected = Path(__file__).with_name("EXPECTED-SHA256SUMS").read_text(encoding="ascii").splitlines()
        self.assertEqual({line.split("  ")[1] for line in expected}, set(candidate.OUTPUT_FILES))
        for line in expected:
            digest, name = line.split("  ")
            self.assertEqual(hashlib.sha256(self.payloads[name]).hexdigest(), digest)
        self.assertEqual(candidate.CANDIDATE_ARCHIVE_SHA256,
                         "56496195081d28be14b9be6d2fd79f77272de8280fcf67347f155544b94056c3")

    def test_gzip_has_canonical_stored_blocks_and_independent_crc(self) -> None:
        encoded = self.payloads["runtime.tar.gz"]
        cursor, decoded, block_sizes = 10, bytearray(), []
        while True:
            header = encoded[cursor]
            self.assertIn(header, (0, 1))  # No compressed/Huffman or padding bits.
            size, complement = struct.unpack("<HH", encoded[cursor + 1:cursor + 5])
            self.assertEqual(size ^ complement, 0xFFFF)
            cursor += 5
            block_sizes.append(size)
            decoded.extend(encoded[cursor:cursor + size])
            cursor += size
            if header == 1:
                break
            self.assertEqual(size, 65535)
        self.assertGreater(len(block_sizes), 1)  # Exercise actual block boundaries.
        self.assertEqual(len(encoded) - cursor, 8)
        crc, size = struct.unpack("<II", encoded[cursor:])
        self.assertEqual(crc, binascii.crc32(decoded) & 0xFFFFFFFF)
        self.assertEqual(size, len(decoded) & 0xFFFFFFFF)
        self.assertEqual(bytes(decoded), gzip.decompress(encoded))
        self.assertEqual(json.loads(self.payloads["provenance.json"])["runtimeArchive"]["encoding"],
                         "ustar-gzip-stored-deflate-v1")

    def test_asset_revisions_independently_follow_pinned_server_algorithm(self) -> None:
        server = self.sources.base["server.mjs"].data.decode("utf-8")
        exported = re.search(r"SITE_FILES=Object\.freeze\(\[([^]]+)\]\)", server).group(1)
        names = ["index.html", *re.findall(r"'([^']+)'", exported)]
        self.assertEqual(len(names), 14)

        def digest(side: str) -> str:
            value = hashlib.sha256()
            for name in sorted(names):
                value.update(name.encode() + b"\0")
                selected = self.sources.target if side == "candidate" and name in candidate.OVERLAY_HASHES else self.sources.base
                value.update(selected[name].data)
            return value.hexdigest()

        provenance = json.loads(self.payloads["provenance.json"])
        self.assertEqual(digest("base"), "29ae45c806fe8be49b9b5a61128ba8749b1a4e3e72b12e0f98f37c96904c2de9")
        self.assertEqual(digest("candidate"), "3035a63b01e2519806befd2a835f54f8a91924a4aa890234672d804a4be0a1f4")
        self.assertEqual(provenance["browserAssets"]["baseRevision"], digest("base"))
        self.assertEqual(provenance["browserAssets"]["candidateRevision"], digest("candidate"))

    def test_known_fourth_source_difference_is_only_test_metadata(self) -> None:
        changed = {p for p in candidate.RUNTIME_FILES if self.sources.base[p].data != self.sources.target[p].data}
        self.assertEqual(changed, {"catalog.json", "cli-installation.md", "site/catalog.js", "package.json"})
        before = json.loads(self.sources.base["package.json"].data)
        after = json.loads(self.sources.target["package.json"].data)
        self.assertNotEqual(before["scripts"]["test"], after["scripts"]["test"])
        after["scripts"]["test"] = before["scripts"]["test"]
        self.assertEqual(before, after)

    def test_refuses_revision_override_and_manifest_tamper(self) -> None:
        for altered in (
            replace(self.sources, base_revision="a" * 40),
            replace(self.sources, overlay_revision="b" * 40),
            replace(self.sources, manifest=changed_blob(self.sources.manifest, self.sources.manifest.data + b" ")),
        ):
            with self.subTest(altered=altered.base_revision), self.assertRaises(candidate.Refusal):
                candidate.bundle(altered)

    def test_refuses_missing_or_extra_private_input_paths(self) -> None:
        for side in ("base", "target"):
            for path in (".env", "cms.key", "state/audit.jsonl", ".git/config"):
                files = dict(getattr(self.sources, side))
                files[path] = changed_blob(self.sources.base["index.html"], b"PRIVATE-INPUT-MUST-NOT-ENTER")
                with self.subTest(side=side, path=path), self.assertRaises(candidate.Refusal):
                    candidate.bundle(replace(self.sources, **{side: files}))
            files = dict(getattr(self.sources, side))
            del files["app.js"]
            with self.subTest(side=side, missing=True), self.assertRaises(candidate.Refusal):
                candidate.bundle(replace(self.sources, **{side: files}))

    def test_refuses_changed_base_blob_even_with_valid_git_object_id(self) -> None:
        changed = self.source_with(side="base", name="app.js", data=self.sources.base["app.js"].data + b"\n")
        with self.assertRaisesRegex(candidate.Refusal, "Base runtime content pin mismatch"):
            candidate.bundle(changed)

    def test_refuses_changed_overlay_and_unexpected_backend(self) -> None:
        for name in ("catalog.json", "cli-installation.md", "site/catalog.js", "server.mjs", "package-lock.json", "package.json"):
            with self.subTest(path=name), self.assertRaises(candidate.Refusal):
                candidate.bundle(self.source_with(side="target", name=name, data=self.sources.target[name].data + b"\n"))

    def test_refuses_symlink_executable_and_wrong_object_identity(self) -> None:
        for mode in ("120000", "100755", "160000"):
            with self.subTest(mode=mode), self.assertRaises(candidate.Refusal):
                candidate.bundle(self.source_with(side="target", name="site/catalog.js", mode=mode))
        files = dict(self.sources.base)
        files["server.mjs"] = replace(files["server.mjs"], oid="0" * 40)
        with self.assertRaisesRegex(candidate.Refusal, "Git blob content"):
            candidate.bundle(replace(self.sources, base=files))

    def test_self_consistent_tampered_artifact_is_refused(self) -> None:
        altered = dict(self.payloads)
        raw = io.BytesIO()
        with tarfile.open(fileobj=io.BytesIO(altered["runtime.tar.gz"]), mode="r:gz") as old:
            with tarfile.open(fileobj=raw, mode="w", format=tarfile.USTAR_FORMAT) as new:
                for member in old.getmembers():
                    data = old.extractfile(member).read()
                    if member.name == "runtime/catalog.json":
                        data += b"\n"
                        member.size = len(data)
                    new.addfile(member, io.BytesIO(data))
        compressed = io.BytesIO()
        with gzip.GzipFile(filename="", mode="wb", fileobj=compressed, mtime=0, compresslevel=9) as stream:
            stream.write(raw.getvalue())
        altered["runtime.tar.gz"] = compressed.getvalue()
        provenance = json.loads(altered["provenance.json"])
        changed_catalog = self.sources.target["catalog.json"].data + b"\n"
        provenance["runtimeFiles"]["catalog.json"].update(sha256=candidate.sha256(changed_catalog), size=len(changed_catalog))
        provenance["runtimeArchive"].update(sha256=candidate.sha256(altered["runtime.tar.gz"]), size=len(altered["runtime.tar.gz"]))
        altered["provenance.json"] = candidate.canonical_json(provenance)
        altered["SHA256SUMS"] = "".join(candidate.sha256(altered[p]) + "  " + p + "\n" for p in ("runtime.tar.gz", "provenance.json")).encode()
        with self.assertRaisesRegex(candidate.Refusal, "differs from pinned source"):
            candidate.verify_bundle(altered, self.sources)

    def test_extra_output_payload_is_refused(self) -> None:
        with self.assertRaises(candidate.Refusal):
            candidate.verify_bundle({**self.payloads, "cms.key": b"not-a-real-key"}, self.sources)

    def test_cli_exposes_no_deployment_or_source_override_options(self) -> None:
        script = Path(__file__).with_name("build.py")
        for flag in ("--apply", "--deploy", "--source-revision", "--manifest", "--output"):
            result = subprocess.run([sys.executable, "-I", "-B", str(script), "--output-name", "unused", flag, "unused"],
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
            with self.subTest(flag=flag):
                self.assertEqual(result.returncode, 2)
                self.assertIn(b"unrecognized arguments", result.stderr)


class FreshOutputTests(unittest.TestCase):
    def test_fresh_write_and_collision_preserve_existing_output(self) -> None:
        with tempfile.TemporaryDirectory(prefix="redx-ui-artifact-test-") as scratch:
            root = Path(scratch).resolve()
            payloads = {name: (name + "\n").encode() for name in candidate.OUTPUT_FILES}
            output = candidate.write_candidate(root, "reviewed-1", payloads)
            self.assertEqual(output.parent, root / ".task-tmp")
            self.assertEqual({p.name for p in output.iterdir()}, set(candidate.OUTPUT_FILES))
            for name, data in payloads.items():
                self.assertEqual((output / name).read_bytes(), data)
            (output / "operator-note").write_text("keep this")
            with self.assertRaisesRegex(candidate.Refusal, "already exists"):
                candidate.write_candidate(root, "reviewed-1", payloads)
            self.assertEqual((output / "operator-note").read_text(), "keep this")

    def test_unsafe_output_names_never_create_an_output_parent(self) -> None:
        with tempfile.TemporaryDirectory(prefix="redx-ui-artifact-test-") as scratch:
            root = Path(scratch).resolve()
            for name in ("../escape", "/tmp/escape", "a/b", "a\\b", "C:escape", ".", "..", "-a", "UPPER", "a" * 64, ""):
                with self.subTest(name=name), self.assertRaises(candidate.Refusal):
                    candidate.write_candidate(root, name, {p: b"x" for p in candidate.OUTPUT_FILES})
            self.assertEqual(list(root.iterdir()), [])

    def test_rejects_parent_file_existing_output_file_and_extra_payload(self) -> None:
        with tempfile.TemporaryDirectory(prefix="redx-ui-artifact-test-") as scratch:
            root = Path(scratch).resolve()
            (root / ".task-tmp").write_text("existing file")
            with self.assertRaises(candidate.Refusal):
                candidate.write_candidate(root, "reviewed", {p: b"x" for p in candidate.OUTPUT_FILES})
        with tempfile.TemporaryDirectory(prefix="redx-ui-artifact-test-") as scratch:
            root = Path(scratch).resolve()
            (root / ".task-tmp").mkdir()
            (root / ".task-tmp" / "reviewed").write_text("existing output")
            with self.assertRaises(candidate.Refusal):
                candidate.write_candidate(root, "reviewed", {p: b"x" for p in candidate.OUTPUT_FILES})
            self.assertEqual((root / ".task-tmp" / "reviewed").read_text(), "existing output")
            with self.assertRaises(candidate.Refusal):
                candidate.write_candidate(root, "new", {**{p: b"x" for p in candidate.OUTPUT_FILES}, "cms.key": b"x"})
            self.assertFalse((root / ".task-tmp" / "new").exists())

    def test_rejects_symlink_output_parent(self) -> None:
        with tempfile.TemporaryDirectory(prefix="redx-ui-artifact-test-") as scratch:
            root = Path(scratch).resolve()
            repository, outside = root / "repository", root / "outside"
            repository.mkdir()
            outside.mkdir()
            try:
                (repository / ".task-tmp").symlink_to(outside, target_is_directory=True)
            except (OSError, NotImplementedError):
                self.skipTest("This test account cannot create directory symlinks")
            with self.assertRaises(candidate.Refusal):
                candidate.write_candidate(repository, "reviewed", {p: b"x" for p in candidate.OUTPUT_FILES})
            self.assertEqual(list(outside.iterdir()), [])

    def test_windows_reparse_attribute_is_refused_without_creating_junctions(self) -> None:
        fake = SimpleNamespace(lstat=lambda: SimpleNamespace(st_mode=stat.S_IFDIR, st_file_attributes=0x400))
        self.assertTrue(candidate.is_reparse(fake))

    @unittest.skipUnless(os.name == "posix", "Descriptor-relative ancestry race is a POSIX test")
    def test_parent_swap_does_not_redirect_candidate_writes(self) -> None:
        with tempfile.TemporaryDirectory(prefix="redx-ui-artifact-test-") as scratch:
            root = Path(scratch).resolve()
            repository, outside = root / "repository", root / "outside"
            repository.mkdir()
            outside.mkdir()
            real_fsync = os.fsync
            swapped = False

            def swap_after_first_file(descriptor: int) -> None:
                nonlocal swapped
                real_fsync(descriptor)
                if not swapped:
                    swapped = True
                    parent = repository / ".task-tmp"
                    parent.rename(repository / ".task-tmp-moved")
                    parent.symlink_to(outside, target_is_directory=True)

            with patch.object(candidate.os, "fsync", side_effect=swap_after_first_file):
                with self.assertRaises(candidate.Refusal):
                    candidate.write_candidate(repository, "reviewed", {p: b"x" for p in candidate.OUTPUT_FILES})
            self.assertTrue(swapped)
            self.assertEqual(list(outside.iterdir()), [])
            self.assertEqual({p.name for p in (repository / ".task-tmp-moved" / "reviewed").iterdir()}, {"runtime.tar.gz"})


if __name__ == "__main__":
    unittest.main()
