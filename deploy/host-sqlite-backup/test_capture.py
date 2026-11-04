"""Owned synthetic fixtures only; no production database or configuration input."""
import contextlib
import copy
from datetime import datetime, timezone
import importlib.util
import io
import json
import os
from pathlib import Path
import platform
import posixpath
import select
import shutil
import sqlite3
import stat
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

HELPER = Path(__file__).with_name("capture.py")
SPEC = importlib.util.spec_from_file_location("host_sqlite_capture_under_test", HELPER)
capture = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = capture
SPEC.loader.exec_module(capture)
SCHEMA = "redx.host-sqlite-backup.v1"
LINUX_ROOT = sys.platform.startswith("linux") and hasattr(os, "geteuid") and os.geteuid() == 0

def expectations():
    return {"version":1,"source":{"path":"/tmp/owned-host-ledger/ledger.sqlite3","device":1,"inode":100},"host":{"boot_id":"12345678-1234-1234-1234-123456789abc","pid":123,"start_ticks":4567,"exe":{"path":"/usr/bin/python3","device":1,"inode":200},"cwd":{"path":"/tmp/owned-host-ledger","device":1,"inode":300},"ledger_fd":{"number":4,"device":1,"inode":100}},"reader":{"python":"3.13.7","sqlite":"3.50.4"}}

def encoded(value):
    return json.dumps(value,ensure_ascii=False,separators=(",",":")).encode("utf-8")

def proc_line(comm="host worker",start="987654321"):
    tail=[str(index) for index in range(4,53)]
    tail[22-4]=start
    return "123 ("+comm+") S "+" ".join(tail)+"\n"

class ParseTests(unittest.TestCase):
    def test_capture_switch_cannot_be_abbreviated_before_any_input_access(self):
        base=["--expectations","/not-opened/pins.json","--output-dir","/not-created/bundle","--max-output-bytes","1048576","--reserve-free-bytes","0","--deadline-seconds","10"]
        for switch in ("--cap","--capt"):
            with self.subTest(switch=switch), mock.patch.object(capture,"run") as run:
                output=io.StringIO()
                with contextlib.redirect_stdout(output):
                    result=capture.main(base+[switch])
                self.assertEqual(result,1)
                self.assertEqual(json.loads(output.getvalue()),{"schema":SCHEMA,"status":"refused","code":"invalid_arguments","cleanup_warning":False})
                run.assert_not_called()

    def test_unavailable_sqlite_module_is_refused_before_any_input_file_access(self):
        with mock.patch.object(capture,"sqlite3",None), mock.patch.object(capture.sys,"platform","linux"), mock.patch.object(capture.os,"geteuid",return_value=0,create=True), mock.patch.object(capture,"load_expectations") as load:
            self.assertEqual(capture.runtime_info()["sqlite"],"unavailable")
            with self.assertRaises(capture.Refusal) as refused:
                capture.run(SimpleNamespace())
            self.assertEqual(refused.exception.code,"unsupported_reader")
            load.assert_not_called()

    def test_proc_start_ticks_handles_parentheses_in_comm(self):
        for comm in ["host worker","host ) worker (draft)","odd))) ((name"]:
            with self.subTest(comm=comm):
                self.assertEqual(capture.parse_proc_start_ticks(proc_line(comm)),987654321)

    def test_malformed_or_negative_proc_start_ticks_are_refused(self):
        for text in ["","123 host S 1 2","123 (host) S 1 2",proc_line(start="invalid"),proc_line(start="-1")]:
            with self.subTest(text=text[:35]):
                with self.assertRaises(capture.Refusal):
                    capture.parse_proc_start_ticks(text)

    def test_exact_expectations_round_trip(self):
        value=expectations()
        self.assertEqual(capture.parse_expectations(encoded(value)),value)

    def test_unknown_missing_or_mistyped_fields_are_refused(self):
        changes=[]
        for keys,value in [(("unexpected",),True),(("host","exe","token"),"not-a-real-secret"),(("version",),True),(("version",),2),(("source","device"),True),(("source","inode"),100.0),(("host","pid"),0),(("host","start_ticks"),-1),(("host","ledger_fd","number"),-1),(("source","path"),"relative/ledger.sqlite3"),(("source","path"),"/tmp/ledger\x00.sqlite3"),(("host","boot_id"),"not-a-boot-id"),(("reader","sqlite"),"latest")]:
            bad=expectations(); target=bad
            for key in keys[:-1]:target=target[key]
            target[keys[-1]]=value; changes.append(bad)
        bad=expectations();del bad["source"]["inode"];changes.append(bad)
        for value in changes:
            with self.subTest(value=value):
                with self.assertRaises(capture.Refusal):
                    capture.parse_expectations(encoded(value))

    def test_oversize_duplicate_nonfinite_and_non_json_input_are_refused(self):
        value=encoded(expectations())
        duplicate=value.replace(b'"version":1',b'"version":1,"version":1',1)
        nonfinite=value.replace(b'"device":1',b'"device":NaN',1)
        for data in [b" "*(16*1024+1),duplicate,nonfinite,b"[]",b"null",b"{",b"\xff"]:
            with self.subTest(data=data[:50]):
                with self.assertRaises(capture.Refusal):capture.parse_expectations(data)

    def test_runtime_pins_describe_this_reader(self):
        info=capture.runtime_info()
        self.assertEqual(set(info),{"python","sqlite"})
        self.assertEqual(info["python"],platform.python_version())
        self.assertEqual(info["sqlite"],sqlite3.sqlite_version)
        for value in info.values():self.assertRegex(value,r"^[0-9]+\.[0-9]+\.[0-9]+$")

