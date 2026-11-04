#!/usr/bin/env python3
"""Resolve one selected native-config observation against pinned RedX source.

Read-only: no network, service operations, keys, environment values or database
files. This proves source interpretation, not runtime provenance/authentication
or full release readiness. Feed only the four documented non-secret fields.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

REVISION = '8512e1704e589d24535e08082c3ab9f832d7655d'
SOURCE_HASHES = {
    'cmd/redxd/main.go': '42ec48024d23777654f1fb39b95c5b653dc45e2d1c47a6869737eab163434e57',
    'cmd/redxd/opener_engine.go': '8356e273caa5d82a92f41ca192e1b6c86a62a7d46a76cdc85443f30c20775ecf',
    'host/engineadapter/adapter.go': '0b558ccc8a10aa34cd4bcabcdb64f793bc822d085a2e19f9f6ab555d34e8e211',
    'engine/db.go': '5b6c15be9eabf131c3a82685b1c7ce28ad6fa0371b48d59fa13c71236a83b96f',
    'engine/wal.go': 'e39f9d36e77ca3eee7a81095cc81e56d2a2c563bd13126ba4e2083cb0e55c3ca',
    'host/admin_api.go': 'd54cf858e3c73a90db9bceacaf0d0c790d44e921083cc7f59067166400bb76df',
    'host/config.go': '111b787f1d6dbb9e27a3216bf006a6415ff4ef2e02a548a1037bafa51082bf1b',
    'host/host.go': '67b4275e7c78b5b14137951baf3ffed1d9a957d1100f08ee668188ea3cc6b87e',
}
SAFE_ENV_NAMES = {'PATH', 'SYSTEMROOT', 'WINDIR', 'TEMP', 'TMP', 'LANG'}


class QuietParser(argparse.ArgumentParser):
    def error(self, _message):
        raise ValueError('Unsupported arguments')


def reject_duplicates(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('Duplicate field')
        result[key] = value
    return result


def decode_observation(data):
    if len(data) > 4096:
        raise ValueError('Observation too large')
    return json.loads(data.decode('utf-8'), object_pairs_hook=reject_duplicates,
                      parse_constant=lambda _: (_ for _ in ()).throw(ValueError('Nonfinite value')))


def interpret(observation, now=None):
    if not isinstance(observation, dict) or set(observation) != {'source', 'observedUtc', 'dbRevision', 'rawEngineDurability'}:
        raise ValueError('Selected observation fields required')
    if observation['source'] != 'owner-authenticated-native-config' or observation['dbRevision'] != REVISION:
        raise ValueError('Unsupported observation provenance')
    stamp = observation['observedUtc']
    if not isinstance(stamp, str) or len(stamp) > 40:
        raise ValueError('Bounded observation timestamp required')
    observed = datetime.fromisoformat(stamp.replace('Z', '+00:00'))
    if observed.tzinfo is None or observed.utcoffset().total_seconds() != 0:
        raise ValueError('UTC observation required')
    age = ((now or datetime.now(timezone.utc)) - observed).total_seconds()
    if age < -30 or age > 600:
        raise ValueError('Fresh observation required')
    raw = observation['rawEngineDurability']
    if not isinstance(raw, str) or raw not in ('', 'sync', 'group', 'async'):
        raise ValueError('Unsupported raw durability')
    effective = 'group' if raw == '' else raw
    return {
        'ok': True, 'dbRevision': REVISION, 'observedUtc': stamp,
        'rawEngineDurability': raw, 'effectiveEngineDurability': effective,
        'resolution': 'pinned-redxd-default' if raw == '' else 'explicit-config',
        'durabilitySettingSupportedForCms': effective in ('sync', 'group'),
        'sourceInterpretationVerified': False,
        'runtimeProvenanceVerified': False, 'authenticatedObservationVerified': False,
        'filesystemPowerLossTested': False, 'productionReady': False,
    }


def verify_source(directory):
    root = Path(directory).resolve(strict=True)
    if not root.is_dir():
        raise ValueError('Native source directory required')
    environment = {key: value for key, value in os.environ.items() if key.upper() in SAFE_ENV_NAMES}
    mode = 'pinned-git-objects'
    try:
        probe = subprocess.run(['git', '--no-pager', '-C', str(root), 'cat-file', '-e', REVISION + '^{commit}'],
                               env=environment, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
        if probe.returncode != 0:
            mode = 'pinned-source-files'
    except (OSError, subprocess.TimeoutExpired):
        mode = 'pinned-source-files'
    for name, expected in SOURCE_HASHES.items():
        if mode == 'pinned-git-objects':
            result = subprocess.run(['git', '--no-pager', '-C', str(root), 'cat-file', 'blob', REVISION + ':' + name],
                                    env=environment, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=5)
            if result.returncode != 0:
                raise ValueError('Pinned source unavailable')
            data = result.stdout
        else:
            file = root / name
            if file.is_symlink() or file.resolve(strict=True) != file or not file.is_file() or file.stat().st_size > 1024 * 1024:
                raise ValueError('Canonical bounded source required')
            data = file.read_bytes()
        if len(data) > 1024 * 1024 or hashlib.sha256(data).hexdigest() != expected:
            raise ValueError('Pinned source mismatch')
    return mode


def main():
    parser = QuietParser(description=__doc__)
    parser.add_argument('--native-source', required=True)
    parser.add_argument('--observation-file', type=Path)
    try:
        args = parser.parse_args()
        if args.observation_file:
            with args.observation_file.open('rb') as file:
                data = file.read(4097)
        else:
            data = sys.stdin.buffer.read(4097)
        report = interpret(decode_observation(data))
        report['sourceVerificationMethod'] = verify_source(args.native_source)
        report['sourceFilesVerified'] = len(SOURCE_HASHES)
        report['sourceInterpretationVerified'] = True
    except Exception:
        print(json.dumps({'ok': False, 'error': 'native_durability_evidence_refused', 'productionReady': False}))
        return 1
    print(json.dumps(report, sort_keys=True))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
