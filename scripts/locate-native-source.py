#!/usr/bin/env python3
"""Bounded Linux filename/metadata search; never parse or execute found source."""
import sys
sys.dont_write_bytecode = True
import argparse
import ctypes
import hashlib
import json
import multiprocessing
import os
import signal
import stat
import subprocess
import time
from dataclasses import asdict, dataclass

ROOTS = ("/root", "/opt", "/srv", "/tmp")
PRUNE = frozenset((".git", "node_modules", "data", "dbs", "var", "backups",
                   "media", "uploads", "certs", ".ssh", ".cloudflared"))
REFERENCES = {
    "canonical_accounts_build": "bcc6c62a2f119bd833ba083c12a3799ca622b980",
    "mailrelay_build": "62516086a3821b5ea5d84cbed2968353903de7d4",
    "gdg_build_base_modified": "b3cfa4ff2bac9789b1132d80cc07e34f26ccf9e3",
}
MAX_REPORT_BYTES = 2 << 20


@dataclass(frozen=True)
class Limits:
    entries: int = 100000
    seconds: float = 15
    depth: int = 20
    matches: int = 64
    file_bytes: int = 5 << 20
    total_hash_bytes: int = 16 << 20
    git_repositories: int = 4

    def valid(self):
        return (1 <= self.entries <= 200000 and 1 <= self.seconds <= 30
                and 0 <= self.depth <= 32 and 1 <= self.matches <= 128
                and 0 <= self.file_bytes <= 5 << 20
                and 0 <= self.total_hash_bytes <= 16 << 20
                and 0 <= self.git_repositories <= 8)


class StopScan(Exception):
    pass


def source_kind(parts, include_accounts=False):
    if len(parts) >= 3 and parts[-3:-1] == ("host", "acct") and parts[-1].endswith(".go"):
        return "canonical_account_package"
    if parts[-2:] == ("host", "accounts_native.go"):
        return "canonical_account_adapter"
    if parts[-3:] == ("cmd", "redx-mailrelay", "main.go"):
        return "mailrelay_main"
    if parts[-3:] == ("cmd", "redx-gdg-accounts", "main.go"):
        return "gdg_main"
    if include_accounts and parts[-2:] == ("host", "accounts.go"):
        return "legacy_host_accounts_optional"
    return None


def same_file(a, b):
    return all(getattr(a, key) == getattr(b, key) for key in
               ("st_dev", "st_ino", "st_size", "st_mtime_ns", "st_mode", "st_nlink"))


class NativeFS:
    """Descriptor-relative no-follow operations; the CLI is Linux only."""
    def root(self, path):
        return os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)

    def close(self, handle):
        os.close(handle)

    def own_stat(self, handle):
        return os.fstat(handle)

    def stat(self, handle, name):
        return os.stat(name, dir_fd=handle, follow_symlinks=False)

    def directory(self, handle, name):
        return os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=handle)

    def names(self, handle):
        with os.scandir(handle) as entries:
            for entry in entries:
                yield entry.name

    def file(self, handle, name):
        return os.fdopen(os.open(name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=handle), "rb")


def git_environment():
    # No inherited HOME, credentials, loaders or caller Git configuration.
    return {"PATH": "/usr/bin:/bin", "LANG": "C", "LC_ALL": "C",
            "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_SYSTEM": "/dev/null",
            "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_TERMINAL_PROMPT": "0",
            "GIT_NO_LAZY_FETCH": "1", "GIT_NO_REPLACE_OBJECTS": "1",
            "GIT_OPTIONAL_LOCKS": "0", "GIT_ALLOW_PROTOCOL": "",
            "GIT_CONFIG_COUNT": "3", "GIT_CONFIG_KEY_0": "protocol.allow",
            "GIT_CONFIG_VALUE_0": "never", "GIT_CONFIG_KEY_1": "credential.helper",
            "GIT_CONFIG_VALUE_1": "", "GIT_CONFIG_KEY_2": "core.hooksPath",
            "GIT_CONFIG_VALUE_2": "/dev/null"}


