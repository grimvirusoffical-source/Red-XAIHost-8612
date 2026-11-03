#!/usr/bin/env python3
"""Bounded Linux metadata inventory. No database reads or service mutations.

Prints JSON to stdout only. This is NOT a writer/quiescence or backup proof.
Use the separately reviewed notes; never substitute this for maintenance planning.
"""
import argparse
from datetime import datetime, timezone
import errno
import hashlib
import json
import os
import posixpath
import pwd
import re
import stat
import subprocess
import sys
import time

DATA = "/opt/redx/var/data"
ACCOUNT = DATA + "/dbs/redx_accounts"
EXTENSION = ACCOUNT + "/gendragoon-extension"
ANCHORS = ("/", "/opt", "/opt/redx", "/opt/redx/var", DATA,
           DATA + "/dbs", ACCOUNT, EXTENSION)
KNOWN_UNITS = ("redx-redxd.service", "redx-panel.service", "redx-hub.service",
               "redx-host-website.service", "redx-db-website.service",
               "cloudflared-redx.service")
PROPERTIES = ("LoadState", "ActiveState", "SubState", "MainPID", "ControlPID",
              "User", "Group", "DynamicUser", "ControlGroup", "FragmentPath",
              "Requires", "Wants", "Requisite", "BindsTo", "PartOf", "Before",
              "After", "RequiresMountsFor", "Triggers", "TriggeredBy")
SAFE_ENV = {"PATH": "/usr/bin:/bin", "LANG": "C", "LC_ALL": "C",
            "SYSTEMD_PAGER": "cat", "SYSTEMD_COLORS": "0"}
MAX_SECONDS, MAX_PIDS, MAX_FDS, MAX_TOTAL_FDS = 45, 2048, 512, 32768
MAX_CANDIDATES, MAX_MATCHES, MAX_MOUNTS, MAX_DISCOVERED_UNITS = 96, 64, 32, 8
UNIT_RE = re.compile(r"[A-Za-z0-9_.@:\\-]{1,180}\.service\Z")


def read_limited(path, limit):
    # Only callers' fixed procfs metadata paths; never a data/config/key file.
    with open(path, "rb") as handle:
        value = handle.read(limit + 1)
    if len(value) > limit:
        raise OSError(errno.EFBIG, "metadata limit")
    return value.decode("utf-8", "replace")


def beneath(value, root):
    return value == root or value.startswith(root + "/")


def path_class(value):
    if not value.startswith("/"):
        return None
    value = posixpath.normpath(value.removesuffix(" (deleted)"))
    for root, label in ((EXTENSION, "gendragoon-extension"),
                        (ACCOUNT, "accounts-namespace"), (DATA, "native-data-other")):
        if beneath(value, root):
            return label
    return None


def identity(path, follow=True):
    try:
        value = os.stat(path, follow_symlinks=follow)
        return {"device": "%d:%d" % (os.major(value.st_dev), os.minor(value.st_dev)),
                "inode": value.st_ino, "mode": format(stat.S_IMODE(value.st_mode), "04o"),
                "uid": value.st_uid, "gid": value.st_gid,
                "type": ("directory" if stat.S_ISDIR(value.st_mode) else
                         "regular" if stat.S_ISREG(value.st_mode) else
                         "symlink" if stat.S_ISLNK(value.st_mode) else "other")}
    except OSError as error:
        return {"unavailableErrno": error.errno}


def parse_mounts(text):
    rows = []
    for line in text.splitlines():
        left, sep, right = line.partition(" - ")
        fields, tail = left.split(), right.split()
        if not sep or len(fields) < 6 or len(tail) < 3:
            continue
        unescape = lambda value: re.sub(r"\\([0-7]{3})", lambda m: chr(int(m[1], 8)), value)
        # Do NOT retain mount source, optional fields, or super/mount options.
        rows.append({"mountId": int(fields[0]), "parentId": int(fields[1]),
                     "device": fields[2], "root": unescape(fields[3]),
                     "mountpoint": unescape(fields[4]), "filesystem": tail[0],
                     "readOnly": "ro" in fields[5].split(",")})
    return rows