class ExpectationReadTests(unittest.TestCase):
    def info(self,size):
        return SimpleNamespace(st_mode=stat.S_IFREG|0o600,st_uid=0,st_gid=0,st_nlink=1,st_dev=1,st_ino=77,st_size=size,st_mtime_ns=100,st_ctime_ns=100)

    def load(self,payload,before,after):
        with contextlib.ExitStack() as stack:
            stack.enter_context(mock.patch.object(capture,"trusted_directories"))
            stack.enter_context(mock.patch.object(capture.os.path,"realpath",side_effect=lambda path:path))
            stack.enter_context(mock.patch.object(capture.os,"O_NOFOLLOW",0x200000,create=True))
            stack.enter_context(mock.patch.object(capture.os,"O_NONBLOCK",0x400000,create=True))
            opened=stack.enter_context(mock.patch.object(capture.os,"open",return_value=73))
            stack.enter_context(mock.patch.object(capture.os,"fstat",side_effect=[before,after]))
            read=stack.enter_context(mock.patch.object(capture.os,"read",return_value=payload))
            closed=stack.enter_context(mock.patch.object(capture.os,"close"))
            try:return capture.load_expectations("/owned/expectations.json")
            finally:
                opened.assert_called_once();read.assert_called_once_with(73,capture.MAX_JSON+1);closed.assert_called_once_with(73)

    def test_stable_full_read_is_accepted_and_same_fd_closed(self):
        import hashlib
        payload=encoded(expectations());before=self.info(len(payload))
        self.assertEqual(self.load(payload,before,copy.copy(before)),(expectations(),hashlib.sha256(payload).hexdigest()))

    def test_short_valid_json_prefix_with_larger_initial_size_is_refused(self):
        payload=encoded(expectations());before=self.info(len(payload)+20)
        with self.assertRaises(capture.Refusal) as raised:self.load(payload,before,copy.copy(before))
        self.assertEqual(raised.exception.code,"invalid_expectations")

    def test_pin_file_metadata_drift_during_read_is_refused(self):
        payload=encoded(expectations());before=self.info(len(payload))
        for field in ["st_dev","st_ino","st_size","st_mode","st_uid","st_gid","st_nlink","st_mtime_ns","st_ctime_ns"]:
            after=copy.copy(before);setattr(after,field,getattr(after,field)+1)
            with self.subTest(field=field),self.assertRaises(capture.Refusal) as raised:self.load(payload,before,after)
            self.assertEqual(raised.exception.code,"invalid_expectations")

WRITER=r'''
import json, os, pathlib, sqlite3, sys, threading, time
path=pathlib.Path(sys.argv[1]); mode=sys.argv[2]
connection=None; thread=None; stop=threading.Event(); request=threading.Event(); done=threading.Event(); errors=[]; lock=threading.Lock(); commits=0; last_commit_ns=0
if mode=='invalid':
    path.write_bytes(b'NOT_SQLITE_SYNTHETIC_DO_NOT_LEAK_ROW_CONTENT'*20)
else:
    connection=sqlite3.connect(str(path),isolation_level=None,check_same_thread=False)
    connection.execute('PRAGMA journal_mode=WAL')
    connection.execute('PRAGMA wal_autocheckpoint=0')
    connection.execute('CREATE TABLE pairs(id INTEGER PRIMARY KEY,a INTEGER NOT NULL,b INTEGER NOT NULL,payload TEXT NOT NULL)')
    connection.execute('CREATE TABLE markers(name TEXT PRIMARY KEY)')
    connection.execute('BEGIN')
    connection.executemany('INSERT INTO pairs VALUES(?,?,?,?)',((i,0,0,'x'*1024) for i in range(1,2049)))
    connection.execute("INSERT INTO markers VALUES('committed')")
    connection.execute('COMMIT')
anchor=os.open(path,os.O_RDONLY)
def commit_pair():
    global commits,last_commit_ns
    if commits>=(1<<32)-1:raise RuntimeError('synthetic_writer_counter_limit')
    n=commits+1;connection.execute('BEGIN IMMEDIATE')
    connection.execute('UPDATE pairs SET a=? WHERE id=?',(n,(n%2048)+1));time.sleep(0.0002)
    connection.execute('UPDATE pairs SET b=? WHERE id=?',(n,(n%2048)+1));connection.execute('COMMIT')
    commits=n;last_commit_ns=time.time_ns()
def status():
    return {'commits':commits,'last_commit_ns':last_commit_ns,'errors':errors[:1],'running':bool(thread and thread.is_alive())}
if mode=='dirty':
    connection.execute('BEGIN IMMEDIATE')
    connection.execute("INSERT INTO markers VALUES('uncommitted')")
    connection.execute('UPDATE pairs SET a=999 WHERE id=1')
elif mode=='busy':
    def update():
        try:
            while not stop.is_set():
                if not request.wait(0.1):continue
                request.clear()
                if stop.is_set():break
                with lock:commit_pair()
                done.set()
        except BaseException:
            with lock:
                if not errors:errors.append('writer_failed')
            done.set()
    thread=threading.Thread(target=update);thread.start()
print(json.dumps({'fd':anchor,'pid':os.getpid()}),flush=True)
try:
    for line in sys.stdin:
        if line.strip()=='report':
            with lock:report={'journal_mode':connection.execute('PRAGMA journal_mode').fetchone()[0],'page_size':connection.execute('PRAGMA page_size').fetchone()[0]}
            print(json.dumps(report),flush=True)
        elif line.strip() in {'status','commit'}:
            if line.strip()=='commit':
                done.clear();request.set()
                if not done.wait(5):
                    with lock:
                        if not errors:errors.append('writer_failed')
            with lock:
                report=status()
            print(json.dumps(report),flush=True)
        elif line.strip()=='stop':break
finally:
    stop.set()
    if thread:thread.join(5)
    if connection:
        if connection.in_transaction:connection.rollback()
        connection.close()
    os.close(anchor)
    if errors or (thread and thread.is_alive()):sys.exit(3)
'''

