#!/usr/bin/env python3
"""Pinned private Hub installer. No public routing, credential reads or DB writes.

Default is inspection only. --apply additionally takes the independently verified
owner ID using a hidden TTY prompt (or --owner-stdin). See companion instructions.
This is not a production-cutover or application-consistent recovery tool.
"""
import argparse
import datetime as dt
import getpass
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import sys
import tarfile
import time
import urllib.request
import uuid
import warnings

REVISION = "c443ce7eb01c0a33261c049bc7071dbc90fd46d4"
ARCHIVE_SHA = "50314f04f166223602cc0e52d86f1d86a18c5e0b8461fa4b7d3276cb62e07b47"
ARCHIVE_ROOT = "Red-XAIHost-8612-" + REVISION
RELEASE = Path("/opt/redx-site-c443ce7")
SITE = RELEASE / "website/red-xai.com"
OLD_SITE = Path("/opt/redx-site-ee62b12/website/red-xai.com")
STATE = Path("/var/lib/red-xai-site")
CONFIG = Path("/etc/red-xai")
KEY = CONFIG / "site-cms.key"
ENV_FILE = CONFIG / "site.env"
UNIT = "redx-site-cms-private.service"
UNIT_PATH = Path("/etc/systemd/system") / UNIT
OLD_UNIT = "redx-site-private-stage.service"
OLD_FALLBACK = Path("/run/systemd/system") / OLD_UNIT
CUSTOMER_UNITS = ("redx-panel.service", "redx-hub.service", "redx-redxd.service",
                  "redx-db-website.service", "redx-host-website.service",
                  "cloudflared-redx.service", "redx-mailrelay.service")
