"""Versioned .Red-XAI snapshot container; no live credentials are exported."""
from __future__ import annotations
import base64
import hashlib
import hmac
import json
import secrets
import struct
from argon2.low_level import Type, hash_secret_raw
from cryptography.exceptions import InvalidTag
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from .language import MAX_SOURCE_BYTES, parse

MAGIC = b'RED-XAI\x00\x02'
MAX_CONTAINER_BYTES = 4 * 1024 * 1024


def _derive(password: str, salt: bytes) -> bytes:
    if not isinstance(password, str) or not 8 <= len(password) <= 600:
        raise ValueError('Snapshot passphrase must contain 8–600 characters')
    return hash_secret_raw(password.encode(), salt, time_cost=3, memory_cost=65536,
                           parallelism=2, hash_len=32, type=Type.ID)


def export_snapshot(source: str, name: str, password: str | None = None) -> bytes:
    parse(source)
    body = json.dumps({'format': 'Red-XAI', 'version': 2, 'name': name,
                       'source': source}, ensure_ascii=False, separators=(',', ':')).encode()
    if password is not None:
        salt, nonce = secrets.token_bytes(16), secrets.token_bytes(12)
        header = MAGIC + b'\x01' + salt + nonce
        payload = header + AESGCM(_derive(password, salt)).encrypt(nonce, body, header)
    else:
        # This hash detects accidental corruption; it is not an authenticity signature.
        header = MAGIC + b'\x00'
        payload = header + hashlib.sha256(body).digest() + body
    if len(payload) > MAX_CONTAINER_BYTES:
        raise ValueError('Snapshot too large')
    return payload


def import_snapshot(payload: bytes, password: str | None = None) -> dict:
    if not isinstance(payload, bytes) or len(payload) > MAX_CONTAINER_BYTES or len(payload) < len(MAGIC)+33:
        raise ValueError('Invalid or oversized .Red-XAI container')
    if not payload.startswith(MAGIC):
        raise ValueError('Unsupported Red-XAI format/version; text source can be pasted into the editor')
    flag = payload[len(MAGIC)]; offset = len(MAGIC)+1
    if flag == 1:
        if password is None: raise ValueError('This snapshot requires its passphrase')
        salt, nonce = payload[offset:offset+16], payload[offset+16:offset+28]
        header, encrypted = payload[:offset+28], payload[offset+28:]
        try:
            body = AESGCM(_derive(password, salt)).decrypt(nonce, encrypted, header)
        except (ValueError, InvalidTag) as exc:
            raise ValueError('Snapshot passphrase incorrect or encrypted data corrupted') from exc
    elif flag == 0:
        checksum, body = payload[offset:offset+32], payload[offset+32:]
        if not hmac.compare_digest(checksum, hashlib.sha256(body).digest()):
            raise ValueError('Snapshot checksum mismatch')
    else:
        raise ValueError('Unknown snapshot encryption mode')
    try:
        result = json.loads(body)
    except (UnicodeDecodeError, ValueError) as exc:
        raise ValueError('Invalid snapshot document') from exc
    if not isinstance(result, dict) or result.get('format') != 'Red-XAI' or result.get('version') != 2:
        raise ValueError('Invalid snapshot metadata')
    parse(result.get('source'))
    return result
