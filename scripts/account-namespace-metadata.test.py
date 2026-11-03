"""Synthetic-only review checks; never calls collect(), systemctl or real procfs."""
import ast
import json
from pathlib import Path
import types

source = Path(__file__).with_name("account-namespace-metadata.py").read_text(encoding="utf-8")
tree = ast.parse(source)
# pwd is Linux-only; these pure function fixtures also run on the review Windows PC.
tree.body = [node for node in tree.body if not
             (isinstance(node, ast.Import) and any(alias.name == "pwd" for alias in node.names))]
ns = {"__name__": "metadata_fixture_review"}
exec(compile(tree, "<metadata-synthetic-fixture>", "exec"), ns)
# Model Linux flag constants without modifying the review machine's os module.
ns["os"] = types.SimpleNamespace(**vars(ns["os"]))
ns["os"].O_ACCMODE, ns["os"].O_PATH = 3, 0o10000000
assert ns["path_class"](ns["EXTENSION"] + "/private-secret-record") == "gendragoon-extension"
assert ns["path_class"](ns["ACCOUNT"] + "x/private-secret-record") == "native-data-other"
assert ns["path_class"](ns["DATA"] + "x/private-secret-record") is None
assert ns["path_class"](ns["ACCOUNT"] + "/a (deleted)") == "accounts-namespace"
assert ns["fd_mode"]("flags:\t0100002\nmnt_id:\t41\npos:\t12345\n") == {
    "access": "read-write", "mountId": 41}
mounts = ns["parse_mounts"](
    "30 1 8:1 / / rw,private-secret-option - ext4 /private-secret-device rw,password=private-secret-value\n"
    "31 30 8:1 /data /opt/redx/var/data rw - ext4 /private-secret-source rw\n"
    "32 31 8:2 /private-secret-bind-root /opt/redx/var/data/dbs/redx_accounts/gendragoon-extension ro - ext4 /secret-source rw\n"
    "33 31 8:2 /private-secret-root /opt/redx/var/data/private-secret-mountpoint ro - ext4 /secret-source rw\n")
safe_mounts = ns["relevant_mounts"](mounts)
assert "private-secret" not in json.dumps(safe_mounts)
assert len(safe_mounts["rows"]) == 3
assert safe_mounts["rows"][-1]["mountpointFixedAnchor"] is None
assert len(safe_mounts["rows"][-1]["mountRootSha256"]) == 64
assert ns["safe_process_path"]('/opt/redx/bin/redxd')['exactExpectedNativeExecutable'] is True
assert 'private-secret' not in json.dumps(ns['safe_process_path'](ns['ACCOUNT'] + '/private-secret-cwd'))

run_calls = []


def fake_run(argv, **kwargs):
    run_calls.append((argv, kwargs))
    return types.SimpleNamespace(stdout=b"LoadState=loaded\nMainPID=13\nEnvironment=private-secret-env\n", returncode=0)


orig_run = ns["subprocess"].run
ns["subprocess"].run = fake_run
try:
    result = ns["service_info"]("redx-redxd.service", ns["time"].monotonic() + 3)
finally:
    ns["subprocess"].run = orig_run
assert "private-secret" not in json.dumps(result)
assert result["properties"]["MainPID"] == "13"
assert run_calls[0][1]["env"] == ns["SAFE_ENV"]
assert run_calls[0][0][-2:] == ["--", "redx-redxd.service"]
assert all(name not in ns["PROPERTIES"] for name in ("ExecStart", "Environment", "EnvironmentFiles"))


class FakeScan:
    def __enter__(self):
        return iter([types.SimpleNamespace(name="9", path="/proc/12/fd/9")])

    def __exit__(self, *args):
        return False


base_row = {"pid": 12, "ppid": 1, "uids": [1001] * 4, "gids": [1001] * 4, "startTicks": 88,
            "cgroupUnits": ["redx-redxd.service"], "exe": "/opt/redx/bin/redxd", "cwd": ns["ACCOUNT"] + "/private-secret-cwd",
            "ns_mnt": "mnt:[123]"}
ns["process_brief"] = lambda pid: dict(base_row)
ns["identity"] = lambda path, follow=True: {
    "device": "8:2", "inode": 55, "mode": "0600", "uid": 1001, "gid": 1001, "type": "regular"}


def fake_read(path, limit):
    if "/fdinfo/" in path:
        return "pos:\tprivate-secret-offset\nflags:\t0100002\nmnt_id:\t32\n"
    if path.endswith("/maps"):
        return "1000-2000 rw-s 00000000 08:02 55 " + ns["EXTENSION"] + "/private-secret-record\n"
    if path.endswith("/mountinfo"):
        return "32 31 8:2 / " + ns["EXTENSION"] + " rw - ext4 /private-secret-source rw,password=private-secret-value\n"
    raise AssertionError("unexpected metadata source")


ns["read_limited"] = fake_read
orig_scan, orig_link = ns["os"].scandir, ns["os"].readlink
ns["os"].scandir = lambda path: FakeScan()
ns["os"].readlink = lambda path: ns["EXTENSION"] + "/private-secret-record"
counters = dict.fromkeys(("fdLinksExamined", "fdScanTruncatedProcesses", "matchingFdOutputTruncatedProcesses",
                          "fdLinkRacesOrDenied", "fdDirectoriesUnavailable", "matchingMapOutputTruncatedProcesses",
                          "mapsUnavailableOrTooLarge"), 0)
try:
    row = ns["inspect_process"](12, {12}, {1001}, counters, ns["time"].monotonic() + 3)
finally:
    ns["os"].scandir, ns["os"].readlink = orig_scan, orig_link
assert "private-secret" not in json.dumps(row)
assert row["matchingFds"][0]["access"] == "read-write"
assert row["matchingFds"][0]["mountId"] == 32
assert row["matchingMappings"][0]["sharedWritable"] is True
assert row["identityStableDuringScan"] is True
assert row["exe"]["exactExpectedNativeExecutable"] is True
assert row["cwd"]["pathClass"] == "accounts-namespace"
print(json.dumps({"syntax": "passed", "syntheticMetadataChecks": "passed", "serverCollection": False}))