NATIVE_REVISION = "8512e1704e589d24535e08082c3ab9f832d7655d"
CLEAN_ENV = {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"}


class Refused(Exception):
    pass


def require(condition, code):
    if not condition:
        raise Refused(code)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def command(args, timeout=30, accepted=(0,)):
    result = subprocess.run(args, stdin=subprocess.DEVNULL, capture_output=True,
                            env=CLEAN_ENV, timeout=timeout, check=False)
    require(result.returncode in accepted, "command_failed:" + Path(args[0]).name)
    return result.stdout.decode("utf-8", "strict")


def inspect_path(path, kind, uid=None, gid=None, mode=None):
    # Check every existing ancestor; a symlink must not redirect fixed targets.
    for ancestor in (*reversed(path.parents), path):
        if ancestor.exists() or ancestor.is_symlink():
            require(not ancestor.is_symlink(), "symlink_path_refused")
    s = path.lstat()
    require((stat.S_ISDIR(s.st_mode) if kind == "dir" else stat.S_ISREG(s.st_mode)),
            "unexpected_path_type")
    if uid is not None:
        require(s.st_uid == uid, "unexpected_path_owner")
    if gid is not None:
        require(s.st_gid == gid, "unexpected_path_group")
    if mode is not None:
        require(stat.S_IMODE(s.st_mode) == mode, "unexpected_path_mode")
    return s


def verify_archive(archive, release):
    s = inspect_path(archive, "file")
    require(0 < s.st_size < 100 * 1024 * 1024, "archive_size_refused")
    with archive.open("rb") as f:
        require(hashlib.file_digest(f, "sha256").hexdigest() == ARCHIVE_SHA,
                "archive_digest_mismatch")
    count = 0
    with tarfile.open(archive, "r:gz") as source:
        seen = set()
        for item in source:
            name = PurePosixPath(item.name)
            require(name.parts and name.parts[0] == ARCHIVE_ROOT and
                    ".." not in name.parts and not name.is_absolute(), "archive_path_refused")
            require(item.name not in seen, "duplicate_archive_path")
            seen.add(item.name)
            if item.isdir():
                continue
            require(item.isfile(), "archive_link_or_special_refused")
            target = release.joinpath(*name.parts[1:])
            inspect_path(target, "file")
            require(target.resolve().is_relative_to(release.resolve()), "release_escape_refused")
            with source.extractfile(item) as a, target.open("rb") as b:
                require(hashlib.file_digest(a, "sha256").digest() ==
                        hashlib.file_digest(b, "sha256").digest(), "release_file_mismatch")
            count += 1
    require(count >= 200, "incomplete_archive")
    return count


def validate_owner(owner):
    # Existing account IDs are opaque. Forbid shell/systemd/env injection without
    # assuming that an email, role header or display name establishes ownership.
    require(isinstance(owner, str) and 1 <= len(owner) <= 200 and
            re.fullmatch(r"[A-Za-z0-9_.:@-]+", owner) is not None, "invalid_owner_id_shape")
    return owner


def environment_bytes(owner):
    owner = validate_owner(owner)
    return ("REDX_SITE_STATE_DIR=/var/lib/red-xai-site\n"
            "REDX_SITE_CMS_DATABASE=website_cms\n"
            "REDX_SITE_CMS_KEY_FILE=/etc/red-xai/site-cms.key\n"
            "REDX_SITE_CMS_DURABILITY=group\n"
            "REDX_SITE_CMS_SINGLE_WRITER=true\n"
            "REDX_WEBSITE_OWNER_ACCOUNT_IDS=" + owner + "\n"
            "REDX_SITE_TRUST_CLOUDFLARED=false\n").encode()


def unit_bytes():
    # env -i prevents inherited NODE_OPTIONS, unrelated credentials and custom
    # module paths. Only this fixed dotenv file is loaded by the supported Node.
    return ("[Unit]\nDescription=Red-XAI private native CMS acceptance stage\n"
            "After=network.target redx-panel.service redx-db-website.service redx-host-website.service\n"
            "StartLimitIntervalSec=60\nStartLimitBurst=2\n\n"
            "[Service]\nType=simple\nUser=redx-site\nGroup=redx-site\n"
            "WorkingDirectory=" + SITE.as_posix() + "\n"
            "ExecStart=/usr/bin/env -i PATH=/usr/bin:/bin NODE_ENV=production /usr/bin/node "
            "--env-file=/etc/red-xai/site.env " + (SITE / "server.mjs").as_posix() + "\n"
            "UMask=0077\nRestart=on-failure\nRestartSec=5\nKillSignal=SIGTERM\n"
            "TimeoutStopSec=45\nNoNewPrivileges=true\nPrivateTmp=true\n"
            "ProtectSystem=strict\nProtectHome=true\nReadWritePaths=/var/lib/red-xai-site\n"
            "RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6\n").encode()


def parse_properties(value):
    return dict(line.split("=", 1) for line in value.splitlines() if "=" in line)


def properties(unit, names):
    return parse_properties(command(["/usr/bin/systemctl", "show", unit,
                                     "--property=" + ",".join(names)], accepted=(0, 1, 4)))


def customer_snapshot():
    result = {}
    for name in CUSTOMER_UNITS:
        p = properties(name, ["ActiveState", "MainPID", "ExecMainStartTimestampMonotonic", "NRestarts"])
        require(p.get("ActiveState") == "active" and p.get("MainPID", "0") != "0",
                "customer_service_not_active")
        result[name] = p
    return result


def metadata(port=14210):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    req = urllib.request.Request(f"http://127.0.0.1:{port}/api/site/health",
                                 headers={"Accept": "application/json"})
    with opener.open(req, timeout=5) as response:
        require(response.status == 200 and response.headers.get_content_type() == "application/json",
                "private_health_unavailable")
        payload = response.read(16385)
    require(len(payload) <= 16384, "oversized_health")
    return json.loads(payload)


def check_listener(pid):
    # ss output stays private and is reduced to exact ownership/bind decisions.
    output = command(["/usr/bin/ss", "-H", "-ltnp", "sport = :14210"])
    lines = [line for line in output.splitlines() if line.strip()]
    require(len(lines) == 1, "unexpected_private_listener_count")
    parts = lines[0].split()
    require(len(parts) >= 6 and parts[3] == "127.0.0.1:14210" and
            re.search(r"\bpid=" + re.escape(str(pid)) + r"[,)]", lines[0]) is not None,
            "private_listener_owner_or_bind_mismatch")


def check_durability(path, now=None):
    inspect_path(path, "file", uid=0, mode=0o600)
    require(path.stat().st_size <= 2048, "oversized_durability_observation")
    def pairs(items):
        out = {}
        for k, v in items:
            require(k not in out, "duplicate_durability_field")
            out[k] = v
        return out
    value = json.loads(path.read_bytes(), object_pairs_hook=pairs)
    require(set(value) == {"source", "observedUtc", "dbRevision", "rawEngineDurability"},
            "unexpected_durability_fields")
    require(value["source"] == "owner-authenticated-native-config" and
            value["dbRevision"] == NATIVE_REVISION and
            value["rawEngineDurability"] in ("", "group"), "durability_observation_mismatch")
    observed = dt.datetime.fromisoformat(value["observedUtc"].replace("Z", "+00:00"))
    require(observed.tzinfo is not None, "durability_timezone_required")
    age = ((now or dt.datetime.now(dt.timezone.utc)) - observed).total_seconds()
    require(-60 <= age <= 1800, "durability_observation_stale")
    return "source-derived-group-default" if value["rawEngineDurability"] == "" else "explicit-group"


def release_permissions(release):
    # Only the new pinned public source/dependencies tree is affected. Do not
    # follow npm .bin links; reject links escaping this immutable release.
    inspect_path(release, "dir")
    entries = [release]
    for folder, dirs, files in os.walk(release, followlinks=False):
        entries.extend(Path(folder) / name for name in dirs + files)
    for p in entries:
        s = p.lstat()
        if stat.S_ISLNK(s.st_mode):
            require(p.resolve().is_relative_to(release.resolve()) and p.resolve().exists(),
                    "release_symlink_escape")
        else:
            require(stat.S_ISDIR(s.st_mode) or stat.S_ISREG(s.st_mode), "release_special_file")
    # Validate the entire tree before changing any metadata.
    for p in entries:
        s = p.lstat()
        if stat.S_ISLNK(s.st_mode):
            os.lchown(p, 0, 0)
        else:
            os.chown(p, 0, 0)
            os.chmod(p, 0o755 if stat.S_ISDIR(s.st_mode) or (s.st_mode & 0o111) else 0o644)
    return len(entries)


def atomic_private(path, data, uid=0, gid=0, mode=0o600):
    require(not path.is_symlink(), "write_symlink_refused")
    temp = path.with_name("." + path.name + ".stage-" + uuid.uuid4().hex)
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    try:
        os.fchown(fd, uid, gid)
        os.fchmod(fd, mode)
        with os.fdopen(fd, "wb") as f:
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        os.replace(temp, path)
        d = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(d)
        finally:
            os.close(d)
    finally:
        if temp.exists():
            temp.unlink()


def protected_copy(source, target):
    # Used only for known unit/env files; never KEY, native files or credentials.
    inspect_path(source, "file")
    atomic_private(target, source.read_bytes())


def backup_support(target):
    # No CMS service is running yet. An existing writer lock is a hard refusal,
    # not permission to remove or overwrite it. This is support-only backup.
    require(not (STATE / "writer.lock").exists(), "existing_writer_lock_refused")
    for folder, dirs, files in os.walk(STATE, followlinks=False):
        for name in dirs + files:
            p = Path(folder) / name
            s = p.lstat()
            require(not stat.S_ISLNK(s.st_mode) and
                    (stat.S_ISREG(s.st_mode) or stat.S_ISDIR(s.st_mode)), "support_special_file_refused")
    fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "wb") as out:
        with tarfile.open(fileobj=out, mode="w:gz", dereference=False) as archive:
            archive.add(STATE, arcname="red-xai-site", recursive=True)
        out.flush()
        os.fsync(out.fileno())


