#!/usr/bin/env python3
"""Prepare a pinned source candidate. This program cannot deploy or start it."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
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
from typing import Mapping
import zlib


BASE_REVISION = "c443ce7eb01c0a33261c049bc7071dbc90fd46d4"
OVERLAY_REVISION = "ce4408632446225462a4f9bdce7f365b55822fe2"
SOURCE_ROOT = "website/red-xai.com/"
BASELINE_MANIFEST = "deploy/scoped-hub-ui-rollout/manifest.json"
BASELINE_MANIFEST_SHA256 = "3ebaf2cf1f59304a60d347a90ba99987bd3d64658b5c825a941f17d85aa4665c"
BASE_ASSET_REVISION = "29ae45c806fe8be49b9b5a61128ba8749b1a4e3e72b12e0f98f37c96904c2de9"
CANDIDATE_ASSET_REVISION = "3035a63b01e2519806befd2a835f54f8a91924a4aa890234672d804a4be0a1f4"
CANDIDATE_ARCHIVE_SHA256 = "56496195081d28be14b9be6d2fd79f77272de8280fcf67347f155544b94056c3"
RUNTIME_FILES = (
    "index.html", "app.js", "styles.css", "catalog.json",
    "public-secrets-docs.json", "cli-installation.md", "site/api.js",
    "site/auth.js", "site/catalog.js", "site/dom.js", "site/editor.js",
    "site/legal.js", "site/pages.js", "site/session.js", "server.mjs",
    "gateway-proxy.mjs", "server-lifecycle.mjs", "site-backend.mjs",
    "redx-cms-store.mjs", "vortex-release.mjs", "package.json", "package-lock.json",
)
BROWSER_FILES = (
    "index.html", "app.js", "styles.css", "catalog.json",
    "public-secrets-docs.json", "cli-installation.md", "site/api.js",
    "site/auth.js", "site/catalog.js", "site/dom.js", "site/editor.js",
    "site/legal.js", "site/pages.js", "site/session.js",
)
OVERLAY_HASHES = {
    "catalog.json": "c7f19a51bc615397b9319dda5f9e566a6a51a15062b496078b4585eb19fff4a8",
    "cli-installation.md": "10b56310f6b53d8ed02575c4f01b19e6fb00531e9e3df042b8c4ecae01b7778d",
    "site/catalog.js": "a9f4b71cb8303623cc9853ca9a7f6f1f370b62af26d7a6fddd4673bab3b4e609",
}
AUDIT_ONLY_TARGET_PACKAGE_SHA256 = "55b7b7f96daa42af4072800b3029d6c6a9ffbb2c373a35d78322a70385a92c4a"
OUTPUT_FILES = ("runtime.tar.gz", "provenance.json", "SHA256SUMS")
MAX_SOURCE_BYTES = 5 * 1024 * 1024
OUTPUT_NAME = re.compile(r"[a-z0-9][a-z0-9-]{0,62}\Z")


class Refusal(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise Refusal(message)


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def canonical_json(value: object) -> bytes:
    return (json.dumps(value, sort_keys=True, indent=2, ensure_ascii=True) + "\n").encode("utf-8")


@dataclass(frozen=True)
class Blob:
    mode: str
    oid: str
    data: bytes


@dataclass(frozen=True)
class Sources:
    base_revision: str
    overlay_revision: str
    manifest: Blob
    base: Mapping[str, Blob]
    target: Mapping[str, Blob]


def git(repository: Path, *arguments: str, stdin: bytes | None = None) -> bytes:
    # Fixed object references and allowlisted paths only. No shell, fetch,
    # checkout, credential helper, external diff, filter, or source execution.
    result = subprocess.run(
        ["git", "-C", str(repository), *arguments], input=stdin,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30,
        check=False,
    )
    require(result.returncode == 0, "Required committed Git objects are unavailable; no fetch attempted")
    return result.stdout


def repository_root(path: Path) -> Path:
    require(not is_reparse(path), "Repository argument must not be a symlink or reparse point")
    resolved = path.resolve(strict=True)
    require(resolved.is_dir(), "Repository directory required")
    returned = Path(os.fsdecode(git(resolved, "rev-parse", "--show-toplevel").strip())).resolve(strict=True)
    require(returned == resolved, "Repository argument must name its exact worktree root")
    return resolved


def read_blobs(repository: Path, revision: str, names: tuple[str, ...], *, root: str) -> dict[str, Blob]:
    paths = tuple(root + name for name in names)
    entries = git(repository, "ls-tree", "-r", "-z", revision, "--", *paths)
    found: dict[str, tuple[str, str]] = {}
    for entry in entries.split(b"\0"):
        if not entry:
            continue
        metadata, encoded_path = entry.split(b"\t", 1)
        mode, kind, oid = metadata.decode("ascii").split(" ")
        path = encoded_path.decode("utf-8")
        require(path in paths and kind == "blob" and mode == "100644", "Only the exact regular source files are accepted")
        name = path[len(root):]
        require(name not in found, "Duplicate committed source path")
        found[name] = (mode, oid)
    require(set(found) == set(names), "Committed source allowlist is incomplete or unexpected")
    ordered = sorted(names)
    request = "".join(found[name][1] + "\n" for name in ordered).encode("ascii")
    stream = io.BytesIO(git(repository, "cat-file", "--batch", stdin=request))
    blobs: dict[str, Blob] = {}
    for name in ordered:
        header = stream.readline().decode("ascii").rstrip("\n").split(" ")
        require(len(header) == 3 and header[0] == found[name][1] and header[1] == "blob", "Unexpected Git object response")
        size = int(header[2])
        require(0 < size <= MAX_SOURCE_BYTES, "Source size is outside the reviewed limit")
        data = stream.read(size)
        require(len(data) == size and stream.read(1) == b"\n", "Truncated Git object response")
        blobs[name] = Blob(found[name][0], found[name][1], data)
    require(stream.read() == b"", "Unexpected trailing Git object response")
    return blobs


def load_sources(repository: Path) -> Sources:
    for revision in (BASE_REVISION, OVERLAY_REVISION):
        require(git(repository, "rev-parse", revision + "^{commit}").strip().decode("ascii") == revision,
                "Exact pinned commit required")
    manifest = read_blobs(repository, OVERLAY_REVISION, (BASELINE_MANIFEST,), root="")[BASELINE_MANIFEST]
    return Sources(
        BASE_REVISION, OVERLAY_REVISION, manifest,
        read_blobs(repository, BASE_REVISION, RUNTIME_FILES, root=SOURCE_ROOT),
        read_blobs(repository, OVERLAY_REVISION, RUNTIME_FILES, root=SOURCE_ROOT),
    )


def validate_blob(blob: Blob) -> None:
    require(isinstance(blob, Blob) and blob.mode == "100644", "Regular non-executable committed blob required")
    require(isinstance(blob.data, bytes) and 0 < len(blob.data) <= MAX_SOURCE_BYTES, "Invalid source payload")
    require(re.fullmatch(r"[a-f0-9]{40}", blob.oid) is not None, "Git blob identifier required")
    calculated = hashlib.sha1(b"blob " + str(len(blob.data)).encode("ascii") + b"\0" + blob.data).hexdigest()
    require(calculated == blob.oid, "Git blob content does not match its identifier")


def asset_revision(files: Mapping[str, Blob]) -> str:
    digest = hashlib.sha256()
    # Matches c443 server.mjs assetSnapshot(): 14 sorted path+NUL+raw-byte pairs.
    for name in sorted(BROWSER_FILES):
        digest.update(name.encode("utf-8") + b"\0")
        digest.update(files[name].data)
    return digest.hexdigest()


def select_runtime(sources: Sources) -> dict[str, Blob]:
    require((sources.base_revision, sources.overlay_revision) == (BASE_REVISION, OVERLAY_REVISION),
            "Source revision override refused")
    validate_blob(sources.manifest)
    require(sha256(sources.manifest.data) == BASELINE_MANIFEST_SHA256, "Existing scoped-rollout manifest pin mismatch")
    manifest = json.loads(sources.manifest.data)
    require(manifest.get("schema") == 1 and manifest.get("runtimeRevision") == BASE_REVISION,
            "Unexpected baseline manifest")
    require(set(manifest.get("runtimeFiles", {})) == set(RUNTIME_FILES), "Baseline runtime allowlist changed")
    require(set(sources.base) == set(RUNTIME_FILES) and set(sources.target) == set(RUNTIME_FILES),
            "Only the 22 reviewed runtime sources are accepted")
    actual_differences: set[str] = set()
    for name in RUNTIME_FILES:
        before, after = sources.base[name], sources.target[name]
        validate_blob(before)
        validate_blob(after)
        require(sha256(before.data) == manifest["runtimeFiles"][name], "Base runtime content pin mismatch: " + name)
        if before.data != after.data:
            actual_differences.add(name)
        if name in OVERLAY_HASHES:
            require(sha256(after.data) == OVERLAY_HASHES[name], "Overlay content pin mismatch: " + name)
        elif name != "package.json":
            require(before == after, "Unexpected target runtime change: " + name)
    # Raw source diff is four files. The fourth is a known test-script-only
    # package.json change, audited here but deliberately excluded from output.
    require(actual_differences == set(OVERLAY_HASHES) | {"package.json"}, "Unexpected base/target difference set")
    require(sha256(sources.target["package.json"].data) == AUDIT_ONLY_TARGET_PACKAGE_SHA256,
            "Target package metadata pin mismatch")
    before_package = json.loads(sources.base["package.json"].data)
    after_package = json.loads(sources.target["package.json"].data)
    require(isinstance(before_package["scripts"]["test"], str) and isinstance(after_package["scripts"]["test"], str),
            "Expected package test scripts")
    after_package["scripts"]["test"] = before_package["scripts"]["test"]
    require(before_package == after_package, "Operational package metadata changed")
    selected = {name: sources.target[name] if name in OVERLAY_HASHES else sources.base[name] for name in RUNTIME_FILES}
    require(asset_revision(sources.base) == BASE_ASSET_REVISION, "Base browser asset revision mismatch")
    require(asset_revision(selected) == CANDIDATE_ASSET_REVISION, "Candidate browser asset revision mismatch")
    return selected


def archive_bytes(files: Mapping[str, Blob]) -> bytes:
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode="w", format=tarfile.USTAR_FORMAT) as archive:
        for name in sorted(files):
            blob = files[name]
            member = tarfile.TarInfo("runtime/" + name)
            member.size = len(blob.data)
            member.mode = 0o644
            member.uid = member.gid = member.mtime = 0
            member.uname = member.gname = ""
            archive.addfile(member, io.BytesIO(blob.data))
    data = raw.getvalue()
    # Canonical stored DEFLATE blocks avoid compression-version-dependent byte
    # changes. This is a standard gzip stream with no filename, timestamp or
    # platform field; the modest size increase buys cross-runtime reproducibility.
    compressed = bytearray(b"\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\xff")
    for position in range(0, len(data), 65535):
        block = data[position:position + 65535]
        final = position + len(block) == len(data)
        compressed.append(1 if final else 0)  # BFINAL, BTYPE=00; byte aligned.
        compressed.extend(struct.pack("<HH", len(block), len(block) ^ 0xFFFF))
        compressed.extend(block)
    compressed.extend(struct.pack("<II", zlib.crc32(data) & 0xFFFFFFFF, len(data) & 0xFFFFFFFF))
    return bytes(compressed)


def bundle(sources: Sources) -> dict[str, bytes]:
    files = select_runtime(sources)
    archive = archive_bytes(files)
    require(sha256(archive) == CANDIDATE_ARCHIVE_SHA256, "Canonical archive content pin mismatch")
    provenance = {
        "schema": 1,
        "kind": "red-xai-private-hub-ui-source-candidate",
        "baseRevision": BASE_REVISION,
        "overlayRevision": OVERLAY_REVISION,
        "sourceMethod": "exact-committed-git-blobs",
        "baselineRolloutManifestSha256": BASELINE_MANIFEST_SHA256,
        "overlayFiles": sorted(OVERLAY_HASHES),
        "preservedBaseFileCount": 19,
        "runtimeFileCount": 22,
        "rawSourceDifferenceFiles": sorted(set(OVERLAY_HASHES) | {"package.json"}),
        "targetPackageMetadata": {"difference": "test-script-only", "included": False,
                                  "sha256": AUDIT_ONLY_TARGET_PACKAGE_SHA256},
        "browserAssets": {"algorithm": "sha256(sorted(path + NUL + raw bytes))",
                          "fileCount": 14, "baseRevision": BASE_ASSET_REVISION,
                          "candidateRevision": CANDIDATE_ASSET_REVISION,
                          "oldRevisionRetentionAcrossRestartVerified": False},
        "runtimeArchive": {"name": "runtime.tar.gz", "sha256": sha256(archive), "size": len(archive),
                           "encoding": "ustar-gzip-stored-deflate-v1"},
        "runtimeFiles": {
            name: {"sourceRevision": OVERLAY_REVISION if name in OVERLAY_HASHES else BASE_REVISION,
                   "gitBlob": files[name].oid, "gitMode": files[name].mode,
                   "sha256": sha256(files[name].data), "size": len(files[name].data)}
            for name in sorted(files)
        },
        "excludedInputs": ["worktree-files", "node_modules", "environment", "credentials",
                           "application-data", "service-units", "tunnel-configuration"],
        "deployment": {"backendCodeChanged": False, "customEngineChanged": False,
                       "existingRuntimeModified": False, "privateRuntimeInstalled": False,
                       "servicesStartedStoppedOrRestarted": False, "publicRoutesChanged": False,
                       "credentialsOrPermissionsChanged": False, "applicationDataCopied": False,
                       "productionAcceptanceVerified": False, "standaloneDeployment": False},
    }
    metadata = canonical_json(provenance)
    checksums = (sha256(archive) + "  runtime.tar.gz\n" + sha256(metadata) + "  provenance.json\n").encode("ascii")
    return {"runtime.tar.gz": archive, "provenance.json": metadata, "SHA256SUMS": checksums}


def verify_bundle(payloads: Mapping[str, bytes], sources: Sources) -> None:
    # Recompute from pinned source; a self-consistent but edited manifest and
    # checksum file cannot bless changed source or archive metadata.
    require(set(payloads) == set(OUTPUT_FILES), "Unexpected artifact output files")
    expected = bundle(sources)
    require(all(payloads[name] == expected[name] for name in OUTPUT_FILES), "Artifact differs from pinned source candidate")
    with tarfile.open(fileobj=io.BytesIO(payloads["runtime.tar.gz"]), mode="r:gz") as archive:
        members = archive.getmembers()
        require([m.name for m in members] == ["runtime/" + name for name in sorted(RUNTIME_FILES)],
                "Unexpected archive members")
        for member in members:
            require(member.isfile() and not member.linkname and not member.pax_headers and
                    member.uid == member.gid == member.mtime == 0 and member.mode == 0o644 and
                    member.uname == member.gname == "", "Unexpected archive type or metadata")


def is_reparse(path: Path) -> bool:
    try:
        info = path.lstat()
    except FileNotFoundError:
        return False
    return stat.S_ISLNK(info.st_mode) or bool(getattr(info, "st_file_attributes", 0) &
                                           getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400))


def directory_identity(path: Path) -> tuple[int, int]:
    require(not is_reparse(path), "Symlink or reparse-point output path refused")
    info = path.lstat()
    require(stat.S_ISDIR(info.st_mode), "Output ancestor is not a directory")
    return info.st_dev, info.st_ino


def write_candidate(repository: Path, output_name: str, payloads: Mapping[str, bytes]) -> Path:
    require(OUTPUT_NAME.fullmatch(output_name) is not None, "Output name must be 1-63 lowercase letters, digits or hyphens")
    require(set(payloads) == set(OUTPUT_FILES) and all(isinstance(v, bytes) for v in payloads.values()),
            "Only the three candidate artifact files may be written")
    repository = repository.resolve(strict=True)
    parent = repository / ".task-tmp"
    try:
        parent.mkdir(mode=0o700)
    except FileExistsError:
        pass
    parent_identity = directory_identity(parent)
    destination = parent / output_name
    require(not os.path.lexists(destination), "Output already exists; it will not be replaced")
    # An already existing output is never reused, overwritten, removed, or
    # chmodded. On failure leave only this newly created candidate for review.
    destination.mkdir(mode=0o700)
    destination_identity = directory_identity(destination)

    def check_paths() -> None:
        require(directory_identity(parent) == parent_identity and
                directory_identity(destination) == destination_identity and
                parent.resolve(strict=True) == repository / ".task-tmp" and
                destination.resolve(strict=True).parent == parent,
                "Output ancestry changed; no rebinding or cleanup attempted")

    descriptor = None
    # POSIX supports descriptor-relative exclusive writes. Windows uses fresh
    # exclusive files plus repeated reparse/inode checks; this ordinary-user
    # packager is not a privileged Windows deployment/confinement primitive.
    if os.open in os.supports_dir_fd:
        descriptor = os.open(destination, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        info = os.fstat(descriptor)
        require((info.st_dev, info.st_ino) == destination_identity, "Output directory changed before open")
    try:
        for name in OUTPUT_FILES:
            check_paths()
            flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_BINARY", 0)
            file_descriptor = os.open(name if descriptor is not None else destination / name,
                                      flags, 0o600, **({"dir_fd": descriptor} if descriptor is not None else {}))
            with os.fdopen(file_descriptor, "wb") as stream:
                stream.write(payloads[name])
                stream.flush()
                os.fsync(stream.fileno())
            check_paths()
        if descriptor is not None:
            os.fsync(descriptor)
        check_paths()
        for name in OUTPUT_FILES:
            require(not is_reparse(destination / name) and (destination / name).read_bytes() == payloads[name],
                    "Written artifact readback mismatch")
        check_paths()
        return destination
    finally:
        if descriptor is not None:
            os.close(descriptor)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument("--repository", type=Path, default=Path(__file__).resolve().parents[2],
                        help="Exact local Git worktree root; no network fetch is performed")
    parser.add_argument("--output-name", required=True, help="Fresh directory name under that repository's .task-tmp")
    args = parser.parse_args()
    try:
        repository = repository_root(args.repository)
        sources = load_sources(repository)
        payloads = bundle(sources)
        verify_bundle(payloads, sources)
        destination = write_candidate(repository, args.output_name, payloads)
        receipt = {"ok": True, "outputDirectory": str(destination), "runtimeFileCount": 22,
                   "baseFileCount": 19, "overlayFileCount": 3,
                   "baseBrowserAssetRevision": BASE_ASSET_REVISION,
                   "candidateBrowserAssetRevision": CANDIDATE_ASSET_REVISION,
                   "files": {name: {"size": len(payloads[name]), "sha256": sha256(payloads[name])}
                             for name in OUTPUT_FILES},
                   "deployed": False, "servicesChanged": False, "credentialsRead": False}
        print(json.dumps(receipt, sort_keys=True))
        return 0
    except (Refusal, OSError, ValueError, subprocess.SubprocessError):
        # Never echo Git diagnostics, host configuration, or file contents.
        print(json.dumps({"ok": False, "error": "candidate_preparation_refused", "deployed": False}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
