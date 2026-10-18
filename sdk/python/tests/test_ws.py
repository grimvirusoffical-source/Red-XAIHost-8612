"""RFC 6455 framing tests against a scripted raw-socket server (no redxd needed)."""
import base64
import hashlib
import os
import re
import socket
import struct
import sys
import threading
import time
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from redx import ws  # noqa: E402

H = {"Authorization": "Bearer test-token"}


def sf(op, payload=b"", fin=True):
    p = payload if isinstance(payload, bytes) else payload.encode()
    b0 = (0x80 if fin else 0) | op
    if len(p) < 126:
        head = bytes([b0, len(p)])
    elif len(p) < 65536:
        head = bytes([b0, 126]) + struct.pack(">H", len(p))
    else:
        head = bytes([b0, 127]) + struct.pack(">Q", len(p))
    return head + p


class Scripted:
    def __init__(self, on_upgraded, bad_accept=False, raw_response=None):
        self.received = []
        self.conns = []
        self.srv = socket.socket()
        self.srv.bind(("127.0.0.1", 0))
        self.srv.listen(1)
        self.port = self.srv.getsockname()[1]
        self.on_upgraded, self.bad_accept, self.raw_response = on_upgraded, bad_accept, raw_response
        threading.Thread(target=self._serve, daemon=True).start()

    def _serve(self):
        try:
            c, _ = self.srv.accept()
        except OSError:
            return
        self.conns.append(c)
        buf = b""
        while b"\r\n\r\n" not in buf:
            d = c.recv(4096)
            if not d:
                return
            buf += d
        if self.raw_response:
            c.sendall(self.raw_response)
            c.close()
            return
        text = buf.decode("latin1")
        assert "Bearer test-token" in text
        key = re.search(r"sec-websocket-key: (.+)\r\n", text, re.I).group(1).strip()
        acc = "nope" if self.bad_accept else base64.b64encode(
            hashlib.sha1(key.encode() + ws.GUID).digest()).decode()
        c.sendall(("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
                   f"Sec-WebSocket-Accept: {acc}\r\n\r\n").encode())
        threading.Thread(target=self.on_upgraded, args=(c,), daemon=True).start()
        rest = buf.partition(b"\r\n\r\n")[2]
        try:
            while True:
                while len(rest) >= 6:
                    n = rest[1] & 0x7F
                    off = 2
                    if n == 126:
                        n = struct.unpack(">H", rest[2:4])[0]
                        off = 4
                    if len(rest) < off + 4 + n:
                        break
                    assert rest[1] & 0x80, "client frames must be masked"
                    mask = rest[off:off + 4]
                    body = bytes(b ^ mask[i % 4] for i, b in enumerate(rest[off + 4:off + 4 + n]))
                    self.received.append((rest[0] & 0xF, body))
                    rest = rest[off + 4 + n:]
                d = c.recv(65536)
                if not d:
                    return
                rest += d
        except OSError:
            return

    def close(self):
        self.srv.close()
        for c in self.conns:
            c.close()


def drain(w, limit=10):
    out = []
    while len(out) < limit:
        m = w.recv_message()
        if m is None:
            break
        out.append(m)
    return out


class WsTests(unittest.TestCase):
    def test_fragments_lengths_ping_split_reads(self):
        big, mid = "x" * 70000, "y" * 300
        data = b"".join([sf(1, "hel", fin=False), sf(9, "pp"), sf(0, "lo"), sf(1, mid), sf(1, big),
                         sf(1, "é✓"), sf(0xA, "x"), sf(8, struct.pack(">H", 1000))])

        def go(c):
            for i in range(0, len(data), 777):
                c.sendall(data[i:i + 777])
                time.sleep(0.001)

        s = Scripted(go)
        w = ws.connect(f"ws://127.0.0.1:{s.port}/x", H)
        self.assertEqual(drain(w), ["hello", mid, big, "é✓"])
        self.assertEqual(w.close_info, (1000, ""))
        time.sleep(0.1)
        self.assertIn((0xA, b"pp"), s.received)
        w.shutdown()
        s.close()

    def test_close_reason(self):
        s = Scripted(lambda c: c.sendall(sf(8, struct.pack(">H", 1001) + b"away")))
        w = ws.connect(f"ws://127.0.0.1:{s.port}/x", H)
        self.assertIsNone(w.recv_message())
        self.assertEqual(w.close_info, (1001, "away"))
        time.sleep(0.1)
        self.assertTrue(any(op == 8 for op, _ in s.received))
        w.shutdown()
        s.close()

    def test_protocol_violations(self):
        for bad in [bytes([0x81 | 0x40, 0]), bytes([0x81, 0x80, 0, 0, 0, 0]), sf(0, "orphan"),
                    sf(9, b"z" * 200), bytes([0x03, 0x00]), sf(1, b"\xff\xfe")]:
            s = Scripted(lambda c, b=bad: c.sendall(b))
            w = ws.connect(f"ws://127.0.0.1:{s.port}/x", H)
            with self.assertRaises(ws.ProtocolError):
                w.recv_message()
            self.assertEqual(w.close_info[0], 1002)
            w.shutdown()
            s.close()

    def test_oversized_header_refused(self):
        s = Scripted(lambda c: c.sendall(bytes([0x82, 127]) + struct.pack(">Q", 1 << 40)))
        w = ws.connect(f"ws://127.0.0.1:{s.port}/x", H)
        with self.assertRaises(ws.ProtocolError):
            w.recv_message()
        w.shutdown()
        s.close()

    def test_bad_accept(self):
        s = Scripted(lambda c: None, bad_accept=True)
        with self.assertRaisesRegex(ws.HandshakeError, "Sec-WebSocket-Accept"):
            ws.connect(f"ws://127.0.0.1:{s.port}/x", H)
        s.close()

    def test_non_101(self):
        s = Scripted(None, raw_response=b'HTTP/1.1 401 Unauthorized\r\nContent-Length: 18\r\n\r\n{"error":"denied"}\n')
        with self.assertRaises(ws.HandshakeError) as cm:
            ws.connect(f"ws://127.0.0.1:{s.port}/x", H)
        self.assertEqual(cm.exception.status, 401)
        self.assertIn("denied", cm.exception.body)
        s.close()

    def test_header_injection_refused(self):
        with self.assertRaises(ValueError):
            ws.connect("ws://127.0.0.1:1/x", {"X": "a\r\nEvil: 1"})


if __name__ == "__main__":
    unittest.main()