def _git_limits(parent_pid):
    import resource
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    resource.setrlimit(resource.RLIMIT_CPU, (1, 1))
    resource.setrlimit(resource.RLIMIT_AS, (256 << 20, 256 << 20))
    resource.setrlimit(resource.RLIMIT_FSIZE, (0, 0))
    # Do not leave a cat-file process behind if the supervisor kills its worker.
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.prctl(1, signal.SIGKILL, 0, 0, 0) != 0 or os.getppid() != parent_pid:
        os._exit(125)


def probe_git(git_fd, deadline, clock=time.monotonic):
    results = {}
    for label, oid in REFERENCES.items():
        remaining = deadline - clock()
        if remaining <= 0:
            results[label] = {"revision": oid, "status": "not_probed_time_budget"}
            continue
        try:
            parent_pid = os.getpid()
            result = subprocess.run(
                ["/usr/bin/git", "--git-dir=/proc/self/fd/" + str(git_fd),
                 "cat-file", "-e", oid + "^{commit}"],
                stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                env=git_environment(), pass_fds=(git_fd,), close_fds=True,
                timeout=min(1.0, remaining), preexec_fn=lambda: _git_limits(parent_pid), check=False)
            status = "locally_available_commit_object" if result.returncode == 0 else "not_confirmed"
        except (OSError, subprocess.SubprocessError):
            status = "not_confirmed"
        results[label] = {"revision": oid, "status": status}
    return results


def empty_report(limits, sha256=False, vcs=False, include_accounts=False):
    return {"schema": 1, "kind": "bounded-native-source-filename-metadata",
            "policy": {"roots": list(ROOTS), "prunedDirectoryNames": sorted(PRUNE),
                       "symlinks": "never followed", "crossFilesystemDirectories": "pruned",
                       "contents": "not parsed or printed; optional hash of exact matched regular files only",
                       "git": "optional fixed cat-file -e only; local Git metadata may be read; no parity inference",
                       "hashRequested": sha256, "vcsRequested": vcs,
                       "optionalHostAccounts": include_accounts, "limits": asdict(limits),
                       "supervisorWaitSeconds": limits.seconds + 1, "workerTerminationGraceSeconds": .5},
            "roots": [], "matches": [], "repositories": [],
            "stats": {key: 0 for key in ("entries", "directories", "policyPruned", "symlinksPruned",
                       "crossFilesystemPruned", "depthPruned", "pathLengthPruned", "metadataErrors",
                       "hashBytesRead", "gitRepositoriesProbed")},
            "coverage": {"traversalFinishedWithinPolicy": False, "stopReasons": [],
                         "exhaustiveFilesystemSearch": False, "sourceAbsenceProven": False,
                         "sourceParityProven": False, "archivesAndGitHistorySearched": False,
                         "partialWorkerResultsDiscarded": False, "statsAvailable": True,
                         "workerTerminationConfirmed": None}}


