import importlib.util
import json
from datetime import datetime, timedelta, timezone
from pathlib import Path
import tempfile
import subprocess
import sys
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('native_durability', Path(__file__).with_name('native-durability-evidence.py'))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)
NOW = datetime(2026, 11, 3, 20, 0, tzinfo=timezone.utc)


def observation(raw=''):
    return {'source': 'owner-authenticated-native-config', 'dbRevision': MODULE.REVISION,
            'observedUtc': NOW.isoformat(), 'rawEngineDurability': raw}


class DurabilityEvidenceTests(unittest.TestCase):
    def test_empty_is_the_pinned_group_default_and_not_unknown(self):
        report = MODULE.interpret(observation(), NOW)
        self.assertEqual(report['effectiveEngineDurability'], 'group')
        self.assertEqual(report['resolution'], 'pinned-redxd-default')
        self.assertTrue(report['durabilitySettingSupportedForCms'])
        self.assertFalse(report['runtimeProvenanceVerified'])
        self.assertFalse(report['authenticatedObservationVerified'])
        self.assertFalse(report['productionReady'])

    def test_explicit_modes_and_async_are_not_conflated(self):
        for raw, supported in [('sync', True), ('group', True), ('async', False)]:
            report = MODULE.interpret(observation(raw), NOW)
            self.assertEqual(report['effectiveEngineDurability'], raw)
            self.assertEqual(report['durabilitySettingSupportedForCms'], supported)
            self.assertEqual(report['resolution'], 'explicit-config')

    def test_missing_unknown_and_wrong_authority_fields_are_refused(self):
        for raw in [None, True, 'SYNC', 'group-commit', 'default', 'secret-value']:
            with self.assertRaises(ValueError):
                MODULE.interpret(observation(raw), NOW)
        for field, value in [('source', 'unverified'), ('dbRevision', '0' * 40), ('unexpected', 'secret-value')]:
            value_dict = observation()
            value_dict[field] = value
            with self.assertRaises(ValueError):
                MODULE.interpret(value_dict, NOW)

    def test_stale_future_and_non_utc_evidence_are_refused(self):
        for stamp in [(NOW - timedelta(seconds=601)).isoformat(), (NOW + timedelta(seconds=31)).isoformat(),
                      NOW.replace(tzinfo=None).isoformat(), '2026-11-03T21:00:00+01:00']:
            value = observation()
            value['observedUtc'] = stamp
            with self.assertRaises(ValueError):
                MODULE.interpret(value, NOW)

    def test_duplicate_and_oversized_json_are_refused_without_echo(self):
        with self.assertRaises(ValueError):
            MODULE.decode_observation(b'{"dbRevision":"a","dbRevision":"b"}')
        with self.assertRaises(ValueError):
            MODULE.decode_observation(b' ' * 4097)
        with self.assertRaises(ValueError):
            MODULE.decode_observation(b'{"value":NaN}')
        self.assertEqual(MODULE.decode_observation(json.dumps(observation()).encode()), observation())

    def test_source_file_mismatch_is_refused_and_cannot_widen_mode(self):
        with tempfile.TemporaryDirectory(prefix='redx-durability-evidence-') as directory:
            file = Path(directory) / 'source.go'
            file.write_bytes(b'reviewed source\n')
            expected = MODULE.hashlib.sha256(file.read_bytes()).hexdigest()
            with patch.object(MODULE, 'SOURCE_HASHES', {'source.go': expected}), patch.object(MODULE.subprocess, 'run', side_effect=OSError()):
                self.assertEqual(MODULE.verify_source(directory), 'pinned-source-files')
                file.write_bytes(b'changed source\n')
                with self.assertRaises(ValueError):
                    MODULE.verify_source(directory)

    def test_cli_failure_never_echoes_refused_input_or_argument_values(self):
        marker = 'synthetic-refused-input-marker'
        script = Path(__file__).with_name('native-durability-evidence.py')
        with tempfile.TemporaryDirectory(prefix='redx-durability-cli-') as directory:
            for arguments, value in [(['--native-source', directory], {'unexpected': marker}),
                                     (['--unsupported', marker], {})]:
                result = subprocess.run([sys.executable, str(script), *arguments],
                                        input=json.dumps(value), encoding='utf-8',
                                        stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=5,
                                        env={key: value for key, value in MODULE.os.environ.items()
                                             if key.upper() in MODULE.SAFE_ENV_NAMES})
                self.assertEqual(result.returncode, 1)
                self.assertTrue(marker not in result.stdout and marker not in result.stderr)
                self.assertEqual(json.loads(result.stdout), {
                    'ok': False, 'error': 'native_durability_evidence_refused', 'productionReady': False})


if __name__ == '__main__':
    unittest.main()
