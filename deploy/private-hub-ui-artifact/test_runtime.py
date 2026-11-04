from __future__ import annotations

import io
import json
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import tarfile
import unittest
import uuid

import build as candidate


class PackagedRuntimeTests(unittest.TestCase):
    @unittest.skipUnless(shutil.which('node'), 'Node.js is required for the HTTP fixture')
    def test_exact_packaged_runtime_serves_only_reviewed_browser_assets(self) -> None:
        repository = candidate.repository_root(Path(__file__).resolve().parents[2])
        sources = candidate.load_sources(repository)
        payloads = candidate.bundle(sources)
        candidate.verify_bundle(payloads, sources)
        # Fresh, ignored fixture output is retained for inspection. No existing
        # files are overwritten and no recursive cleanup is performed.
        destination = candidate.write_candidate(repository, 'runtime-smoke-' + uuid.uuid4().hex, payloads)
        with tarfile.open(fileobj=io.BytesIO(payloads['runtime.tar.gz']), mode='r:gz') as archive:
            members = archive.getmembers()
            self.assertEqual([member.name for member in members],
                             ['runtime/' + name for name in sorted(candidate.RUNTIME_FILES)])
            for member in members:
                self.assertTrue(member.isfile() and not member.linkname)
                target = destination.joinpath(*PurePosixPath(member.name).parts)
                self.assertTrue(target.resolve().is_relative_to(destination.resolve()))
                target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                with target.open('xb') as stream:
                    stream.write(archive.extractfile(member).read())
        result = subprocess.run(
            [shutil.which('node'), str(Path(__file__).with_name('runtime-smoke.mjs')),
             str(destination / 'runtime'), str(destination / 'provenance.json')],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30, check=False,
        )
        self.assertEqual(result.returncode, 0,
                         'Isolated packaged-runtime HTTP check failed; no credential output is included')
        report = json.loads(result.stdout)
        self.assertEqual(report['browserRevision'], candidate.CANDIDATE_ASSET_REVISION)
        self.assertEqual(report['renderedPages'], 4)
        self.assertEqual(report['assetRequests'], 26)
        self.assertEqual(report['hiddenRequests'], 7)
        self.assertEqual(report['externalRequests'], 0)
        for flag in ['cmsStarted', 'accountAuthorityStarted', 'productionAccess', 'dotfileSourceExposed']:
            self.assertIs(report[flag], False)
        self.assertIs(report['fixtureOnly'], True)


if __name__ == '__main__':
    unittest.main()