def scan(limits, sha256=False, vcs=False, include_accounts=False, *, fs=None,
         clock=time.monotonic, git_probe=probe_git):
    """Exactly one fixed-root traversal; injections are for unit fixtures only."""
    if not limits.valid():
        raise ValueError("invalid_limits")
    fs = fs or NativeFS()
    report = empty_report(limits, sha256, vcs, include_accounts)
    stats, coverage = report["stats"], report["coverage"]
    deadline = clock() + limits.seconds
    repositories = {}

    def stop(reason):
        if reason not in coverage["stopReasons"]:
            coverage["stopReasons"].append(reason)
        raise StopScan()

    def check_time():
        if clock() >= deadline:
            stop("time_budget")

    def marker(handle, name, required=False):
        check_time()
        try:
            return fs.stat(handle, name)
        except FileNotFoundError:
            if required:
                stats["metadataErrors"] += 1
            return None
        except OSError:
            stats["metadataErrors"] += 1
            return None

    def limited_names(handle):
        iterator = iter(fs.names(handle))
        try:
            while True:
                check_time()
                if stats["entries"] >= limits.entries:
                    stop("entry_budget")
                try:
                    name = next(iterator)
                except StopIteration:
                    return
                stats["entries"] += 1
                yield name
        finally:
            close = getattr(iterator, "close", None)
            if close:
                close()

    def git_pointer_guard(handle):
        # Worktree gitfiles are never read. Refuse separate common/object
        # directories and alternates instead of following pointer contents.
        if marker(handle, "commondir") is not None:
            return False
        config = marker(handle, "config")
        if config is not None and not stat.S_ISREG(config.st_mode):
            return False
        opened = []
        current = handle
        try:
            for name in ("objects", "info"):
                info = marker(current, name)
                if info is None:
                    return True
                if not stat.S_ISDIR(info.st_mode):
                    return False
                child = fs.directory(current, name)
                opened.append(child)
                checked = fs.own_stat(child)
                if checked.st_dev != fs.own_stat(handle).st_dev or checked.st_ino != info.st_ino:
                    return False
                current = child
            return all(marker(current, name) is None for name in ("alternates", "http-alternates"))
        except OSError:
            stats["metadataErrors"] += 1
            return False
        finally:
            for item in reversed(opened):
                fs.close(item)

    def hash_match(handle, name, before):
        if not sha256:
            return {"status": "not_requested"}
        if before.st_nlink != 1:
            return {"status": "refused_hardlink"}
        if before.st_size > limits.file_bytes:
            return {"status": "skipped_file_size_cap"}
        if before.st_size > limits.total_hash_bytes - stats["hashBytesRead"]:
            return {"status": "skipped_total_hash_cap"}
        try:
            check_time()
            with fs.file(handle, name) as file:
                opened = fs.own_stat(file.fileno())
                if not same_file(before, opened) or not stat.S_ISREG(opened.st_mode):
                    return {"status": "refused_changed_file"}
                digest, count = hashlib.sha256(), 0
                while True:
                    check_time()
                    remaining = min(limits.file_bytes - count, limits.total_hash_bytes - stats["hashBytesRead"])
                    if remaining == 0:
                        break  # Metadata below verifies exact length without an extra-byte read.
                    chunk = file.read(min(65536, remaining))
                    stats["hashBytesRead"] += len(chunk)
                    count += len(chunk)
                    if not chunk:
                        break
                    digest.update(chunk)
                after = fs.own_stat(file.fileno())
            check_time()
            if count != before.st_size or not same_file(before, after) or not same_file(before, fs.stat(handle, name)):
                return {"status": "refused_changed_file"}
            return {"status": "verified", "sha256": digest.hexdigest(), "bytes": count}
        except OSError:
            stats["metadataErrors"] += 1
            return {"status": "unavailable"}

    def visit(handle, path, depth, root_device, module=None, repository=None):
        check_time()
        stats["directories"] += 1
        mod = marker(handle, "go.mod")
        if mod is not None and stat.S_ISREG(mod.st_mode):
            module = path
        git_handle = None
        try:
            git = marker(handle, ".git")
            if git is not None:
                if stat.S_ISDIR(git.st_mode) and vcs:
                    try:
                        git_handle = fs.directory(handle, ".git")
                        opened = fs.own_stat(git_handle)
                        errors_before = stats["metadataErrors"]
                        valid = (opened.st_dev == root_device and opened.st_ino == git.st_ino
                                 and git_pointer_guard(git_handle) and stats["metadataErrors"] == errors_before)
                        repository = (path, git_handle if valid else None, "directory" if valid else "external_git_pointer_refused")
                    except OSError:
                        stats["metadataErrors"] += 1
                        repository = (path, None, "unavailable")
                else:
                    kind = "directory_not_opened" if stat.S_ISDIR(git.st_mode) else "gitfile_not_followed" if stat.S_ISREG(git.st_mode) else "symlink_or_special_refused"
                    repository = (path, None, kind)
            for name in limited_names(handle):
                check_time()
                target = path + "/" + name
                if len(target.encode("utf-8", "surrogateescape")) > 4096:
                    stats["pathLengthPruned"] += 1
                    continue
                info = marker(handle, name, required=True)
                if info is None:
                    continue
                if stat.S_ISLNK(info.st_mode):
                    stats["symlinksPruned"] += 1
                    continue
                if stat.S_ISDIR(info.st_mode):
                    if name.casefold() in PRUNE:
                        stats["policyPruned"] += 1
                        continue
                    if depth >= limits.depth:
                        stats["depthPruned"] += 1
                        continue
                    if info.st_dev != root_device:
                        stats["crossFilesystemPruned"] += 1
                        continue
                    child = None
                    try:
                        child = fs.directory(handle, name)
                        opened = fs.own_stat(child)
                        if (opened.st_dev, opened.st_ino) != (info.st_dev, info.st_ino):
                            stats["metadataErrors"] += 1
                            continue
                        visit(child, target, depth + 1, root_device, module, repository)
                    except OSError:
                        stats["metadataErrors"] += 1
                    finally:
                        if child is not None:
                            fs.close(child)
                    continue
                kind = source_kind(tuple(target.split("/")), include_accounts)
                if kind is None or not stat.S_ISREG(info.st_mode):
                    continue
                record = {"path": target, "kind": kind, "bytes": info.st_size,
                          "mtimeNs": info.st_mtime_ns, "device": info.st_dev, "inode": info.st_ino,
                          "links": info.st_nlink, "closestGoModDirectory": module,
                          "closestRepositoryDirectory": repository[0] if repository else None,
                          "hash": hash_match(handle, name, info)}
                if repository:
                    repo, git_fd, git_kind = repository
                    record["gitMarker"] = git_kind
                    if vcs and repo not in repositories:
                        status, objects = "git_marker_not_probeable", None
                        if git_fd is not None:
                            if stats["gitRepositoriesProbed"] >= limits.git_repositories:
                                status = "not_probed_repository_cap"
                            else:
                                check_time()
                                objects = git_probe(git_fd, deadline, clock)
                                stats["gitRepositoriesProbed"] += 1
                                status = "object_checks_only"
                        repositories[repo] = {"directory": repo, "status": status, "objects": objects}
                report["matches"].append(record)
                if len(report["matches"]) >= limits.matches:
                    stop("match_budget")
        finally:
            if git_handle is not None:
                fs.close(git_handle)

    stopped = False
    for root in ROOTS:
        summary = {"path": root, "status": "not_started_budget"}
        report["roots"].append(summary)
        if stopped:
            continue
        handle = None
        try:
            check_time()
            handle = fs.root(root)
            info = fs.own_stat(handle)
            if not stat.S_ISDIR(info.st_mode):
                summary["status"] = "not_a_directory"
                continue
            visit(handle, root, 0, info.st_dev)
            summary["status"] = "walked_with_policy_filters"
        except StopScan:
            summary["status"] = "stopped_budget"
            stopped = True
        except FileNotFoundError:
            summary["status"] = "missing"
        except OSError:
            summary["status"] = "unavailable_or_symlink_refused"
            stats["metadataErrors"] += 1
        finally:
            if handle is not None:
                fs.close(handle)
    report["repositories"] = list(repositories.values())
    coverage["traversalFinishedWithinPolicy"] = (not stopped and not any(stats[x] for x in
        ("metadataErrors", "crossFilesystemPruned", "depthPruned", "pathLengthPruned"))
        and all(x["status"] in ("walked_with_policy_filters", "missing") for x in report["roots"]))
    return report


