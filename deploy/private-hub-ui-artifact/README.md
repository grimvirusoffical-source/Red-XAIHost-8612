# Private Hub UI source candidate

This prepares a new, reproducible source package for the reviewed CLI preview UI. It cannot install, extract, apply, start, stop or restart anything. It never reads live configuration, credentials, application data or worktree source files. No network fetch or upload is performed.

The existing scoped Hub rollout still targets immutable release `c443ce7eb01c0a33261c049bc7071dbc90fd46d4`. Its code, manifest and runtime pins are unchanged. **Do not replace files inside that existing release or relax its preflight checks to use this candidate.**

## Exact source selection

The package contains the same 22 reviewed runtime paths selected by the existing rollout manifest, whose committed SHA-256 is `3ebaf2cf1f59304a60d347a90ba99987bd3d64658b5c825a941f17d85aa4665c`. Nineteen files come byte-for-byte from c443. Only these three files come from `ce4408632446225462a4f9bdce7f365b55822fe2`:

| File | Candidate SHA-256 |
| --- | --- |
| `catalog.json` | `c7f19a51bc615397b9319dda5f9e566a6a51a15062b496078b4585eb19fff4a8` |
| `cli-installation.md` | `10b56310f6b53d8ed02575c4f01b19e6fb00531e9e3df042b8c4ecae01b7778d` |
| `site/catalog.js` | `a9f4b71cb8303623cc9853ca9a7f6f1f370b62af26d7a6fddd4673bab3b4e609` |

The raw c443-to-ce comparison changes four of the selected files. The fourth is `package.json`, whose only change adds a maintenance test to its test script. The builder verifies that exact difference but retains the c443 `package.json` and unchanged lockfile. The backend code, registration authority, sessions, CMS writer, custom RedX/BoxQL formats, CLI packages, Quick Login and Secrets code are unchanged by this artifact.

Every payload is read from a fixed committed Git blob with a regular-file mode. Neither CRLF checkout conversion nor uncommitted files can enter the package. The builder checks the exact base manifest, all base file hashes, all three target hashes, the complete known difference set, and unchanged operational package metadata. There is no argument for selecting another revision, manifest, overlay or arbitrary input file.

## Prepare and inspect locally

Run the tests from this source directory:

```text
python -I -B -m unittest discover -s deploy/private-hub-ui-artifact -p "test_*.py"
```

Prepare a fresh output under the repository's ignored `.task-tmp` directory:

```text
python -I -B deploy/private-hub-ui-artifact/build.py --output-name private-hub-cli-preview-1
```

The output name accepts lowercase letters, digits and hyphens only. Existing output paths are refused and never overwritten or removed. Symlink/reparse-point output ancestors are refused. POSIX writes are descriptor-relative and exclusive; Windows uses exclusive new files and repeated ancestry checks. This ordinary-user packaging tool is not a privileged Windows filesystem confinement or deployment primitive. On a failure, any newly created partial candidate is left for inspection rather than recursively deleted or rebound to a new path.

Exactly three output files are created:

- `runtime.tar.gz`: exactly 22 regular files under `runtime/`, sorted paths, fixed gzip/tar metadata and no links or extra members. Gzip uses canonical stored DEFLATE blocks so compression-library versions cannot change the bytes; it deliberately does not optimize size.
- `provenance.json`: exact Git revisions, blob identifiers, sizes, hashes, browser asset revisions and explicit incomplete deployment flags.
- `SHA256SUMS`: hashes of the archive and provenance file. These checksums are integrity records, not a digital signature or independent approval.

The stdout receipt records the output path and hashes without showing source or private configuration. Building the same pinned inputs produces identical bytes. The builder performs no extraction and has no deploy/apply option.

The additional HTTP test verifies the complete archive against those pinned blobs, materializes a fresh ignored fixture, and serves it only on an ephemeral loopback port. It checks four Hub pages, all 26 versioned/unversioned browser responses, seven rejected private/unknown asset paths, the preserved dotfile SPA fallback's exact HTML response, and zero external requests. It starts no CMS, account authority or production service. Its `runtime-smoke-*` output stays under `.task-tmp` for inspection. The dedicated pull-request check runs these tests on Linux, including the POSIX directory-swap test that Windows skips.

`EXPECTED-SHA256SUMS` records the reviewed outer hashes of all three output files, including the detached checksum file. The canonical archive is 327728 bytes with SHA-256 `56496195081d28be14b9be6d2fd79f77272de8280fcf67347f155544b94056c3`. The builder also pins that archive hash; a packaging-format change requires a separate source review and pin update. Repository tests compare all three outputs to the outer pins rather than trusting only a checksum file supplied beside an archive.

## Source package limits and future deployment gates

The selected files cover the runtime's relative module imports and browser asset allowlist. This is still **not a standalone deployment**: Node.js 20.9+, dependencies installed from the retained lockfile (including platform-compatible `sharp`), protected existing configuration and the account4200/native DB17790/Host14202/Quick Login17774 services are separate prerequisites. The retained package's test script references repository tests excluded from this minimal package; run tests in the repository, not from the archive.

The c443 server hashes sorted `index.html` plus 13 `SITE_FILES` as `name + NUL + raw bytes`. Provenance records:

| Browser asset set | Revision |
| --- | --- |
| Original c443 | `29ae45c806fe8be49b9b5a61128ba8749b1a4e3e72b12e0f98f37c96904c2de9` |
| This mixed candidate | `3035a63b01e2519806befd2a835f54f8a91924a4aa890234672d804a4be0a1f4` |

Each request snapshots the files, while only three revisions are retained in the current process. Restart seeds only the current revision: an old tab's versioned asset URL can return404 after restart. A future public upgrade therefore needs a reviewed asset-retention strategy or explicit reload/old-tab acceptance; this package does not make that transition seamless.

A separately reviewed installer must target a **new** protected release directory, verify these source hashes and dependency/runtime conditions, preserve all current tenant/session/owner/MFA boundaries, and verify the actual service configuration. It must take fresh reversible backups of current CMS/native/support state and retain one CMS writer; starting another process on the existing live CMS support directory is not a staging method. A second process must never reuse the live writer lock or original account storage.

Independent private health, owner/session, media, normal-user isolation and old-tab/reload acceptance are required. If the service is publicly routed by that time, the operator must obtain the applicable action-time approval for its restart, verify the existing tunnel policy/routing separately, and retain code/config rollback without restoring older application data. This package does not authorize or perform that deployment. It does not complete the outstanding account/email, billing, resource-isolation or production CMS acceptance work.
