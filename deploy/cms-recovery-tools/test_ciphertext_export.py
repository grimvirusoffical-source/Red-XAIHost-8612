import contextlib
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import types
import unittest
from unittest.mock import patch, Mock

spec = importlib.util.spec_from_file_location('ciphertext_export', Path(__file__).with_name('ciphertext-export.py'))
M = importlib.util.module_from_spec(spec)
spec.loader.exec_module(M)


class ExportTests(unittest.TestCase):
    def staging(self, root, child=None):
        stack = contextlib.ExitStack()
        self.addCleanup(stack.close)
        export = root / 'cms-export-fixture'
        export.mkdir()
        (export / 'pair.age').write_bytes(b'synthetic-encrypted-fixture')
        support = root / 'support'
        support.mkdir()
        receipt = root / 'transport-receipt.json'
        value = {'ok': True, 'kind': 'cms-pair-age-ciphertext-v1', 'pairSha256': M.PAIR_SHA,
                 'exportDirectory': str(export), 'ciphertextSha256': 'b'*64,
                 'ciphertextBytes': (export / 'pair.age').stat().st_size}
        output = {'ok': True, 'cmsStateUnchanged': True, 'ciphertextSha256': value['ciphertextSha256'],
                  'ciphertextBytes': value['ciphertextBytes'], 'pairSha256': M.PAIR_SHA}
        mocks = {}
        overrides = {'safe': lambda p: Path(p), 'BACKUPS': root, 'SUPPORT': support,
                     'code_file': Mock(), 'private_dir': Mock(), 'services': Mock(return_value={'public': 'same'}),
                     'native_identity': Mock(return_value={'pid': 123}),
                     'props': Mock(return_value={'ActiveState': 'active', 'User': 'redx-site', 'Group': 'redx-site',
                          'WorkingDirectory': str(M.c.SITE), 'FragmentPath': '/etc/systemd/system/'+M.c.HUB}),
                     'run': Mock(return_value=''), 'stopped': Mock(), 'probe_verify': Mock(return_value={'fixture': True}),
                     'inventory': Mock(return_value={'files': {}}), 'resumed_health': Mock(), 'write': Mock()}
        for name, value_override in overrides.items():
            stack.enter_context(patch.object(M.c, name, value_override))
            mocks[name] = value_override
        stack.enter_context(patch.object(M, 'STAGE_RECEIPT', receipt))
        stack.enter_context(patch.object(M, 'paired', return_value=(root, {'support': {'files': {}}})))
        stack.enter_context(patch.object(M, 'private_json', return_value=value))
        call = stack.enter_context(patch.object(M.subprocess, 'run', side_effect=child,
            return_value=types.SimpleNamespace(returncode=0, stdout=json.dumps(output).encode(), stderr=b'')))
        return mocks, call, support, receipt

    def test_inspection_never_stops_or_runs_transport(self):
        with tempfile.TemporaryDirectory() as directory:
            mocks, call, _, _ = self.staging(Path(directory))
            self.assertTrue(M.stage(False, False)['inspectionOnly'])
            mocks['services'].assert_not_called()
            mocks['run'].assert_not_called()
            call.assert_not_called()
            mocks['write'].assert_not_called()

    def test_success_resumes_only_private_hub_and_writes_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            mocks, call, _, _ = self.staging(Path(directory))
            self.assertTrue(M.stage(True, True)['privateHubResumed'])
            self.assertEqual([x.args[0] for x in mocks['run'].call_args_list],
                             [['/usr/bin/systemctl', 'stop', M.c.HUB], ['/usr/bin/systemctl', 'start', M.c.HUB]])
            self.assertEqual(call.call_count, 1)
            mocks['write'].assert_called_once()
            mocks['resumed_health'].assert_called_once()

    def test_timeout_before_lock_keeps_private_gateway_stopped(self):
        with tempfile.TemporaryDirectory() as directory:
            mocks, _, support, _ = self.staging(Path(directory), subprocess.TimeoutExpired('synthetic-command', 60))
            self.assertFalse((support / 'writer.lock').exists())
            with self.assertRaisesRegex(M.c.Refused, 'transport_unknown_or_failed'):
                M.stage(True, True)
            self.assertEqual(mocks['run'].call_count, 1)
            mocks['resumed_health'].assert_not_called()
            mocks['write'].assert_not_called()

    def test_retained_lock_refuses_resume_even_after_child_success(self):
        with tempfile.TemporaryDirectory() as directory:
            mocks, _, support, _ = self.staging(Path(directory))
            (support / 'writer.lock').write_text('synthetic fixture')
            with self.assertRaisesRegex(M.c.Refused, 'resume_refused'):
                M.stage(True, True)
            self.assertEqual(mocks['run'].call_count, 1)
            mocks['write'].assert_not_called()

    def test_existing_receipt_or_missing_attestation_never_changes_services(self):
        with tempfile.TemporaryDirectory() as directory:
            mocks, call, _, receipt = self.staging(Path(directory))
            with self.assertRaisesRegex(M.c.Refused, 'exclusive_prefix_writer'):
                M.stage(True, False)
            receipt.write_text('synthetic existing receipt')
            with self.assertRaisesRegex(M.c.Refused, 'one_off_transport'):
                M.stage(True, True)
            mocks['run'].assert_not_called()
            call.assert_not_called()

    def test_restore_receipt_must_bind_to_exact_pair_and_result(self):
        pair = {'nativeRevision': 'synthetic-revision', 'support': {'files': {}}}
        fixture = M.c.FIXTURES / 'cms-restore-synthetic'
        receipt = {'ok': True, 'pairSha256': M.PAIR_SHA, 'pairDirectory': str(M.c.BACKUPS / 'cms-pair-synthetic')}
        restored = {'ok': True, 'backupRestoreVerified': True, 'fullSupportMatched': True,
                    'liveServicesUnchanged': True, 'fixtureStopped': True,
                    'fixtureDirectory': str(fixture), 'resultSha256': 'c'*64}
        result = {'backupRestoreVerified': True, 'fullSupportMatched': True, 'nativeRevision': pair['nativeRevision']}
        with patch.object(M, 'private_json', side_effect=lambda p: receipt if p == M.PAIR_RECEIPT else restored), \
             patch.object(M.c, 'safe', side_effect=lambda p: Path(p)), patch.object(M.c, 'private_dir'), \
             patch.object(M.c, 'load_pair', return_value=pair), patch.object(M.c, 'sha', return_value='c'*64), \
             patch.object(M, 'fixture_json', side_effect=lambda p: dict(pair) if p.name == 'expected.json' else result):
            self.assertEqual(M.paired()[1], pair)
        with patch.object(M, 'private_json', side_effect=lambda p: receipt if p == M.PAIR_RECEIPT else restored), \
             patch.object(M.c, 'safe', side_effect=lambda p: Path(p)), patch.object(M.c, 'private_dir'), \
             patch.object(M.c, 'load_pair', return_value=pair), \
             patch.object(M, 'fixture_json', return_value={'nativeRevision': 'different-pair'}):
            with self.assertRaisesRegex(M.c.Refused, 'restore_pair_binding_mismatch'):
                M.paired()


if __name__ == '__main__':
    unittest.main()
