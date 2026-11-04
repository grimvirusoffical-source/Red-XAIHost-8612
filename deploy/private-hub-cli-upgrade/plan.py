#!/usr/bin/env python3
"""Print an offline, non-executable proposal for one pinned Hub UI upgrade."""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import sys
import tarfile

MANIFEST_SHA256 = "ee1770a2614a41156937c3d33a6430b6771a37801739a5354f12147e82917efd"
BROWSER_FILES = ("index.html", "app.js", "styles.css", "catalog.json", "public-secrets-docs.json",
                 "cli-installation.md", "site/api.js", "site/auth.js", "site/catalog.js", "site/dom.js",
                 "site/editor.js", "site/legal.js", "site/pages.js", "site/session.js")


class Refusal(ValueError):
    pass


def require(ok, message):
    if not ok:
        raise Refusal(message)


def exact(value, fields):
    require(type(value) is dict and set(value) == set(fields), "Unexpected evidence fields")
    return value


def json_object(data):
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, "Duplicate JSON field")
            result[key] = value
        return result
    value = json.loads(data, object_pairs_hook=pairs)
    require(type(value) is dict, "JSON object required")
    return value


def sha(data):
    return hashlib.sha256(data).hexdigest()


def read_input(path, maximum, size=None):
    before = path.lstat()
    require(stat.S_ISREG(before.st_mode) and not stat.S_ISLNK(before.st_mode) and
            not getattr(before, "st_file_attributes", 0) & 0x400, "Regular non-reparse review input required")
    require(0 < before.st_size <= maximum and (size is None or before.st_size == size), "Review input size mismatch")
    fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_BINARY", 0))
    with os.fdopen(fd, "rb") as stream:
        opened = os.fstat(stream.fileno())
        require((opened.st_dev, opened.st_ino) == (before.st_dev, before.st_ino), "Review input changed")
        data = stream.read(maximum + 1)
    after = path.lstat()
    require((after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns) ==
            (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns) and len(data) == before.st_size,
            "Review input changed")
    return data


def manifest():
    data = read_input(Path(__file__).with_name("manifest.json"), 65536)
    require(sha(data) == MANIFEST_SHA256, "Upgrade manifest pin mismatch")
    return json_object(data)


def artifacts(directory, plan):
    require(directory.is_dir() and not directory.is_symlink() and
            not getattr(directory.lstat(), "st_file_attributes", 0) & 0x400, "Candidate directory required")
    payloads = {}
    for name, pin in plan["artifacts"].items():
        payloads[name] = read_input(directory / name, pin["size"], pin["size"])
        require(sha(payloads[name]) == pin["sha256"], "Candidate artifact pin mismatch")
    proof = json_object(payloads["provenance.json"])
    for field in ("baseRevision", "overlayRevision", "runtimeFiles", "overlayFiles", "browserAssets", "preservedBaseFileCount"):
        require(proof[field] == plan[field], "Candidate provenance mismatch")
    require(len(proof["runtimeFiles"]) == 22 and all(value is False for value in proof["deployment"].values()),
            "Exact source-only mixed candidate required")
    runtime = {}
    with tarfile.open(fileobj=io.BytesIO(payloads["runtime.tar.gz"]), mode="r:gz") as archive:
        members = archive.getmembers()
        require([m.name for m in members] == ["runtime/" + name for name in sorted(plan["runtimeFiles"])],
                "Unexpected archive members")
        for item in members:
            require(item.isfile() and not item.linkname and not item.pax_headers and item.mode == 0o644 and
                    item.uid == item.gid == item.mtime == 0 and item.uname == item.gname == "", "Unexpected archive metadata")
            name = item.name.removeprefix("runtime/")
            pin = plan["runtimeFiles"][name]
            value = archive.extractfile(item).read(pin["size"] + 1)
            require(len(value) == pin["size"] and sha(value) == pin["sha256"], "Runtime content mismatch")
            require(pin["sourceRevision"] == (plan["overlayRevision"] if name in plan["overlayFiles"] else plan["baseRevision"]),
                    "Mixed source attribution mismatch")
            runtime[name] = value
    h = hashlib.sha256()
    for name in sorted(BROWSER_FILES):
        h.update(name.encode() + b"\0")
        h.update(runtime[name])
    require(h.hexdigest() == plan["browserAssets"]["candidateRevision"], "Candidate asset revision mismatch")
    return {"archiveSha256": plan["artifacts"]["runtime.tar.gz"]["sha256"], "runtimeFilesVerified": 22,
            "preservedBaseFiles": 19, "overlayFiles": 3, "candidateAssetRevision": h.hexdigest(),
            "extractedToFilesystem": False}


