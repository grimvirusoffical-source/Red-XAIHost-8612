#!/usr/bin/env python3
"""Stage one reviewed public Host origin-index change; apply only with --apply.

Reads only the specified public index, pinned public asset, backup and stage.
Never reads credentials, configuration, environment or cookies.
The caller must independently inspect/hash the actual origin index first.
"""
import argparse
import hashlib
from html.parser import HTMLParser
import json
import os
from pathlib import Path
import re
import stat
import sys

OLD_NAME = "index-kxtL07zC.js"
NEW_NAME = "index-no-vendor-dc980958bb5f.js"
EXPECTED_ASSET_SHA256 = "dc980958bb5f1fdb4691033bfaddb7e0e5b86cfcb84e3f13fd94fef60c3cd9f8"
COLLECTOR = "https://r.lilstts.com/events"
QUICK = "/redxai-connect/ux.js"
SCRIPT = re.compile(r"<script\b[^>]*>\s*</script\s*>", re.I)


def digest(data):
    return hashlib.sha256(data).hexdigest()


class Attributes(HTMLParser):
    def __init__(self, tag):
        super().__init__(convert_charrefs=False)
        self.values = None
        self.feed(tag)

    def handle_starttag(self, tag, attrs):
        if tag != "script" or self.values is not None:
            raise ValueError("Unexpected script element")
        names = [name for name, _ in attrs]
        if len(names) != len(set(names)):
            raise ValueError("Duplicate script attribute")
        self.values = dict(attrs)


def transform(data, expected_sha, old_src):
    if not re.fullmatch(r"[0-9a-f]{64}", expected_sha) or digest(data) != expected_sha:
        raise ValueError("Origin digest mismatch")
    if old_src not in ("/assets/" + OLD_NAME, "/host/assets/" + OLD_NAME):
        raise ValueError("Unexpected original asset path")
    html = data.decode("utf-8")
    if len(data) > 2 * 1024 * 1024:
        raise ValueError("Unexpected index size")
    tags = [(match.group(0), Attributes(match.group(0)).values) for match in SCRIPT.finditer(html)]
    asset = [tag for tag, attrs in tags if attrs.get("src") == old_src and attrs.get("type") == "module"]
    analytics = [tag for tag, attrs in tags if attrs.get("src") == "/analytics-consent.js" and attrs.get("data-url") == COLLECTOR]
    quick = [tag for tag, attrs in tags if attrs.get("src") == QUICK]
    if len(asset) != 1 or html.count(old_src) != 1 or len(analytics) != 1 or html.count("/analytics-consent.js") != 1 or len(quick) != 1:
        raise ValueError("Exact origin script set mismatch")
    new_src = old_src.removesuffix(OLD_NAME) + NEW_NAME
    output = html.replace(asset[0], asset[0].replace(old_src, new_src), 1).replace(analytics[0], "", 1)
    if output.count(quick[0]) != 1 or old_src in output or "/analytics-consent.js" in output:
        raise ValueError("Script preservation mismatch")
    return output.encode("utf-8"), {
        "inputSha256": digest(data), "outputSha256": digest(output.encode("utf-8")),
        "oldAsset": old_src, "newAsset": new_src, "assetReferencesChanged": 1,
        "analyticsLoadersRemoved": 1, "quickLoginScriptPreserved": True,
    }


def fingerprint(metadata):
    return (metadata.st_dev, metadata.st_ino, metadata.st_size, metadata.st_mtime_ns,
            stat.S_IMODE(metadata.st_mode), metadata.st_uid, metadata.st_gid)


def read_regular(path):
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1:
        raise ValueError("Single regular index file required")
    descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    with os.fdopen(descriptor, "rb") as source:
        if fingerprint(os.fstat(source.fileno())) != fingerprint(before):
            raise ValueError("Index identity changed")
        data = source.read(2 * 1024 * 1024 + 1)
    if fingerprint(path.lstat()) != fingerprint(before):
        raise ValueError("Index changed while reading")
    return data, before


def exclusive(path, data):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
    with os.fdopen(descriptor, "wb") as destination:
        destination.write(data)
        destination.flush()
        os.fsync(destination.fileno())


def sync_directory(path):
    if sys.platform != "linux":
        return False
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    return True


