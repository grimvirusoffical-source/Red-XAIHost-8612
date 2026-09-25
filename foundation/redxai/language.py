"""Red-XAI language v0.2: bounded parser, typed values, canonical source formatter.

The source is lexed normally (strings are never reversed). Box opening IDs and
closing metadata are validated before evaluating its top-to-bottom members.
"""
from __future__ import annotations

import json
import re
from dataclasses import asdict, dataclass, field
from decimal import Decimal, InvalidOperation
from typing import Any, Iterator

MAX_SOURCE_BYTES = 2 * 1024 * 1024
MAX_DEPTH = 64
MAX_TOKENS = 200_000
MAX_NODES = 50_000


class LanguageError(ValueError):
    def __init__(self, message: str, line: int = 1, column: int = 1):
        self.line, self.column = line, column
        super().__init__(f'{message} (line {line}, column {column})')


@dataclass(frozen=True)
class Token:
    kind: str
    text: str
    line: int
    column: int


@dataclass
class Value:
    kind: str
    data: Any = None

    def wire(self) -> dict[str, Any]:
        if self.kind == 'array':
            data = [v.wire() for v in self.data]
        elif self.kind == 'table':
            data = [{'key': k.wire(), 'value': v.wire()} for k, v in self.data]
        elif self.kind == 'meta':
            data = {'table': self.data[0].wire(), 'metadata': self.data[1].wire()}
        else:
            data = self.data
        return {'type': self.kind, 'value': data}


@dataclass
class Packer:
    name: str
    local_id: str | int | None
    value: Value
    global_id: str | int | None


@dataclass
class Box:
    name: str
    local_id: str | int | None
    members: list[Box | Packer] = field(default_factory=list)
    share_project: bool = False
    global_id: str | int | None = None
    share_namespaces: bool = False
    key_ref: str | None = None
    grants: list[tuple[str, str]] = field(default_factory=list)


@dataclass
class Document:
    root: Box

    def index(self) -> list[dict[str, Any]]:
        rows: list[dict[str, Any]] = []
        def walk(box: Box, path: list[int]) -> None:
            rows.append({'kind': 'box', 'name': box.name, 'path': path,
                         'local_id': box.local_id, 'global_id': box.global_id,
                         'share_project': box.share_project,
                         'share_namespaces': box.share_namespaces})
            for i, member in enumerate(box.members):
                if isinstance(member, Box):
                    walk(member, path + [i])
                else:
                    rows.append({'kind': 'packer', 'name': member.name,
                                 'path': path + [i], 'box_path': path,
                                 'local_id': member.local_id,
                                 'global_id': member.global_id,
                                 'value': member.value.wire()})
        walk(self.root, [])
        return rows

    def node(self, path: list[int]) -> Box | Packer:
        if not isinstance(path, list) or len(path) > MAX_DEPTH:
            raise LanguageError('Invalid structural path')
        node: Box | Packer = self.root
        for index in path:
            if type(index) is not int or index < 0 or not isinstance(node, Box) or index >= len(node.members):
                raise LanguageError('Structural path does not exist')
            node = node.members[index]
        return node


