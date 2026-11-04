#!/usr/bin/env python3
"""Scoped Hub UI file transaction. Never restarts a tunnel or restores data."""
import argparse
import base64
from contextlib import contextmanager
from functools import lru_cache
import hashlib
from html.parser import HTMLParser
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import time
import urllib.request
import uuid

MANIFEST_SHA = "3ebaf2cf1f59304a60d347a90ba99987bd3d64658b5c825a941f17d85aa4665c"
BEFORE_SHA = "8144e37d5067e1bd3a4b7807bc34c9963ec4eef9adb2053bb3a5ec7403052b7b"
AFTER_SHA = "773b99e3be5d4405ac043c3f11e5e0af50c4aa63aebfaa33aa593733aaf11ead"
OLD_ASSET = "/assets/index-native-navigation-8144e37d5067.js"
NEW_ASSET = "/assets/index-hub-completion-773b99e3be5d.js"
HOOK_BEFORE = b'function tg(){const n=Yt();return()=>{window.history.replaceState(null,"",Lx()),n.invalidateQueries({queryKey:["me"]})}}'
HOOK_AFTER = b'function tg(){const n=Yt();return()=>{n.invalidateQueries({queryKey:["me"]}),window.location.replace(Lx())}}'
CMS_UNIT = "redx-site-cms-private.service"
UNITS = ("redx-panel.service", "redx-hub.service", "redx-redxd.service", "redx-db-website.service", "redx-host-website.service", "cloudflared-redx.service", "redx-mailrelay.service", CMS_UNIT)
CLEAN_ENV = {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"}
BACKUPS = Path("/srv/red-xai/backups")
HERE = Path(__file__).resolve().parent
LEGACY_ROOT = Path("/opt/redx/lib/panel-dist")
LEGACY_DIRECTORY_MODES = {Path("/opt/redx"): 0o750, Path("/opt/redx/lib"): 0o755,
                          LEGACY_ROOT: 0o755, LEGACY_ROOT / "assets": 0o755}
_LEGACY = None


class Refused(Exception):
    pass


def need(value, code):
    if not value:
        raise Refused(code)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def json_bytes(value):
    return (json.dumps(value, sort_keys=True) + "\n").encode()


def strict_json(data):
    def pairs(rows):
        out = {}
        for key, value in rows:
            need(key not in out, "duplicate_json_field")
            out[key] = value
        return out
    return json.loads(data.decode("utf-8"), object_pairs_hook=pairs, parse_constant=lambda _: need(False, "invalid_json_number"))


def manifest():
    raw = (HERE / "manifest.json").read_bytes()
    need(sha(raw) == MANIFEST_SHA, "manifest_digest_mismatch")
    return strict_json(raw)


def route_catalog(meta):
    pages = ["/", "/signin", "/verify-email", "/host", "/db", "/settings", "/api-keys", "/contact", "/community", "/vortex", "/cloud-pc", "/api/quick-login", "/products"]
    pages += ["/products/" + key for key in ("software", "games", "bots", "sdks", "libraries", "repositories", "cli", "subscriptions")]
    pages += meta["productPages"]
    pages += ["/news/" + key for key in ("announcements", "updates", "maintenance")]
    pages += ["/media/" + key for key in ("videos", "podcasts", "videos-podcasts")]
    pages += ["/brands/" + key for key in ("recommended-brands", "partnerships")]
    pages += ["/legal/" + key for key in ("terms", "user-agreement", "copyright")]
    assets = [name for name in meta["runtimeFiles"] if name in ("app.js", "styles.css", "catalog.json", "public-secrets-docs.json", "cli-installation.md") or name.startswith("site/")]
    api = ["health", "config", "session", "posts", "media", "preferences", "subscriptions", "checkout", "account/delete", "verification/resend", "releases/vortex"]
    return sorted(set(pages)), assets, api


def route_patterns(meta):
    pages, assets, api = route_catalog(meta)
    literal = lambda values: "(?:" + "|".join(re.escape(value) for value in sorted(set(values))) + ")"
    return ["^" + literal(pages) + "$", "^/" + literal(assets) + "$", "^/site-assets/[a-f0-9]{64}/" + literal(assets) + "$",
            "^/api/site/" + literal(api) + "$", "^/api/site/(?:posts/p_[a-f0-9]{32}|media/[a-f0-9]{32})$"]


def transform_config(data, expected_sha, meta):
    need(re.fullmatch(r"[a-f0-9]{64}", expected_sha) and sha(data) == expected_sha, "config_digest_mismatch")
    text = data.decode("utf-8")
    need(0 < len(data) <= 262144 and "# redx-scoped-hub-ui-v1" not in text and "\x00" not in text, "config_layout_refused")
    lines = text.splitlines(keepends=True)
    positions = [i for i, line in enumerate(lines) if re.fullmatch(r"ingress:[ \t]*(?:#[^\r\n]*)?\r?\n", line)]
    need(len(positions) == 1, "exact_ingress_section_required")
    at = positions[0] + 1
    first = next((line for line in lines[at:] if line.strip() and not line.lstrip().startswith("#")), "")
    match = re.match(r"( {0,2})- (?:hostname|service):", first)
    need(match is not None and len(match[1]) in (0, 2), "simple_ingress_list_required")
    indent, newline = match[1], "\r\n" if lines[at - 1].endswith("\r\n") else "\n"
    # Inserting a new rule must not silently skip per-rule Access/TLS/origin
    # policy or YAML merges attached to an existing matched rule. Global
    # originRequest policy stays byte-identical and is inherited as before.
    for line in lines[at:]:
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if line[0] not in " -\t":
            break  # another top-level configuration section
        need(re.match(re.escape(indent) + r"(?:- |  )(?:hostname|path|service):[^\r\n]*\r?\n?$", line) is not None, "per_rule_policy_requires_separate_review")
    block = indent + "# redx-scoped-hub-ui-v1" + newline
    for pattern in route_patterns(meta):
        block += indent + "- hostname: red-xai.com" + newline + indent + "  path: '" + pattern + "'" + newline + indent + "  service: http://127.0.0.1:14210" + newline
    output = "".join(lines[:at]) + block + "".join(lines[at:])
    need(output.replace(block, "", 1) == text, "existing_ingress_changed")
    return output.encode()


class Scripts(HTMLParser):
    def __init__(self, text):
        super().__init__(convert_charrefs=False)
        self.scripts = []
        self.feed(text)

    def handle_starttag(self, tag, attrs):
        if tag == "script":
            need(len({k for k, _ in attrs}) == len(attrs), "duplicate_script_attribute")
            self.scripts.append(dict(attrs))


def transform_index(data, expected_sha):
    need(re.fullmatch(r"[a-f0-9]{64}", expected_sha) and sha(data) == expected_sha, "legacy_index_digest_mismatch")
    text = data.decode("utf-8")
    scripts = Scripts(text).scripts
    need(text.count(OLD_ASSET) == 1 and NEW_ASSET not in text and sum(s.get("src") == OLD_ASSET and s.get("type") == "module" for s in scripts) == 1, "legacy_module_reference_mismatch")
    need(all("integrity" not in s for s in scripts if s.get("src") == OLD_ASSET), "legacy_module_integrity_requires_review")
    output = data.replace(OLD_ASSET.encode(), NEW_ASSET.encode(), 1)
    need(output.replace(NEW_ASSET.encode(), OLD_ASSET.encode(), 1) == data, "legacy_index_other_bytes_changed")
    return output


def transform_asset(data):
    need(sha(data) == BEFORE_SHA and data.count(HOOK_BEFORE) == 1 and HOOK_AFTER not in data, "legacy_bundle_mismatch")
    output = data.replace(HOOK_BEFORE, HOOK_AFTER, 1)
    need(sha(output) == AFTER_SHA and len(output) == 441942, "legacy_completion_output_mismatch")
    return output


def fingerprint(s):
    return [s.st_dev, s.st_ino, s.st_size, s.st_mtime_ns, s.st_uid, s.st_gid, stat.S_IMODE(s.st_mode)]


def legacy_directory_metadata(path, metadata):
    need(stat.S_ISDIR(metadata.st_mode), "legacy_directory_not_regular")
    if path in LEGACY_DIRECTORY_MODES:
        need(metadata.st_uid == 999 and stat.S_IMODE(metadata.st_mode) == LEGACY_DIRECTORY_MODES[path], "legacy_directory_owner_or_mode_mismatch")
    else:
        need(metadata.st_uid == 0 and not metadata.st_mode & 0o022, "legacy_ancestor_metadata_mismatch")


class PinnedLegacyDirectories:
    """Pin each directory with openat/O_NOFOLLOW; never rebind a changed tree."""
    def __init__(self):
        self.fds = self.open_chain()

    @staticmethod
    def open_chain():
        fds = {}
        try:
            paths = [*reversed(LEGACY_ROOT.parents), LEGACY_ROOT, LEGACY_ROOT / "assets"]
            for path in paths:
                fd = os.open(path if path == Path("/") else path.name,
                             os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW,
                             **({} if path == Path("/") else {"dir_fd": fds[path.parent]}))
                fds[path] = fd
                legacy_directory_metadata(path, os.fstat(fd))
            return fds
        except Exception:
            for fd in reversed(list(fds.values())):
                os.close(fd)
            raise

    def verify(self):
        fresh = self.open_chain()
        try:
            need(all((os.fstat(fd).st_dev, os.fstat(fd).st_ino) == (os.fstat(fresh[path]).st_dev, os.fstat(fresh[path]).st_ino) for path, fd in self.fds.items()), "legacy_directory_identity_changed")
        finally:
            for fd in reversed(list(fresh.values())):
                os.close(fd)

    def close(self):
        for fd in reversed(list(self.fds.values())):
            os.close(fd)


def verify_panel_identity():
    import grp
    import pwd
    props = dict(line.split("=", 1) for line in command(["/usr/bin/systemctl", "show", "redx-panel.service", "--property=User,Group,MainPID"]).splitlines() if "=" in line)
    need(props.get("User") == "redx" and props.get("Group") == "redx" and pwd.getpwnam("redx").pw_uid == 999 and pwd.getpwnam("redx").pw_gid == 987 and grp.getgrnam("redx").gr_gid == 987, "legacy_panel_service_identity_mismatch")
    pid = props.get("MainPID", "")
    need(pid.isdigit() and int(pid) > 1, "legacy_panel_pid_unavailable")
    status = Path("/proc/" + pid + "/status").read_text()
    for field, expected in (("Uid", 999), ("Gid", 987)):
        values = re.findall(r"^" + field + r":\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s*$", status, re.M)
        need(len(values) == 1 and all(int(value) == expected for value in values[0]), "legacy_panel_process_identity_mismatch")


@contextmanager
def legacy_context(enabled):
    global _LEGACY
    if not enabled:
        yield
        return
    need(_LEGACY is None, "legacy_directory_context_already_active")
    verify_panel_identity()
    handles = PinnedLegacyDirectories()
    _LEGACY = handles
    try:
        yield
    finally:
        _LEGACY = None
        handles.close()


def legacy_parent(path):
    selected = path.parent == LEGACY_ROOT and (path.name == "index.html" or re.fullmatch(r"\.index\.html\.ui-stage-[a-f0-9]{32}", path.name))
    selected = selected or path.parent == LEGACY_ROOT / "assets" and path.name in (Path(OLD_ASSET).name, Path(NEW_ASSET).name)
    if not selected:
        return None
    need(_LEGACY is not None, "verified_legacy_directory_context_required")
    _LEGACY.verify()
    return _LEGACY.fds[path.parent]


def entry_stat(path):
    parent = legacy_parent(path)
    return path.lstat() if parent is None else os.stat(path.name, dir_fd=parent, follow_symlinks=False)


def open_entry(path, flags, mode=0o600):
    parent = legacy_parent(path)
    flags |= getattr(os, "O_NONBLOCK", 0)  # a raced FIFO must not block root
    return os.open(path, flags, mode) if parent is None else os.open(path.name, flags, mode, dir_fd=parent)


def entry_exists(path):
    try:
        entry_stat(path)
        return True
    except FileNotFoundError:
        return False


def unlink_entry(path):
    parent = legacy_parent(path)
    if parent is None:
        path.unlink()
    else:
        os.unlink(path.name, dir_fd=parent)


def safe_path(path):
    if legacy_parent(path) is not None:
        s = entry_stat(path)
        need(s.st_uid == 0 and s.st_gid == 0 and not s.st_mode & 0o022 and not stat.S_ISLNK(s.st_mode), "legacy_file_not_root_owned_or_safe")
        return
    need(path.is_absolute() and path.resolve() == path, "canonical_absolute_path_required")
    for item in (*reversed(path.parents), path):
        s = item.lstat()
        need(not stat.S_ISLNK(s.st_mode) and s.st_uid == os.geteuid() and not s.st_mode & 0o022, "untrusted_path_metadata")


def read_regular(path, limit=8 * 1024 * 1024, private=False):
    safe_path(path)
    before = entry_stat(path)
    need(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and 0 < before.st_size <= limit and (not private or stat.S_IMODE(before.st_mode) == 0o600), "regular_file_metadata_refused")
    fd = open_entry(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as source:
        need(fingerprint(os.fstat(source.fileno())) == fingerprint(before), "file_identity_changed")
        data = source.read(limit + 1)
    need(len(data) <= limit and fingerprint(entry_stat(path)) == fingerprint(before), "file_changed_during_read")
    return data, before


def xattrs(path):
    fd = open_entry(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        s = os.fstat(fd)
        need(stat.S_ISREG(s.st_mode) and s.st_nlink == 1 and s.st_uid == (0 if legacy_parent(path) is not None else os.geteuid()), "xattr_file_metadata_refused")
        return {name: base64.b64encode(os.getxattr(fd, name)).decode("ascii") for name in os.listxattr(fd)}
    finally:
        os.close(fd)


def sync_dir(path):
    if path in (LEGACY_ROOT, LEGACY_ROOT / "assets"):
        need(_LEGACY is not None, "verified_legacy_directory_context_required")
        _LEGACY.verify()
        os.fsync(_LEGACY.fds[path])
        return
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def exclusive(path, data):
    fd = open_entry(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "wb") as target:
        target.write(data)
        target.flush()
        os.fsync(target.fileno())
        identity = fingerprint(os.fstat(target.fileno()))
    sync_dir(path.parent)
    return identity


def set_metadata(path, identity, metadata, attributes):
    fd = open_entry(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        current = os.fstat(fd)
        need(fingerprint(current) == identity and stat.S_ISREG(current.st_mode) and current.st_nlink == 1, "created_file_identity_changed")
        os.fchown(fd, metadata[4], metadata[5])
        os.fchmod(fd, metadata[6])
        for name, value in attributes.items():
            os.setxattr(fd, name, base64.b64decode(value, validate=True))
        os.fsync(fd)
        need(fingerprint(entry_stat(path))[:2] == identity[:2], "created_file_entry_changed")
    finally:
        os.close(fd)


def atomic(path, data, metadata, attributes, expected=None):
    prior, prior_st = read_regular(path)
    need((expected is None or prior in expected) and fingerprint(prior_st)[4:] == metadata[4:] and xattrs(path) == attributes, "atomic_prior_state_mismatch")
    temp = path.with_name("." + path.name + ".ui-stage-" + uuid.uuid4().hex)
    identity = exclusive(temp, data)
    try:
        set_metadata(temp, identity, metadata, attributes)
        current, current_st = read_regular(path)
        need(current == prior and fingerprint(current_st) == fingerprint(prior_st) and xattrs(path) == attributes, "atomic_target_changed_before_replace")
        src_fd, dst_fd = legacy_parent(temp), legacy_parent(path)
        need((src_fd is None) == (dst_fd is None), "cross_boundary_replace_refused")
        if src_fd is None:
            os.replace(temp, path)
        else:
            os.replace(temp.name, path.name, src_dir_fd=src_fd, dst_dir_fd=dst_fd)
        sync_dir(path.parent)
        current, st = read_regular(path)
        need(current == data and fingerprint(st)[4:] == metadata[4:] and xattrs(path) == attributes, "atomic_readback_failed")
    finally:
        if entry_exists(temp):
            need(fingerprint(entry_stat(temp))[:2] == identity[:2], "temporary_cleanup_identity_changed")
            unlink_entry(temp)
            sync_dir(temp.parent)


def command(args):
    result = subprocess.run(args, capture_output=True, stdin=subprocess.DEVNULL, env=CLEAN_ENV, timeout=30, check=False)
    need(result.returncode == 0 and len(result.stdout) + len(result.stderr) < 1024 * 1024, "command_failed")
    return result.stdout.decode("utf-8")


@contextmanager
def deployment_lock():
    lock = Path("/run/redx-scoped-hub-ui.lock")
    safe_path(lock.parent)
    fd = os.open(lock, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    identity = os.fstat(fd)
    try:
        with os.fdopen(fd, "wb") as output:
            output.write(json_bytes({"pid": os.getpid(), "purpose": "scoped-hub-ui-file-transaction"}))
            output.flush()
            os.fsync(output.fileno())
        sync_dir(lock.parent)
        yield
    finally:
        current = lock.lstat()
        need((current.st_ino, current.st_dev) == (identity.st_ino, identity.st_dev), "deployment_lock_identity_changed")
        lock.unlink()
        sync_dir(lock.parent)


def baseline():
    result = {}
    for unit in UNITS:
        text = command(["/usr/bin/systemctl", "show", unit, "--property=ActiveState,MainPID,ExecMainStartTimestampMonotonic,NRestarts"])
        props = dict(line.split("=", 1) for line in text.splitlines() if "=" in line)
        need(props.get("ActiveState") == "active" and props.get("MainPID", "0") != "0", "required_service_inactive")
        result[unit] = props
    return result


def runtime(meta):
    root = Path(meta["runtimeDirectory"])
    for name, expected in meta["runtimeFiles"].items():
        need(sha(read_regular(root / name)[0]) == expected, "runtime_source_digest_mismatch")
    props = dict(line.split("=", 1) for line in command(["/usr/bin/systemctl", "show", CMS_UNIT, "--property=MainPID,User,Group,WorkingDirectory,FragmentPath,UnitFileState"]).splitlines() if "=" in line)
    need(props.get("User") == "redx-site" and props.get("WorkingDirectory") == str(root) and props.get("FragmentPath") == "/etc/systemd/system/" + CMS_UNIT, "unexpected_hub_unit")
    pid = props.get("MainPID", "")
    need(pid.isdigit() and int(pid) > 1, "invalid_hub_pid")
    need(Path("/proc/" + pid + "/cmdline").read_bytes().split(b"\0")[:-1] == [b"/usr/bin/node", b"--env-file=/etc/red-xai/site.env", str(root / "server.mjs").encode()], "unexpected_hub_process")
    listener = command(["/usr/bin/ss", "-H", "-ltnp", "sport = :14210"]).strip().splitlines()
    need(len(listener) == 1 and listener[0].split()[3] == "127.0.0.1:14210" and re.search(r"\bpid=" + pid + r"[,)]", listener[0]), "unexpected_hub_listener")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    for path in ("/api/site/health", "/api/site/config"):
        with opener.open("http://127.0.0.1:14210" + path, timeout=5) as response:
            need(response.status == 200 and response.headers.get_content_type() == "application/json", "private_hub_metadata_failed")
            value = strict_json(response.read(32769))
        if path.endswith("health"):
            need(value == {"ok": True, "service": "redxai-site", "cmsConfigured": True, "cmsStorage": "native-redx-single-writer"}, "private_hub_not_native")
        else:
            caps = value.get("capabilities", {})
            need(caps.get("cms") is True and caps.get("media") is True and all(caps.get(k) is False for k in ("resendVerification", "deleteAccount", "subscriptions")), "unexpected_capabilities")
    return props.get("UnitFileState") == "enabled"


def ingress_service(config, url):
    output = command([cloudflared(), "tunnel", "--config", str(config), "ingress", "rule", url])
    services = re.findall(r"^\s*service:\s*(\S+)\s*$", output, re.M)
    need(len(services) == 1, "cloudflared_rule_output_unrecognized")
    return services[0]


@lru_cache(maxsize=1)
def cloudflared():
    choices = list({Path(name).resolve() for name in ("/usr/bin/cloudflared", "/usr/local/bin/cloudflared") if Path(name).exists()})
    need(len(choices) == 1, "exact_cloudflared_binary_required")
    selected = choices[0]
    safe_path(selected)
    need(selected.is_file() and selected.stat().st_mode & 0o111, "cloudflared_not_executable")
    pid = dict(line.split("=", 1) for line in command(["/usr/bin/systemctl", "show", "cloudflared-redx.service", "--property=MainPID"]).splitlines() if "=" in line).get("MainPID", "")
    need(pid.isdigit() and int(pid) > 1 and os.path.samefile(selected, "/proc/" + pid + "/exe"), "cloudflared_live_binary_mismatch")
    return str(selected)


def verify_live_config(config):
    cloudflared()
    pid = dict(line.split("=", 1) for line in command(["/usr/bin/systemctl", "show", "cloudflared-redx.service", "--property=MainPID"]).splitlines() if "=" in line).get("MainPID", "")
    need(pid.isdigit() and int(pid) > 1, "cloudflared_pid_unavailable")
    # Process arguments stay private; only explicit config-path equality is
    # retained. Token arguments, if any, are neither printed nor copied.
    args = Path("/proc/" + pid + "/cmdline").read_bytes().split(b"\0")[:-1]
    values = [args[i + 1] for i, value in enumerate(args[:-1]) if value == b"--config"]
    values += [value.removeprefix(b"--config=") for value in args if value.startswith(b"--config=")]
    need(values == [str(config).encode()], "live_explicit_tunnel_config_path_mismatch")


def target_paths(config, index):
    reviewed_index = index.as_posix().startswith("/srv/red-xai/") or index.as_posix() == "/opt/redx/lib/panel-dist/index.html"
    need(any(config.as_posix().startswith(root) for root in ("/etc/cloudflared/", "/root/.cloudflared/", "/srv/red-xai/")) and reviewed_index and index.name == "index.html", "target_paths_outside_reviewed_roots")


def validate_routes(original, candidate):
    command([cloudflared(), "tunnel", "--config", str(candidate), "ingress", "validate"])
    pages, assets, api = route_catalog(manifest())
    changed = pages + ["/" + name for name in assets] + ["/site-assets/" + "a" * 64 + "/" + name for name in assets] + ["/api/site/" + name for name in api]
    changed += ["/signin?next=%2Fdb", "/host?embed=site", "/api/site/posts/p_" + "a" * 32, "/api/site/media/" + "b" * 32]
    for path in changed:
        need(ingress_service(candidate, "https://red-xai.com" + path) == "http://127.0.0.1:14210", "candidate_ui_route_mismatch")
    preserved = {4200: ("/signup", "/activate", "/tos", "/api/accounts/login", "/api/accounts/me", "/api/rpc/example", "/assets/old-account.js", "/hostile", "/unknown-client-path", "/products/not-a-real-product", "/vortex/cloudpc/"),
                 14202: ("/host/", "/host/api/rpc/nodes.list", "/host/api/agent/heartbeat", "/host/settings?embed=site", "/host/assets/index-amJkAgmM.js"),
                 17790: ("/v1", "/v1/console/?embed=site", "/v1/db/example/path/file", "/ui", "/ui/assets/example.js"),
                 17774: ("/redxai-connect", "/redxai-connect/ux.js", "/redxai-connectx", "/redxai-connectx/authorize")}
    count = len(changed)
    for port, paths in preserved.items():
        for path in paths:
            url = "https://red-xai.com" + path
            before, after = ingress_service(original, url), ingress_service(candidate, url)
            need(before == after and before.rstrip("/") in ("http://127.0.0.1:" + str(port), "http://localhost:" + str(port)), "preserved_route_mismatch")
            count += 1
    for path in ("/", "/v1/auth/config", "/api/accounts/me", "/unknown-client-path"):
        url = "https://api.red-xai.com" + path
        need(ingress_service(original, url) == ingress_service(candidate, url), "api_domain_changed")
        count += 1
    return count


def inspect(args, meta):
    config, index = Path(args.config), Path(args.legacy_index)
    target_paths(config, index)
    verify_live_config(config)
    old_config, cst = read_regular(config, 262144)
    old_index, ist = read_regular(index, 2 * 1024 * 1024)
    asset = index.parent / OLD_ASSET.lstrip("/")
    old_asset, ast = read_regular(asset)
    candidate_config = transform_config(old_config, args.config_sha256, meta)
    candidate_index = transform_index(old_index, args.index_sha256)
    candidate_asset = transform_asset(old_asset)
    need(not entry_exists(index.parent / NEW_ASSET.lstrip("/")), "completion_asset_already_exists")
    state = {"schema": 1, "config": str(config), "index": str(index), "configMetadata": fingerprint(cst), "indexMetadata": fingerprint(ist), "assetMetadata": fingerprint(ast), "configXattrs": xattrs(config), "indexXattrs": xattrs(index), "assetXattrs": xattrs(asset), "originalConfigSha256": sha(old_config), "originalIndexSha256": sha(old_index), "candidateConfigSha256": sha(candidate_config), "candidateIndexSha256": sha(candidate_index), "candidateAssetSha256": AFTER_SHA, "runtimeRevision": meta["runtimeRevision"], "baseline": baseline(), "hubEnabledAtBoot": runtime(meta)}
    return state, {"original-config.yml": old_config, "original-index.html": old_index, "candidate-config.yml": candidate_config, "candidate-index.html": candidate_index, "candidate-asset.js": candidate_asset}


def stage(args, meta):
    state, files = inspect(args, meta)
    safe_path(BACKUPS)
    directory = BACKUPS / ("scoped-hub-ui-" + time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()) + "-" + uuid.uuid4().hex)
    directory.mkdir(mode=0o700)
    sync_dir(BACKUPS)
    for name, data in files.items():
        exclusive(directory / name, data)
    definitions = b"\n".join(command(["/usr/bin/systemctl", "cat", unit]).encode() for unit in UNITS)
    exclusive(directory / "protected-units.txt", definitions)
    count = validate_routes(Path(args.config), directory / "candidate-config.yml")
    need(baseline() == state["baseline"], "service_changed_during_staging")
    state["validatedRouteCount"] = count
    exclusive(directory / "receipt.json", json_bytes(state))
    return {"ok": True, "stagedDirectory": str(directory), "validatedRouteCount": count, "activeFilesChanged": False, "hubBootEnableRequired": not state["hubEnabledAtBoot"], "productionAccountMigration": False}


def load_stage(directory):
    need(directory.parent == BACKUPS and directory.name.startswith("scoped-hub-ui-"), "stage_directory_refused")
    safe_path(directory)
    need(stat.S_IMODE(directory.stat().st_mode) == 0o700, "stage_directory_not_private")
    state = strict_json(read_regular(directory / "receipt.json", private=True)[0])
    files = {name: read_regular(directory / name, private=True)[0] for name in ("original-config.yml", "original-index.html", "candidate-config.yml", "candidate-index.html", "candidate-asset.js")}
    for name, field in (("original-config.yml", "originalConfigSha256"), ("original-index.html", "originalIndexSha256"), ("candidate-config.yml", "candidateConfigSha256"), ("candidate-index.html", "candidateIndexSha256"), ("candidate-asset.js", "candidateAssetSha256")):
        need(sha(files[name]) == state[field], "staged_file_digest_mismatch")
    meta = manifest()
    need(state.get("schema") == 1 and state.get("runtimeRevision") == meta["runtimeRevision"], "stage_release_mismatch")
    target_paths(Path(state["config"]), Path(state["index"]))
    need(transform_config(files["original-config.yml"], state["originalConfigSha256"], meta) == files["candidate-config.yml"], "staged_config_not_reviewed_transform")
    need(transform_index(files["original-index.html"], state["originalIndexSha256"]) == files["candidate-index.html"] and sha(files["candidate-asset.js"]) == AFTER_SHA, "staged_legacy_transform_mismatch")
    return state, files


def apply_stage(directory, meta):
    state, files = load_stage(directory)
    need(runtime(meta), "hub_must_be_enabled_at_boot_before_public_routes")
    need(baseline() == state["baseline"], "service_changed_since_staging")
    config, index = Path(state["config"]), Path(state["index"])
    verify_live_config(config)
    for file, data, metadata, attrs in ((config, files["original-config.yml"], state["configMetadata"], state["configXattrs"]), (index, files["original-index.html"], state["indexMetadata"], state["indexXattrs"])):
        current, st = read_regular(file)
        need(current == data and fingerprint(st) == metadata and xattrs(file) == attrs, "original_file_changed_since_staging")
    original_asset = index.parent / OLD_ASSET.lstrip("/")
    current_asset, asset_st = read_regular(original_asset)
    need(sha(current_asset) == BEFORE_SHA and fingerprint(asset_st) == state["assetMetadata"] and xattrs(original_asset) == state["assetXattrs"], "original_asset_changed_since_staging")
    validate_routes(config, directory / "candidate-config.yml")
    exclusive(directory / "apply-started.json", json_bytes({"startedUtc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}))
    new_asset = index.parent / NEW_ASSET.lstrip("/")
    changed = []
    try:
        identity = exclusive(new_asset, files["candidate-asset.js"])
        set_metadata(new_asset, identity, state["assetMetadata"], state["assetXattrs"])
        sync_dir(new_asset.parent)
        need(sha(read_regular(new_asset)[0]) == AFTER_SHA, "installed_asset_readback_failed")
        for file, key in ((index, "index"), (config, "config")):
            need(read_regular(file)[0] == files["original-" + key + (".html" if key == "index" else ".yml")], "file_changed_before_promotion")
            changed.append((file, key))  # arm before replace, including later fsync failure
            atomic(file, files["candidate-" + key + (".html" if key == "index" else ".yml")], state[key + "Metadata"], state[key + "Xattrs"], (files["original-" + key + (".html" if key == "index" else ".yml")],))
        need(baseline() == state["baseline"], "service_changed_during_file_transaction")
        result = {"ok": True, "candidateFilesInstalled": True, "tunnelRestartRequired": True, "tunnelRestarted": False, "dataRestored": False, "publicBehaviorVerified": False}
        exclusive(directory / "apply-result.json", json_bytes(result))
        return result
    except Exception:
        recovered = True
        for file, key in reversed(changed):
            try:
                current, current_st = read_regular(file)
                need(current in (files["candidate-" + key + (".html" if key == "index" else ".yml")], files["original-" + key + (".html" if key == "index" else ".yml")]) and fingerprint(current_st)[4:] == state[key + "Metadata"][4:] and xattrs(file) == state[key + "Xattrs"], "rollback_would_overwrite_unknown_change")
                atomic(file, files["original-" + key + (".html" if key == "index" else ".yml")], state[key + "Metadata"], state[key + "Xattrs"], (files["candidate-" + key + (".html" if key == "index" else ".yml")], files["original-" + key + (".html" if key == "index" else ".yml")]))
            except Exception:
                recovered = False
        exclusive(directory / "apply-failed.json", json_bytes({"rollbackFilesRestored": recovered, "tunnelRestarted": False, "dataRestored": False}))
        raise Refused("file_transaction_failed_rollback_" + ("succeeded" if recovered else "needs_manual_review"))


def rollback_stage(directory):
    state, files = load_stage(directory)
    config, index = Path(state["config"]), Path(state["index"])
    verify_live_config(config)
    read_regular(directory / "apply-started.json", private=True)
    for file, key in ((config, "config"), (index, "index")):
        current, metadata = read_regular(file)
        suffix = ".html" if key == "index" else ".yml"
        need(current in (files["candidate-" + key + suffix], files["original-" + key + suffix]) and fingerprint(metadata)[4:] == state[key + "Metadata"][4:] and xattrs(file) == state[key + "Xattrs"], "rollback_current_file_not_owned_transition")
    command([cloudflared(), "tunnel", "--config", str(directory / "original-config.yml"), "ingress", "validate"])
    if (directory / "rollback-started.json").exists():
        need(strict_json(read_regular(directory / "rollback-started.json", private=True)[0]) == {"requested": True}, "rollback_marker_mismatch")
    else:
        exclusive(directory / "rollback-started.json", json_bytes({"requested": True}))
    for file, key in ((config, "config"), (index, "index")):
        current, metadata = read_regular(file)
        suffix = ".html" if key == "index" else ".yml"
        need(current in (files["candidate-" + key + suffix], files["original-" + key + suffix]) and fingerprint(metadata)[4:] == state[key + "Metadata"][4:] and xattrs(file) == state[key + "Xattrs"], "rollback_file_changed_before_promotion")
        atomic(file, files["original-" + key + (".html" if key == "index" else ".yml")], state[key + "Metadata"], state[key + "Xattrs"], (files["candidate-" + key + (".html" if key == "index" else ".yml")], files["original-" + key + (".html" if key == "index" else ".yml")]))
    result = {"ok": True, "originalFilesRestored": True, "tunnelRestartRequired": True, "tunnelRestarted": False, "dataRestored": False, "unusedCompletionAssetRetained": True}
    if not (directory / "rollback-result.json").exists():
        exclusive(directory / "rollback-result.json", json_bytes(result))
    return result


class Parser(argparse.ArgumentParser):
    def error(self, _message):
        raise Refused("arguments_refused")


def main():
    try:
        parser = Parser(description=__doc__)
        for flag in ("config", "config-sha256", "legacy-index", "index-sha256"):
            parser.add_argument("--" + flag)
        modes = parser.add_mutually_exclusive_group()
        modes.add_argument("--stage", action="store_true")
        modes.add_argument("--apply-from")
        modes.add_argument("--rollback-from")
        args = parser.parse_args()
        need(sys.platform == "linux" and os.geteuid() == 0, "linux_root_required")
        safe_path(HERE)
        read_regular(HERE / "rollout.py")
        read_regular(HERE / "manifest.json")
        meta = manifest()
        if args.apply_from or args.rollback_from:
            selected, _ = load_stage(Path(args.apply_from or args.rollback_from))
            selected_index = Path(selected["index"])
        else:
            need(all((args.config, args.config_sha256, args.legacy_index, args.index_sha256)), "actual_target_paths_and_independent_hashes_required")
            selected_index = Path(args.legacy_index)
        with legacy_context(selected_index == LEGACY_ROOT / "index.html"):
            if args.apply_from:
                with deployment_lock():
                    result = apply_stage(Path(args.apply_from), meta)
            elif args.rollback_from:
                with deployment_lock():
                    result = rollback_stage(Path(args.rollback_from))
            elif args.stage:
                result = stage(args, meta)
            else:
                state, _ = inspect(args, meta)
                result = {"ok": True, "inspectionOnly": True, "configSha256": state["originalConfigSha256"], "indexSha256": state["originalIndexSha256"], "candidateConfigSha256": state["candidateConfigSha256"], "candidateIndexSha256": state["candidateIndexSha256"], "cloudflaredCandidateValidated": False, "hubBootEnableRequired": not state["hubEnabledAtBoot"], "activeFilesChanged": False, "productionAccountMigration": False}
        print(json.dumps(result, sort_keys=True))
        return 0
    except Exception as error:
        print(json.dumps({"ok": False, "code": str(error) if isinstance(error, Refused) else "private_failure_withheld", "privateOutputWithheld": True}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