def stage(index, expected_sha, old_src, backup, staged, apply=False, progress=None):
    progress = progress if progress is not None else {}
    index, backup, staged = (Path(value).absolute() for value in (index, backup, staged))
    for value in (index.parent, backup.parent, staged.parent):
        if value.resolve(strict=True) != value:
            raise ValueError("Existing canonical parent directories required")
    if index == backup or index == staged or backup == staged or staged.parent != index.parent:
        raise ValueError("Separate backup and same-directory stage required")
    if backup.parent == index.parent or index.parent in backup.parent.parents:
        raise ValueError("Backup must be outside the public origin directory")
    if backup.exists() or backup.is_symlink() or staged.exists() or staged.is_symlink():
        raise ValueError("Exclusive new backup and stage paths required")
    if apply and sys.platform != "linux":
        raise ValueError("Atomic application is Linux-only")
    original, metadata = read_regular(index)
    new_asset = index.parent / "assets" / NEW_NAME
    if new_asset.parent.resolve(strict=True) != new_asset.parent:
        raise ValueError("Canonical public asset directory required")
    asset_bytes, asset_metadata = read_regular(new_asset)
    if digest(asset_bytes) != EXPECTED_ASSET_SHA256 or stat.S_IMODE(asset_metadata.st_mode) & 0o444 != 0o444:
        raise ValueError("Pinned readable public asset required")
    original_xattrs = ({name: os.getxattr(index, name, follow_symlinks=False)
                       for name in os.listxattr(index, follow_symlinks=False)}
                      if sys.platform == "linux" else {})
    output, report = transform(original, expected_sha, old_src)
    exclusive(backup, original)
    progress["backupCreated"] = True
    sync_directory(backup.parent)
    exclusive(staged, output)
    progress["stageCreated"] = True
    sync_directory(staged.parent)
    saved, _ = read_regular(backup)
    candidate, _ = read_regular(staged)
    current, current_metadata = read_regular(index)
    if saved != original or candidate != output or current != original or fingerprint(current_metadata) != fingerprint(metadata):
        raise ValueError("Backup, stage or unchanged-origin comparison failed")
    if apply:
        # Preserve origin ownership, access mode and extended access metadata.
        # Refuse if any metadata cannot be preserved; the original stays active.
        if os.geteuid() == 0:
            os.chown(staged, metadata.st_uid, metadata.st_gid, follow_symlinks=False)
        elif staged.stat().st_uid != metadata.st_uid or staged.stat().st_gid != metadata.st_gid:
            raise ValueError("Origin ownership cannot be preserved")
        os.chmod(staged, stat.S_IMODE(metadata.st_mode), follow_symlinks=False)
        for name in os.listxattr(staged, follow_symlinks=False):
            if name not in original_xattrs:
                os.removexattr(staged, name, follow_symlinks=False)
        for name, value in original_xattrs.items():
            os.setxattr(staged, name, value, follow_symlinks=False)
        descriptor = os.open(staged, os.O_RDONLY | os.O_NOFOLLOW)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)
        current, current_metadata = read_regular(index)
        if current != original or fingerprint(current_metadata) != fingerprint(metadata):
            raise ValueError("Origin changed before atomic application")
        current_xattrs = {name: os.getxattr(index, name, follow_symlinks=False)
                         for name in os.listxattr(index, follow_symlinks=False)}
        if current_xattrs != original_xattrs:
            raise ValueError("Origin access metadata changed before application")
        final_asset, final_asset_metadata = read_regular(new_asset)
        if final_asset != asset_bytes or fingerprint(final_asset_metadata) != fingerprint(asset_metadata):
            raise ValueError("Reviewed public asset changed before application")
        final_candidate, final_metadata = read_regular(staged)
        candidate_xattrs = {name: os.getxattr(staged, name, follow_symlinks=False)
                            for name in os.listxattr(staged, follow_symlinks=False)}
        if (final_candidate != output or final_metadata.st_uid != metadata.st_uid
                or final_metadata.st_gid != metadata.st_gid
                or stat.S_IMODE(final_metadata.st_mode) != stat.S_IMODE(metadata.st_mode)
                or candidate_xattrs != original_xattrs):
            raise ValueError("Reviewed stage or access metadata changed before application")
        os.replace(staged, index)
        progress["applied"] = True
        sync_directory(index.parent)
        final, _ = read_regular(index)
        if final != output:
            raise ValueError("Applied index verification failed")
    return {"ok": True, **report, **progress, "applied": bool(progress.get("applied")),
            "backupVerified": True, "stageVerified": True, "originalMode": oct(stat.S_IMODE(metadata.st_mode)),
            "originalUid": metadata.st_uid, "originalGid": metadata.st_gid,
            "directorySyncSupported": sys.platform == "linux", "newAssetVerified": True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("index")
    parser.add_argument("expected_sha256")
    parser.add_argument("old_src")
    parser.add_argument("backup")
    parser.add_argument("staged")
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    progress = {"backupCreated": False, "stageCreated": False, "applied": False}
    try:
        result = stage(args.index, args.expected_sha256, args.old_src, args.backup, args.staged, args.apply, progress)
    except Exception:
        print(json.dumps({"ok": False, **progress, "error": "origin_index_patch_refused"}))
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
