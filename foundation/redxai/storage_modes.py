"""Versioned portable containers for local and quick-search Red-XAI databases."""
from __future__ import annotations

import hashlib
import hmac
import json
import secrets
import struct
from pathlib import Path
from typing import Any

from cryptography.exceptions import InvalidTag
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

from .format import MAX_CONTAINER_BYTES, _derive
from .language import MAX_SOURCE_BYTES, parse

MAGIC = b'RXDBMODE\x00'
FORMAT_VERSION = 1
MAX_SEARCH_ROWS = 100_000
MAX_SEARCH_TEXT_BYTES = 16 * 1024 * 1024
SEARCHABLE_KINDS = frozenset({'box', 'packer'})
MODE_CODES = {'LS': 0, 'QS': 1, 'LSQS': 2}


class StorageModeError(ValueError):
    pass


def normalize_mode(mode: str) -> str:
    if mode not in MODE_CODES:
        raise StorageModeError('Storage mode must be LS, QS, or LSQS')
    return mode


def _records(source: str) -> list[dict[str, Any]]:
    document = parse(source)
    records = []
    for row in document.index():
        if row['kind'] not in SEARCHABLE_KINDS:
            continue
        name = row.get('name') or ''
        value = row.get('value')
        text = (
            json.dumps(value, ensure_ascii=False, sort_keys=True)
            if value is not None
            else ''
        )
        records.append({
            'path': row['path'],
            'kind': row['kind'],
            'name': name,
            'text': text,
            'search': f'{name}\n{text}',
        })
    return records


def _validated_rows(source: str) -> list[dict[str, Any]]:
    rows = _records(source)
    encoded = json.dumps(rows, ensure_ascii=False, separators=(',', ':')).encode('utf-8')
    if len(rows) > MAX_SEARCH_ROWS or len(encoded) > MAX_SEARCH_TEXT_BYTES:
        raise StorageModeError('Quick-search index exceeds its row or size limit')
    return rows


def build_search_index(source: str) -> list[dict[str, Any]]:
    """Build the bounded typed search records used by quick-search databases."""
    return _validated_rows(source)


def search_index_rows(
    rows: list[dict[str, Any]], query: str, limit: int = 20
) -> list[dict[str, Any]]:
    """Search a previously validated index without reparsing database source."""
    if not isinstance(query, str) or not 1 <= len(query.strip()) <= 500:
        raise StorageModeError('Search query must contain 1–500 characters')
    if type(limit) is not int or not 1 <= limit <= 100:
        raise StorageModeError('Search result limit must be from 1 to 100')
    needle = query.casefold()
    results = []
    for row in rows:
        if needle in row['search'].casefold():
            results.append({
                'path': row['path'],
                'kind': row['kind'],
                'name': row['name'],
                'text': row['text'][:4000],
            })
            if len(results) >= limit:
                break
    return results


def _header(mode: str, name: str, encrypted: bool, salt: bytes = b'', nonce: bytes = b'') -> bytes:
    encoded_name = name.encode('utf-8')
    if (
        not encoded_name
        or len(encoded_name) > 255
        or Path(name).name != name
        or name in {'.', '..'}
        or '/' in name
        or '\\' in name
        or '\x00' in name
        or not name.endswith(f'.Red-XAI-DB-{mode}')
    ):
        raise StorageModeError(
            f'Database name must be a simple filename ending in .Red-XAI-DB-{mode}'
        )
    header = MAGIC + struct.pack(
        '>BBBH', FORMAT_VERSION, MODE_CODES[mode], int(encrypted), len(encoded_name)
    ) + encoded_name
    return header + (salt + nonce if encrypted else b'')


def build_snapshot(source: str, mode: str, name: str = 'Database') -> bytes:
    """Build a bounded integrity-checked container with cleartext contents."""
    mode = normalize_mode(mode)
    if not isinstance(source, str) or len(source.encode('utf-8')) > MAX_SOURCE_BYTES:
        raise StorageModeError('Red-XAI source exceeds the 2 MiB limit')
    parse(source)
    rows = _validated_rows(source) if mode in {'QS', 'LSQS'} else None
    payload = {'source': source}
    if rows is not None:
        payload['index'] = rows
    encoded = json.dumps(payload, ensure_ascii=False, separators=(',', ':')).encode('utf-8')
    header = _header(mode, name, encrypted=False)
    checksum = hashlib.sha256(header + encoded).digest()
    result = header + encoded + checksum
    if len(result) > MAX_CONTAINER_BYTES:
        raise StorageModeError('Red-XAI database snapshot exceeds the 4 MiB limit')
    return result