def path_string(value):
    require(type(value) is str and re.fullmatch(r"/[A-Za-z0-9][A-Za-z0-9._/-]{0,300}", value) is not None and
            str(PurePosixPath(value)) == value and ".." not in PurePosixPath(value).parts and "UNVERIFIED" not in value,
            "Canonical absolute Linux metadata path required")
    return value


def number(value, zero=False):
    require(type(value) is int and (value >= 0 if zero else value > 0) and value < 2 ** 64, "Invalid numeric metadata")


def hash_string(value):
    require(type(value) is str and re.fullmatch(r"[a-f0-9]{64}", value) is not None, "SHA-256 metadata required")


def identity(value, kind, uid, gid, modes, hashed=False):
    fields = {"path", "kind", "device", "inode", "uid", "gid", "mode", "size", "mtimeNs", "symlink"}
    item = exact(value, fields | ({"sha256"} if hashed else set()))
    path_string(item["path"])
    for key in ("device", "uid", "gid", "size"):
        number(item[key], zero=True)
    for key in ("inode", "mtimeNs"):
        number(item[key])
    require(item["kind"] == kind and item["uid"] == uid and item["gid"] == gid and item["mode"] in modes and
            item["symlink"] is False and (kind != "regular" or item["size"] > 0), "Protected identity mismatch")
    if hashed:
        hash_string(item["sha256"])
    return item["path"]


