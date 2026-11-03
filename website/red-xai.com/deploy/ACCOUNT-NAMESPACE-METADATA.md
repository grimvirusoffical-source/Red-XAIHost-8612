# Legacy account namespace: bounded metadata plan

Prepared 2026-11-03 from owned local notes/source only. **Not run on Kamatera.** No server, database, account, credential, service, permission or ownership change was performed. The companion Python script prints metadata JSON to stdout; it does not create an output file or start an engine.

## Evidence and scope

`outputs/audit-status.txt` records the failed native backup at `/opt/redx/var/data/dbs/redx_accounts/gendragoon-extension`, historically mode `0700`, owner `redx-gdg`, while the original native daemon runs as `redx`. `outputs/production-recovery-path-review.md` correctly leaves its purpose, real mount and active writers unknown. Directory ownership alone does not identify a writer or establish that it is expendable.

`work/kamatera-recovery/manifests/known-deployments.json` and `outputs/hub-private-stage-preflight.py` ground the six fixed unit names used by the script: `redx-redxd.service`, `redx-panel.service`, `redx-hub.service`, `redx-host-website.service`, `redx-db-website.service`, and `cloudflared-redx.service`. The original authority's expected executable is `/opt/redx/bin/redxd`; its observed build revision was `bcc6c62a2f119bd833ba083c12a3799ca622b980`. The script does not revalidate that revision or assume current source matches it.

The owned `work/accounts/docs/integration/REDXAI_ACCOUNTS_GENDRAGOON.md` is explicitly an October 19 **proposal** against a different account contract. It neither identifies the deployed extension process/unit nor documents its storage, maintenance drain or checkpoint operation. No matching local GenDragoon implementation/deployment definition was found in this bounded review. Do not treat that proposal as a production quiescence runbook.

## Exact command after separately reviewing/placing the script

Use the already authorized provider root console. A root console establishes operating-system inventory authority; it does not grant the application-owner role. Place the exact reviewed companion script at the shown path through the existing authorized workflow, then run only:

```sh
/usr/bin/timeout --signal=TERM --kill-after=2s 50s /usr/bin/python3 -B /root/account-namespace-metadata.py --metadata-only
```

The script itself writes only JSON to stdout. `timeout` bounds this helper and its own child commands, not existing services. If `/usr/bin/timeout` is unavailable, the command fails; do not install anything or silently omit the outer bound. A timeout/failure is an incomplete inventory, never a clean finding. Do not append a pipe, file redirection, service command, backup command or permission change to this inventory step. No SSH, HTTP endpoint, machine credential, application session or new listening port is required. Keep resulting server metadata in the private recovery workspace rather than a public repository or issue.

The one external command form used internally is fixed and read-only:

```sh
/usr/bin/systemctl show --no-pager --property=LoadState,ActiveState,SubState,MainPID,ControlPID,User,Group,DynamicUser,ControlGroup,FragmentPath,Requires,Wants,Requisite,BindsTo,PartOf,Before,After,RequiresMountsFor,Triggers,TriggeredBy -- UNIT.service
```

`UNIT.service` is one of the six fixed names, or at most eight exact `.service` components discovered in the cgroup of a candidate process. It is passed directly as an argument without a shell. No arbitrary unit search, configuration-body read, `systemctl cat/status`, log read, service start/stop/reload, `ExecStart`, `Environment` or `EnvironmentFiles` query occurs. Dependency unit names are recorded but not recursively expanded. This can identify a GenDragoon unit when a related process is visible; it cannot prove that an idle or undiscovered unit does not exist.

## What is read and emitted

