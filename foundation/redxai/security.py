"""Credential validation and established cryptography; no homemade encryption."""
from __future__ import annotations
import hashlib
import base64
import os
import re
import secrets
import string
from pathlib import Path
import platform
import keyring
from argon2 import PasswordHasher
from argon2.exceptions import VerificationError, InvalidHashError
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

OWNER_EMAIL = 'grimvirusoffical@gmail.com'
PASSWORDS = PasswordHasher(time_cost=3, memory_cost=65536, parallelism=2, hash_len=32, salt_len=16)


def validate_registration(username: str, password: str, confirm_password: str,
                          email: str, confirm_email: str) -> str:
    if not isinstance(username, str) or not 5 <= len(username) <= 64:
        raise ValueError('Username must contain 5–64 characters')
    if sum(c.isalpha() for c in username) < 3 or any(c.isspace() or ord(c) < 32 for c in username):
        raise ValueError('Username needs at least three letters and no whitespace/control characters')
    if not isinstance(password, str) or not 8 <= len(password) <= 600:
        raise ValueError('Password must contain 8–600 characters; it is never truncated')
    if not (any(c.isupper() for c in password) and any(c.islower() for c in password)
            and any(c.isdigit() for c in password) and any(c in string.punctuation for c in password)):
        raise ValueError('Password needs uppercase, lowercase, a number, and a special symbol')
    if password != confirm_password:
        raise ValueError('Passwords do not match')
    if not isinstance(email, str) or len(email) > 254 or not re.fullmatch(r'[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+', email):
        raise ValueError('Enter a valid email, including custom-domain addresses')
    normalized = email.strip().casefold()
    if not isinstance(confirm_email, str) or normalized != confirm_email.strip().casefold():
        raise ValueError('Email addresses do not match')
    return normalized


def verify_password(encoded: str, candidate: str) -> bool:
    if not isinstance(candidate, str) or not 8 <= len(candidate) <= 600:
        return False
    try:
        return PASSWORDS.verify(encoded, candidate)
    except (VerificationError, InvalidHashError):
        return False


def token(prefix: str) -> str:
    return prefix + secrets.token_urlsafe(32)


def digest(value: str) -> str:
    return hashlib.sha256(value.encode('utf-8')).hexdigest()


def private_file(path: Path, data: bytes) -> None:
    """Atomic exclusive creation; caller owns the protected parent directory."""
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(data); stream.flush(); os.fsync(stream.fileno())


class Vault:
    def __init__(self, root: Path):
        root.mkdir(parents=True, exist_ok=True)
        if os.name != 'nt':
            root.chmod(0o700)
        root = root.resolve()
        path = root / 'master.key'
        if path.is_symlink():
            raise ValueError('Master key cannot be a symbolic link')
        backend = self._secure_backend()
        account = hashlib.sha256(os.path.normcase(str(root)).encode('utf-8')).hexdigest()
        encoded = backend.get_password('Red-XAI database vault', account)
        key = base64.b64decode(encoded, validate=True) if encoded else None
        legacy_key = path.read_bytes() if path.exists() else None
        if legacy_key is not None:
            if len(legacy_key) != 32:
                raise ValueError('Invalid legacy master key; restore the key with its database backup')
            if key is not None and legacy_key == bytes(32):
                self._remove_legacy_key(path)
            elif key is not None and key != legacy_key:
                raise ValueError('OS credential and legacy master key differ; refusing to choose one')
            elif key is None:
                if legacy_key == bytes(32):
                    raise ValueError('Invalid empty legacy master key; restore the key with its database backup')
                key = legacy_key
                backend.set_password(
                    'Red-XAI database vault',
                    account,
                    base64.b64encode(key).decode('ascii'),
                )
                confirmed = backend.get_password('Red-XAI database vault', account)
                if confirmed != base64.b64encode(key).decode('ascii'):
                    raise RuntimeError('Could not verify master-key migration to the OS credential store')
                self._remove_legacy_key(path)
            else:
                self._remove_legacy_key(path)
        elif key is None:
            if (root / 'redxai.sqlite3').exists():
                raise ValueError(
                    'The OS-protected master key is missing; restore the matching database key before continuing'
                )
            key = AESGCM.generate_key(bit_length=256)
            encoded = base64.b64encode(key).decode('ascii')
            backend.set_password('Red-XAI database vault', account, encoded)
            if backend.get_password('Red-XAI database vault', account) != encoded:
                raise RuntimeError('Could not verify master-key creation in the OS credential store')
        self._cipher = AESGCM(key)

    @staticmethod
    def _secure_backend():
        backend = keyring.get_keyring()
        providers = {
            'Windows': ('keyring.backends.Windows',),
            'Darwin': ('keyring.backends.macOS',),
            'Linux': ('keyring.backends.SecretService', 'keyring.backends.kwallet'),
        }
        allowed = providers.get(platform.system(), ())
        module = type(backend).__module__
        if not allowed or not any(module.startswith(prefix) for prefix in allowed):
            raise RuntimeError(
                'A supported OS-protected credential store is required '
                '(Windows Credential Manager, macOS Keychain, or Linux Secret Service/KWallet).'
            )
        return backend

    @staticmethod
    def _remove_legacy_key(path: Path) -> None:
        size = path.stat().st_size
        with path.open('r+b', buffering=0) as stream:
            stream.write(b'\0' * size)
            os.fsync(stream.fileno())
        path.unlink()

    def encrypt(self, text: str, associated_data: str) -> bytes:
        nonce = secrets.token_bytes(12)
        return nonce + self._cipher.encrypt(nonce, text.encode('utf-8'), associated_data.encode())

    def decrypt(self, payload: bytes, associated_data: str) -> str:
        return self._cipher.decrypt(payload[:12], payload[12:], associated_data.encode()).decode('utf-8')
