"""RedX client (stdlib only, Python >= 3.9).

    from redx import RedX
    db = RedX("http://127.0.0.1:7777", os.environ["REDX_KEY"])
    db.set("players.red-xai.db:Player[ID=1]", {"Name": "Ann", "Level": 5})
    db.get("players.red-xai.db:Player[ID=1]")   # {'Level': 5, 'Name': 'Ann'}

The API key is kept out of repr(), error messages and pickles.
"""
from __future__ import annotations

import json
import queue
import re
import threading
import urllib.error
import urllib.request
from typing import Any, Callable, Dict, Iterator, Optional
from urllib.parse import quote, urlencode, urlsplit

from .boxtext import BoxTextError, from_box
from . import ws as _ws

_DB_NAME = re.compile(r"^[a-z0-9][a-z0-9_-]{0,62}$")
_KEY_SHAPE = re.compile(r"rx_live_[A-Za-z0-9]+")


class RedXError(Exception):
    """HTTP or client-side failure. status 0 = network/timeout/decoding problem."""

    def __init__(self, status: int, message: str, retry_after: Optional[float] = None, body: Any = None):
        super().__init__(message)
        self.status = status
        self.message = message
        self.retry_after = retry_after
        self.body = body

    @property
    def unauthorized(self) -> bool:
        return self.status == 401

    @property
    def forbidden(self) -> bool:
        return self.status == 403

    @property
    def not_found(self) -> bool:
        return self.status == 404

    @property
    def rate_limited(self) -> bool:
        return self.status == 429

    def __str__(self) -> str:
        return f"[{self.status}] {self.message}" if self.status else self.message


def _scrub(s: Any) -> str:
    return _KEY_SHAPE.sub("rx_live_[redacted]", str(s))


def escape_path(p: str) -> str:
    """Percent-encode a RedX path for a URL path ([ ] : = are encoded too)."""
    if not isinstance(p, str) or p == "":
        raise TypeError("path must be a non-empty string")
    return quote(p, safe="")


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):  # never follow: it could carry the key elsewhere
        return None


# No environment proxies (they would see the key) and no redirects.
_OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}), _NoRedirect())


class Subscription:
    """A live subscription. Iterate it (``for ev in sub``) or pass a handler to subscribe()."""

    def __init__(self, sock: _ws.WebSocket, handler: Optional[Callable[[dict], None]],
                 on_error: Optional[Callable[[BaseException], None]],
                 on_close: Optional[Callable[[int, str], None]]):
        self._ws = sock
        self._handler = handler
        self._on_error = on_error
        self._on_close = on_close
        self._q: "queue.Queue[Any]" = queue.Queue()
        self._done = threading.Event()
        self.close_code: Optional[int] = None
        self.close_reason = ""
        self._t = threading.Thread(target=self._run, name="redx-subscribe", daemon=True)
        self._t.start()

    def _err(self, e: BaseException):
        if self._on_error:
            try:
                self._on_error(e)
            except Exception:
                pass

    def _run(self):
        try:
            while True:
                try:
                    text = self._ws.recv_message()
                except _ws.ProtocolError as e:
                    self._err(e)
                    break
                if text is None:
                    break
                try:
                    ev = json.loads(text)
                except ValueError:
                    self._err(RedXError(0, "bad event JSON"))
                    continue
                data = None
                if ev.get("op") == "set" and isinstance(ev.get("value"), str):
                    try:
                        data = from_box(ev["value"])
                    except BoxTextError:
                        data = None
                ev["data"] = data
                if self._handler:
                    try:
                        self._handler(ev)
                    except Exception as e:  # handler bugs must not kill the stream
                        self._err(e)
                else:
                    self._q.put(ev)
        finally:
            code, reason = self._ws.close_info or (1006, "connection closed")
            self.close_code, self.close_reason = code, reason
            self._ws.shutdown()
            self._done.set()
            self._q.put(None)
            if self._on_close:
                try:
                    self._on_close(code, reason)
                except Exception:
                    pass

    def __iter__(self) -> Iterator[dict]:
        while True:
            ev = self._q.get()
            if ev is None:
                self._q.put(None)
                return
            yield ev

    def get(self, timeout: Optional[float] = None) -> Optional[dict]:
        """Next event, or None on timeout / when the subscription ended."""
        try:
            ev = self._q.get(timeout=timeout)
        except queue.Empty:
            return None
        if ev is None:
            self._q.put(None)
        return ev

    def ping(self):
        self._ws.ping()

    def close(self, timeout: float = 2.0) -> None:
        """Clean close; drops the connection if the server does not answer within ``timeout``."""
        self._ws.send_close(1000, "bye")
        if not self._done.wait(timeout):
            self._ws.shutdown()
            self._done.wait(timeout)

    def wait_closed(self, timeout: Optional[float] = None) -> bool:
        return self._done.wait(timeout)

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        self.close()