- Fixed ancestors from `/` through the exact extension path: `lstat` and followed `stat` type, numeric UID/GID, permission mode, device and inode. No directory children are listed and no database file is opened for content. A symlink or unexpected mount requires review; the script does not repair or remove it.
- Only the two known account names `redx` and `redx-gdg` are resolved to numeric UID/GID; no password, shadow, key, environment or account-record content is read.
- At most 2,048 `/proc` PIDs, 512 FD links per PID and 32,768 total FD links, with a 45-second scan budget checked between reads and the command's outer 50-second timeout. Individual kernel metadata operations may otherwise block beyond a Python deadline. Numeric PID/PPID, UID/GID, start ticks, mount-namespace link and cgroup service names are retained for candidate processes. Executable/cwd links are read for classification but emit only exact fixed anchors, path classes, hashes, deletion booleans and object metadata; expected `/opt/redx/bin/redxd` equality is an explicit boolean. Candidates are known-unit PIDs, either known UID, an executable/cwd under the reviewed prefix, or a path-matched data FD/mapping. At most 96 candidates are emitted.
- FD links under the fixed native-data namespace yield **path class**, FD number, device/inode, permission metadata, FD access mode and `mnt_id`. Target filenames/relative paths, offsets and content are omitted. The access mode comes only from `flags` in `/proc/PID/fdinfo/FD`. Matching file mappings yield path class, permissions, device/inode and whether the mapping is shared/writable; memory addresses, offsets, filenames and memory contents are omitted. Each list is capped at 64 entries per process.
- `/proc/self/mountinfo` and candidate `/proc/PID/mountinfo`: only containing/nested mounts for the fixed targets are emitted, at most 32 per namespace, with mount/parent ID, major:minor device, filesystem and read-only flag. Mountpoints are printed only when exactly equal to a fixed reviewed anchor; all mount roots and other mountpoints are SHA-256 hashes with containment/root booleans and lists of fixed covered targets. Raw mount/source identifiers, optional fields and full mount/superblock options are never emitted. Candidate process-root `stat` identities for the three fixed anchors help expose a different namespace view.
- The script reads bounded procfs metadata text into memory to select these fields. It never reads `/proc/PID/cmdline`, `/proc/PID/environ`, process memory, a config body, private key, native DB record, WAL or checkpoint. All emitted server metadata remains operationally private.

Unavailable/racing reads and all scan limits are explicit counters/flags. A process whose start ticks change during its scan is marked unstable. Generic failures emit no raw exception, stderr, credential or unfiltered procfs text. No `lsof` installation or invocation is needed.

## How to assess the result

Confirm the present extension owner/mode and device/inode; identify any separate/nested mount and compare process-root identities. Review candidate `redx-gdg` processes, executable/cwd object identities and path hashes, cgroup unit and dependencies. Unexpected executable paths remain redacted; establish the exact source/deployment mapping through a separately bounded follow-up rather than dumping a process command line. Match a FD's `mountId` to that process's mount rows; compare device/inode identities as supporting evidence. A writable FD or shared writable mapping is a **writer candidate**, not proof that a write was active or a safe way to stop it. Service `User` describes configuration; the process UID fields describe the observed process.

The global scan is a non-atomic, path-based observation. Bind aliases, detached mounts, chroots, other mount namespaces, inaccessible procfs, already-closed mappings, remote clients, scheduled/short-lived processes and future writers can evade matching. Even a clean scan with no matching FD does **not** establish absence of a writer. Read-only FDs do not establish consistency. Unit dependency order alone does not establish an application's drain/checkpoint contract. Exit zero only means this bounded metadata routine completed; `writerAndQuiescenceStatus` always remains unknown.

Next, verify the exact discovered executable/unit against owned source or deployment documentation and obtain GenDragoon's supported drain/checkpoint/shutdown procedure plus the old native binary's backup/lock behavior. Explicitly account for clients, timers, workers, mounts and any external writers. **Do not yet stop a service, chmod/chown/move/skip the child, run a second engine, force an offline copy, or declare a native backup safe.** A later reviewed maintenance sequence must quiesce every confirmed writer and preserve restart/dependency order. This inventory deliberately does not supply that still-unknown sequence.

## Local validation

The script was syntax-checked and its pure metadata parsing/redaction functions exercised with synthetic fixtures locally using `python -B outputs/account-namespace-metadata.test.py`. Those tests mock subprocess/procfs and check namespace boundaries, filename/option/environment redaction, explicit systemctl option termination, FD write mode, shared writable maps and PID identity comparison. No Linux/server collection was executed. In particular, real procfs visibility, systemd values, aliases and extension writer/quiescence remain unverified.