def relevant_mounts(rows):
    # Containing mounts and descendants only; longest containing mount is decisive.
    selected = {}
    for target in (DATA, ACCOUNT, EXTENSION):
        parents = [row for row in rows if beneath(target, row["mountpoint"].rstrip("/") or "/")
                   or row["mountpoint"] == "/"]
        if parents:
            winner = max(parents, key=lambda row: len(row["mountpoint"]))
            selected[winner["mountId"]] = winner
    for row in rows:
        if beneath(row["mountpoint"], DATA):
            selected[row["mountId"]] = row
    result = sorted(selected.values(), key=lambda row: row["mountId"])
    safe = []
    for row in result[:MAX_MOUNTS]:
        # Bind roots and unexpected mountpoint names can contain private identifiers.
        safe.append({key: row[key] for key in ("mountId", "parentId", "device", "filesystem", "readOnly")})
        safe[-1].update({
            "mountpointFixedAnchor": row["mountpoint"] if row["mountpoint"] in ANCHORS else None,
            "mountpointSha256": hashlib.sha256(row["mountpoint"].encode()).hexdigest(),
            "mountRootSha256": hashlib.sha256(row["root"].encode()).hexdigest(),
            "mountRootIsFilesystemRoot": row["root"] == "/",
            "containsFixedTargets": [target for target in (DATA, ACCOUNT, EXTENSION)
                                     if row["mountpoint"] == "/" or beneath(target, row["mountpoint"])],
            "nestedWithinNativeData": beneath(row["mountpoint"], DATA)})
    return {"rows": safe, "truncated": len(result) > MAX_MOUNTS}


def safe_process_path(value):
    clean = value.removesuffix(" (deleted)")
    return {"fixedAnchor": clean if clean in ANCHORS else None,
            "pathClass": path_class(clean) or "outside-native-data",
            "pathSha256": hashlib.sha256(clean.encode()).hexdigest(),
            "exactExpectedNativeExecutable": clean == "/opt/redx/bin/redxd",
            "deletedSuffix": value.endswith(" (deleted)")}


def service_info(unit, deadline):
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        return {"unit": unit, "unavailable": "time-budget"}
    try:
        result = subprocess.run(
            ["/usr/bin/systemctl", "show", "--no-pager", "--property=" + ",".join(PROPERTIES), "--", unit],
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            env=SAFE_ENV, timeout=min(2, remaining), check=False)
        if len(result.stdout) > 65536:
            return {"unit": unit, "unavailable": "property-output-limit"}
        values = dict(line.split("=", 1) for line in result.stdout.decode("utf-8", "replace").splitlines()
                      if "=" in line)
        return {"unit": unit, "exitCode": result.returncode,
                "properties": {key: values[key] for key in PROPERTIES if key in values}}
    except (OSError, subprocess.TimeoutExpired):
        return {"unit": unit, "unavailable": "systemctl-failed-or-timed-out"}


def fd_mode(text):
    values = dict(line.split(":", 1) for line in text.splitlines() if ":" in line)
    flags = int(values["flags"].strip(), 8)
    mode = flags & os.O_ACCMODE
    return {"access": "path-only" if flags & getattr(os, "O_PATH", 0) else
            {os.O_RDONLY: "read-only", os.O_WRONLY: "write-only", os.O_RDWR: "read-write"}.get(mode, "unknown"),
            "mountId": int(values["mnt_id"].strip()) if "mnt_id" in values else None}


def process_brief(pid):
    root = "/proc/" + str(pid)
    text = read_limited(root + "/status", 16384)
    fields = dict(line.split(":", 1) for line in text.splitlines() if ":" in line)
    row = {"pid": pid, "ppid": int(fields["PPid"].strip()),
           "uids": [int(value) for value in fields["Uid"].split()],
           "gids": [int(value) for value in fields["Gid"].split()]}
    # Field 22, after the last ')' of the possibly space-containing process name.
    ticks = read_limited(root + "/stat", 8192).rsplit(")", 1)[1].split()[19]
    row["startTicks"] = int(ticks)
    units = []
    try:
        for line in read_limited(root + "/cgroup", 16384).splitlines():
            for component in line.split(":", 2)[-1].split("/"):
                if UNIT_RE.fullmatch(component) and component not in units:
                    units.append(component)
    except OSError:
        row["cgroupUnavailable"] = True
    row["cgroupUnits"] = units[:8]
    for name in ("exe", "cwd", "ns/mnt"):
        try:
            row[name.replace("/", "_")] = os.readlink(root + "/" + name)[:1024]
        except OSError as error:
            row[name.replace("/", "_")] = {"unavailableErrno": error.errno}
    return row


