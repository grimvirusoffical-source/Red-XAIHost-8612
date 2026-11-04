#!/usr/bin/env python3
"""Guarded Quick Login UI link transaction; no service or live-state writes."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import stat
import subprocess
import sys
import time
import urllib.request
import uuid

MANIFEST_SHA = "45f2d92dbb26a886bbce81269ce2fa533151d7278520a4ce7e015c42811467d4"
BACKEND_SHA = "236d62bbcb710989207f2b22c7936404f9fc937064415197380dcc07afb96a09"
SOURCE_REVISION = "5c54aa9702af7e27cdbd44be752a3210bc428770"
BASE = Path("/usr/local/lib/red-xai-site-bridge")
CURRENT = BASE / "current"
ORIGINAL = BASE / "releases/quicklogin-2cbedc4"
CANDIDATE = BASE / "releases/quicklogin-ui-5c54aa9"
STATE = Path("/var/lib/red-xai-site-bridge/app-grants.json")
BACKUPS = Path("/srv/red-xai/backups")
TOOLS = Path("/srv/red-xai/stage-tools")
LOCK = Path("/run/redx-quicklogin-ui.lock")
UNIT = "redxai-site-bridge.service"
UNIT_OBJECT_PATH = "/org/freedesktop/systemd1/unit/redxai_2dsite_2dbridge_2eservice"
FRAGMENT = Path("/etc/systemd/system") / UNIT
NODE = Path("/usr/bin/node")
SERVICE_UID, SERVICE_GID = 981, 981
LIMIT = 8 * 1024 * 1024
RUNTIME_NAMES = {"server.cjs", "web/index.html", "web/app.js", "web/style.css", "web/developer.html",
                 "web/developer.js", "web/ux.js", "web/ux.css", "web/notifications-sw.js"}
CONFIG_KEYS = ("REDXAI_QUICK_STATE", "REDX_SITE_BRIDGE_STATE", "REDXAI_QUICK_PORT", "REDX_SITE_BRIDGE_PORT")
CLEAN_ENV = {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"}


class Refused(Exception):
    pass


def need(value, code):
    if not value:
        raise Refused(code)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def encoded(value):
    return (json.dumps(value, sort_keys=True) + "\n").encode()


def strict_json(data):
    def unique(rows):
        result = {}
        for key, value in rows:
            need(key not in result, "duplicate_json_key")
            result[key] = value
        return result
    return json.loads(data.decode("utf-8"), object_pairs_hook=unique,
                      parse_constant=lambda _: need(False, "invalid_json_number"))


def identity(value):
    return [value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns,
            value.st_uid, value.st_gid, stat.S_IMODE(value.st_mode)]


def dir_policy(path, value):
    need(stat.S_ISDIR(value.st_mode), "directory_required")
    if path == STATE.parent:
        need(value.st_uid == SERVICE_UID and value.st_gid == SERVICE_GID and
             stat.S_IMODE(value.st_mode) == 0o700, "private_state_directory_metadata")
    else:
        need(value.st_uid == 0 and not value.st_mode & 0o022, "root_directory_metadata")


class Tree:
    """Root-anchored directory descriptors. Changed paths are never rebound."""
    def __init__(self):
        self.fds = {}

    def close(self):
        for fd in reversed(list(self.fds.values())):
            os.close(fd)
        self.fds.clear()

    def directory(self, path):
        need(path.is_absolute() and ".." not in path.parts, "absolute_canonical_path_required")
        if path in self.fds:
            self.verify(path)
            return self.fds[path]
        parent = None if path == Path("/") else self.directory(path.parent)
        fd = os.open(path if parent is None else path.name,
                     os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW,
                     **({} if parent is None else {"dir_fd": parent}))
        try:
            dir_policy(path, os.fstat(fd))
            self.fds[path] = fd
            self.verify(path)
            return fd
        except Exception:
            self.fds.pop(path, None)
            os.close(fd)
            raise

    def verify(self, path):
        if path == Path("/"):
            fresh = os.open("/", os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        else:
            self.verify(path.parent)
            fresh = os.open(path.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW,
                            dir_fd=self.fds[path.parent])
        try:
            before, after = os.fstat(self.fds[path]), os.fstat(fresh)
            dir_policy(path, before)
            dir_policy(path, after)
            need((before.st_dev, before.st_ino) == (after.st_dev, after.st_ino), "directory_identity_changed")
        finally:
            os.close(fresh)

    def entry(self, path):
        return os.stat(path.name, dir_fd=self.directory(path.parent), follow_symlinks=False)

    def exists(self, path):
        try:
            self.entry(path)
            return True
        except FileNotFoundError:
            return False

    def sync(self, path):
        os.fsync(self.directory(path))

    def mkdir(self, path, mode):
        need(not path.is_relative_to(STATE.parent), "live_state_write_forbidden")
        parent = self.directory(path.parent)
        os.mkdir(path.name, mode=mode, dir_fd=parent)
        os.fsync(parent)
        fd = self.directory(path)
        os.fchmod(fd, mode)
        os.fsync(fd)
        need(stat.S_IMODE(os.fstat(fd).st_mode) == mode, "created_directory_mode_mismatch")

    def read(self, path, private=False, state=False, limit=LIMIT):
        parent = self.directory(path.parent)
        before = self.entry(path)
        uid, gid = (SERVICE_UID, SERVICE_GID) if state else (0, 0)
        need(not state or path == STATE, "state_path_refused")
        need(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and before.st_uid == uid and
             before.st_gid == gid and not before.st_mode & 0o022 and 0 < before.st_size <= limit and
             (not (private or state) or stat.S_IMODE(before.st_mode) == 0o600), "regular_file_metadata_refused")
        fd = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
        try:
            need(identity(os.fstat(fd)) == identity(before), "file_identity_changed")
            chunks, length = [], 0
            while True:
                chunk = os.read(fd, min(65536, limit + 1 - length))
                if not chunk:
                    break
                chunks.append(chunk)
                length += len(chunk)
                need(length <= limit, "file_too_large")
            need(identity(os.fstat(fd)) == identity(before) and identity(self.entry(path)) == identity(before),
                 "file_changed_during_read")
            return b"".join(chunks), before
        finally:
            os.close(fd)

    def attributes(self, path):
        if stat.S_ISDIR(self.entry(path).st_mode):
            fd, close = self.directory(path), False
        else:
            _, before = self.read(path)
            fd, close = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK,
                                dir_fd=self.directory(path.parent)), True
        try:
            if close:
                need(identity(os.fstat(fd)) == identity(before), "attribute_file_identity_changed")
            result = {name: base64.b64encode(os.getxattr(fd, name)).decode("ascii") for name in os.listxattr(fd)}
            if close:
                need(identity(self.entry(path)) == identity(before), "attribute_file_entry_changed")
            return result
        finally:
            if close:
                os.close(fd)

    def exclusive(self, path, data, mode=0o600, attributes=None):
        need(not path.is_relative_to(STATE.parent), "live_state_write_forbidden")
        parent = self.directory(path.parent)
        fd = os.open(path.name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode, dir_fd=parent)
        try:
            with os.fdopen(fd, "wb", closefd=False) as output:
                output.write(data)
                output.flush()
            if attributes is not None:
                need(set(os.listxattr(fd)) <= set(attributes), "new_file_attribute_requires_review")
                for name, value in attributes.items():
                    os.setxattr(fd, name, base64.b64decode(value, validate=True))
            os.fchmod(fd, mode)
            os.fsync(fd)
            made = os.fstat(fd)
            need(made.st_uid == 0 and made.st_gid == 0 and stat.S_ISREG(made.st_mode) and
                 made.st_nlink == 1 and stat.S_IMODE(made.st_mode) == mode, "created_file_metadata")
            need(identity(self.entry(path)) == identity(made), "created_file_entry_changed")
            if attributes is not None:
                need(self.attributes(path) == attributes, "created_file_attribute_mismatch")
            os.fsync(parent)
            return identity(made)
        finally:
            os.close(fd)

    def link(self, target):
        parent = self.directory(CURRENT.parent)
        before = self.entry(CURRENT)
        need(stat.S_ISLNK(before.st_mode) and before.st_uid == 0 and before.st_gid == 0, "current_link_metadata")
        text = os.readlink(CURRENT.name, dir_fd=parent)
        need(text == str(target), "current_link_target_refused")
        need(identity(self.entry(CURRENT)) == identity(before), "current_link_changed")
        return {"metadata": identity(before), "target": text}

    def link_snapshot(self):
        for target in (ORIGINAL, CANDIDATE):
            try:
                return self.link(target)
            except Refused as error:
                if str(error) != "current_link_target_refused":
                    raise
        raise Refused("unknown_current_link_preserved")

    def replace_link(self, expected, target, transaction, arm=None):
        parent = self.directory(CURRENT.parent)
        need(self.link_snapshot() == expected, "current_link_changed_before_replace")
        temp = ".current.quicklogin-" + uuid.uuid4().hex
        os.symlink(str(target), temp, dir_fd=parent)
        made = os.stat(temp, dir_fd=parent, follow_symlinks=False)
        transaction.update(before=expected, created=identity(made), target=str(target), armed=False)
        try:
            os.fsync(parent)
            if arm is not None:
                arm(transaction)
            need(self.link_snapshot() == expected, "current_link_changed_before_replace")
            transaction["armed"] = True
            os.replace(temp, CURRENT.name, src_dir_fd=parent, dst_dir_fd=parent)
            os.fsync(parent)
            result = self.link(target)
            need(result["metadata"][:2] == transaction["created"][:2], "promoted_link_identity_changed")
            return result
        finally:
            try:
                found = os.stat(temp, dir_fd=parent, follow_symlinks=False)
            except FileNotFoundError:
                found = None
            if found is not None:
                need((found.st_dev, found.st_ino) == (made.st_dev, made.st_ino), "temporary_link_identity_changed")
                os.unlink(temp, dir_fd=parent)
                os.fsync(parent)


def command(args, timeout=15):
    result = subprocess.run(args, capture_output=True, stdin=subprocess.DEVNULL, env=CLEAN_ENV,
                            timeout=timeout, check=False)
    need(result.returncode == 0 and len(result.stdout) + len(result.stderr) < LIMIT, "command_failed")
    return result.stdout


def manifest(tree, artifact):
    need(artifact.is_relative_to(TOOLS) and artifact != TOOLS, "artifact_directory_refused")
    raw, _ = tree.read(artifact / "manifest.json")
    need(sha(raw) == MANIFEST_SHA, "artifact_manifest_mismatch")
    value = strict_json(raw)
    need(value["sourceRevision"] == SOURCE_REVISION and set(value["originalRuntimeFiles"]) == RUNTIME_NAMES,
         "artifact_runtime_refused")
    files = {}
    for name, expected in value["files"].items():
        need(name in {"web/app.js", "web/index.html", "test/approval-ui.test.cjs", "README.md"}, "artifact_file_refused")
        data, _ = tree.read(artifact / name)
        need(len(data) == expected["bytes"] and sha(data) == expected["sha256"], "artifact_file_mismatch")
        files[name] = data
    return value, files


def runtime_files(tree, root, meta, candidate=False):
    need(root in (ORIGINAL, CANDIDATE), "runtime_directory_refused")
    for directory in (root, root / "web"):
        value = os.fstat(tree.directory(directory))
        need(value.st_gid == 0 and stat.S_IMODE(value.st_mode) == 0o755, "runtime_directory_mode_requires_review")
    result = {}
    for name, old in meta["originalRuntimeFiles"].items():
        expected = meta["files"][name] if candidate and name in ("web/app.js", "web/index.html") else old
        data, metadata = tree.read(root / name)
        need(len(data) == expected["bytes"] and sha(data) == expected["sha256"] and
             stat.S_IMODE(metadata.st_mode) == 0o644, "runtime_file_mismatch")
        result[name] = data
    return result


def state_json(data):
    value = strict_json(data)
    need(isinstance(value, dict) and set(value) == {"schema", "grants", "apps"} and value["schema"] == 2 and
         isinstance(value["grants"], list) and isinstance(value["apps"], list), "state_schema_refused")
    return value


def properties():
    fields = "LoadState,ActiveState,MainPID,User,Group,WorkingDirectory,FragmentPath,UnitFileState,Environment,EnvironmentFiles,ExecStart,DropInPaths,NRestarts,ExecMainStartTimestampMonotonic"
    # --all retains ordinary empty selected properties, but systemctl's custom
    # EnvironmentFiles array printer emits nothing for an empty array.
    raw = command(["/usr/bin/systemctl", "show", UNIT, "--all", "--property=" + fields]).decode()
    rows = [line.split("=", 1) for line in raw.splitlines() if "=" in line]
    need(len({key for key, _ in rows}) == len(rows), "unit_duplicate_property")
    result = dict(rows)
    typed = command(["/usr/bin/busctl", "get-property", "org.freedesktop.systemd1", UNIT_OBJECT_PATH,
                     "org.freedesktop.systemd1.Service", "EnvironmentFiles"])
    need(typed == b"a(sb) 0\n", "environment_files_typed_empty_required")
    # Only a positive, exact typed proof permits canonicalizing the omitted
    # display value. Contradictory/nonempty display metadata still refuses.
    need("EnvironmentFile" not in result and ("EnvironmentFiles" not in result or result["EnvironmentFiles"] == ""),
         "environment_files_display_requires_review")
    result["EnvironmentFiles"] = ""
    return result


def selected_configuration(tree, active=True):
    props = properties()
    need(props.get("LoadState") == "loaded" and props.get("User") == "redxai" and props.get("Group") == "redxai" and
         props.get("WorkingDirectory") == str(CURRENT) and props.get("FragmentPath") == str(FRAGMENT) and
         props.get("UnitFileState") == "enabled", "unit_configuration_refused")
    need(props.get("EnvironmentFiles") == "", "environment_files_require_review")
    configured = re.search(r"^\{ path=(\S+) ; argv\[\]=(.*?) ; ignore_errors=(?:yes|no) ;", props.get("ExecStart", ""))
    expected_args = [str(NODE), str(CURRENT / "server.cjs")]
    need(configured is not None and props["ExecStart"].count("{") == 1 and props["ExecStart"].count("}") == 1 and
         configured[1] == str(NODE) and configured[2] == " ".join(expected_args),
         "startup_shape_requires_review")
    environment = {}
    for row in shlex.split(props.get("Environment", "")):
        key, sep, value = row.partition("=")
        need(sep and re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", key) and key not in environment,
             "unit_environment_layout_refused")
        environment[key] = value
    need(all(not environment.get(key) for key in ("NODE_OPTIONS", "NODE_PATH", "LD_PRELOAD", "LD_LIBRARY_PATH")),
         "runtime_injection_environment_refused")
    state = environment.get(CONFIG_KEYS[0]) or environment.get(CONFIG_KEYS[1])
    port = environment.get(CONFIG_KEYS[2]) or environment.get(CONFIG_KEYS[3]) or "17774"
    need(state == str(STATE) and port == "17774", "selected_state_or_port_refused")
    source_files = {str(FRAGMENT): tree.read(FRAGMENT)[0]}
    for name in shlex.split(props.get("DropInPaths", "")):
        path = Path(name)
        need(path.parent == FRAGMENT.with_name(UNIT + ".d") and re.fullmatch(r"[A-Za-z0-9_.-]+\.conf", path.name),
             "unit_dropin_requires_review")
        source_files[str(path)] = tree.read(path)[0]
    configuration = {key: props[key] for key in ("User", "Group", "WorkingDirectory", "FragmentPath", "UnitFileState")}
    configuration.update(exec=expected_args, environmentSha256=sha(props.get("Environment", "").encode()),
                         files={name: sha(value) for name, value in source_files.items()})
    pid = props.get("MainPID", "")
    import grp
    import pwd
    user = pwd.getpwnam("redxai")
    need(user.pw_uid == SERVICE_UID and user.pw_gid == SERVICE_GID and grp.getgrnam("redxai").gr_gid == SERVICE_GID,
         "service_os_identity_refused")
    if not active and props.get("ActiveState") in ("inactive", "failed") and pid == "0":
        return configuration, None, source_files
    need(props.get("ActiveState") == "active" and pid.isdigit() and int(pid) > 1, "approval_service_inactive")
    proc = Path("/proc") / pid
    status = (proc / "status").read_text()
    for field, expected in (("Uid", SERVICE_UID), ("Gid", SERVICE_GID)):
        values = re.findall(r"^" + field + r":\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s*$", status, re.M)
        need(len(values) == 1 and all(int(value) == expected for value in values[0]), "process_identity_refused")
    need((proc / "cmdline").read_bytes().split(bytes([0]))[:-1] == [value.encode() for value in expected_args],
         "process_arguments_refused")
    need(os.readlink(proc / "exe") == str(NODE) and os.readlink(proc / "cwd") in (str(ORIGINAL), str(CANDIDATE)),
         "process_source_refused")
    raw_env = (proc / "environ").read_bytes()
    process_env = dict(row.split(b"=", 1) for row in raw_env.split(bytes([0])) if b"=" in row)
    need(all(process_env.get(key.encode(), b"") == environment.get(key, "").encode() for key in CONFIG_KEYS),
         "effective_configuration_mismatch")
    need(all(not process_env.get(key.encode()) for key in ("NODE_OPTIONS", "NODE_PATH", "LD_PRELOAD", "LD_LIBRARY_PATH")),
         "process_injection_environment_refused")
    need((proc / "cgroup").read_text().strip().endswith("/" + UNIT), "process_cgroup_refused")
    node_meta = tree.entry(NODE)
    need(stat.S_ISREG(node_meta.st_mode) and node_meta.st_uid == 0 and node_meta.st_gid == 0 and
         node_meta.st_nlink == 1 and not node_meta.st_mode & 0o022 and node_meta.st_mode & 0o111 and
         (node_meta.st_dev, node_meta.st_ino) == ((proc / "exe").stat().st_dev, (proc / "exe").stat().st_ino),
         "node_executable_identity_refused")
    baseline = {key: props[key] for key in ("MainPID", "NRestarts", "ExecMainStartTimestampMonotonic")}
    baseline.update(cwd=os.readlink(proc / "cwd"), node=identity(node_meta))
    listener = command(["/usr/bin/ss", "-H", "-ltnp", "sport", "=", ":17774"]).decode()
    lines = listener.splitlines()
    need(len(lines) == 1 and len(lines[0].split()) >= 6 and lines[0].split()[3] == "127.0.0.1:17774" and
         re.findall(r"pid=(\d+)", lines[0]) == [pid], "listener_process_identity_refused")
    # Requery after procfs reads to reject a changing/reused process.
    after = properties()
    need(all(after.get(key) == props.get(key) for key in props), "service_changed_during_inspection")
    return configuration, baseline, source_files


def loopback(meta, candidate=False):
    expected = meta["files"] if candidate else meta["originalRuntimeFiles"]
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    checks = {"/redxai-connect/": "web/index.html", "/redxai-connectx/": "web/index.html",
              "/redxai-connect/app.js": "web/app.js"}
    for route, name in checks.items():
        request = urllib.request.Request("http://127.0.0.1:17774" + route, headers={"Host": "red-xai.com"})
        with opener.open(request, timeout=5) as response:
            data = response.read(1024 * 1024)
            need(response.status == 200 and sha(data) == expected[name]["sha256"], "running_asset_mismatch")
    request = urllib.request.Request("http://127.0.0.1:17774/redxai-connect/healthz", headers={"Host": "red-xai.com"})
    with opener.open(request, timeout=5) as response:
        need(response.status == 200 and strict_json(response.read(4096)) == {"ok": True, "service": "red-xai-quick-login"},
             "approval_health_refused")
    request = urllib.request.Request("http://127.0.0.1:17774/redxai-connect/config", headers={"Host": "red-xai.com"})
    with opener.open(request, timeout=5) as response:
        need(response.status == 200 and strict_json(response.read(4096)) == {
            "service": "Red-XAI Quick Login", "protocol": "redx-site-session-v1", "issuer": "https://red-xai.com",
            "client_id": "gendragoon", "supported_clients": ["gendragoon", "vortex", "bridge"],
            "accountSource": "/api/accounts/me", "siteLogin": "/signin", "siteRegistration": "/signup",
            "receiverReady": False, "scope": "gendragoon.receiver"}, "approval_protocol_refused")


def restore_probe(tree, backend, copied_state):
    before, _ = tree.read(copied_state, private=True)
    state_json(before)
    need(sha(tree.read(backend, private=True)[0]) == BACKEND_SHA, "probe_backend_mismatch")
    parent = tree.directory(NODE.parent)
    node_fd = os.open(NODE.name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=parent)
    script = """(async()=>{let b;try{const fs=require('node:fs');
const filename=process.argv[2];if(!fs.lstatSync(filename).isFile())throw Error();
b=require(process.argv[1]).createBridge({publicOrigin:'https://red-xai.com',stateFile:filename,fetchImpl:async()=>{throw Error('NETWORK_DISABLED')}});
if(b.server.listening)throw Error();await b.close();process.stdout.write('RESTORE_COPY_OK');
}catch{try{if(b)await b.close()}catch{}process.exitCode=1}})();"""
    try:
        actual = os.fstat(node_fd)
        expected = tree.entry(NODE)
        need(identity(actual) == identity(expected) and actual.st_uid == 0 and not actual.st_mode & 0o022,
             "probe_node_identity_changed")
        result = subprocess.run(["/proc/self/fd/" + str(node_fd), "-e", script, str(backend), str(copied_state)],
                                capture_output=True, stdin=subprocess.DEVNULL, env=CLEAN_ENV, pass_fds=(node_fd,),
                                timeout=5, check=False)
        need(result.returncode == 0 and result.stdout == b"RESTORE_COPY_OK" and result.stderr == b"",
             "private_copy_constructor_failed")
    finally:
        os.close(node_fd)
    after, _ = tree.read(copied_state, private=True)
    need(after == before, "private_copy_changed_by_constructor")


def state_snapshot(tree, directory, backend, name):
    raw, metadata = tree.read(STATE, state=True)
    state_json(raw)
    copied = directory / name
    tree.exclusive(copied, raw)
    restore_probe(tree, backend, copied)
    return {"snapshotSha256": sha(raw), "snapshotMetadata": identity(metadata), "copy": name,
            "constructorAcceptedCopy": True, "allClientRecoveryVerified": False}


def inspect(tree, artifact):
    meta, assets = manifest(tree, artifact)
    configuration, baseline, unit_files = selected_configuration(tree)
    need(baseline["cwd"] == str(ORIGINAL), "original_process_required")
    link = tree.link(ORIGINAL)
    files = runtime_files(tree, ORIGINAL, meta)
    need(not tree.exists(CANDIDATE), "candidate_already_exists_requires_review")
    raw, metadata = tree.read(STATE, state=True)
    state_json(raw)
    loopback(meta)
    need(selected_configuration(tree)[:2] == (configuration, baseline), "service_changed_during_inspection")
    return {"schema": 1, "artifact": str(artifact), "manifestSha256": MANIFEST_SHA, "sourceRevision": SOURCE_REVISION,
            "configuration": configuration, "baseline": baseline, "originalLink": link,
            "statePath": str(STATE), "stateMetadataAtInspection": identity(metadata)}, files, assets, unit_files, meta


class Lock:
    def __init__(self, tree):
        self.tree, self.keep = tree, False

    def __enter__(self):
        self.made = self.tree.exclusive(LOCK, encoded({"pid": os.getpid(), "purpose": "quicklogin-ui-link-transaction"}))
        return self

    def __exit__(self, kind, error, traceback):
        if not self.keep:
            found = self.tree.entry(LOCK)
            need(identity(found) == self.made, "deployment_lock_changed_preserved")
            os.unlink(LOCK.name, dir_fd=self.tree.directory(LOCK.parent))
            try:
                self.tree.sync(LOCK.parent)
            except Exception:
                # The transaction journal remains authoritative even if lock
                # removal cannot be made durable. Never report normal success.
                self.keep = True
                try:
                    self.tree.exclusive(LOCK, encoded({"pid": os.getpid(), "purpose": "lock-release-review"}))
                except Exception:
                    pass
                raise Refused("lock_release_failed_manual_review") from None


def stage(tree, artifact):
    with Lock(tree):
        receipt, files, assets, unit_files, meta = inspect(tree, artifact)
        receipt["runtimeAttributes"] = {name: tree.attributes(ORIGINAL / name) for name in files}
        receipt["directoryAttributes"] = {name: tree.attributes(ORIGINAL / name) for name in (".", "web")}
        directory = BACKUPS / ("quicklogin-ui-" + time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()) + "-" + uuid.uuid4().hex)
        tree.mkdir(directory, 0o700)
        original_copy = directory / "original"
        tree.mkdir(original_copy, 0o700)
        tree.mkdir(original_copy / "web", 0o700)
        for name, data in files.items():
            tree.exclusive(original_copy / name, data)
        tree.mkdir(directory / "unit", 0o700)
        receipt["unitBackups"] = {}
        for offset, (name, data) in enumerate(sorted(unit_files.items())):
            dest = "unit/" + str(offset) + ".private"
            tree.exclusive(directory / dest, data)
            receipt["unitBackups"][dest] = {"source": name, "sha256": sha(data)}
        receipt["stateSnapshot"] = state_snapshot(tree, directory, original_copy / "server.cjs", "state.snapshot.json")
        need(selected_configuration(tree)[:2] == (receipt["configuration"], receipt["baseline"]),
             "service_changed_during_staging")
        need(tree.link(ORIGINAL) == receipt["originalLink"], "current_link_changed_during_staging")
        # This directory is deliberately never reused or overwritten after a
        # failed stage. An incomplete candidate requires explicit review.
        tree.mkdir(CANDIDATE, 0o755)
        tree.mkdir(CANDIDATE / "web", 0o755)
        need({name: tree.attributes(CANDIDATE / name) for name in (".", "web")} == receipt["directoryAttributes"],
             "new_directory_attributes_require_review")
        for name, data in files.items():
            tree.exclusive(CANDIDATE / name, assets.get(name, data), mode=0o644,
                           attributes=receipt["runtimeAttributes"][name])
        runtime_files(tree, CANDIDATE, meta, candidate=True)
        need(selected_configuration(tree)[:2] == (receipt["configuration"], receipt["baseline"]),
             "service_changed_during_staging")
        runtime_files(tree, ORIGINAL, meta)
        need(tree.link(ORIGINAL) == receipt["originalLink"], "current_link_changed_during_staging")
        receipt["candidateFiles"] = {name: sha(assets.get(name, data)) for name, data in files.items()}
        tree.exclusive(directory / "receipt.json", encoded(receipt))
        return {"ok": True, "stagedDirectory": str(directory), "candidateDirectory": str(CANDIDATE),
                "codeLinkChanged": False, "directLiveStateWrite": False, "serviceRestarted": False,
                "constructorAcceptedPrivateCopy": True, "allClientRecoveryVerified": False,
                "maintenanceApprovalRequired": True}


def load_stage(tree, directory):
    need(directory.parent == BACKUPS and re.fullmatch(r"quicklogin-ui-[0-9]{8}T[0-9]{6}Z-[a-f0-9]{32}", directory.name),
         "stage_directory_refused")
    need(stat.S_IMODE(os.fstat(tree.directory(directory)).st_mode) == 0o700, "stage_not_private")
    receipt = strict_json(tree.read(directory / "receipt.json", private=True)[0])
    need(receipt.get("schema") == 1 and receipt.get("manifestSha256") == MANIFEST_SHA and
         receipt.get("sourceRevision") == SOURCE_REVISION and receipt.get("statePath") == str(STATE), "stage_schema_refused")
    meta, _ = manifest(tree, Path(receipt["artifact"]))
    for name, value in meta["originalRuntimeFiles"].items():
        raw, _ = tree.read(directory / "original" / name, private=True)
        need(sha(raw) == value["sha256"], "original_backup_mismatch")
    need(receipt["originalLink"]["target"] == str(ORIGINAL), "original_link_receipt_refused")
    for dest, value in receipt["unitBackups"].items():
        need(re.fullmatch(r"unit/[0-9]+\.private", dest), "unit_backup_path_refused")
        need(sha(tree.read(directory / dest, private=True)[0]) == value["sha256"], "unit_backup_mismatch")
    need({value["source"]: value["sha256"] for value in receipt["unitBackups"].values()} ==
         receipt["configuration"]["files"], "unit_backup_set_mismatch")
    snapshot = receipt["stateSnapshot"]
    need(snapshot["copy"] == "state.snapshot.json" and snapshot["constructorAcceptedCopy"] is True,
         "state_snapshot_receipt_refused")
    raw, _ = tree.read(directory / snapshot["copy"], private=True)
    need(sha(raw) == snapshot["snapshotSha256"], "state_snapshot_mismatch")
    state_json(raw)
    files = runtime_files(tree, CANDIDATE, meta, candidate=True)
    need({name: sha(data) for name, data in files.items()} == receipt["candidateFiles"], "candidate_receipt_mismatch")
    need(set(receipt["runtimeAttributes"]) == RUNTIME_NAMES and set(receipt["directoryAttributes"]) == {".", "web"},
         "runtime_attribute_receipt_refused")
    for root in (ORIGINAL, CANDIDATE):
        need({name: tree.attributes(root / name) for name in RUNTIME_NAMES} == receipt["runtimeAttributes"] and
             {name: tree.attributes(root / name) for name in (".", "web")} == receipt["directoryAttributes"],
             "runtime_attributes_changed")
    return receipt, meta


def promote(tree, directory):
    with Lock(tree) as lock:
        receipt, meta = load_stage(tree, directory)
        need(not any(tree.exists(directory / name) for name in
                     ("promotion.json", "rollback.json", "promotion.arm.json", "rollback.arm.json", "promotion-recovery.arm.json")),
             "stage_transaction_already_attempted_requires_review")
        configuration, baseline, _ = selected_configuration(tree)
        need((configuration, baseline) == (receipt["configuration"], receipt["baseline"]), "service_changed_since_stage")
        runtime_files(tree, ORIGINAL, meta)
        need(tree.link(ORIGINAL) == receipt["originalLink"], "current_link_changed_since_stage")
        loopback(meta)
        snapshot = state_snapshot(tree, directory, directory / "original/server.cjs",
                                  "pre-promotion-" + uuid.uuid4().hex + ".json")
        need(selected_configuration(tree)[:2] == (configuration, baseline), "service_changed_before_promotion")
        transaction = {}
        try:
            # Recovery is armed before replace_link: an fsync/readback error
            # may occur after the atomic rename has already completed.
            changed = tree.replace_link(receipt["originalLink"], CANDIDATE, transaction,
                                        arm=lambda value: tree.exclusive(directory / "promotion.arm.json", encoded(value)))
            need(selected_configuration(tree)[:2] == (configuration, baseline), "service_changed_during_promotion")
            need(tree.link(CANDIDATE) == changed, "current_link_changed_after_promotion")
            tree.exclusive(directory / "promotion.json",
                           encoded({"link": changed, "stateCopy": snapshot, "serviceRestarted": False}))
            need(tree.link(CANDIDATE) == changed, "current_link_changed_after_promotion")
        except Exception:
            try:
                found = tree.link_snapshot()
                ours = transaction.get("armed") and found["target"] == str(CANDIDATE) and \
                    found["metadata"][:2] == transaction["created"][:2]
                if ours:
                    tree.replace_link(found, ORIGINAL, {}, arm=lambda value: tree.exclusive(
                        directory / "promotion-recovery.arm.json", encoded(value)))
                else:
                    need(found == receipt["originalLink"], "intervening_link_preserved")
                need(selected_configuration(tree)[:2] == (configuration, baseline), "service_review_required")
            except Exception:
                lock.keep = True
                raise Refused("promotion_failed_manual_review_lock_retained") from None
            raise Refused("promotion_failed_original_link_restored") from None
        return {"ok": True, "codeLinkChanged": True, "directLiveStateWrite": False, "liveStateRestored": False,
                "stateAdvancedSinceStage": snapshot["snapshotSha256"] != receipt["stateSnapshot"]["snapshotSha256"],
                "serviceRestartRequired": True, "serviceRestarted": False, "publicBehaviorVerified": False,
                "pendingOrSessionOnlyFlowsPreserved": False, "allClientRecoveryVerified": False}


def rollback(tree, directory):
    with Lock(tree) as lock:
        receipt, meta = load_stage(tree, directory)
        configuration, baseline, _ = selected_configuration(tree, active=False)
        need(configuration == receipt["configuration"], "unit_changed_since_stage")
        need(identity(tree.entry(NODE)) == receipt["baseline"]["node"], "node_changed_since_stage")
        runtime_files(tree, ORIGINAL, meta)
        # This always checks a fresh copy of CURRENT state. No stored snapshot
        # is ever copied back into the live application store.
        state_snapshot(tree, directory, directory / "original/server.cjs", "pre-rollback-" + uuid.uuid4().hex + ".json")
        try:
            found = tree.link_snapshot()
            if found["target"] == str(CANDIDATE):
                proof = strict_json(tree.read(directory / "promotion.json", private=True)[0])
                need(proof.get("link") == found and proof.get("serviceRestarted") is False,
                     "unowned_candidate_link_preserved")
                restored = tree.replace_link(found, ORIGINAL, {}, arm=lambda value: tree.exclusive(
                    directory / "rollback.arm.json", encoded(value)))
                tree.exclusive(directory / "rollback.json", encoded({"link": restored, "liveStateRestored": False}))
            elif found != receipt["originalLink"]:
                proof = strict_json(tree.read(directory / "rollback.json", private=True)[0])
                need(proof.get("link") == found and proof.get("liveStateRestored") is False,
                     "unowned_original_link_preserved")
            final = tree.link(ORIGINAL)
            need(final == (restored if found["target"] == str(CANDIDATE) else found), "rollback_link_readback_failed")
        except Exception:
            lock.keep = True
            raise Refused("rollback_incomplete_manual_review_lock_retained") from None
        return {"ok": True, "originalLinkSelected": True, "directLiveStateWrite": False, "liveStateRestored": False,
                "serviceRestartRequired": baseline is None or baseline["cwd"] != str(ORIGINAL),
                "serviceRestarted": False, "publicBehaviorVerified": False, "allClientRecoveryVerified": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifact-directory", type=Path, default=TOOLS / "quicklogin-ui-5c54aa9")
    group = parser.add_mutually_exclusive_group()
    group.add_argument("--stage", action="store_true")
    group.add_argument("--promote-from", type=Path)
    group.add_argument("--rollback-from", type=Path)
    parser.add_argument("--maintenance-approved", action="store_true",
                        help="Operator acknowledgment of a separately approved maintenance decision; not permission by itself")
    args = parser.parse_args()
    tree = Tree()
    try:
        need(sys.platform.startswith("linux") and os.geteuid() == 0, "linux_root_required")
        need(not (args.promote_from or args.rollback_from) or args.maintenance_approved,
             "separate_maintenance_approval_required")
        if args.promote_from:
            result = promote(tree, args.promote_from)
        elif args.rollback_from:
            result = rollback(tree, args.rollback_from)
        elif args.stage:
            result = stage(tree, args.artifact_directory)
        else:
            inspect(tree, args.artifact_directory)
            result = {"ok": True, "inspectionOnly": True, "codeLinkChanged": False,
                      "directLiveStateWrite": False, "serviceRestarted": False,
                      "constructorAcceptedPrivateCopy": False, "maintenanceApprovalRequired": True}
        print(json.dumps(result, sort_keys=True))
        return 0
    except Refused as error:
        print(json.dumps({"ok": False, "code": str(error), "privateOutputWithheld": True}))
        return 2
    except Exception:
        print(json.dumps({"ok": False, "code": "guarded_operation_failed", "privateOutputWithheld": True}))
        return 2
    finally:
        tree.close()


if __name__ == "__main__":
    sys.exit(main())
