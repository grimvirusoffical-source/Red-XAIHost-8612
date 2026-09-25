"""Bounded local-only tests. Never targets production services or external hosts."""
from __future__ import annotations
import argparse
from concurrent.futures import ThreadPoolExecutor
import datetime
import json
import os
from pathlib import Path
import random
import queue
import statistics
import subprocess
import sys
import tempfile
import threading
import time
from redxai.language import LanguageError,parse
from redxai.security import OWNER_EMAIL
from redxai.service import LocalServer
from redxai.store import Store,Conflict
from redxai.client import Client


def run():
    parser=argparse.ArgumentParser();parser.add_argument('--output',type=Path,default=Path('stress-report.json'));args=parser.parse_args()
    report={'version':'0.2.0-preview','scope':'temporary local test directories and loopback only',
            'platform':sys.platform,'python':sys.version.split()[0],
            'timestamp':datetime.datetime.now(datetime.timezone.utc).isoformat(),'rounds':[]}
    for round_ in range(3):
        with tempfile.TemporaryDirectory(prefix='redxai-stress-') as directory:
            root=Path(directory)/'data';store=Store(root)
            registration={'username':'OwnerTest','password':'SyntheticStress9!','confirm_password':'SyntheticStress9!',
                          'email':OWNER_EMAIL,'confirm_email':OWNER_EMAIL}
            login=store.bootstrap(store.setup_file.read_text(),registration);p=store.principal(login['token'])
            db=store.create_database(p,'Stress');uid=db['id']
            source='{Red-XAI}[1]{\n'+''.join(f'{{P{i}}}[{i}]=[{i}][{i}],\n' for i in range(1000))+'<[True,1,false,"keyref:test"]>}'
            t=time.perf_counter();doc=parse(source);parse_ms=(time.perf_counter()-t)*1000
            store.save_source(p,uid,source,1)
            t=time.perf_counter()
            def compete(_):
                try:store.save_source(p,uid,source,2);return 'committed'
                except Conflict:return 'conflict'
            with ThreadPoolExecutor(max_workers=16) as pool:results=list(pool.map(compete,range(64)))
            assert results.count('committed')==1 and results.count('conflict')==63
            concurrency_ms=(time.perf_counter()-t)*1000
            rng=random.Random(926+round_);rejected=0;accepted=0
            alphabet='{}[]<>=,()/\\" abcdefNELLTrueFalse0123456789\n\t'
            for _ in range(3000):
                candidate=''.join(rng.choice(alphabet) for _ in range(rng.randrange(1,300)))
                try:parse(candidate);accepted+=1
                except (LanguageError,ValueError):rejected+=1
            assert accepted+rejected==3000
            server=LocalServer(0,root);thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
            url=f'http://127.0.0.1:{server.server_port}'
            durations=[]
            def request(_):
                t=time.perf_counter();client=Client(url,login['token']);rows=client.databases()
                assert len(rows)==1;return (time.perf_counter()-t)*1000
            try:
                with ThreadPoolExecutor(max_workers=8) as pool:durations=list(pool.map(request,range(300)))
            finally:server.shutdown();server.server_close();thread.join(3)
            child_code='''from redxai.store import Store
from redxai.security import OWNER_EMAIL
import sys,time
s=Store(sys.argv[1]);p=s.principal(s.login(OWNER_EMAIL,'SyntheticStress9!')['token']);uid=sys.argv[2]
d=s.read_database(p,uid);s.save_source(p,uid,d['source'],d['revision'])
c=s.connect();c.execute('BEGIN IMMEDIATE');c.execute('UPDATE databases SET revision=999999 WHERE id=?',(uid,))
print('transaction-open',flush=True)
time.sleep(60)
'''
            child=subprocess.Popen([sys.executable,'-c',child_code,str(root),uid],stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
            started=queue.Queue()
            threading.Thread(target=lambda:started.put(child.stdout.readline()),daemon=True).start()
            try:
                assert started.get(timeout=12).strip()=='transaction-open'
            except BaseException:
                child.kill();child.communicate(timeout=5);raise
            child.kill();_,stderr=child.communicate(timeout=5)
            reopened=Store(root);snapshot=reopened.read_database(p,uid);parse(snapshot['source'])
            with reopened.connect() as connection:integrity=connection.execute('PRAGMA integrity_check').fetchone()[0]
            assert integrity=='ok' and snapshot['revision']==4
            report['rounds'].append({'round':round_+1,'objects':len(doc.index()),'parse_ms':round(parse_ms,2),
                'concurrent_writers':16,'contending_writes':64,'committed_writes':results.count('committed'),
                'expected_conflicts':results.count('conflict'),'write_contention_ms':round(concurrency_ms,2),
                'malformed_candidates':3000,'rejected_candidates':rejected,
                'http_read_requests':300,'http_errors':0,'http_concurrency':8,
                'http_latency_ms_p50':round(statistics.median(durations),2),
                'http_latency_ms_p95':round(sorted(durations)[int(.95*len(durations))-1],2),
                'kill_point':'confirmed open uncommitted SQLite transaction','killed_writer_exit_code':child.returncode,'recovered_revision':snapshot['revision'],
                'recovery_integrity':integrity})
    report['result']='passed';report['limits']='Not a distributed, long-duration, Windows, or macOS load certification.'
    args.output.write_text(json.dumps(report,indent=2));print(json.dumps(report,indent=2))

if __name__=='__main__':run()