def evidence(value, plan, now):
    exact(value, {"schema", *plan["requiredEvidence"]})
    require(type(value["schema"]) is int and value["schema"] == 1 and type(value["observedAt"]) is str and len(value["observedAt"]) <= 40,
            "Fresh observation required")
    observed = datetime.fromisoformat(value["observedAt"].replace("Z", "+00:00"))
    require(observed.tzinfo is not None and observed.utcoffset().total_seconds() == 0 and now.tzinfo is not None,
            "UTC observation required")
    age = (now - observed).total_seconds()
    require(-plan["evidencePolicy"]["futureClockSkewSeconds"] <= age <= plan["evidencePolicy"]["maxAgeSeconds"],
            "Stale or future evidence")
    u = exact(value["unit"], {"name", "type", "activeState", "mainPid", "user", "group", "workingDirectory",
                              "execStart", "effectivePropertiesSha256", "fragment", "dropIns", "newDropInAbsent"})
    target = plan["unit"]
    require(u["name"] == target["name"] and u["type"] == "simple" and u["activeState"] == "active" and
            u["user"] == target["requiredUser"] and u["group"] == target["requiredGroup"] and
            u["workingDirectory"] == target["baseWorkingDirectory"] and u["newDropInAbsent"] is True,
            "Reviewed simple active baseline required")
    baseline = exact(value["currentRuntime"], {"directory", "runtimeFiles", "browserAssetRevision"})
    require(identity(baseline["directory"], "directory", 0, 0, {"0755"}) == target["baseWorkingDirectory"] and
            baseline["runtimeFiles"] == plan["baselineRuntimeFiles"] and
            baseline["browserAssetRevision"] == plan["browserAssets"]["baseRevision"],
            "Observed baseline runtime pins mismatch")
    number(u["mainPid"])
    hash_string(u["effectivePropertiesSha256"])
    fragment = identity(u["fragment"], "regular", 0, 0, {"0600", "0644"}, True)
    require(fragment in {prefix + target["name"] for prefix in ("/etc/systemd/system/", "/usr/lib/systemd/system/", "/lib/systemd/system/")},
            "Unexpected unit fragment")
    require(type(u["dropIns"]) is list and len(u["dropIns"]) <= 20, "Drop-in inventory required")
    drop_names = []
    for item in u["dropIns"]:
        path = PurePosixPath(identity(item, "regular", 0, 0, {"0600", "0644"}, True))
        require(str(path.parent) in {prefix + target["name"] + ".d" for prefix in
                                    ("/etc/systemd/system/", "/run/systemd/system/", "/usr/lib/systemd/system/", "/lib/systemd/system/")} and
                re.fullmatch(r"[A-Za-z0-9_.-]+\.conf", path.name) is not None, "Unexpected existing drop-in")
        drop_names.append(path.name)
    require(drop_names == sorted(set(drop_names)) and all(name < PurePosixPath(target["newDropInPath"]).name for name in drop_names),
            "Drop-in precedence requires separate review")
    uid, gid, pid = target["requiredUid"], target["requiredGid"], u["mainPid"]
    p = exact(value["process"], {"pid", "startTicks", "uids", "gids"})
    require(type(p["pid"]) is int and p["pid"] == pid and p["uids"] == [uid] * 4 and p["gids"] == [gid] * 4 and
            all(type(n) is int for n in p["uids"] + p["gids"]), "Process identity mismatch")
    number(p["startTicks"])
    n = exact(value["nodeExecutable"], {"identity", "version"})
    node = identity(n["identity"], "regular", 0, 0, {"0755"}, True)
    version = re.fullmatch(r"v?([0-9]+)\.([0-9]+)\.([0-9]+)", n["version"] if type(n["version"]) is str else "")
    require(PurePosixPath(node).name == "node" and version is not None and tuple(map(int, version.groups())) >= (20, 9, 0),
            "Reviewed Node executable/version required")
    require(type(u["execStart"]) is list and len(u["execStart"]) == 2 and u["execStart"][0] == node and
            u["execStart"][1] in {"server.mjs", target["baseWorkingDirectory"] + "/server.mjs"},
            "Unsupported ExecStart requires separate review")
    c = exact(value["configuration"], {"environmentFiles", "cmsDatabase", "cmsBoxPrefix", "cmsDurability", "cmsSingleWriter", "keyFile",
                                       "supportDirectory", "mediaDirectory", "auditFile", "writerLock", "writerLockPid", "valuesOmitted"})
    require(c["cmsDatabase"] == "website_cms" and c["cmsBoxPrefix"] == "site-cms.red-xai.db" and c["cmsDurability"] in {"sync", "group"} and
            c["cmsSingleWriter"] is True and c["valuesOmitted"] is True, "Preserve existing CMS declarations; omit secret values")
    require(type(c["environmentFiles"]) is list and 1 <= len(c["environmentFiles"]) <= 8, "Environment-file identity inventory required")
    env_paths = []
    for item in c["environmentFiles"]:
        require(type(item) is dict and item.get("uid") in {0, uid}, "Protected environment-file owner required")
        env = identity(item, "regular", item["uid"], 0 if item["uid"] == 0 else gid, {"0600"}, True)
        require(env.startswith("/etc/red-xai/"), "Reviewed environment path required")
        env_paths.append(env)
    require(len(env_paths) == len(set(env_paths)), "Duplicate environment file")
    require(identity(c["keyFile"], "regular", uid, gid, {"0600"}, True).startswith("/etc/red-xai/"), "Reviewed CMS key identity required")
    support = identity(c["supportDirectory"], "directory", uid, gid, {"0700"})
    require(support == "/var/lib/red-xai-site" or support.startswith(("/srv/red-xai/", "/var/lib/red-xai/")),
            "Reviewed CMS support path required")
    for field, suffix, kind, mode in (("mediaDirectory", "/media", "directory", "0700"),
                                     ("auditFile", "/audit.jsonl", "regular", "0600"), ("writerLock", "/writer.lock", "regular", "0600")):
        require(identity(c[field], kind, uid, gid, {mode}) == support + suffix, "CMS support linkage mismatch")
    require(type(c["writerLockPid"]) is int and c["writerLockPid"] == pid, "Writer-lock owner mismatch")
    listener = exact(value["listener"], {"address", "port", "pid"})
    require(listener == {"address": "127.0.0.1", "port": 14210, "pid": pid} and
            type(listener["port"]) is int and type(listener["pid"]) is int, "Listener mismatch")
    routing = exact(value["routing"], {"exposure", "configuration", "inventorySha256"})
    require(routing["exposure"] in {"private-loopback-only", "public-routed"}, "Exposure is unverified")
    hash_string(routing["inventorySha256"])
    require(identity(routing["configuration"], "regular", 0, 0, {"0600"}, True) == "/etc/cloudflared/config.yml",
            "Tunnel configuration requires separate review")
    release = exact(value["newRelease"], {"parent", "directoryAbsent"})
    require(identity(release["parent"], "directory", 0, 0, {"0755"}) == "/opt" and release["directoryAbsent"] is True,
            "Fresh separate immutable release required")
    bound = (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True) + "\n").encode("ascii")
    return {"observedAt": value["observedAt"], "ageSecondsAtPlanning": round(age, 3), "nodePath": node,
            "observationSha256": sha(bound), "observationHashEncoding": "sorted-compact-ascii-json-newline",
            "exposure": routing["exposure"], "authenticityVerified": False, "liveIdentitiesRechecked": False}


