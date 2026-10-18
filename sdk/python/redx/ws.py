"""Minimal RFC 6455 client over socket/ssl (stdlib only).

Text messages (with continuation), ping/pong and close, which is all the RedX
subscribe endpoint uses. Client frames are masked as the RFC requires.
"""
from __future__ import annotations

import base64
import hashlib
import os
import socket
import ssl
import struct
import threading
from typing import Dict, Optional, Tuple
from urllib.parse import urlsplit

GUID = b"258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
MAX_MESSAGE = 16 * 1024 * 1024


class HandshakeError(Exception):
    """The server did not upgrade. ``status``/``body``/``headers`` describe its answer."""

    def __init__(self, message: str, status: int = 0, body: str = "", headers: Optional[Dict[str, str]] = None):
        super().__init__(message)
        self.status = status
        self.body = body
        self.headers = headers or {}


class ProtocolError(Exception):
    pass


def _xor(data: bytes, mask: bytes) -> bytes:
    n = len(data)
    if n == 0:
        return b""
    m = (mask * (n // 4 + 1))[:n]
    return (int.from_bytes(data, "big") ^ int.from_bytes(m, "big")).to_bytes(n, "big")


def encode_frame(opcode: int, payload: bytes) -> bytes:
    n = len(payload)
    if n < 126:
        head = bytes([0x80 | opcode, 0x80 | n])
    elif n < 65536:
        head = bytes([0x80 | opcode, 0x80 | 126]) + struct.pack(">H", n)
    else:
        head = bytes([0x80 | opcode, 0x80 | 127]) + struct.pack(">Q", n)
    mask = os.urandom(4)
    return head + mask + _xor(payload, mask)


class WebSocket:
    def __init__(self, sock: socket.socket, leftover: bytes = b""):
        self._sock = sock
        self._buf = bytearray(leftover)
        self._frag: Optional[Tuple[bool, bytearray]] = None
        self._send_lock = threading.Lock()
        self.closed = False
        self._close_sent = False
        self.close_info: Optional[Tuple[int, str]] = None

    # -- sending ---------------------------------------------------------
    def _send(self, opcode: int, payload: bytes = b""):
        with self._send_lock:
            if self.closed:
                return
            try:
                self._sock.sendall(encode_frame(opcode, payload))
            except OSError:
                pass

    def ping(self, data: bytes = b""):
        self._send(0x9, data)

    def send_close(self, code: int = 1000, reason: str = ""):
        if self._close_sent:
            return
        self._close_sent = True
        self._send(0x8, struct.pack(">H", code) + reason.encode()[:120])

    # -- receiving -------------------------------------------------------
    def _need(self, n: int) -> bool:
        while len(self._buf) < n:
            try:
                d = self._sock.recv(65536)
            except (OSError, ssl.SSLError):
                return False
            if not d:
                return False
            self._buf += d
        return True

    def _fail(self, msg: str):
        self.send_close(1002, msg)
        self.close_info = (1002, msg)
        raise ProtocolError(msg)

    def recv_message(self) -> Optional[str]:
        """Next text message, or None once the connection is closed (see close_info)."""
        while True:
            if not self._need(2):
                self.close_info = self.close_info or (1006, "connection closed")
                return None
            b0, b1 = self._buf[0], self._buf[1]
            fin = bool(b0 & 0x80)
            if b0 & 0x70:
                self._fail("reserved bits set")
            op = b0 & 0x0F
            if b1 & 0x80:
                self._fail("server frame is masked")
            n = b1 & 0x7F
            off = 2
            if n == 126:
                if not self._need(4):
                    return self._lost()
                n = struct.unpack(">H", self._buf[2:4])[0]
                off = 4
            elif n == 127:
                if not self._need(10):
                    return self._lost()
                n = struct.unpack(">Q", self._buf[2:10])[0]
                off = 10
            if n > MAX_MESSAGE:
                self._fail("frame too large")
            if not self._need(off + n):
                return self._lost()
            payload = bytes(self._buf[off : off + n])
            del self._buf[: off + n]
            if op >= 0x8:
                if not fin or n > 125:
                    self._fail("bad control frame")
                if op == 0x9:
                    self._send(0xA, payload)
                elif op == 0xA:
                    pass
                elif op == 0x8:
                    code = struct.unpack(">H", payload[:2])[0] if n >= 2 else 1005
                    reason = payload[2:].decode("utf-8", "replace") if n > 2 else ""
                    self.send_close(1000 if code == 1005 else code)
                    self.close_info = (code, reason)
                    return None
                else:
                    self._fail("unknown control opcode")
                continue
            if op == 0x0:
                if self._frag is None:
                    self._fail("unexpected continuation")
            elif op in (0x1, 0x2):
                if self._frag is not None:
                    self._fail("new message inside a fragmented one")
                self._frag = (op == 0x2, bytearray())
            else:
                self._fail("unknown opcode")
            binary, acc = self._frag  # type: ignore[misc]
            acc += payload
            if len(acc) > MAX_MESSAGE:
                self._fail("message too large")
            if fin:
                self._frag = None
                if not binary:
                    try:
                        return bytes(acc).decode("utf-8")
                    except UnicodeDecodeError:
                        self._fail("invalid UTF-8 in text message")

    def _lost(self):
        self.close_info = self.close_info or (1006, "connection closed")
        return None

    def shutdown(self):
        """Drop the TCP connection immediately."""
        self.closed = True
        try:
            self._sock.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        try:
            self._sock.close()
        except OSError:
            pass


def connect(url: str, headers: Optional[Dict[str, str]] = None, timeout: float = 30.0) -> WebSocket:
    """Open a WebSocket. Raises HandshakeError if the server refuses the upgrade."""
    u = urlsplit(url)
    if u.scheme in ("http", "ws"):
        tls = False
    elif u.scheme in ("https", "wss"):
        tls = True
    else:
        raise ValueError("unsupported URL scheme")
    host = u.hostname or ""
    port = u.port or (443 if tls else 80)
    path = (u.path or "/") + (("?" + u.query) if u.query else "")
    for k, v in (headers or {}).items():
        if "\r" in k + v or "\n" in k + v:
            raise ValueError("illegal header value")
    sock = socket.create_connection((host, port), timeout=timeout)
    try:
        if tls:
            sock = ssl.create_default_context().wrap_socket(sock, server_hostname=host)
        key = base64.b64encode(os.urandom(16)).decode()
        hdrs = {
            "Host": f"{host}:{port}" if u.port else host,
            "Upgrade": "websocket",
            "Connection": "Upgrade",
            "Sec-WebSocket-Key": key,
            "Sec-WebSocket-Version": "13",
        }
        hdrs.update(headers or {})
        req = f"GET {path} HTTP/1.1\r\n" + "".join(f"{k}: {v}\r\n" for k, v in hdrs.items()) + "\r\n"
        sock.sendall(req.encode("latin1"))
        buf = b""
        while b"\r\n\r\n" not in buf:
            d = sock.recv(4096)
            if not d:
                raise HandshakeError("connection closed during handshake")
            buf += d
            if len(buf) > 65536:
                raise HandshakeError("handshake response too large")
        head, _, rest = buf.partition(b"\r\n\r\n")
        lines = head.decode("latin1").split("\r\n")
        try:
            status = int(lines[0].split(" ", 2)[1])
        except (IndexError, ValueError):
            raise HandshakeError("bad HTTP status line")
        resp = {}
        for ln in lines[1:]:
            if ":" in ln:
                k, _, v = ln.partition(":")
                resp[k.strip().lower()] = v.strip()
        if status != 101:
            body = rest
            want = min(int(resp.get("content-length", "0") or 0), 8192)
            while len(body) < want:
                d = sock.recv(4096)
                if not d:
                    break
                body += d
            raise HandshakeError(f"websocket upgrade refused: HTTP {status}", status,
                                 body[:8192].decode("utf-8", "replace"), resp)
        accept = base64.b64encode(hashlib.sha1(key.encode() + GUID).digest()).decode()
        if resp.get("sec-websocket-accept") != accept:
            raise HandshakeError("websocket handshake: bad Sec-WebSocket-Accept")
        sock.settimeout(None)
        return WebSocket(sock, rest)
    except BaseException:
        try:
            sock.close()
        except OSError:
            pass
        raise