def inspect_process(pid, known_pids, relevant_uids, counters, deadline):
    base = "/proc/" + str(pid)
    row = process_brief(pid)
    matches, maps = [], []
    reasons = []
    if pid in known_pids:
        reasons.append("known-unit-main-or-control-pid")
    if set(row["uids"]) & relevant_uids:
        reasons.append("redx-or-redx-gdg-uid")
    for name in ("exe", "cwd"):
        value = row[name]
        if isinstance(value, str) and (path_class(value) or beneath(value, "/opt/redx")):
            reasons.append(name + "-under-reviewed-prefix")
    fd_count = 0
    try:
        with os.scandir(base + "/fd") as entries:
            for entry in entries:
                if not entry.name.isdecimal():
                    continue
                if fd_count >= MAX_FDS or counters["fdLinksExamined"] >= MAX_TOTAL_FDS or time.monotonic() >= deadline:
                    counters["fdScanTruncatedProcesses"] += 1
                    break
                fd_count += 1
                counters["fdLinksExamined"] += 1
                try:
                    category = path_class(os.readlink(entry.path))
                    if category:
                        if len(matches) >= MAX_MATCHES:
                            counters["matchingFdOutputTruncatedProcesses"] += 1
                            break
                        # Do not expose target names, relative paths, positions or file contents.
                        item = {"fd": int(entry.name), "pathClass": category,
                                "object": identity(entry.path)}
                        try:
                            item.update(fd_mode(read_limited(base + "/fdinfo/" + entry.name, 4096)))
                        except (OSError, ValueError, KeyError):
                            item["access"] = "unknown"
                        matches.append(item)
                except OSError:
                    counters["fdLinkRacesOrDenied"] += 1
    except OSError:
        counters["fdDirectoriesUnavailable"] += 1
    try:
        seen = set()
        for line in read_limited(base + "/maps", 2 * 1024 * 1024).splitlines():
            fields = line.split(None, 5)
            if len(fields) < 6:
                continue
            category = path_class(fields[5])
            key = (category, fields[1], fields[3], fields[4])
            if category and key not in seen:
                seen.add(key)
                if len(maps) >= MAX_MATCHES:
                    counters["matchingMapOutputTruncatedProcesses"] += 1
                    break
                maps.append({"pathClass": category, "permissions": fields[1],
                             "deviceHex": fields[3], "inode": int(fields[4]),
                             "sharedWritable": "w" in fields[1] and "s" in fields[1]})
    except (OSError, ValueError):
        counters["mapsUnavailableOrTooLarge"] += 1
    if matches:
        reasons.append("path-matched-open-fd")
    if maps:
        reasons.append("path-matched-file-mapping")
    if not reasons:
        return None
    for name in ("exe", "cwd"):
        if isinstance(row[name], str):
            row[name] = safe_process_path(row[name])
            row[name]["object"] = identity(base + "/" + name)
    row.update({"candidateReasons": reasons, "matchingFds": matches, "matchingMappings": maps})
    # Process-root anchor identities reveal differing namespace views, without listing a DB.
    row["processRootAnchors"] = {target: identity(base + "/root" + target)
                                 for target in (DATA, ACCOUNT, EXTENSION)}
    try:
        row["relevantMounts"] = relevant_mounts(parse_mounts(read_limited(base + "/mountinfo", 1024 * 1024)))
    except (OSError, ValueError):
        row["relevantMounts"] = {"unavailable": True}
    try:
        after = process_brief(pid)
        row["identityStableDuringScan"] = row["startTicks"] == after["startTicks"]
    except (OSError, ValueError, KeyError, IndexError):
        row["identityStableDuringScan"] = False
    return row


