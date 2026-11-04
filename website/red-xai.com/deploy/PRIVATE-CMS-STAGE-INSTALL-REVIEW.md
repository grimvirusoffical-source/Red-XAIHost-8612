# Private CMS stage installer review

`install-private-cms-stage.py` is prepared for the existing Kamatera server. It
has not run there and has not been pushed. It binds only the already-reviewed
Hub process at `127.0.0.1:14210`; no ingress, tunnel, public index, authentication
authority, Host backend or native DB service changes are included.

Pinned source: `c443ce7eb01c0a33261c049bc7071dbc90fd46d4` at
`/opt/redx-site-c443ce7`. Pinned archive SHA256:
`50314f04f166223602cc0e52d86f1d86a18c5e0b8461fa4b7d3276cb62e07b47`.
The helper compares every archived regular file to the extracted source before
any service mutation. Dependencies must already have been installed using the
reviewed lock. It verifies Node `v20.20.2`, supported `--env-file`, and imports
the gateway and the real pinned sharp package under `redx-site` in a clean
environment before stopping the previous private stage. The probe uses
`node:module` `createRequire()` anchored at this release's `server.mjs` to load
`sharp` through the package's supported entry/exports. It does not depend on a
historical internal `sharp/lib/index.js` path; pinned sharp0.35.5 declares
`./dist/index.cjs` and the former path is absent.

The approved key stays at `/etc/red-xai/site-cms.key`. The helper checks only
its metadata: regular file, no symlink, one hardlink, UID986/GID976, mode0600,
51 bytes. It never opens, prints, copies or modifies that file. The actual
application validates the exact native write/database/prefix/nonowner scope at
startup. Owner ID is separately entered via a hidden prompt/private stdin;
it is written only into mode0600 `/etc/red-xai/site.env`, owned by `redx-site`.
`--owner-hex` permits reliable lowercase hex input through VNC; the encoded ID
must still go through that hidden prompt, never an argument or URL.

The helper sets effective native mode `group` based on an independently fresh
owner-authenticated config observation and exact native revision. It requires
the redacted four-field observation below, root-owned mode0600, no older than
30 minutes. This is operator evidence, not a substitute for the separate
running-binary/source resolver checks already performed by the parent:

```json
{"source":"owner-authenticated-native-config","observedUtc":"FRESH_OBSERVATION_UTC","dbRevision":"8512e1704e589d24535e08082c3ab9f832d7655d","rawEngineDurability":""}
```

The paths below are placeholders for the already downloaded archive and the
selected observation; use their actual protected VPS paths. Inspection is the
default, with no writes or service changes:

```sh
python3 -B /srv/red-xai/stage-tools/install-private-cms-stage.py --source-archive /ACTUAL/hub-c443ce7-source.tgz --durability-observation /ACTUAL/selected-durability.json
```

After exit0 and reviewing the helper, the private-only apply command is:

```sh
python3 -B /srv/red-xai/stage-tools/install-private-cms-stage.py --source-archive /ACTUAL/hub-c443ce7-source.tgz --durability-observation /ACTUAL/selected-durability.json --apply --single-writer-reviewed --owner-hex
```

`--single-writer-reviewed` records an actual operator prerequisite: nobody else
may write the dedicated CMS namespace. The flag cannot independently establish
that fact. Enter only the independently canonical-verified owner's ID, encoded
as hex, at the hidden prompt. The helper never asks for a password or key.

Apply verifies the old `redx-site-private-stage.service` is the expected
DynamicUser transient ee62 release, owns the sole loopback14210 listener, and
reports CMS unconfigured. The new `redx-site-cms-private.service` and its unit
path must not already exist; unknown deployments or existing writer locks are
hard refusals. The new public release tree alone is made root-owned/readable;
npm symlinks must resolve inside that release. No other release or data path has
its permissions changed.

Before applying, it writes a protected backup directory under
`/srv/red-xai/backups/cms-private-stage-UTC-UNIQUE`, containing any prior site
environment, the complete old private unit definition, the pre-CMS support
directory, candidate unit and final receipt. It validates the candidate unit
with `systemd-analyze verify`. Only the old private stage is stopped. The new
unit uses `env -i` plus the fixed private env file, `redx-site`, strict filesystem
protection, private temp, no new privileges and the original lifecycle drain.
Seven existing customer services' active states/PIDs/start times/restart counts
must remain unchanged. Acceptance requires exact loopback listener ownership
and health `native-redx-single-writer`.

Failure attempts to stop only the newly installed private unit, restore the
prior environment and old private stage, and records rollback outcome. Since
the old `--collect` transient unit may disappear when stopped, its captured
definition is restored to `/run/systemd/system/redx-site-private-stage.service`
for runtime-only fallback. Both mutation flags are armed before atomic writes
so post-replacement directory-fsync errors cannot silently escape rollback.
No support snapshot is ever restored over newer writes. Native data and key
are never modified, and no writer lock is automatically removed.

The new unit is installed persistently but is deliberately **not enabled at
boot**, and remains private pending the real owner/session, tenant preferences,
draft/media/audit restart and coordinated native/support restore acceptance.
There is no claim of account recovery, real mail delivery, original phone-call
MFA compatibility or public cutover readiness.

Offline validation command (the source archive is not checked into Git):

```sh
REDX_STAGE_TEST_ARCHIVE=/reviewed/hub-c443ce7-source.tgz REDX_STAGE_TEST_SITE=/reviewed/release/website/red-xai.com python3 -B scripts/test_install_private_cms_stage.py
```

The archive test fails if its reviewed input is absent; it does not silently
skip source verification. The second path must contain the installed, locked
sharp0.35.5 dependency. Offline validation: ten tests passed, including a real
clean-environment Node load of that native package through its declared entry,
and an orchestration fault injection confirming a failing service-user runtime
probe cannot write the environment/unit or stop the existing private service.
The tests also include the actual archive/extracted
source comparison, tamper rejection, invalid observation and duplicate/stale
fields, dotenv injection refusal, listener ownership and lock preservation.
An independent reviewer found and verified the atomic-replacement rollback fix;
its in-memory fault injection passed for environment and unit fsync failures.
Windows cannot validate actual systemd startup, Linux permissions or runtime
fallback. The final unit must still pass the real server's parser and startup
checks; the helper refuses rather than prints raw command output on failure.