NO_SQLITE_WRAPPER='''
import runpy, sqlite3, sys
def forbidden(*args, **kwargs):
    raise AssertionError('sqlite_open_forbidden_in_inspection')
sqlite3.connect=forbidden
sys.argv=sys.argv[1:]
runpy.run_path(sys.argv[0],run_name='__main__')
'''

COORDINATED_WRAPPER='''
import os, runpy, select, sqlite3, sys
helper,ready,release=sys.argv[1],int(sys.argv[2]),int(sys.argv[3])
connect=sqlite3.connect
class ObservedConnection(sqlite3.Connection):
    def backup(self,target,**options):
        original=options.get('progress');paused=False
        def progress(status,remaining,total):
            nonlocal paused
            if not paused and remaining>0:
                paused=True;os.write(ready,b'R')
                if not select.select([release],[],[],10)[0] or os.read(release,1)!=b'C':raise RuntimeError('synthetic_coordination_failed')
            if original:original(status,remaining,total)
        options['progress']=progress
        return super().backup(target,**options)
def observed_connect(*args,**options):
    options['factory']=ObservedConnection
    return connect(*args,**options)
sqlite3.connect=observed_connect
sys.argv=[helper]+sys.argv[4:]
runpy.run_path(helper,run_name='__main__')
'''

