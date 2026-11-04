# Offline private Hub CLI upgrade proposal

This separate manifest and planner describe one future upgrade to the exact mixed CLI UI candidate. They preserve the existing artifact outputs/checksums, PR11 and c443 rollout pins. No archive is duplicated here.

`plan.py` reads its pinned manifest, three named local artifact files and optional deliberately prepared `operator-evidence.json`. It never opens live paths named inside evidence, collects observations, runs commands, extracts files, writes a drop-in or applies changes. Valid inputs still retain `readyForDeployment:false`, `approvalVerified:false` and `productionAcceptanceVerified:false`.

## Local checks

```text
python -I -B -m unittest discover -s deploy/private-hub-cli-upgrade -p "test_*.py" -v
python -I -B deploy/private-hub-cli-upgrade/plan.py --artifact-directory PATH_TO_EXISTING_PINNED_ARTIFACT
```

Without observations, output has `operatorEvidenceStatus:"UNVERIFIED"` and `unitProposal:null`. A future authorized operator may add `--evidence PRIVATE_PATH/operator-evidence.json`. Unknown/apply/install/service flags and abbreviated options are refused. Keep real observations and proposal output in protected private storage, outside this public repository. The incomplete example is intentionally refused.

## Evidence contract

JSON must have exactly `schema:1`, `observedAt` and the eight sections named by `requiredEvidence` in the manifest. Time must be UTC, at most 300 seconds old and 30 seconds ahead. Recheck at action time: the tool cannot authenticate observations or verify the live host.

| Section | Required declarations |
| --- | --- |
| `unit` | Exact active simple baseline, original WorkingDirectory, two-argument Node ExecStart, user/group/PID, effective-property digest, protected fragment and ordered drop-in identities, new drop-in absent |
| `currentRuntime` | Original directory identity, complete 22-file c443 hash map and original browser revision |
| `process` | Matching PID/start ticks and four UID/GID fields |
| `nodeExecutable` | Protected regular executable identity and Node 20.9+ |
| `configuration` | Environment-file identities, website_cms, exact site-cms.red-xai.db prefix, retained durability/single-writer declarations, key/support/media/audit/lock identities, matching lock PID, values omitted |
| `listener` | Same PID on 127.0.0.1:14210 |
| `routing` | Actual private-loopback-only or public-routed inventory and selected config identity/digests; unknown refused |
| `newRelease` | Protected parent identity and separate proposed directory absent |

Identity fields are `path`, `kind`, `device`, `inode`, `uid`, `gid`, `mode`, `size`, `mtimeNs`, `symlink:false`. Unit/drop-in, Node, tunnel, environment-file and CMS-key entries also require provided `sha256` metadata. The planner never calculates a live private-file hash or reads key/environment contents. Metadata collection needs a separately reviewed protected workflow. No secrets or raw environments belong in evidence/source control.

Paths exclude whitespace, shell syntax, systemd expansions, traversal and unresolved placeholders. Unsupported command shapes, changed units/users/baselines, existing targets, stale evidence or uncertain drop-in order require separate review. Never move/reseed legitimate data to fit the narrow accepted path prefixes.

The complete sanitized observation document is bound by SHA-256 of sorted compact ASCII JSON plus newline. This retains all reviewed identities/effective-property inputs without repeating them; it proves neither authenticity nor currentness.

## Preview and future gates

Fresh declarations produce JSON-only preview text for exactly two effective properties: the new immutable WorkingDirectory and the same Node executable running its server.mjs. The new drop-in must sort after existing effective drop-ins and be absent. Preserve all other fragments, environment, key, CMS/support/audit, authority, lifecycle and effective properties.

The [runbook](RUNBOOK.md) requires actual protected path/unit rechecks, separate directory/dependency installation with scripts disabled, isolated static HTTP smoke, fresh paired backup and isolated verify-only restore, single-writer transition, owner/session/media/normal-user acceptance and code/configuration-only rollback. No old-data restore, reseed, lock deletion or second writer is allowed.

The unit name containing private and loopback listening are not privacy proof. Actual public routing requires fresh applicable action-time restart approval and separately reviewed runtime/routing pins. Do not relax c443 rollout checks. Restart drops old in-memory asset revisions, so a separately reviewed retention or old-tab/reload acceptance plan is mandatory.