def lex(source: str) -> Iterator[Token]:
    if not isinstance(source, str):
        raise LanguageError('Source must be text')
    if len(source.encode('utf-8')) > MAX_SOURCE_BYTES:
        raise LanguageError('Source exceeds 2 MiB limit')
    i, line, column, count = 0, 1, 1, 0
    def advance(text: str) -> None:
        nonlocal i, line, column
        i += len(text)
        if '\n' in text:
            line += text.count('\n')
            column = len(text.rsplit('\n', 1)[1]) + 1
        else:
            column += len(text)
    while i < len(source):
        if source[i].isspace():
            advance(source[i]); continue
        if source.startswith('/-', i):
            start_line, start_column = line, column
            line_end = source.find('\n', i)
            if line_end < 0:
                line_end = len(source)
            end = source.find('-\\', i + 2)
            opener_alone = not source[i + 2:line_end].strip()
            if end >= 0 and (opener_alone or end < line_end):
                advance(source[i:end + 2]); continue
            if opener_alone and line_end < len(source):
                raise LanguageError('Unterminated multiline comment', start_line, start_column)
            advance(source[i:line_end]); continue
        count += 1
        if count > MAX_TOKENS:
            raise LanguageError('Token limit exceeded', line, column)
        loc = (line, column)
        ch = source[i]
        if ch == '"':
            j, escaped = i + 1, False
            while j < len(source):
                c = source[j]
                if c == '\n':
                    raise LanguageError('Newline in string; use \\n', *loc)
                if c == '"' and not escaped:
                    j += 1; break
                if c == '\\' and not escaped:
                    escaped = True
                else:
                    escaped = False
                j += 1
            else:
                raise LanguageError('Unterminated string', *loc)
            text = source[i:j]
            try:
                json.loads(text)
            except (ValueError, RecursionError) as exc:
                raise LanguageError('Invalid string escape', *loc) from exc
            advance(text)
            yield Token('string', text, *loc)
        elif ch in '{}[]=,<>():':
            advance(ch); yield Token(ch, ch, *loc)
        else:
            number = re.match(r'-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?', source[i:])
            if number:
                text = number.group(0)
                if len(text) > 128:
                    raise LanguageError('Number literal too long', *loc)
                advance(text); yield Token('number', text, *loc)
            else:
                word = re.match(r'[^\s{}\[\]=,<>():"/\\]+', source[i:])
                if not word:
                    raise LanguageError(f'Unexpected character {ch!r}', *loc)
                text = word.group(0)
                advance(text); yield Token('word', text, *loc)
    yield Token('EOF', '', line, column)