def export_database(
    source: str,
    mode: str,
    name: str,
    passphrase: str | None = None,
) -> bytes:
    """Export one of the LS, QS, or LSQS containers, optionally AES-GCM encrypted."""
    mode = normalize_mode(mode)
    if not isinstance(source, str) or len(source.encode('utf-8')) > MAX_SOURCE_BYTES:
        raise StorageModeError('Red-XAI source exceeds the 2 MiB limit')
    parse(source)
    payload: dict[str, Any] = {'source': source}
    if mode in {'QS', 'LSQS'}:
        payload['index'] = _validated_rows(source)
    encoded = json.dumps(payload, ensure_ascii=False, separators=(',', ':')).encode('utf-8')
    encrypted = passphrase is not None
    if encrypted:
        salt, nonce = secrets.token_bytes(16), secrets.token_bytes(12)
        header = _header(mode, name, True, salt, nonce)
        body = AESGCM(_derive(passphrase, salt)).encrypt(nonce, encoded, header)
        result = header + body
    else:
        header = _header(mode, name, False)
        result = header + encoded + hashlib.sha256(header + encoded).digest()
    if len(result) > MAX_CONTAINER_BYTES:
        raise StorageModeError('Red-XAI database snapshot exceeds the 4 MiB limit')
    return result


def inspect_snapshot(payload: bytes, passphrase: str | None = None) -> dict[str, Any]:
    if (
        not isinstance(payload, bytes)
        or len(payload) > MAX_CONTAINER_BYTES
        or len(payload) < len(MAGIC) + 5 + 1 + 32
        or not payload.startswith(MAGIC)
    ):
        raise StorageModeError('Invalid or oversized Red-XAI database container')
    cursor = len(MAGIC)
    version, mode_code, encrypted, name_size = struct.unpack_from('>BBBH', payload, cursor)
    cursor += 5
    if version != FORMAT_VERSION or mode_code not in (0, 1, 2) or encrypted not in (0, 1):
        raise StorageModeError('Unsupported Red-XAI database mode or format version')
    if not 1 <= name_size <= 255 or cursor + name_size > len(payload):
        raise StorageModeError('Red-XAI database name length is invalid')
    try:
        name = payload[cursor:cursor + name_size].decode('utf-8')
    except UnicodeDecodeError as exc:
        raise StorageModeError('Red-XAI database name is not valid UTF-8') from exc
    cursor += name_size
    if (
        Path(name).name != name
        or name in {'.', '..'}
        or '/' in name
        or '\\' in name
        or '\x00' in name
    ):
        raise StorageModeError('Red-XAI database name is invalid')
    header = payload[:cursor]
    if encrypted:
        if passphrase is None:
            raise StorageModeError('This database container requires its passphrase')
        if len(payload) - cursor < 16 + 16 + 12:
            raise StorageModeError('Encrypted database container is truncated')
        salt, nonce = payload[cursor:cursor + 16], payload[cursor + 16:cursor + 28]
        header += salt + nonce
        try:
            encoded = AESGCM(_derive(passphrase, salt)).decrypt(
                nonce, payload[cursor + 28:], header
            )
        except (ValueError, InvalidTag) as exc:
            raise StorageModeError('Database passphrase incorrect or encrypted data corrupted') from exc
    else:
        if passphrase is not None:
            raise StorageModeError('Passphrase supplied for an unencrypted database container')
        if len(payload) - cursor < 32:
            raise StorageModeError('Red-XAI database checksum is missing')
        encoded, checksum = payload[cursor:-32], payload[-32:]
        if not hmac.compare_digest(hashlib.sha256(header + encoded).digest(), checksum):
            raise StorageModeError('Red-XAI database checksum mismatch')
    try:
        data = json.loads(encoded)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise StorageModeError('Red-XAI database payload is invalid JSON') from exc
    mode = {value: key for key, value in MODE_CODES.items()}[mode_code]
    if not isinstance(data, dict) or not isinstance(data.get('source'), str):
        raise StorageModeError('Red-XAI database source is missing')
    if not name.endswith(f'.Red-XAI-DB-{mode}'):
        raise StorageModeError('Red-XAI database filename does not match its storage mode')
    source = data['source']
    if len(source.encode('utf-8')) > MAX_SOURCE_BYTES:
        raise StorageModeError('Red-XAI source exceeds the 2 MiB limit')
    parse(source)
    rows = None
    if mode in {'QS', 'LSQS'}:
        rows = data.get('index')
        if not isinstance(rows, list) or len(rows) > MAX_SEARCH_ROWS:
            raise StorageModeError('Quick-search index is missing or exceeds the row limit')
        expected = _validated_rows(source)
        if rows != expected:
            raise StorageModeError('Quick-search index does not match the Red-XAI source')
    elif 'index' in data:
        raise StorageModeError('Local-only database container must not contain a search index')
    return {'version': version, 'mode': mode, 'name': name, 'source': source, 'index': rows}


def import_database(payload: bytes, passphrase: str | None = None) -> dict[str, Any]:
    """Validate an LS/QS/LSQS file before returning data for a transactional import."""
    return inspect_snapshot(payload, passphrase)


def search_index(source: str, query: str, limit: int = 20) -> list[dict[str, Any]]:
    """Bounded case-insensitive search over typed local source records."""
    return search_index_rows(_validated_rows(source), query, limit)