def collect():
    start = time.monotonic()
    deadline = start + MAX_SECONDS
    counters = dict.fromkeys(("pidsSeen", "pidsExamined", "pidRacesOrDenied", "fdLinksExamined",
                              "fdScanTruncatedProcesses", "fdLinkRacesOrDenied", "fdDirectoriesUnavailable",
                              "matchingFdOutputTruncatedProcesses", "matchingMapOutputTruncatedProcesses",
                              "mapsUnavailableOrTooLarge"), 0)
    accounts = {}
    for name in ("redx", "redx-gdg"):
        try:
            entry = pwd.getpwnam(name)
            accounts[name] = {"uid": entry.pw_uid, "gid": entry.pw_gid}
        except KeyError:
            accounts[name] = {"notFound": True}
    units = [service_info(unit, deadline) for unit in KNOWN_UNITS]
    known_pids = set()
    for unit in units:
        for name in ("MainPID", "ControlPID"):
            value = unit.get("properties", {}).get(name, "0")
            if value.isdecimal() and int(value) > 0:
                known_pids.add(int(value))
    relevant_uids = {entry["uid"] for entry in accounts.values() if "uid" in entry}
    anchors = {target: {"lstat": identity(target, False), "stat": identity(target)} for target in ANCHORS}
    host_mounts = relevant_mounts(parse_mounts(read_limited("/proc/self/mountinfo", 1024 * 1024)))
    with os.scandir("/proc") as entries:
        pids = sorted(int(entry.name) for entry in entries if entry.name.isdecimal())
    counters["pidsSeen"] = len(pids)
    candidates = []
    for pid in pids[:MAX_PIDS]:
        if time.monotonic() >= deadline or counters["fdLinksExamined"] >= MAX_TOTAL_FDS:
            break
        counters["pidsExamined"] += 1
        try:
            row = inspect_process(pid, known_pids, relevant_uids, counters, deadline)
            if row:
                candidates.append(row)
                if len(candidates) >= MAX_CANDIDATES:
                    break
        except (OSError, ValueError, KeyError, IndexError):
            counters["pidRacesOrDenied"] += 1
    discovered = sorted({unit for row in candidates for unit in row["cgroupUnits"]} - set(KNOWN_UNITS))
    for unit in discovered[:MAX_DISCOVERED_UNITS]:
        units.append(service_info(unit, deadline))
    return {"schemaVersion": 1, "capturedUtc": datetime.now(timezone.utc).isoformat(),
            "scope": "read-only-metadata-not-writer-quiescence-proof", "legacyBuildExpectedNotVerified":
            "bcc6c62a2f119bd833ba083c12a3799ca622b980", "accounts": accounts,
            "fixedPathMetadata": anchors, "hostRelevantMounts": host_mounts,
            "units": units, "candidateProcesses": candidates, "coverage": counters,
            "discoveredUnitLimitHit": len(discovered) > MAX_DISCOVERED_UNITS,
            "processScanIncomplete": counters["pidsExamined"] < counters["pidsSeen"],
            "elapsedSeconds": round(time.monotonic() - start, 3),
            "writerAndQuiescenceStatus": "unknown-even-if-no-fd-or-mapping-matches",
            "limitations": ["Non-atomic procfs snapshot; PID races and denied reads are possible.",
                            "Path matching does not prove alias/bind-mount, detached-mount or chroot coverage.",
                            "No open FD now does not exclude scheduled, short-lived, external or future writers.",
                            "Dependency ordering is not a supported application drain/checkpoint procedure.",
                            "Exact old binary backup behavior and GenDragoon quiescence remain unverified."]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--metadata-only", action="store_true", required=True)
    parser.parse_args()
    if sys.platform != "linux" or os.geteuid() != 0:
        print(json.dumps({"error": "requires-authorized-root-linux-metadata-context"}))
        return 2
    try:
        result = collect()
    except (OSError, ValueError, KeyError, IndexError):
        print(json.dumps({"error": "metadata-collection-incomplete-no-raw-diagnostics", "writerAndQuiescenceStatus": "unknown"}))
        return 1
    print(json.dumps(result, sort_keys=True, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