def _worker(connection, kwargs):
    try:
        import resource
        resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
        resource.setrlimit(resource.RLIMIT_FSIZE, (0, 0))
        connection.send(scan(**kwargs))
    except BaseException:
        try:
            connection.send(None)
        except BaseException:
            pass  # Supervisor may already have closed its end; never print raw errors.
    finally:
        connection.close()


def supervised_scan(limits, **options):
    if not limits.valid():
        raise ValueError("invalid_limits")
    receive, send = multiprocessing.get_context("fork").Pipe(duplex=False)
    worker = multiprocessing.get_context("fork").Process(target=_worker, args=(send, {"limits": limits, **options}))
    worker.start()
    send.close()
    report = None
    try:
        if receive.poll(limits.seconds + 1):
            try:
                report = receive.recv()
            except (EOFError, OSError):
                pass
    except (EOFError, OSError):
        pass
    finally:
        if report is None:
            report = empty_report(limits, **options)
            report["coverage"]["stopReasons"] = ["worker_timeout_or_failure"]
            report["coverage"]["partialWorkerResultsDiscarded"] = True
            report["coverage"]["statsAvailable"] = False
            report["stats"] = {key: None for key in report["stats"]}
            report["roots"] = [{"path": root, "status": "unknown_worker_timeout_or_failure"} for root in ROOTS]
        receive.close()
        if worker.is_alive():
            worker.terminate()
        worker.join(.25)
        if worker.is_alive():
            worker.kill()
            worker.join(.25)
        report["coverage"]["workerTerminationConfirmed"] = not worker.is_alive()
    return report