def restore_file(path, saved):
    if saved is None:
        require(not path.is_symlink(), "rollback_symlink_refused")
        path.unlink(missing_ok=True)
    else:
        data, uid, gid, mode = saved
        atomic_private(path, data, uid, gid, mode)


def install(args):
    require(sys.platform.startswith("linux") and os.geteuid() == 0, "linux_root_required")
    import pwd
    require(Path("/proc/1/comm").read_text().strip() == "systemd", "systemd_required")
    user = pwd.getpwnam("redx-site")
    require((user.pw_uid, user.pw_gid) == (986, 976) and user.pw_shell.endswith("/nologin"),
            "service_identity_mismatch")
    inspect_path(CONFIG, "dir", 0, user.pw_gid, 0o750)
    inspect_path(STATE, "dir", user.pw_uid, user.pw_gid, 0o700)
    key_stat = inspect_path(KEY, "file", user.pw_uid, user.pw_gid, 0o600)
    require(key_stat.st_size == 51 and key_stat.st_nlink == 1, "private_key_metadata_mismatch")
    # This helper does not open KEY. RedXCmsStore will validate it at startup.
    count = verify_archive(Path(args.source_archive).resolve(), RELEASE)
    durability = check_durability(Path(args.durability_observation).resolve())
    require(command(["/usr/bin/node", "--version"]).strip() == "v20.20.2", "runtime_revision_mismatch")
    require("--env-file" in command(["/usr/bin/node", "--help"]), "runtime_env_file_unsupported")
    before = customer_snapshot()
    old = properties(OLD_UNIT, ["ActiveState", "MainPID", "WorkingDirectory", "DynamicUser", "FragmentPath"])
    require(old.get("ActiveState") == "active" and old.get("WorkingDirectory") == str(OLD_SITE) and
            old.get("DynamicUser") == "yes" and old.get("FragmentPath", "").startswith("/run/systemd/transient/"),
            "old_private_stage_mismatch")
    check_listener(old["MainPID"])
    health = metadata()
    require(health.get("ok") is True and health.get("cmsConfigured") is False and
            health.get("cmsStorage") == "unconfigured", "old_private_stage_not_unconfigured")
    require(not UNIT_PATH.exists() and not UNIT_PATH.is_symlink(), "new_unit_already_exists")
    require(properties(UNIT, ["LoadState"]).get("LoadState") == "not-found", "new_unit_already_loaded")
    require(not OLD_FALLBACK.exists() and not OLD_FALLBACK.is_symlink(), "old_fallback_already_exists")
    require(not (STATE / "writer.lock").exists(), "existing_writer_lock_refused")
    require(not ENV_FILE.is_symlink(), "environment_symlink_refused")
    if ENV_FILE.exists():
        inspect_path(ENV_FILE, "file")
    old_definition = command(["/usr/bin/systemctl", "cat", OLD_UNIT]).encode()
    require(0 < len(old_definition) < 256 * 1024 and str(OLD_SITE).encode() in old_definition,
            "old_unit_definition_mismatch")
    result = {"ok": True, "mode": "inspection", "sourceRevision": REVISION,
              "sourceArchiveSha256": ARCHIVE_SHA, "verifiedSourceFiles": count,
              "durabilityInterpretation": durability, "credentialFileRead": False,
              "publicIngressChanged": False, "productionCutover": False}
    if not args.apply:
        print(json.dumps(result, sort_keys=True))
        return
    require(args.single_writer_reviewed, "single_writer_operator_review_required")
    warnings.simplefilter("error", getpass.GetPassWarning)
    owner_input = sys.stdin.readline().rstrip("\n") if args.owner_stdin else getpass.getpass(
        "Verified canonical owner ID" + (" hex" if args.owner_hex else "") + " (hidden): ")
    owner = validate_owner(bytes.fromhex(owner_input).decode("utf-8") if args.owner_hex else owner_input)
    del owner_input
    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    backup = Path("/srv/red-xai/backups") / ("cms-private-stage-" + stamp + "-" + uuid.uuid4().hex[:8])
    inspect_path(backup.parent, "dir", uid=0)
    backup.mkdir(mode=0o700)
    env_before = None
    if ENV_FILE.exists():
        s = ENV_FILE.stat()
        env_before = (ENV_FILE.read_bytes(), s.st_uid, s.st_gid, stat.S_IMODE(s.st_mode))
        protected_copy(ENV_FILE, backup / "site.env.before")
    atomic_private(backup / "old-private-stage.service", old_definition)
    backup_support(backup / "support-before.tgz")
    # Preserve the candidate unit as a non-loaded review file for systemd's
    # parser before changing either private service.
    candidate = backup / UNIT
    atomic_private(candidate, unit_bytes())
    command(["/usr/bin/systemd-analyze", "verify", str(candidate)])
    permission_count = release_permissions(RELEASE)
    command(["/usr/sbin/runuser", "-u", "redx-site", "--", "/usr/bin/env", "-i",
             "PATH=/usr/bin:/bin", "NODE_ENV=production", "/usr/bin/node", "--input-type=module", "-e",
             "await import('file://" + str(SITE / "server.mjs") + "'); await import('file://" +
             str(SITE / "node_modules/sharp/lib/index.js") + "');"], timeout=30)
    env_changed = False
    installed = False
    old_stop_attempted = False
    try:
        # The directory fsync after os.replace can fail after a replacement has
        # already occurred. Arm rollback before either fallible atomic write.
        env_changed = True
        atomic_private(ENV_FILE, environment_bytes(owner), user.pw_uid, user.pw_gid)
        installed = True
        atomic_private(UNIT_PATH, unit_bytes(), mode=0o644)
        command(["/usr/bin/systemctl", "daemon-reload"])
        # This is the only pre-existing service this helper ever stops.
        old_stop_attempted = True
        command(["/usr/bin/systemctl", "stop", OLD_UNIT], timeout=55)
        require(customer_snapshot() == before, "customer_services_changed_before_start")
        command(["/usr/bin/systemctl", "start", UNIT], timeout=45)
        for attempt in range(20):
            try:
                p = properties(UNIT, ["ActiveState", "MainPID", "User", "Group", "WorkingDirectory"])
                require(p.get("ActiveState") == "active" and p.get("User") == "redx-site" and
                        p.get("Group") == "redx-site" and p.get("WorkingDirectory") == str(SITE),
                        "private_cms_unit_mismatch")
                check_listener(p["MainPID"])
                health = metadata()
                require(health.get("ok") is True and health.get("cmsConfigured") is True and
                        health.get("cmsStorage") == "native-redx-single-writer", "native_cms_health_mismatch")
                break
            except (Refused, OSError, ValueError, urllib.error.URLError):
                if attempt == 19:
                    raise Refused("private_cms_start_acceptance_failed")
                time.sleep(0.5)
        require(customer_snapshot() == before, "customer_services_changed_after_start")
        result.update(mode="private-cms-started", backupDirectory=str(backup),
                      publicSourceMetadataEntries=permission_count, persistentUnitInstalled=True,
                      enabledAtBoot=False, nativeCmsScopeValidatedByApplication=True,
                      identityAcceptance=False, tenantAcceptance=False,
                      applicationConsistentBackupRestore=False)
        atomic_private(backup / "receipt.json", (json.dumps(result, sort_keys=True) + "\n").encode())
        print(json.dumps(result, sort_keys=True))
    except Exception:
        # Never restore a support snapshot over newer state or remove a lock.
        # Native data, key, customer services and ingress remain untouched.
        rollback_ok = True
        try:
            if installed:
                command(["/usr/bin/systemctl", "stop", UNIT], timeout=55, accepted=(0, 5))
                UNIT_PATH.unlink(missing_ok=True)
            if env_changed:
                restore_file(ENV_FILE, env_before)
            if installed or env_changed:
                command(["/usr/bin/systemctl", "daemon-reload"])
            if old_stop_attempted:
                # --collect transient units disappear when stopped. Restore the
                # captured private definition to a runtime-only fallback path.
                atomic_private(OLD_FALLBACK, old_definition)
                command(["/usr/bin/systemctl", "daemon-reload"])
                command(["/usr/bin/systemctl", "start", OLD_UNIT], timeout=45)
                require(metadata().get("cmsConfigured") is False, "rollback_health_failed")
        except Exception:
            rollback_ok = False
        try:
            customers_unchanged = customer_snapshot() == before
        except Exception:
            customers_unchanged = False
        atomic_private(backup / "failure.json", (json.dumps({"privateStageRollbackVerified": rollback_ok,
                       "customerSnapshotUnchanged": customers_unchanged,
                       "supportAndNativeDataRestored": False}) + "\n").encode())
        raise Refused("private_stage_install_failed:" + ("rollback_verified" if rollback_ok else "operator_recovery_required"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-archive", required=True)
    parser.add_argument("--durability-observation", required=True)
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--single-writer-reviewed", action="store_true")
    parser.add_argument("--owner-stdin", action="store_true")
    parser.add_argument("--owner-hex", action="store_true", help="Decode hidden/stdin owner input from hex; value is never a command argument")
    args = parser.parse_args()
    try:
        install(args)
    except Exception as error:
        # Never echo command output, stdin, owner IDs or private file contents.
        code = str(error) if isinstance(error, Refused) else "inspection_or_install_error"
        print(json.dumps({"ok": False, "reason": code, "publicIngressChanged": False}), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