@unittest.skipUnless(LINUX_ROOT,"Linux/root synthetic process and SQLite fixtures required")
class LinuxCaptureTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory(prefix="owned-host-sqlite-tests-")
        self.base=Path(self.temp.name).resolve()
        self.source_dir=self.base/"DO_NOT_LEAK_source_%?#_unicode_\u03c0"
        self.source_dir.mkdir(mode=0o700)
        self.source=self.source_dir/"ledger_%?#.sqlite3"
        self.processes=[];self.counter=0

    def tearDown(self):
        failures=[]
        for process in self.processes:
            if process.poll() is None:
                try:
                    process.stdin.write("stop\n");process.stdin.flush();process.communicate(timeout=8)
                except (BrokenPipeError,subprocess.TimeoutExpired):
                    process.kill();process.communicate(timeout=5)
                    failures.append("writer_shutdown_failed")
            else:process.communicate(timeout=5)
            if process.returncode!=0:failures.append("writer_exit_not_clean")
        self.temp.cleanup()
        self.assertEqual(failures,[],"the owned writer must not silently fail")

    def read_json(self,process):
        ready,_,_=select.select([process.stdout],[],[],10)
        self.assertTrue(ready,"synthetic writer did not report readiness")
        line=process.stdout.readline(1025)
        self.assertTrue(line,"synthetic writer exited before readiness")
        self.assertLessEqual(len(line),1024);self.assertTrue(line.endswith("\n"))
        return json.loads(line)

    def assert_writer_status(self,value):
        self.assertEqual(set(value),{"commits","last_commit_ns","errors","running"})
        self.assertIs(type(value["commits"]),int);self.assertGreaterEqual(value["commits"],0);self.assertLess(value["commits"],1<<32)
        self.assertIs(type(value["last_commit_ns"]),int);self.assertGreaterEqual(value["last_commit_ns"],0);self.assertLess(value["last_commit_ns"],1<<64)
        self.assertEqual(value["errors"],[]);self.assertIs(type(value["running"]),bool);self.assertTrue(value["running"])

    def writer(self,mode="dirty"):
        process=subprocess.Popen([sys.executable,"-c",WRITER,str(self.source),mode],cwd=self.base,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
        self.processes.append(process);info=self.read_json(process)
        self.assertEqual(info["pid"],process.pid);self.assertNotEqual(process.pid,os.getpid())
        root=Path("/proc")/str(process.pid);source_info=self.source.stat()
        exe=Path(os.readlink(root/"exe"));cwd=Path(os.readlink(root/"cwd"))
        exe_info,cwd_info=exe.stat(),cwd.stat()
        start_fields=(root/"stat").read_text().rsplit(") ",1)[1].split()
        value={"version":1,"source":{"path":str(self.source),"device":source_info.st_dev,"inode":source_info.st_ino},"host":{"boot_id":Path("/proc/sys/kernel/random/boot_id").read_text().strip(),"pid":process.pid,"start_ticks":int(start_fields[19]),"exe":{"path":str(exe),"device":exe_info.st_dev,"inode":exe_info.st_ino},"cwd":{"path":str(cwd),"device":cwd_info.st_dev,"inode":cwd_info.st_ino},"ledger_fd":{"number":info["fd"],"device":source_info.st_dev,"inode":source_info.st_ino}},"reader":{"python":platform.python_version(),"sqlite":sqlite3.sqlite_version}}
        return process,value

    def invoke(self,value,*,capture_now=False,output=None,limits=None,forbid_sqlite=False,coordinate=None,mutate_expectations=None):
        self.counter+=1;expectation_path=self.base/("expectations-"+str(self.counter)+".json")
        expectation_path.write_bytes(encoded(value));expectation_path.chmod(0o600)
        if mutate_expectations:
            replacement=mutate_expectations(expectation_path)
            if replacement is not None:expectation_path=replacement
        output=output or self.base/("bundle-"+str(self.counter))
        args=[sys.executable,str(HELPER),"--expectations",str(expectation_path),"--output-dir",str(output),"--max-output-bytes",str(64*1024*1024),"--reserve-free-bytes",str(1024*1024),"--deadline-seconds","10"]
        for name,number in (limits or {}).items():args[args.index("--"+name)+1]=str(number)
        if capture_now:args.append("--capture")
        if forbid_sqlite:args=[sys.executable,"-c",NO_SQLITE_WRAPPER]+args[1:]
        if coordinate is None:result=subprocess.run(args,capture_output=True,text=True,timeout=20)
        else:
            ready_read,ready_write=os.pipe();release_read,release_write=os.pipe();child=None
            try:
                coordinated=[sys.executable,"-c",COORDINATED_WRAPPER,str(HELPER),str(ready_write),str(release_read)]+args[2:]
                child=subprocess.Popen(coordinated,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,pass_fds=(ready_write,release_read))
                os.close(ready_write);ready_write=None;os.close(release_read);release_read=None
                ready,_,_=select.select([ready_read],[],[],10)
                self.assertTrue(ready,"backup did not reach a nonfinal step")
                self.assertEqual(os.read(ready_read,1),b"R","backup ended without an observed nonfinal step")
                coordinate.stdin.write("status\n");coordinate.stdin.flush();before=self.read_json(coordinate)
                self.assert_writer_status(before)
                coordinate.stdin.write("commit\n");coordinate.stdin.flush();receipt=self.read_json(coordinate)
                self.assert_writer_status(receipt)
                self.assertGreater(receipt["commits"],before["commits"])
                self.assertLess(receipt["commits"],1<<53);self.last_overlap=receipt
                os.write(release_write,b"C")
                stdout,stderr=child.communicate(timeout=20)
                result=subprocess.CompletedProcess(coordinated,child.returncode,stdout,stderr)
            finally:
                for fd in (ready_read,ready_write,release_read,release_write):
                    if fd is not None:os.close(fd)
                if child is not None and child.poll() is None:child.kill();child.communicate(timeout=5)
        self.assertEqual(result.stderr,"");self.assertEqual(len(result.stdout.splitlines()),1)
        payload=json.loads(result.stdout)
        self.assertEqual(set(payload),{"schema","status","code","cleanup_warning"});self.assertEqual(payload["schema"],SCHEMA)
        self.assertIs(type(payload["cleanup_warning"]),bool)
        self.assertIn(payload["status"],{"inspected","complete","refused"})
        self.assertRegex(payload["code"],r"^[A-Za-z][A-Za-z0-9_.-]{0,63}$")
        for forbidden in [str(self.source),"DO_NOT_LEAK","NOT_SQLITE_SYNTHETIC","Traceback","OperationalError"]:self.assertNotIn(forbidden,result.stdout+result.stderr)
        return result,payload,output

    def assert_refused(self,result,payload,output):
        self.assertEqual(result.returncode,1);self.assertEqual(payload["status"],"refused")
        self.assertFalse((output/"COMPLETE").exists())

    def assert_complete(self,result,payload,output):
        self.assertEqual(result.returncode,0);self.assertEqual(payload["status"],"complete")
        self.assertEqual(stat.S_IMODE(output.stat().st_mode),0o700)
        self.assertTrue((output/"COMPLETE").is_file());self.assertFalse((output/"INCOMPLETE").exists())
        report=json.loads((output/"report.json").read_text())
        import hashlib
        artifact=output/"ledger.sqlite3"
        self.assertEqual(report["artifact"]["bytes"],artifact.stat().st_size)
        self.assertEqual(report["artifact"]["sha256"],hashlib.sha256(artifact.read_bytes()).hexdigest())
        self.assertEqual((output/"COMPLETE").read_text().strip(),hashlib.sha256((output/"report.json").read_bytes()).hexdigest())
        for path in output.iterdir():
            self.assertTrue(path.is_file());self.assertEqual(stat.S_IMODE(path.stat().st_mode),0o600)
        for suffix in ["-wal","-shm","-journal"]:self.assertFalse((output/("ledger.sqlite3"+suffix)).exists())

    def test_inspection_opens_no_sqlite_or_output(self):
        process,value=self.writer();before={path.name for path in self.source_dir.iterdir()}
        result,payload,output=self.invoke(value,forbid_sqlite=True)
        self.assertEqual(result.returncode,0);self.assertEqual(payload["status"],"inspected")
        self.assertFalse(output.exists());self.assertEqual({path.name for path in self.source_dir.iterdir()},before)
        self.assertIsNone(process.poll())

    def test_committed_wal_is_private_standalone_and_excludes_uncommitted_content(self):
        process,value=self.writer();process.stdin.write("report\n");process.stdin.flush();before=self.read_json(process)
        self.assertGreater(Path(str(self.source)+"-wal").stat().st_size,0)
        result,payload,output=self.invoke(value,capture_now=True);self.assert_complete(result,payload,output)
        process.stdin.write("report\n");process.stdin.flush();self.assertEqual(self.read_json(process),before)
        self.assertIsNone(process.poll())
        with contextlib.closing(sqlite3.connect((output/"ledger.sqlite3").as_uri()+"?mode=ro",uri=True)) as connection:
            self.assertEqual(connection.execute("PRAGMA integrity_check").fetchall(),[("ok",)])
            self.assertEqual(connection.execute("SELECT name FROM markers ORDER BY name").fetchall(),[("committed",)])
            self.assertEqual(connection.execute("SELECT count(*) FROM pairs").fetchone()[0],2048)
            self.assertEqual(connection.execute("SELECT count(*) FROM pairs WHERE a<>b").fetchone()[0],0)
            self.assertEqual(connection.execute("PRAGMA journal_mode").fetchone()[0],"delete")

    def test_concurrent_writer_keeps_transaction_pairs_consistent(self):
        process,value=self.writer("busy")
        result,payload,output=self.invoke(value,capture_now=True,coordinate=process);self.assert_complete(result,payload,output)
        self.assertIsNone(process.poll())
        interval=json.loads((output/"report.json").read_text())["backup_interval"]
        def nanoseconds(value):
            delta=datetime.fromisoformat(value).astimezone(timezone.utc)-datetime(1970,1,1,tzinfo=timezone.utc)
            return (delta.days*86400+delta.seconds)*1_000_000_000+delta.microseconds*1000
        self.assertLessEqual(nanoseconds(interval["started"]),self.last_overlap["last_commit_ns"])
        self.assertLessEqual(self.last_overlap["last_commit_ns"],nanoseconds(interval["finished"]))
        process.stdin.write("status\n");process.stdin.flush();after=self.read_json(process)
        self.assert_writer_status(after);self.assertGreaterEqual(after["commits"],self.last_overlap["commits"])
        with contextlib.closing(sqlite3.connect((output/"ledger.sqlite3").as_uri()+"?mode=ro",uri=True)) as connection:
            self.assertEqual(connection.execute("PRAGMA integrity_check").fetchall(),[("ok",)])
            self.assertEqual(connection.execute("SELECT count(*) FROM pairs WHERE a<>b").fetchone()[0],0)

    def test_identity_and_reader_pins_are_checked(self):
        _,value=self.writer()
        changes=[(("source","device"),100000),(("source","inode"),100000),(("host","start_ticks"),100000),(("host","exe","device"),100000),(("host","exe","inode"),100000),(("host","cwd","inode"),100000),(("host","ledger_fd","number"),100000),(("host","ledger_fd","device"),100000),(("host","ledger_fd","inode"),100000)]
        for keys,increment in changes:
            bad=copy.deepcopy(value);target=bad
            for key in keys[:-1]:target=target[key]
            target[keys[-1]]+=increment
            if keys[0]=="source":bad["host"]["ledger_fd"][keys[-1]]=target[keys[-1]]
            if keys[:2]==("host","ledger_fd") and keys[-1] in {"device","inode"}:bad["source"][keys[-1]]=target[keys[-1]]
            with self.subTest(keys=keys):
                result,payload,output=self.invoke(bad,capture_now=True);self.assert_refused(result,payload,output)
                if keys[0]=="source" or (keys[:2]==("host","ledger_fd") and keys[-1] in {"device","inode"}):self.assertEqual(payload["code"],"source_identity_changed")
        for keys,changed in [(("host","boot_id"),"00000000-0000-0000-0000-000000000000"),(("host","pid"),2147483647),(("host","exe","path"),"/nonexistent/synthetic-exe"),(("host","cwd","path"),str(self.source_dir)),(("reader","python"),"0.0.0"),(("reader","sqlite"),"0.0.0")]:
            bad=copy.deepcopy(value);target=bad
            for key in keys[:-1]:target=target[key]
            target[keys[-1]]=changed
            with self.subTest(keys=keys):self.assert_refused(*self.invoke(bad,capture_now=True))

    def test_existing_output_is_never_overwritten(self):
        _,value=self.writer();output=self.base/"existing-bundle";output.mkdir(mode=0o700)
        marker=output/"keep.txt";marker.write_text("owned existing fixture");marker.chmod(0o600)
        self.assert_refused(*self.invoke(value,capture_now=True,output=output))
        self.assertEqual(marker.read_text(),"owned existing fixture")
        self.assertEqual({path.name for path in output.iterdir()},{"keep.txt"})

    def test_output_size_space_and_deadline_caps_refuse_without_success(self):
        _,value=self.writer()
        for limits in [{"max-output-bytes":65536},{"reserve-free-bytes":shutil.disk_usage(self.base).free+1024*1024},{"deadline-seconds":0.000001}]:
            with self.subTest(limits=limits):self.assert_refused(*self.invoke(value,capture_now=True,limits=limits))

    def test_source_symlink_is_refused(self):
        _,value=self.writer();alias=self.source_dir/"alias.sqlite3";alias.symlink_to(self.source)
        value["source"]["path"]=str(alias);self.assert_refused(*self.invoke(value,capture_now=True))

    def test_invalid_sqlite_is_sanitized_and_never_complete(self):
        _,value=self.writer("invalid");self.assert_refused(*self.invoke(value,capture_now=True))

    def test_expectation_file_modes_and_links_are_refused(self):
        _,value=self.writer()
        def public_mode(path):path.chmod(0o644)
        def symlink(path):
            alias=path.with_name(path.stem+"-alias.json");alias.symlink_to(path);return alias
        def hardlink(path):
            alias=path.with_name(path.stem+"-hardlink.json");os.link(path,alias);return alias
        for mutation in [public_mode,symlink,hardlink]:
            with self.subTest(mutation=mutation.__name__):self.assert_refused(*self.invoke(value,capture_now=True,mutate_expectations=mutation))

    def test_source_hardlinks_and_group_write_are_refused(self):
        _,value=self.writer();alias=self.source_dir/"hardlink.sqlite3";os.link(self.source,alias)
        try:
            result,payload,output=self.invoke(value,capture_now=True);self.assert_refused(result,payload,output);self.assertEqual(payload["code"],"source_identity_changed")
        finally:alias.unlink()
        self.source.chmod(0o620)
        result,payload,output=self.invoke(value,capture_now=True);self.assert_refused(result,payload,output);self.assertEqual(payload["code"],"unsafe_namespace")

    def test_immediate_source_parent_must_be_protected_even_if_root_owned_and_sticky(self):
        _,value=self.writer();self.source_dir.chmod(0o1777)
        try:
            self.assertEqual(self.source_dir.stat().st_uid,0)
            result,payload,output=self.invoke(value,capture_now=True);self.assert_refused(result,payload,output)
            self.assertEqual(payload["code"],"unsafe_namespace")
        finally:self.source_dir.chmod(0o700)

class MockCapture:
    """No real SQLite connection or OS descriptor is opened by this harness."""
    def __init__(self,failure=None):
        self.failure=failure;self.events=[];self.files={};self.created=False;self.sync_failed=False
        self.output="/owned/bundle";self.parent="/owned";self.payload=b"synthetic artifact bytes"
        self.value=expectations();self.value["reader"]=capture.runtime_info()
        self.args=SimpleNamespace(expectations="/owned/expectations.json",output_dir=self.output,max_output_bytes="65536",reserve_free_bytes="0",deadline_seconds="10",capture=True)
        def info(mode,inode,size=0):return SimpleNamespace(st_mode=mode,st_uid=0,st_nlink=1,st_dev=1,st_ino=inode,st_size=size,st_mtime_ns=0,st_ctime_ns=0)
        self.parent_info=info(stat.S_IFDIR|0o700,300);self.bundle_info=info(stat.S_IFDIR|0o700,301)
        self.source_info=info(stat.S_IFREG|0o600,100,4096);self.artifact_info=info(stat.S_IFREG|0o600,101,len(self.payload))
        self.names={10:"parent",11:"bundle",12:"anchor",13:"artifact"};self.next_fd=20;self.read_done=False
        self.connections=[]

    def open(self,path,*args,**kwargs):
        if path==self.parent:fd=10
        elif path=="bundle":fd=11
        elif path==self.value["source"]["path"]:fd=12
        elif path=="ledger.sqlite3":fd=13;self.files[path]=b""
        else:
            if path in self.files:raise FileExistsError()
            fd=self.next_fd;self.next_fd+=1;self.names[fd]=path;self.files[path]=b""
        self.events.append(("open",self.names[fd]));return fd

    def fstat(self,fd):
        return {10:self.parent_info,11:self.bundle_info,12:self.source_info,13:self.artifact_info}.get(fd,self.artifact_info)

    def stat(self,path,*args,**kwargs):
        if path in {"/",self.parent}:return self.parent_info
        if path==self.output:return self.bundle_info
        if path==self.output+"/ledger.sqlite3":
            if self.failure=="artifact_replaced" and ("close","artifact") in self.events:
                changed=copy.copy(self.artifact_info);changed.st_ino+=1;return changed
            return self.artifact_info
        if path in self.files:return self.artifact_info
        raise FileNotFoundError()

    def mkdir(self,*args,**kwargs):self.created=True
    def close(self,fd):
        self.events.append(("close",self.names[fd]))
        if (self.failure=="artifact_close" and fd==13) or (self.failure=="anchor_close" and fd==12):raise OSError("synthetic raw close detail")
        if (self.failure=="bundle_close" and fd==11) or (self.failure=="parent_close" and fd==10):raise OSError("synthetic directory close detail")
    def unlink(self,path,**kwargs):
        if path not in self.files:raise FileNotFoundError()
        del self.files[path]
    def lexists(self,path):return self.created if path==self.output else False
    def verify(self,value):
        self.events.append(("verify","identity"))
        if ("backup","started") in self.events and self.failure in {"source_change","pid_change"}:
            raise capture.Refusal("source_identity_changed" if self.failure=="source_change" else "host_identity_changed")
        return self.source_info
    def monotonic(self):return 11 if self.failure=="deadline" and ("backup","started") in self.events else 0
    def space(self,*args):
        enough=not (self.failure=="space" and ("backup","started") in self.events)
        return SimpleNamespace(f_bavail=1<<40 if enough else 0,f_frsize=4096,f_favail=1000)
    def fsync(self,fd):
        self.events.append(("fsync",self.names[fd]))
        fail=(self.failure=="fsync_artifact" and fd==13) or (self.failure=="fsync_complete" and fd==11 and "COMPLETE" in self.files)
        if fail and not self.sync_failed:self.sync_failed=True;raise OSError("synthetic detail must not leak")
    def read(self,fd,size):
        if self.read_done:return b""
        self.read_done=True;return self.payload
    def fdopen(self,fd,*args,**kwargs):
        fixture=self
        class Stream:
            def __enter__(self):return self
            def __exit__(self,*args):return False
            def write(self,payload):fixture.files[fixture.names[fd]]+=payload
            def flush(self):pass
        return Stream()

    def connect(self,database,**kwargs):
        label=["source","target","validation"][len(self.connections)]
        fixture=self
        class Connection:
            in_transaction=False
            def execute(self,sql):
                fixture.events.append((label,sql))
                if label=="validation" and sql=="PRAGMA integrity_check" and fixture.failure=="validation_interrupt":raise KeyboardInterrupt()
                if sql=="PRAGMA page_size":rows=[(4096,)]
                elif sql=="PRAGMA page_count":rows=[(1,)]
                elif sql=="PRAGMA integrity_check":rows=[("ok",)]
                else:rows=[("wal" if label=="source" else "delete",)]
                return SimpleNamespace(fetchone=lambda:rows[0],fetchmany=lambda n:rows[:n])
            def backup(self,target,**options):
                fixture.events.append(("backup","started"))
                if fixture.failure=="bundle_mode":fixture.bundle_info.st_mode=stat.S_IFDIR|0o755
                if fixture.failure=="ancestor_mode":fixture.parent_info.st_mode=stat.S_IFDIR|0o777
                options["progress"](101,0,1)
            def set_progress_handler(self,*args):pass
            def close(self):
                fixture.events.append(("sqlite_close",label))
                if fixture.failure=="source_close" and label=="source":raise RuntimeError("synthetic close detail")
        connection=Connection();self.connections.append(connection);return connection

    @contextlib.contextmanager
    def patched(self):
        with contextlib.ExitStack() as stack:
            patches=[mock.patch.object(capture.sys,"platform","linux"),mock.patch.object(capture,"load_expectations",return_value=(self.value,"a"*64)),mock.patch.object(capture,"verify_identity",side_effect=self.verify),mock.patch.object(capture.sqlite3,"connect",side_effect=self.connect),mock.patch.object(capture.time,"monotonic",side_effect=self.monotonic),mock.patch.object(capture,"Path",side_effect=lambda path:SimpleNamespace(as_uri=lambda:"file://"+path))]
            for name,value in {"geteuid":lambda:0,"open":self.open,"fstat":self.fstat,"stat":self.stat,"lstat":self.stat,"statvfs":self.space,"mkdir":self.mkdir,"close":self.close,"unlink":self.unlink,"fdopen":self.fdopen,"fsync":self.fsync,"read":self.read,"lseek":lambda *args:0,"umask":lambda value:0o022,"O_DIRECTORY":0x100000,"O_NOFOLLOW":0x200000,"O_NONBLOCK":0x400000}.items():
                patches.append(mock.patch.object(capture.os,name,value,create=True))
            patches.extend([mock.patch.object(capture.os.path,"realpath",side_effect=lambda path:path),mock.patch.object(capture.os.path,"lexists",side_effect=self.lexists),mock.patch.object(capture.os.path,"join",side_effect=posixpath.join)])
            for patch in patches:stack.enter_context(patch)
            yield

class CleanupTests(unittest.TestCase):
    def run_fixture(self,failure=None):
        fixture=MockCapture(failure)
        with fixture.patched():
            try:result=capture.run(fixture.args)
            except (capture.Refusal,KeyboardInterrupt,OSError) as error:result=error
        return fixture,result

    def test_source_anchor_closes_only_after_sqlite_source_and_artifact_after_validation(self):
        fixture,result=self.run_fixture();self.assertEqual(result,"complete")
        self.assertLess(fixture.events.index(("sqlite_close","source")),fixture.events.index(("close","anchor")))
        self.assertLess(fixture.events.index(("sqlite_close","target")),fixture.events.index(("close","artifact")))
        self.assertLess(fixture.events.index(("sqlite_close","validation")),fixture.events.index(("close","artifact")))
        self.assertIn("COMPLETE",fixture.files);self.assertNotIn("INCOMPLETE",fixture.files)

    def test_failures_keep_incomplete_state_and_close_source_before_anchor(self):
        for failure in ["validation_interrupt","fsync_artifact","space","deadline","source_change","pid_change"]:
            with self.subTest(failure=failure):
                fixture,result=self.run_fixture(failure);self.assertIsInstance(result,BaseException)
                self.assertIn("INCOMPLETE",fixture.files);self.assertNotIn("COMPLETE",fixture.files)
                self.assertLess(fixture.events.index(("sqlite_close","source")),fixture.events.index(("close","anchor")))
                self.assertIn(("close","artifact"),fixture.events)

    def test_fsync_failure_after_complete_write_removes_ambiguous_success_marker(self):
        fixture,result=self.run_fixture("fsync_complete");self.assertIsInstance(result,BaseException)
        self.assertIn("INCOMPLETE",fixture.files);self.assertNotIn("COMPLETE",fixture.files)

    def test_failed_sqlite_source_close_leaves_anchor_for_process_exit(self):
        fixture,result=self.run_fixture("source_close");self.assertIsInstance(result,capture.Refusal)
        self.assertEqual(result.code,"cleanup_failed");self.assertNotIn(("close","anchor"),fixture.events)
        self.assertIn("INCOMPLETE",fixture.files);self.assertNotIn("COMPLETE",fixture.files)

    def test_late_raw_close_failure_is_not_retried_or_published(self):
        for failure,label in [("artifact_close","artifact"),("anchor_close","anchor")]:
            with self.subTest(failure=failure):
                fixture,result=self.run_fixture(failure);self.assertIsInstance(result,OSError)
                self.assertEqual(fixture.events.count(("close",label)),1)
                self.assertIn("INCOMPLETE",fixture.files);self.assertNotIn("COMPLETE",fixture.files)

    def test_bundle_or_ancestor_mode_change_during_copy_refuses(self):
        for failure in ["bundle_mode","ancestor_mode"]:
            with self.subTest(failure=failure):
                fixture,result=self.run_fixture(failure);self.assertIsInstance(result,capture.Refusal)
                self.assertIn("INCOMPLETE",fixture.files);self.assertNotIn("COMPLETE",fixture.files)

    def test_directory_only_close_failure_keeps_valid_capture_and_sets_warning_once(self):
        for failure,label in [("bundle_close","bundle"),("parent_close","parent")]:
            with self.subTest(failure=failure):
                fixture,result=self.run_fixture(failure);self.assertEqual(result,"complete")
                self.assertTrue(fixture.args.cleanup_warning);self.assertEqual(fixture.events.count(("close",label)),1)
                self.assertIn("COMPLETE",fixture.files);self.assertNotIn("INCOMPLETE",fixture.files)
                self.assertNotIn("failure.json",fixture.files)

    def test_artifact_replacement_after_raw_close_refuses_before_publication(self):
        fixture,result=self.run_fixture("artifact_replaced");self.assertIsInstance(result,capture.Refusal)
        self.assertEqual(result.code,"output_identity_changed");self.assertEqual(fixture.events.count(("close","artifact")),1)
        self.assertIn("INCOMPLETE",fixture.files);self.assertNotIn("COMPLETE",fixture.files)

    def test_inspection_does_not_open_source_sqlite_or_create_bundle(self):
        fixture=MockCapture();fixture.args.capture=False
        with fixture.patched():self.assertEqual(capture.run(fixture.args),"inspected")
        self.assertFalse(fixture.connections);self.assertFalse(fixture.created);self.assertFalse(fixture.files)

    def test_main_sanitizes_exceptions_and_argument_errors(self):
        argv=["--expectations","SYNTHETIC_PRIVATE_PATH","--output-dir","SYNTHETIC_PRIVATE_OUTPUT","--max-output-bytes","65536","--reserve-free-bytes","0","--deadline-seconds","1"]
        for error,code in [(capture.Refusal("source_identity_changed"),"source_identity_changed"),(RuntimeError("SYNTHETIC_SECRET_DETAIL"),"operation_failed"),(KeyboardInterrupt(),"interrupted")]:
            with self.subTest(code=code),mock.patch.object(capture,"run",side_effect=error),contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(capture.main(argv),1)
                self.assertEqual(json.loads(output.getvalue()),{"schema":SCHEMA,"status":"refused","code":code,"cleanup_warning":False})
                self.assertNotIn("SYNTHETIC",output.getvalue())
        with contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(capture.main(["--unknown=SYNTHETIC_SECRET_DETAIL"]),1)
        self.assertEqual(json.loads(output.getvalue())["code"],"invalid_arguments");self.assertNotIn("SYNTHETIC",output.getvalue())

    def test_main_exposes_directory_cleanup_warning_as_a_fixed_boolean(self):
        argv=["--expectations","SYNTHETIC_PRIVATE_PATH","--output-dir","SYNTHETIC_PRIVATE_OUTPUT","--max-output-bytes","65536","--reserve-free-bytes","0","--deadline-seconds","1"]
        def warning(args):args.cleanup_warning=True;return "complete"
        with mock.patch.object(capture,"run",side_effect=warning),contextlib.redirect_stdout(io.StringIO()) as output:self.assertEqual(capture.main(argv),0)
        self.assertEqual(json.loads(output.getvalue()),{"schema":SCHEMA,"status":"complete","code":"ok","cleanup_warning":True})

class NamespaceTests(unittest.TestCase):
    def test_root_owned_sticky_ancestor_is_supported_but_untrusted_namespaces_refuse(self):
        root=SimpleNamespace(st_mode=stat.S_IFDIR|0o755,st_uid=0)
        for mode,uid,accepted in [(0o1777,0,True),(0o755,1000,True),(0o777,0,False),(0o1777,1000,False),(0o755,2000,False),(stat.S_IFLNK|0o777,0,False)]:
            child=SimpleNamespace(st_mode=mode if stat.S_ISLNK(mode) else stat.S_IFDIR|mode,st_uid=uid)
            with self.subTest(mode=mode,uid=uid),mock.patch.object(capture.os,"lstat",side_effect=lambda path:root if path=="/" else child):
                if accepted:capture.trusted_directories("/owned",{0,1000})
                else:
                    with self.assertRaises(capture.Refusal) as raised:capture.trusted_directories("/owned",{0,1000})
                    self.assertEqual(raised.exception.code,"unsafe_namespace")

    def test_sidecar_symlinks_links_permissions_owners_and_inode_aliases_refuse(self):
        path="/owned/ledger.sqlite3"
        source=SimpleNamespace(st_mode=stat.S_IFREG|0o600,st_uid=1000,st_dev=1,st_ino=100,st_nlink=1)
        safe=SimpleNamespace(st_mode=stat.S_IFREG|0o600,st_uid=1000,st_dev=1,st_ino=101,st_nlink=1)
        for field,value in [("st_mode",stat.S_IFLNK|0o777),("st_nlink",2),("st_mode",stat.S_IFREG|0o666),("st_uid",2000),("st_ino",100)]:
            sidecar=copy.copy(safe);setattr(sidecar,field,value)
            def observe(name):
                if name==path+"-wal":return sidecar
                raise FileNotFoundError()
            with self.subTest(field=field,value=value),mock.patch.object(capture.os,"lstat",side_effect=observe),mock.patch.object(capture.os.path,"realpath",side_effect=lambda name:name):
                with self.assertRaises(capture.Refusal) as raised:capture.verify_sidecars(path,source)
                self.assertEqual(raised.exception.code,"unsafe_source_sidecar")

    def test_distinct_safe_sidecars_are_accepted_and_shared_inodes_refuse(self):
        path="/owned/ledger.sqlite3";source=SimpleNamespace(st_uid=1000,st_dev=1,st_ino=100)
        def observe(name):
            if name.endswith("-journal"):raise FileNotFoundError()
            return SimpleNamespace(st_mode=stat.S_IFREG|0o600,st_uid=1000,st_dev=1,st_ino=101 if name.endswith("-wal") else 102,st_nlink=1)
        with mock.patch.object(capture.os,"lstat",side_effect=observe),mock.patch.object(capture.os.path,"realpath",side_effect=lambda name:name):capture.verify_sidecars(path,source)
        duplicate=SimpleNamespace(st_mode=stat.S_IFREG|0o600,st_uid=1000,st_dev=1,st_ino=101,st_nlink=1)
        with mock.patch.object(capture.os,"lstat",return_value=duplicate),mock.patch.object(capture.os.path,"realpath",side_effect=lambda name:name):
            with self.assertRaises(capture.Refusal):capture.verify_sidecars(path,source)

if __name__=="__main__":
    unittest.main()