class Parser:
    def __init__(self, source: str):
        self.tokens = list(lex(source)); self.position = 0; self.nodes = 0

    @property
    def t(self) -> Token:
        return self.tokens[self.position]

    def error(self, message: str) -> LanguageError:
        return LanguageError(message, self.t.line, self.t.column)

    def take(self, kind: str | None = None) -> Token:
        t = self.t
        if kind is not None and t.kind != kind:
            raise self.error(f'Expected {kind}, got {t.text or "end of file"}')
        self.position += 1
        return t

    def at(self, kind: str) -> bool:
        return self.t.kind == kind

    def identifier(self) -> str | int | None:
        t = self.t
        if t.kind == 'word' and t.text.lower() == 'false':
            self.take(); return None
        if t.kind == 'number':
            text = self.take().text
            if not re.fullmatch(r'\d+', text) or len(text) > 18:
                raise self.error('IDs must be non-negative integers up to 18 digits')
            return int(text)
        if t.kind in ('word', 'string'):
            value = self.take().text
            if t.kind == 'string':
                value = json.loads(value)
            if not value or len(value) > 128:
                raise self.error('IDs must contain 1–128 characters')
            if t.kind == 'word' and value.lower() in ('true', 'nell'):
                raise self.error('Use a quoted string ID, not a Boolean/NELL keyword')
            return value
        raise self.error('Expected an ID or False')

    def optional_id(self) -> str | int | None:
        if not self.at('['):
            return None
        self.take('[')
        value = None if self.at(']') else self.identifier()
        self.take(']')
        return value

    def boolean(self) -> bool:
        t = self.take('word')
        if t.text.lower() not in ('true', 'false'):
            raise self.error('Expected True or False')
        return t.text.lower() == 'true'

    def name(self) -> str:
        self.take('{')
        if self.at('string'):
            name = json.loads(self.take().text)
        else:
            parts = []
            while self.at('word') or self.at('number'):
                parts.append(self.take().text)
            name = ' '.join(parts)
        self.take('}')
        if not name or len(name) > 256 or any(ord(c) < 32 for c in name):
            raise self.error('Name must contain 1–256 printable characters')
        return name

    def values(self, depth: int) -> list[Value]:
        self.take('['); values: list[Value] = []
        while not self.at(']'):
            values.append(self.value(depth + 1))
            if not self.at(','):
                break
            self.take(',')
        self.take(']')
        return values

    def value(self, depth: int) -> Value:
        if depth > MAX_DEPTH:
            raise self.error('Maximum nesting depth exceeded')
        t = self.t
        if t.kind == 'number':
            text = self.take().text
            try:
                if not Decimal(text).is_finite():
                    raise InvalidOperation
            except InvalidOperation as exc:
                raise self.error('Invalid finite number') from exc
            return Value('number', text)
        if t.kind == 'string':
            return Value('string', json.loads(self.take().text))
        if t.kind == '[':
            return Value('array', self.values(depth + 1))
        if t.kind == '{':
            self.take('{'); entries: list[tuple[Value, Value]] = []; keys: set[tuple] = set()
            while not self.at('}'):
                if self.at('['):
                    self.take('['); key = self.value(depth + 1); self.take(']')
                elif self.at('string'):
                    key = Value('string', json.loads(self.take().text))
                else:
                    key = Value('string', self.take('word').text)
                if key.kind not in ('number', 'string'):
                    raise self.error('Table keys must be strings or numbers')
                normalized = (key.kind, Decimal(key.data) if key.kind == 'number' else key.data)
                if normalized in keys:
                    raise self.error('Duplicate table key')
                keys.add(normalized)
                self.take('='); entries.append((key, self.value(depth + 1)))
                if not self.at(','):
                    break
                self.take(',')
            self.take('}'); return Value('table', entries)
        if t.kind == 'word':
            word = self.take().text.lower()
            if word in ('true', 'false'):
                return Value('boolean', word == 'true')
            if word == 'nell':
                return Value('nell')
            if word == 'meta':
                self.take('('); table = self.value(depth + 1); self.take(',')
                metadata = self.value(depth + 1); self.take(')')
                if table.kind != 'table' or metadata.kind != 'table':
                    raise self.error('meta requires two tables')
                return Value('meta', (table, metadata))
        raise self.error('Expected a typed value')

    def member(self, depth: int) -> Box | Packer:
        if depth > MAX_DEPTH:
            raise self.error('Maximum box nesting depth exceeded')
        self.nodes += 1
        if self.nodes > MAX_NODES:
            raise self.error('Maximum object count exceeded')
        name = self.name(); local_id = self.optional_id()
        if self.at('='):
            self.take('='); values = self.values(depth + 1)
            value = values[0] if len(values) == 1 else Value('array', values)
            global_id = self.optional_id(); self.take(',')
            return Packer(name, local_id, value, global_id)
        self.take('{'); box = Box(name, local_id)
        while True:
            if self.at('{'):
                box.members.append(self.member(depth + 1)); continue
            self.take('<'); self.take('[')
            if self.at('word') and self.t.text.lower() == 'gaccsess':
                self.take(); self.take(',')
                token_name = self.identifier(); self.take(','); token_ref = self.identifier()
                if self.at(','):
                    self.take(',')
                self.take(']'); self.take('>')
                if not isinstance(token_name, str) or not isinstance(token_ref, str) or not token_ref.startswith('tokenref:'):
                    raise self.error('GAccsess requires a name and tokenref: reference, not a secret')
                box.grants.append((token_name, token_ref)); continue
            box.share_project = self.boolean(); self.take(','); box.global_id = self.identifier()
            self.take(','); box.share_namespaces = self.boolean()
            if self.at(','):
                self.take(','); ref = self.identifier()
                if not isinstance(ref, str) or not ref.startswith('keyref:'):
                    raise self.error('Root must use keyref:, never an API secret')
                box.key_ref = ref
            self.take(']'); self.take('>'); self.take('}'); break
        return box

    def parse(self) -> Document:
        root = self.member(0)
        self.take('EOF')
        if not isinstance(root, Box) or root.name != 'Red-XAI' or root.local_id != 1 or root.global_id != 1:
            raise LanguageError('Exactly one Red-XAI root with local/global ID 1 is required')
        if not root.key_ref:
            raise LanguageError('Root footer must include a keyref: reference')
        document = Document(root); validate(document); return document


