from __future__ import annotations

import base64
from pathlib import Path

import pytest

from redxai.security import Vault


def _account(root: Path) -> str:
    import hashlib
    import os

    return hashlib.sha256(os.path.normcase(str(root.resolve())).encode('utf-8')).hexdigest()


def test_new_master_key_is_kept_in_os_credential_store_not_in_a_key_file(
    tmp_path, isolated_os_credentials
):
    root = tmp_path / 'vault'
    first = Vault(root)
    encrypted = first.encrypt('private note', 'database/revision')

    assert not (root / 'master.key').exists()
    stored = isolated_os_credentials.items[('Red-XAI database vault', _account(root))]
    assert len(base64.b64decode(stored, validate=True)) == 32
    assert Vault(root).decrypt(encrypted, 'database/revision') == 'private note'


def test_legacy_key_is_migrated_after_verification_and_removed(tmp_path, isolated_os_credentials):
    root = tmp_path / 'vault'
    root.mkdir()
    legacy_key = bytes(range(32))
    legacy_path = root / 'master.key'
    legacy_path.write_bytes(legacy_key)

    vault = Vault(root)

    assert not legacy_path.exists()
    saved = isolated_os_credentials.items[('Red-XAI database vault', _account(root))]
    assert base64.b64decode(saved, validate=True) == legacy_key
    assert vault.decrypt(vault.encrypt('legacy data', 'db/1'), 'db/1') == 'legacy data'

def test_interrupted_legacy_key_wipe_is_recovered_from_os_credential(
    tmp_path, isolated_os_credentials
):
    root = tmp_path / 'vault'
    root.mkdir()
    legacy_key = bytes(range(32))
    account = _account(root)
    isolated_os_credentials.set_password(
        'Red-XAI database vault',
        account,
        base64.b64encode(legacy_key).decode('ascii'),
    )
    (root / 'master.key').write_bytes(bytes(32))

    vault = Vault(root)

    assert not (root / 'master.key').exists()
    assert vault.decrypt(vault.encrypt('recovered', 'db/2'), 'db/2') == 'recovered'


def test_existing_database_with_missing_os_key_fails_closed(tmp_path):
    root = tmp_path / 'lost-key'
    root.mkdir()
    (root / 'redxai.sqlite3').write_bytes(b'existing database')

    with pytest.raises(ValueError, match='OS-protected master key is missing'):
        Vault(root)


def test_insecure_or_unavailable_keyring_backend_is_rejected(
    tmp_path, monkeypatch, isolated_os_credentials
):
    class PlaintextBackend:
        def get_password(self, *_args):
            return None

        def set_password(self, *_args):
            return None

    monkeypatch.setattr('redxai.security.keyring.get_keyring', PlaintextBackend)
    with pytest.raises(RuntimeError, match='OS-protected credential store is required'):
        Vault(tmp_path / 'no-backend')
