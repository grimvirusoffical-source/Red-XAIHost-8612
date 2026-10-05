from __future__ import annotations

import hashlib
import json
import sqlite3
import struct

import pytest

from redxai.language import MAX_SOURCE_BYTES
from redxai.storage_modes import (
    MAGIC,
    StorageModeError,
    build_snapshot,
    export_database,
    import_database,
    inspect_snapshot,
    search_index,
)
from redxai.store import Store


@pytest.mark.parametrize('mode', ['LS', 'QS', 'LSQS'])
def test_storage_mode_roundtrip_and_expected_index(sample, mode):
    payload = build_snapshot(sample, mode, f'Accounts.Red-XAI-DB-{mode}')
    loaded = inspect_snapshot(payload)

    assert loaded['source'] == sample
    assert loaded['mode'] == mode
    assert loaded['name'] == f'Accounts.Red-XAI-DB-{mode}'
    if mode == 'LS':
        assert loaded['index'] is None
    else:
        assert loaded['index']
        assert all(row['kind'] in {'box', 'packer'} for row in loaded['index'])


@pytest.mark.parametrize('mode', ['LS', 'QS', 'LSQS'])
@pytest.mark.parametrize('encrypted', [False, True])
def test_portable_export_import_roundtrip(sample, mode, encrypted):
    passphrase = 'SyntheticDatabase9!' if encrypted else None
    filename = f'Accounts.Red-XAI-DB-{mode}'
    payload = export_database(sample, mode, filename, passphrase)
    if encrypted:
        assert b'PlayerName' not in payload
    restored = import_database(payload, passphrase)

    assert restored['mode'] == mode
    assert restored['name'] == filename
    assert restored['source'] == sample


def test_encrypted_snapshot_rejects_missing_wrong_passphrase_and_tampering(sample):
    payload = export_database(sample, 'LSQS', 'Accounts.Red-XAI-DB-LSQS', 'SyntheticDatabase9!')
    with pytest.raises(StorageModeError, match='requires its passphrase'):
        import_database(payload)
    with pytest.raises(StorageModeError, match='passphrase incorrect'):
        import_database(payload, 'WrongDatabase9!')
    damaged = payload[:-1] + bytes([payload[-1] ^ 1])
    with pytest.raises(StorageModeError, match='passphrase incorrect'):
        import_database(damaged, 'SyntheticDatabase9!')


def test_plain_snapshot_rejects_tampering(sample):
    payload = bytearray(build_snapshot(sample, 'LS', 'Accounts.Red-XAI-DB-LS'))
    payload[-33] ^= 1
    with pytest.raises(StorageModeError, match='checksum mismatch'):
        inspect_snapshot(bytes(payload))


def test_snapshot_filename_suffix_must_match_mode(sample):
    payload = bytearray(build_snapshot(sample, 'LS', 'Accounts.Red-XAI-DB-LS'))
    name_offset = len(MAGIC) + 5
    old_name = b'Accounts.Red-XAI-DB-LS'
    new_name = b'Accounts.Red-XAI-DB-QS'
    assert len(old_name) == len(new_name)
    payload[name_offset:name_offset + len(old_name)] = new_name
    payload[-32:] = hashlib.sha256(payload[:-32]).digest()

    with pytest.raises(StorageModeError, match='filename does not match'):
        inspect_snapshot(bytes(payload))


def test_search_index_is_bounded_and_returns_typed_object_paths(sample):
    result = search_index(sample, 'PlayerName')
    assert len(result) == 1
    assert result[0]['kind'] == 'packer'
    assert result[0]['path']
    with pytest.raises(StorageModeError, match='query'):
        search_index(sample, ' ')
    with pytest.raises(StorageModeError, match='limit'):
        search_index(sample, 'Player', 101)


def test_invalid_modes_filenames_and_large_sources_are_rejected(sample):
    with pytest.raises(StorageModeError, match='mode'):
        build_snapshot(sample, 'SQL', 'Accounts')
    for filename in ('../escape', 'C:\\escape', '', '.', 'Accounts.Red-XAI-DB-QS'):
        with pytest.raises(StorageModeError, match='filename'):
            build_snapshot(sample, 'LS', filename)
    with pytest.raises(StorageModeError, match='2 MiB'):
        export_database('x' * (MAX_SOURCE_BYTES + 1), 'LS', 'large.Red-XAI-DB-LS')


def test_index_tampering_cannot_be_recovered_by_recomputing_plain_checksum(sample):
    payload = build_snapshot(sample, 'QS', 'Accounts.Red-XAI-DB-QS')
    mode_offset = len(MAGIC)
    _version, _mode, encrypted, name_size = struct.unpack_from('>BBBH', payload, mode_offset)
    assert encrypted == 0
    header_size = len(MAGIC) + 5 + name_size
    data = json.loads(payload[header_size:-32])
    data['index'][0]['name'] = 'tampered'
    encoded = json.dumps(data, ensure_ascii=False, separators=(',', ':')).encode()
    forged = payload[:header_size] + encoded
    forged += hashlib.sha256(forged).digest()
    with pytest.raises(StorageModeError, match='does not match'):
        inspect_snapshot(forged)


def test_existing_database_schema_migrates_to_ls(session):
    store, principal, _ = session
    created = store.create_database(principal, 'Legacy')
    with sqlite3.connect(store.path) as db:
        db.execute('ALTER TABLE databases DROP COLUMN storage_mode')

    migrated = Store(store.root)
    document = migrated.read_database(principal, created['id'])
    assert document['storage_mode'] == 'LS'
    assert document['source'] == created['source']