def parse(source: str) -> Document:
    try:
        return Parser(source).parse()
    except RecursionError as exc:
        raise LanguageError('Maximum nesting depth exceeded') from exc


def validate(document: Document) -> None:
    local_boxes, global_boxes, global_packers = set(), set(), set()
    def walk(box: Box, root: bool = False) -> None:
        if not root:
            if box.key_ref is not None:
                raise LanguageError('Only the root may declare a database key reference')
            if box.local_id is not None:
                key = (type(box.local_id), box.local_id)
                if box.local_id == 1 or key in local_boxes:
                    raise LanguageError('Duplicate or reserved box local ID')
                local_boxes.add(key)
            if box.global_id is not None:
                key = (type(box.global_id), box.global_id)
                if box.global_id == 1 or key in global_boxes:
                    raise LanguageError('Duplicate or reserved box global ID')
                global_boxes.add(key)
        if (box.share_project or box.share_namespaces) and box.global_id is None:
            raise LanguageError('Shared boxes require a global ID')
        locals_ = set()
        for node in box.members:
            if isinstance(node, Box):
                walk(node)
            else:
                if node.local_id is not None:
                    key = (node.name, type(node.local_id), node.local_id)
                    if key in locals_:
                        raise LanguageError('Duplicate Packer name/local ID in this box')
                    locals_.add(key)
                if node.global_id is not None:
                    key = (node.name, type(node.global_id), node.global_id)
                    if key in global_packers:
                        raise LanguageError('Duplicate Packer name/global ID')
                    global_packers.add(key)
    walk(document.root, True)


def id_source(value: str | int | None) -> str:
    return 'False' if value is None else (json.dumps(value, ensure_ascii=False) if isinstance(value, str) else str(value))


def value_source(value: Value) -> str:
    if value.kind == 'number': return value.data
    if value.kind == 'string': return json.dumps(value.data, ensure_ascii=False)
    if value.kind == 'boolean': return 'True' if value.data else 'False'
    if value.kind == 'nell': return 'NELL'
    if value.kind == 'array': return '[' + ', '.join(value_source(v) for v in value.data) + ']'
    if value.kind == 'table':
        return '{' + ', '.join('[' + value_source(k) + '] = ' + value_source(v) for k, v in value.data) + '}'
    if value.kind == 'meta':
        return 'meta(' + value_source(value.data[0]) + ', ' + value_source(value.data[1]) + ')'
    raise LanguageError('Unsupported value type')


def format_document(doc: Document) -> str:
    validate(doc)
    lines: list[str] = []
    def walk(box: Box, depth: int) -> None:
        pad = '    ' * depth
        lines.append(f'{pad}{{{json.dumps(box.name, ensure_ascii=False)}}}[{id_source(box.local_id)}]{{')
        for member in box.members:
            if isinstance(member, Box):
                walk(member, depth + 1)
            else:
                lines.append(f'{pad}    {{{json.dumps(member.name, ensure_ascii=False)}}}[{id_source(member.local_id)}] = [{value_source(member.value)}][{id_source(member.global_id)}],')
        for name, ref in box.grants:
            lines.append(f'{pad}    <[GAccsess,{json.dumps(name)},{json.dumps(ref)},]>')
        extra = ',' + json.dumps(box.key_ref) if box.key_ref else ''
        lines.append(f'{pad}<[{str(box.share_project)},{id_source(box.global_id)},{str(box.share_namespaces)}{extra}]>}}')
    walk(doc.root, 0)
    return '\n'.join(lines) + '\n'


def empty_source(key_ref: str = 'keyref:local') -> str:
    return '{Red-XAI}[1]{\n\n<[True,1,false,' + json.dumps(key_ref) + ']>}\n'