def private_stdout():
    try:
        info = os.fstat(1)
        return (stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and info.st_uid == os.geteuid()
                and info.st_mode & 0o077 == 0)
    except OSError:
        return False


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise ValueError("invalid_arguments")


def main(argv=None):
    if not sys.platform.startswith("linux"):
        sys.stderr.write("Source locator requires Linux descriptor-relative access.\n")
        return 2
    if not private_stdout():
        sys.stderr.write("Source locator requires a private regular stdout file.\n")
        return 2
    parser = Parser(add_help=False)
    parser.add_argument("--sha256", action="store_true")
    parser.add_argument("--vcs", action="store_true")
    parser.add_argument("--include-legacy-host-accounts", action="store_true")
    for field, kind in (("entries", int), ("seconds", float), ("depth", int), ("matches", int),
                        ("file-bytes", int), ("total-hash-bytes", int), ("git-repositories", int)):
        parser.add_argument("--max-" + field, type=kind)
    try:
        args = vars(parser.parse_args(argv))
        values = {name: args["max_" + name] for name in asdict(Limits()) if args["max_" + name] is not None}
        limits = Limits(**values)
        if not limits.valid():
            raise ValueError("invalid_limits")
        report = supervised_scan(limits, sha256=args["sha256"], vcs=args["vcs"], include_accounts=args["include_legacy_host_accounts"])
        data = json.dumps(report, ensure_ascii=True, separators=(",", ":"))
        if len(data.encode("ascii")) > MAX_REPORT_BYTES:
            raise ValueError("report_size_cap")
        sys.stdout.write(data + "\n")
        sys.stdout.flush()
        if report["coverage"].get("workerTerminationConfirmed") is False:
            # Avoid multiprocessing's unbounded atexit join if a killed worker
            # remains stuck in an uninterruptible kernel filesystem operation.
            os._exit(2)
        return 0 if report["coverage"]["traversalFinishedWithinPolicy"] else 1
    except BaseException:
        sys.stdout.write('{"schema":1,"kind":"bounded-native-source-filename-metadata","error":"locator_refused","coverage":{"sourceAbsenceProven":false,"sourceParityProven":false,"exhaustiveFilesystemSearch":false,"traversalFinishedWithinPolicy":false}}\n')
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
