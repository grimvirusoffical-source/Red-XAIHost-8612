"""Decoder for the Box text returned by the host (GRAMMAR.md section 2).

Mapping follows the host's JSON mapping (GRAMMAR.md section 5):
string/number/TRUE/FALSE -> str/int|float/bool, NELL -> None,
``;[a, b]:`` -> list, ``[ {K[]} = v, {K[ID=2]} = w ]`` -> ``{"K": v, "K[ID=2]": w}``,
link -> ``{"$link": "<link text>"}``. TTL markers after a value are skipped.
The decoder is strict: anything it does not understand raises BoxTextError.
"""
from __future__ import annotations

import json
import re
from typing import Any

MAX_DEPTH = 200
_NUM = re.compile(r"-?\d+(\.\d+)?([eE][+-]?\d+)?")
_ID_ATTR = re.compile(r"(?:ID\s*=\s*)?(-?\d+)\s*(?=[,\]])", re.I)
_TTL = re.compile(r"\(\s*~?\d+(@\d+)?\s*$")


class BoxTextError(ValueError):
    def __init__(self, message: str, offset: int):
        super().__init__(f"{message} (at offset {offset})")
        self.offset = offset


def _ident_start(c: str) -> bool:
    return c.isalpha() or c == "_" or c > "\x7f"


def _ident_part(c: str) -> bool:
    return c.isalnum() or c in "_-" or c > "\x7f"


class _Reader:
    def __init__(self, src: str):
        self.s = src
        self.i = 0

    def fail(self, msg: str):
        raise BoxTextError(msg, self.i)

    def ws(self):
        s = self.s
        while self.i < len(s):
            c = s[self.i]
            if c in " \t\r\n":
                self.i += 1
            elif s.startswith("<<", self.i):
                end = s.find(">>", self.i + 2)
                if end < 0:
                    self.fail("unterminated comment")
                self.i = end + 2
            else:
                return

    def peek(self):
        self.ws()
        return self.s[self.i] if self.i < len(self.s) else None

    def eat(self, ch: str) -> bool:
        if self.peek() == ch:
            self.i += 1
            return True
        return False

    def expect(self, ch: str):
        if not self.eat(ch):
            self.fail(f"expected '{ch}'")

    def string(self) -> str:
        start = self.i
        s = self.s
        if s[self.i : self.i + 1] != '"':
            self.fail("expected a string")
        self.i += 1
        while True:
            if self.i >= len(s):
                self.fail("unterminated string")
            c = s[self.i]
            if c == "\\":
                self.i += 2
                continue
            self.i += 1
            if c == '"':
                break
        try:
            return json.loads(s[start : self.i])
        except ValueError:
            self.i = start
            self.fail("bad string literal")

    def number(self):
        m = _NUM.match(self.s, self.i)
        if not m:
            self.fail("expected a number")
        self.i = m.end()
        txt = m.group(0)
        if m.group(1) is None and m.group(2) is None:
            return int(txt)
        return float(txt)

    def ident(self) -> str:
        st = self.i
        if self.i >= len(self.s) or not _ident_start(self.s[self.i]):
            self.fail("expected an identifier")
        while self.i < len(self.s) and _ident_part(self.s[self.i]):
            self.i += 1
        return self.s[st : self.i]

    def ttl(self):
        if self.peek() != "(":
            return
        end = self.s.find(")", self.i)
        if end < 0 or not _TTL.match(self.s[self.i : end]):
            self.fail("bad TTL marker")
        self.i = end + 1
        if self.s[self.i : self.i + 1] == "*":
            self.i += 1

    def link_text(self, start: int) -> str:
        depth = 0
        self.i = start
        s = self.s
        while self.i < len(s):
            c = s[self.i]
            if c == '"':
                self.string()
                continue
            if c == "[":
                depth += 1
            elif c in "]}":
                if depth == 0:
                    break
                depth -= 1
            elif c in ",;" and depth == 0:
                break
            self.i += 1
        return s[start : self.i].strip()

    def value(self, depth: int) -> Any:
        if depth > MAX_DEPTH:
            self.fail("nested too deeply")
        c = self.peek()
        if c is None:
            self.fail("unexpected end of input")
        s = self.s
        if c == '"':
            start = self.i
            val = self.string()
            j = self.i
            while j < len(s) and s[j].isspace():
                j += 1
            if j + 1 < len(s) and s[j] == ":" and _ident_start(s[j + 1]):
                return {"$link": self.link_text(start)}
            return val
        if c == ";":
            self.i += 1
            self.expect("[")
            arr = []
            if not self.eat("]"):
                while True:
                    arr.append(self.value(depth + 1))
                    if not self.eat(","):
                        break
                self.expect("]")
            self.expect(":")
            return arr
        if c == "[":
            self.i += 1
            return self.box_body(depth + 1)
        if c == "-" or c.isdigit():
            return self.number()
        if _ident_start(c):
            w = self.ident().upper()
            if w == "TRUE":
                return True
            if w == "FALSE":
                return False
            if w == "NELL":
                return None
            self.fail(f"unexpected word {w}")
        self.fail("unexpected character")

    def box_body(self, depth: int) -> dict:
        out: dict = {}
        while not self.eat("]"):
            self.expect("{")
            name = self.ident()
            id_ = None
            if self.eat("["):
                while not self.eat("]"):
                    self.ws()
                    m = _ID_ATTR.match(self.s, self.i)
                    if m and id_ is None:
                        id_ = m.group(1)
                        self.i = m.end()
                    else:  # other attributes are skipped up to the next ',' or ']'
                        while self.i < len(self.s) and self.s[self.i] not in ",]":
                            if self.s[self.i] == '"':
                                self.string()
                            else:
                                self.i += 1
                    self.eat(",")
            self.expect("}")
            self.expect("=")
            v = self.value(depth + 1)
            self.ttl()
            out[name if id_ is None else f"{name}[ID={id_}]"] = v
            if not self.eat(","):
                self.eat(";")
        return out


def from_box(text: str) -> Any:
    """Decode the Box text of one value (scalar, array or box)."""
    if not isinstance(text, str):
        raise TypeError("from_box needs a str")
    r = _Reader(text)
    v = r.value(0)
    r.ttl()
    if r.peek() is not None:
        r.fail("unexpected text after the value")
    return v