def proposal(checked_artifacts, plan, observations=None, now=None):
    result = {"schema": 1, "kind": plan["kind"], "sourceOnly": True, "readyForDeployment": False,
              "artifactValidation": checked_artifacts, "operatorEvidenceStatus": "UNVERIFIED", "unitProposal": None,
              "requiredEvidence": plan["requiredEvidence"], "futureGates": plan["futureGates"], "preserve": plan["preserve"],
              "forbidden": plan["forbidden"], "liveConfigurationRead": False, "credentialFilesOpened": False,
              "serviceCommandsExecuted": False, "filesWritten": False, "approvalVerified": False,
              "backupRestoreProofVerified": False, "productionAcceptanceVerified": False}
    if observations is None:
        return result
    checked = evidence(observations, plan, now or datetime.now(timezone.utc))
    u = plan["unit"]
    argv = [checked["nodePath"], u["newWorkingDirectory"] + "/server.mjs"]
    result.update(operatorEvidenceStatus="FRESH_DECLARATIONS_ONLY_NOT_LIVE_VERIFIED", evidenceValidation=checked)
    result["unitProposal"] = {"unitName": u["name"], "proposedNewDropInPath": u["newDropInPath"],
        "changes": [{"property": "WorkingDirectory", "before": observations["unit"]["workingDirectory"], "after": u["newWorkingDirectory"]},
                    {"property": "ExecStart", "before": observations["unit"]["execStart"], "after": argv}],
        "previewOnly": "[Service]\nWorkingDirectory=" + u["newWorkingDirectory"] + "\nExecStart=\nExecStart=" + " ".join(argv) + "\n",
        "effectivePropertyAllowlist": u["propertyChangeAllowlist"], "allOtherPropertiesMustBeComparedBeforeUse": True,
        "environmentAndStatePathsChanged": False, "actualUnitOrDropInWritten": False}
    result["applicableApproval"] = ("fresh action-time public-service restart approval plus separately reviewed runtime/routing pins"
                                     if checked["exposure"] == "public-routed" else
                                     "reviewed private maintenance; recheck actual exposure at action time")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument("--artifact-directory", required=True, type=Path)
    parser.add_argument("--evidence", type=Path, help="Sanitized operator-evidence.json; paths within it are never opened")
    args = parser.parse_args()
    try:
        plan = manifest()
        checked = artifacts(args.artifact_directory, plan)
        observations = None
        if args.evidence is not None:
            require(args.evidence.name == "operator-evidence.json", "Deliberately prepared operator-evidence.json required")
            observations = json_object(read_input(args.evidence, 65536))
        print(json.dumps(proposal(checked, plan, observations), sort_keys=True, indent=2))
        return 0
    except (Refusal, OSError, ValueError, TypeError, KeyError, RecursionError, tarfile.TarError):
        print(json.dumps({"ok": False, "error": "offline_upgrade_proposal_refused", "sourceOnly": True,
                          "readyForDeployment": False, "filesWritten": False}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
