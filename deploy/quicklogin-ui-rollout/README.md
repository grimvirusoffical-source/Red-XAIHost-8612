# Original Quick Login UI rollout guard

This separate, source-only helper stages the two approved browser assets on the verified original `2cbedc4` backend. It does not change the immutable `../quicklogin-ui-5c54aa9` artifact. It never controls systemd, changes environment settings, writes live account/app-session state, creates credentials, or supplies authentication/readiness bypasses.

The default action is read-only inspection. `--stage` creates private backups and a new root-owned versioned release, without changing the current link. Promotion and rollback require a separate maintenance decision and change only the `current` symlink. They do not restart the application or establish that the public UI works.

## Fixed production assumptions

- Unit `redxai-site-bridge.service`, enabled, `User=Group=redxai`, OS UID/GID981, no EnvironmentFiles, no Node/loader injection settings.
- Exact process argv `/usr/bin/node /usr/local/lib/red-xai-site-bridge/current/server.cjs`; CWD is the verified release; PID owns the sole `127.0.0.1:17774` listener. The effective unit and process selected state/port settings agree.
- Absolute `current` link starts at `releases/quicklogin-2cbedc4`; both release and web directories are root:root0755. All nine runtime files must match the trusted artifact's original pins and root:root0644 metadata. No backend or other browser source is upgraded.
- State remains `/var/lib/red-xai-site-bridge/app-grants.json`, regular/nlink1, UID/GID981/mode0600, existing schema2 JSON. Its exact parent is981:981/0700. All other path ancestors are root-owned and cannot be group/other writable. Missing state is a refusal, never an empty-state bootstrap.
- Artifact root defaults to `/srv/red-xai/stage-tools/quicklogin-ui-5c54aa9`. Its bytes must match manifest SHA256 `45f2d92dbb26a886bbce81269ce2fa533151d7278520a4ce7e015c42811467d4`. Preserve Linux artifact bytes; a CRLF-converted checkout is not the deployment artifact.

Unknown startup shapes, EnvironmentFiles, metadata, source hashes, symlink identities, interpreter identity, existing candidate, stale lock, or changed configuration require review. The helper does not invent a parser for unknown production configuration. A successful inspection proves observed disk/runtime metadata and canonical loopback responses; it does not attest every JavaScript object already cached inside Node.

The selected `systemctl show --all` fields remain fixed. Its custom `EnvironmentFiles` printer can emit no line for an empty array, even with `--all`: the [systemd v255 source](https://github.com/systemd/systemd/blob/v255/src/systemctl/systemctl-show.c#L1168-L1184) prints only within its per-member loop. An omitted display line is therefore insufficient proof. Every property snapshot also queries the exact unit's `org.freedesktop.systemd1.Service.EnvironmentFiles` through `/usr/bin/busctl get-property` and requires the observed typed response bytes `a(sb) 0\n`. Only that positive proof permits canonicalizing an omitted display value to empty. Missing output, wrong types, nonempty arrays, command errors, and contradictory display metadata refuse without printing values.

## Stage, review, and maintenance

Run the tests first on Linux as root in an isolated checkout. They use only synthetic temporary files below `/srv/redx-quicklogin-guard-test-*`; production systemd, network, state and application paths are replaced in fixtures. The Linux ownership/dirfd/fault cases intentionally skip on Windows.

```sh
sudo python3 -m unittest discover -s deploy/quicklogin-ui-rollout -p test_rollout.py -v
sudo python3 deploy/quicklogin-ui-rollout/rollout.py
sudo python3 deploy/quicklogin-ui-rollout/rollout.py --stage
```

Stage creates a unique root0700 directory below `/srv/red-xai/backups`, root0600 original code/config/state copies and receipt, then the fixed candidate `releases/quicklogin-ui-5c54aa9` with root0755 directories and0644 source files. It copies applicable source xattrs and refuses unexpected directory/default attributes. Nine runtime bytes are verified again before the private receipt is written. Existing or incomplete candidates are preserved for review.

The exact original backend constructs only the private state copy, using a fixed public origin and a throwing `fetchImpl`; it never listens. The copy must exist as schema2 before construction and must remain byte-identical afterward. The installed Node executable is opened through a verified descriptor with a clean environment and a five-second subprocess bound. Constructor acceptance proves parser/factory acceptance only: the original loader may skip expired or malformed grants. It is not proof that every remembered client recovered.

Before approving maintenance, independently review the stage result, backup accessibility and relevant application/CLI compatibility. Restarting this original backend loses pending approvals, opaque access credentials, developer CSRF contexts and session-only grants. Remembered refresh grants and registered apps depend on current private state and subsequent real client acceptance. This helper never deletes, rewinds, relocates or replaces that live state.

After the separately approved decision, select exactly the reported private stage directory:

```sh
sudo python3 deploy/quicklogin-ui-rollout/rollout.py --promote-from /srv/red-xai/backups/quicklogin-ui-TIMESTAMP-ID --maintenance-approved
```

The acknowledgment flag records an operator's separate decision; it does not grant approval. Promotion refuses a changed process/configuration/original link, checks canonical loopback responses, validates a fresh private state copy, then atomically swaps the current link through pinned directory descriptors. A private arm receipt containing the created temporary symlink identity is fsynced before each rename; a completed promotion/rollback proof follows readback. No service restart follows. The operator must separately perform any approved service action and verify canonical/public responses, browser Sign in, pending/app flows, remembered refresh, app registration, owner and normal-user behavior. Public proxy transforms may change HTML bytes; compare the correct layers.

## Code rollback and failures

```sh
sudo python3 deploy/quicklogin-ui-rollout/rollout.py --rollback-from /srv/red-xai/backups/quicklogin-ui-TIMESTAMP-ID --maintenance-approved
```

Rollback checks unchanged configuration/interpreter/source, validates a fresh copy of current state, and selects the original code only when the current link exactly matches the recorded promoted symlink. A recorded rollback can be repeated. The original untouched starting link is also recognized. It never restores stored state or changes the service.

Promotion compensates after a rename/fsync/readback/proof failure only if the current link is the symlink this transaction created. Different inodes, unknown targets and intervening edits are preserved with a review lock. Rollback ambiguity also preserves its lock. A stale or changed lock, partial candidate, or partial journal is never automatically removed. Failures report fixed codes without printing state JSON, keys, environment values, unit contents, child output or private errors. Review the private files on the server, establish the exact current state, and resolve deliberately; do not repeatedly rerun or delete evidence to force progress.

Backups in this helper remain on Kamatera. An independently protected off-server recovery copy is a separate requirement and is not established by staging or these tests.
