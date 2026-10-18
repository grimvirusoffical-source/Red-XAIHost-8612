"""Starts a real redxd on a free local port with a throwaway data dir.

The bootstrap admin key is read from redxd's stdout and never printed; all
other keys are created through the API during the run.
"""
import os
import re
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import urllib.request

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
_KEY = re.compile(r"rx_live_[A-Za-z0-9]{43}")


def free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


class Host:
    def __init__(self):
        self.tmp = tempfile.mkdtemp(prefix="redx-sdk-py-")
        self.admin_key = None
        self.proc = None
        try:
            binary = os.environ.get("REDXD_BIN")
            if not binary:
                binary = os.path.join(self.tmp, "redxd")
                subprocess.run(["go", "build", "-o", binary, "./cmd/redxd"], cwd=REPO, check=True)
            port = free_port()
            self.url = f"http://127.0.0.1:{port}"
            env = {"PATH": os.environ.get("PATH", ""), "HOME": self.tmp}  # no inherited REDX_* settings
            self.proc = subprocess.Popen(
                [binary, "run", "--data-dir", os.path.join(self.tmp, "data"), "--listen", f"127.0.0.1:{port}"],
                env=env, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
            self._out = ""
            self._pump_t = threading.Thread(target=self._pump, daemon=True)
            self._pump_t.start()
            for i in range(300):
                if self.proc.poll() is not None:
                    raise RuntimeError("redxd exited early")
                try:
                    with urllib.request.urlopen(self.url + "/healthz", timeout=1) as r:
                        if r.status == 200 and self.admin_key:
                            return
                except OSError:
                    pass
                time.sleep(0.05)
            raise RuntimeError("redxd did not become ready")
        except BaseException:
            self.stop()
            raise

    def _pump(self):
        for line in self.proc.stdout:
            self._out = (self._out + line)[-20000:]
            if not self.admin_key:
                m = _KEY.search(self._out)
                if m:
                    self.admin_key = m.group(0)

    def stop(self):
        if self.proc and self.proc.poll() is None:
            self.proc.kill()
            self.proc.wait()
        if self.proc:
            t = getattr(self, "_pump_t", None)
            if t:
                t.join(2)
            self.proc.stdout.close()
        shutil.rmtree(self.tmp, ignore_errors=True)