class RedX:
    def __init__(self, url: str, key: str, db: Optional[str] = None, timeout: float = 30.0):
        if not url:
            raise TypeError("url is required")
        if not key or not isinstance(key, str):
            raise TypeError("key is required")
        if db is not None and not _DB_NAME.match(db):
            raise TypeError("invalid db name")
        u = urlsplit(url)
        if u.scheme not in ("http", "https") or not u.hostname:
            raise TypeError("url must be http(s)://host[:port]")
        self.base_url = f"{u.scheme}://{u.netloc}"
        self.db = db
        self.timeout = timeout
        self.__key = key

    def __repr__(self) -> str:
        return f"RedX(base_url={self.base_url!r}, db={self.db!r})"

    def __reduce__(self):
        raise TypeError("RedX clients hold an API key and cannot be pickled")

    def with_db(self, db: str) -> "RedX":
        return RedX(self.base_url, self.__key, db, self.timeout)

    def _prefix(self) -> str:
        return f"/v1/db/{self.db}" if self.db else "/v1"

    def _request(self, method: str, path: str, *, body: Optional[bytes] = None,
                 headers: Optional[Dict[str, str]] = None, query: Optional[Dict[str, Any]] = None,
                 auth: bool = True) -> str:
        url = self.base_url + path
        if query:
            q = {k: ("true" if v is True else "false" if v is False else v) for k, v in query.items() if v is not None}
            if q:
                url += "?" + urlencode(q)
        h = dict(headers or {})
        if auth:
            h["Authorization"] = f"Bearer {self.__key}"
        req = urllib.request.Request(url, data=body, method=method, headers=h)
        try:
            with _OPENER.open(req, timeout=self.timeout) as r:
                return r.read().decode("utf-8")
        except urllib.error.HTTPError as e:
            text = e.read(65536).decode("utf-8", "replace")
            msg, parsed = f"HTTP {e.code}", None
            try:
                parsed = json.loads(text)
                if isinstance(parsed, dict) and parsed.get("error"):
                    msg = str(parsed["error"])
            except ValueError:
                pass
            ra = e.headers.get("Retry-After") if e.headers else None
            try:
                retry_after = float(ra) if ra is not None else None
            except ValueError:
                retry_after = None
            raise RedXError(e.code, _scrub(msg), retry_after, parsed) from None
        except (urllib.error.URLError, OSError) as e:
            reason = getattr(e, "reason", e)
            raise RedXError(0, _scrub(f"request failed: {reason}")) from None

    def _json(self, method: str, path: str, **kw) -> Any:
        text = self._request(method, path, **kw)
        try:
            return json.loads(text)
        except ValueError:
            raise RedXError(0, "server sent a non-JSON response") from None

    # -- API -----------------------------------------------------------------
    def health(self) -> dict:
        """GET /healthz (no key needed)."""
        return self._json("GET", "/healthz", auth=False)

    def stats(self) -> dict:
        return self._json("GET", "/v1/stats")

    def databases(self) -> dict:
        return self._json("GET", "/v1/dbs")

    def get_box(self, path: str) -> str:
        """Raw Box text of the value at ``path``."""
        return self._request("GET", f"{self._prefix()}/path/{escape_path(path)}",
                             headers={"Accept": "text/x-redx-box"})

    def get(self, path: str) -> Any:
        """The value at ``path`` as plain Python data. Raises RedXError(404) if absent."""
        j = self._json("GET", f"{self._prefix()}/path/{escape_path(path)}")
        if not isinstance(j, dict) or not isinstance(j.get("value"), str):
            raise RedXError(0, "unexpected response shape")
        try:
            return from_box(j["value"])
        except BoxTextError as e:
            raise RedXError(0, f"cannot decode value: {e}") from None

    def get_or(self, path: str, default: Any = None) -> Any:
        try:
            return self.get(path)
        except RedXError as e:
            if e.status == 404:
                return default
            raise

    def set(self, path: str, value: Any, *, ttl: Optional[float] = None, sliding: Optional[bool] = None,
            checkpoint: Optional[float] = None) -> dict:
        """Write a JSON-compatible value (dict -> box, None -> NELL). ttl/checkpoint in seconds."""
        body: Dict[str, Any] = {"value": value}
        if ttl is not None:
            body["ttl_seconds"] = ttl
        if sliding is not None:
            body["sliding"] = bool(sliding)
        if checkpoint is not None:
            body["checkpoint_seconds"] = checkpoint
        return self._json("PUT", f"{self._prefix()}/path/{escape_path(path)}",
                          body=json.dumps(body).encode(), headers={"Content-Type": "application/json"})

    def set_box(self, path: str, box_text: str, *, ttl: Optional[float] = None, sliding: Optional[bool] = None,
                checkpoint: Optional[float] = None) -> dict:
        """Write raw Box text (see GRAMMAR.md)."""
        return self._json("PUT", f"{self._prefix()}/path/{escape_path(path)}", body=box_text.encode(),
                          headers={"Content-Type": "text/x-redx-box"},
                          query={"ttl": ttl, "sliding": sliding, "checkpoint": checkpoint})

    def delete(self, path: str) -> dict:
        return self._json("DELETE", f"{self._prefix()}/path/{escape_path(path)}")

    def query(self, q: str) -> dict:
        """Run a RedX path query (POST /v1/query), e.g. ``players.red-xai.db:Player[ID=*].Name``.

        This is the host's path-pattern query; use :meth:`sql` for the SQL-like language (both stay). Returns {"count", "results" (Box text), "values" (decoded)}.
        """
        if not isinstance(q, str) or q == "":
            raise TypeError("query must be a non-empty string")
        j = self._json("POST", f"{self._prefix()}/query", body=json.dumps({"q": q}).encode(),
                       headers={"Content-Type": "application/json"})
        results = j.get("results") or []
        values = []
        for r in results:
            try:
                values.append(from_box(r))
            except BoxTextError:
                values.append(r)
        return {"count": j.get("count"), "results": results, "values": values}

    def sql(self, q: str, params: Optional[list] = None) -> dict:
        """Run a RXQL statement (POST /v1/sql), e.g. ``SELECT Name FROM players.Player WHERE Level >= ?``.

        Values go in ``params`` (never concatenated into the text). Returns the host's result object:
        ``{kind, columns, rows, count, affected, paths, access, plan}``.
        """
        if not isinstance(q, str) or q == "":
            raise TypeError("sql must be a non-empty string")
        if params is not None and not isinstance(params, (list, tuple)):
            raise TypeError("params must be a list")
        return self._json("POST", f"{self._prefix()}/sql", body=json.dumps({"q": q, "params": list(params or [])}).encode(),
                          headers={"Content-Type": "application/json"})

    def create_key(self, *, scope: str, db: Optional[str] = None, prefix: Optional[str] = None,
                   label: Optional[str] = None, ttl: Optional[str] = None,
                   rate_rps: Optional[float] = None, rate_burst: Optional[int] = None) -> dict:
        """Create an API key (needs an admin key). The returned ``key`` is shown once; store it."""
        body: Dict[str, Any] = {"db": db or self.db or "default", "scope": scope}
        for k, v in (("prefix", prefix), ("label", label), ("ttl", ttl), ("rate_rps", rate_rps), ("rate_burst", rate_burst)):
            if v is not None:
                body[k] = v
        return self._json("POST", "/v1/keys", body=json.dumps(body).encode(), headers={"Content-Type": "application/json"})

    def revoke_key(self, key_id: str) -> dict:
        return self._json("DELETE", f"/v1/keys/{quote(key_id, safe='')}")

    def subscribe(self, path: str, handler: Optional[Callable[[dict], None]] = None, *,
                  on_error: Optional[Callable[[BaseException], None]] = None,
                  on_close: Optional[Callable[[int, str], None]] = None) -> Subscription:
        """Subscribe to changes matching ``path`` (a path pattern such as ``file.red-xai.db:Box[ID=*]``
        or ``file.red-xai.db:*``). Events are dicts {path, op: set|delete|expire, value (Box text), at, data}.

        With a handler it is called on the subscription's thread; without one, iterate the
        returned Subscription. Raises RedXError if the host refuses (401/403/429...).
        """
        if handler is not None and not callable(handler):
            raise TypeError("handler must be callable")
        url = self.base_url + self._prefix() + "/subscribe?" + urlencode({"path": path})
        try:
            sock = _ws.connect(url, {"Authorization": f"Bearer {self.__key}"}, timeout=self.timeout)
        except _ws.HandshakeError as e:
            msg, body = str(e), None
            try:
                body = json.loads(e.body)
                if isinstance(body, dict) and body.get("error"):
                    msg = str(body["error"])
            except ValueError:
                pass
            ra = e.headers.get("retry-after")
            raise RedXError(e.status, _scrub(msg), float(ra) if ra else None, body) from None
        except OSError as e:
            raise RedXError(0, _scrub(f"request failed: {e}")) from None
        return Subscription(sock, handler, on_error, on_close)
