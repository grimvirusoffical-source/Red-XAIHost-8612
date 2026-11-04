#!/usr/bin/env python3
"""Prepare or stage one protected, encrypted CMS pair. Never reads a key."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import types
import uuid

import cms_recovery as c

PAIR_RECEIPT = Path('/srv/red-xai/stage-tools/cms-pair-live-20261103.json')
PAIR_SHA = '41002e46e56ac9176650dcc056950216f7942b812fa1719979092ca4424ed571'
RESTORE_RECEIPT = Path('/srv/red-xai/stage-tools/cms-restore-private-pid-20261103.json')
EXPORT_RECEIPT = Path('/srv/red-xai/stage-tools/cms-export-live-20261103.json')
STAGE_RECEIPT = Path('/srv/red-xai/stage-tools/cms-transport-live-20261103.json')
AGE_ARCHIVE = Path('/srv/red-xai/stage-tools/age-v1.3.2-linux-amd64.tar.gz')
AGE_ARCHIVE_SHA = 'cbe24006683f8eb669266162894b9a522a1af52f2665fbc63a4bb032ed26ac10'
AGE = Path('/srv/red-xai/stage-tools/age-1.3.2/age')
RECIPIENT = 'age15us8u4ap4twmw3wdhwgusfywydanhhyj74h0juuptujfuwl4kdhqqx0eem'
PROBE = Path('/opt/redx-cms-maintenance-ba8f91f/cms-maintenance-probe.mjs')
PROBE_SHA = 'cee27eff622777d999f0a07475bcd221ef696200ba03b1316299d9fde1fec48e'
TRANSPORT = Path('/opt/redx-cms-maintenance-ba8f91f/ciphertext-transport.mjs')
TRANSPORT_SHA = 'a62461df4f3bf59bb0ce014ff91f60ab81ce376a6be005c8259cd5a0a811d123'
MAX_CIPHER = 1024 * 1024


def private_json(p):
    st = c.code_file(p)
    c.need(st.st_mode & 0o077 == 0 and st.st_size < 1024 * 1024, 'private_receipt_required')
    return json.loads(p.read_bytes())


def paired():
    receipt = private_json(PAIR_RECEIPT)
    c.need(receipt.get('ok') is True and receipt.get('pairSha256') == PAIR_SHA,
           'capture_receipt_mismatch')
    bundle = c.safe(receipt['pairDirectory'])
    c.need(bundle.parent == c.BACKUPS and bundle.name.startswith('cms-pair-'), 'pair_path_refused')
    restored = private_json(RESTORE_RECEIPT)
    c.need(all(restored.get(x) is True for x in ('ok', 'backupRestoreVerified',
           'fullSupportMatched', 'liveServicesUnchanged', 'fixtureStopped')), 'actual_restore_required')
    pair = c.load_pair(bundle, PAIR_SHA)
    fixture = c.safe(restored['fixtureDirectory'])
    c.need(fixture.parent == c.FIXTURES and fixture.name.startswith('cms-restore-'), 'restore_fixture_path_refused')
    c.private_dir(fixture)
    expected = fixture_json(fixture / 'work/expected.json')
    for extension in ('restoreDriverSha256', 'moduleSha256', 'hostPidNamespaceInode'):
        expected.pop(extension, None)
    c.need(expected == pair, 'restore_pair_binding_mismatch')
    result_path = fixture / 'work/restore-result.json'
    result = fixture_json(result_path)
    c.need(c.sha(result_path) == restored.get('resultSha256') and
           result.get('backupRestoreVerified') is True and result.get('fullSupportMatched') is True and
           result.get('nativeRevision') == pair['nativeRevision'], 'restore_result_binding_mismatch')
    return bundle, pair


def fixture_json(p):
    st = c.safe(p).lstat()
    import stat
    c.need(stat.S_ISREG(st.st_mode) and st.st_nlink == 1 and st.st_uid == 986 and
           st.st_gid == 976 and stat.S_IMODE(st.st_mode) == 0o600 and st.st_size < 1024*1024,
           'private_fixture_receipt_required')
    return json.loads(p.read_bytes())


def verified_age():
    c.code_file(AGE_ARCHIVE, AGE_ARCHIVE_SHA)
    with tarfile.open(AGE_ARCHIVE, 'r:gz') as tar:
        selected = [m for m in tar if m.name == 'age/age']
        c.need(len(selected) == 1 and selected[0].isreg() and selected[0].size < 64*1024*1024,
               'vendor_binary_member_refused')
        with tar.extractfile(selected[0]) as f:
            digest = hashlib.file_digest(f, 'sha256').hexdigest()
    c.code_file(AGE, digest, True)
    c.need(c.run([AGE, '--version']).strip().lstrip('v') == '1.3.2', 'vendor_version_mismatch')
    return digest


def prepare(apply):
    bundle, pair = paired()
    age_sha = verified_age()
    c.need(not EXPORT_RECEIPT.exists() and not STAGE_RECEIPT.exists(), 'one_off_receipt_exists')
    if not apply:
        return {'ok': True, 'inspectionOnly': True, 'pairVerified': True, 'ageVerified': True}
    c.private_dir(c.BACKUPS)
    export = c.BACKUPS / ('cms-export-' + uuid.uuid4().hex)
    export.mkdir(mode=0o700)
    archive, cipher = export / 'pair.tar', export / 'pair.age'
    with archive.open('xb') as f:
        os.fchmod(f.fileno(), 0o600)
        with tarfile.open(fileobj=f, mode='w') as tar:
            for name in ('pair.json', 'native.tgz', 'support.tgz'):
                member = bundle / name
                st = c.code_file(member)
                info = tarfile.TarInfo(name)
                info.size, info.mode = st.st_size, 0o600
                with member.open('rb') as source:
                    tar.addfile(info, source)
        f.flush()
        os.fsync(f.fileno())
    c.need(archive.stat().st_size < MAX_CIPHER-4096, 'bounded_export_size_exceeded')
    c.run([AGE, '-r', RECIPIENT, '-o', cipher, archive])
    c.code_file(cipher)
    fd = os.open(cipher, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)
    c.sync_dir(export)
    with cipher.open('rb') as encrypted:
        c.need(100 < cipher.stat().st_size <= MAX_CIPHER and
               encrypted.read(22) == b'age-encryption.org/v1\n', 'encrypted_export_refused')
    c.need(c.load_pair(bundle, PAIR_SHA) == pair, 'pair_changed_during_export')
    result = {'ok': True, 'kind': 'cms-pair-age-ciphertext-v1', 'exportDirectory': str(export),
              'ciphertextSha256': c.sha(cipher), 'ciphertextBytes': cipher.stat().st_size,
              'plaintextTarSha256': c.sha(archive), 'pairSha256': PAIR_SHA,
              'ageVersion': '1.3.2', 'ageBinarySha256': age_sha, 'offServerVerified': False}
    c.write(EXPORT_RECEIPT, c.encoded(result))
    return result


def stage(apply, exclusive):
    _, pair = paired()
    value = private_json(EXPORT_RECEIPT)
    c.need(value.get('ok') is True and value.get('pairSha256') == PAIR_SHA and
           value.get('kind') == 'cms-pair-age-ciphertext-v1', 'export_receipt_mismatch')
    export = c.safe(value['exportDirectory'])
    c.need(export.parent == c.BACKUPS and export.name.startswith('cms-export-'), 'export_path_refused')
    c.private_dir(export)
    cipher = export / 'pair.age'
    c.code_file(cipher, value['ciphertextSha256'])
    c.need(cipher.stat().st_size == value['ciphertextBytes'] <= MAX_CIPHER, 'cipher_size_mismatch')
    c.code_file(TRANSPORT, TRANSPORT_SHA)
    c.code_file(PROBE, PROBE_SHA)
    c.need(not STAGE_RECEIPT.exists(), 'one_off_transport_already_recorded')
    if not apply:
        return {'ok': True, 'inspectionOnly': True, 'encryptedExportVerified': True}
    c.need(exclusive, 'exclusive_prefix_writer_attestation_required')
    baseline = c.services()
    identity = c.native_identity(Path('/opt/redx-db-website-ec6e9798/data'))
    hub = c.props(c.HUB, ('ActiveState', 'User', 'Group', 'WorkingDirectory', 'FragmentPath'))
    c.need(hub.get('ActiveState') == 'active' and hub.get('User') == 'redx-site' and
           hub.get('Group') == 'redx-site' and hub.get('WorkingDirectory') == str(c.SITE) and
           hub.get('FragmentPath') == '/etc/systemd/system/' + c.HUB, 'healthy_private_hub_required')
    transport_started, transport_succeeded = False, False
    try:
        c.run(['/usr/bin/systemctl', 'stop', c.HUB])
        c.stopped()
        probe = types.SimpleNamespace(probe=str(PROBE))
        before = c.probe_verify(probe)
        c.need(c.inventory(c.SUPPORT) == pair['support'], 'current_support_differs_from_pair')
        data = {'kind': value['kind'], 'exclusiveWriterReviewed': True,
                'sha256': value['ciphertextSha256'], 'pairSha256': PAIR_SHA,
                'data': base64.b64encode(cipher.read_bytes()).decode('ascii')}
        transport_started = True
        run = subprocess.run(['/usr/sbin/runuser', '-u', 'redx-site', '--', '/usr/bin/env', '-i',
                              'PATH=/usr/bin:/bin', 'NODE_ENV=production', '/usr/bin/node',
                              str(TRANSPORT), '--stage-once'], input=c.encoded(data),
                             stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=c.ENV, timeout=60)
        transport_succeeded = run.returncode == 0
        c.need(transport_succeeded and len(run.stdout) < 4096, 'transport_failed_private_output_withheld')
        result = json.loads(run.stdout)
        c.need(result.get('ok') is True and result.get('cmsStateUnchanged') is True and
               result.get('ciphertextSha256') == value['ciphertextSha256'] and
               result.get('ciphertextBytes') == value['ciphertextBytes'] and
               result.get('pairSha256') == PAIR_SHA, 'transport_receipt_mismatch')
        c.need(c.probe_verify(probe) == before and c.inventory(c.SUPPORT) == pair['support'],
               'cms_changed_during_transport')
        c.need(c.native_identity(Path('/opt/redx-db-website-ec6e9798/data')) == identity and
               c.services() == baseline, 'customer_service_changed')
    finally:
        # Timeout/launch errors can leave a descendant not yet owning the lock.
        # No absent-lock assumption may resume the gateway in that case.
        c.need(not transport_started or transport_succeeded,
               'transport_unknown_or_failed_private_hub_remains_stopped')
        # Failed helper operations retain the lock and forbid an unsafe resume.
        c.need(not (c.SUPPORT / 'writer.lock').exists(), 'resume_refused_inspect_retained_lock')
        c.run(['/usr/bin/systemctl', 'start', c.HUB])
        c.resumed_health()
    c.need(c.services() == baseline, 'customer_service_changed_after_resume')
    result.update(privateHubResumed=True, customerServicesUnchanged=True)
    c.write(STAGE_RECEIPT, c.encoded(result))
    return result


def main():
    os.umask(0o077)
    c.need(sys.platform == 'linux' and os.geteuid() == 0, 'linux_root_required')
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('action', choices=('prepare', 'stage'))
    p.add_argument('--apply', action='store_true')
    p.add_argument('--exclusive-writer-reviewed', action='store_true')
    a = p.parse_args()
    print(json.dumps(prepare(a.apply) if a.action == 'prepare' else
                     stage(a.apply, a.exclusive_writer_reviewed), sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except Exception as exc:
        print(json.dumps({'ok': False, 'code': str(exc) if isinstance(exc, c.Refused) else
                          'operation_failed_private_details_withheld', 'partialEvidenceRetained': True}), file=sys.stderr)
        raise SystemExit(1)
