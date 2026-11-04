# Host project removal lifecycle

This source change keeps the existing project/worker API paths, identifiers and
job shape. It requires no table/column migration and does not activate the fresh
account prototype. The additional project status is `removing`, shown as
**removal pending**. The served worker version becomes 1.3.1.

A removal request atomically disables automatic deployment, cancels unfinished
deployment work and queues one removal per owned node that may hold a replica.
Offline/disabled nodes keep their queued work and the project stays visible until
all required nodes acknowledge success. A never-deployed project with no node
references can be removed immediately. Unknown/foreign node relationships and
unsafe legacy workload slugs refuse removal before changing state.

Repeated removal requests retain active attempts and create a new job only for a
failed/cancelled attempt. Restart/offline reconnect recovers in-flight removals;
expired 90-second delivery leases recover lost responses when the worker is not
advertising `busy`. Older workers omit that optional flag and may receive an
idempotent retry. Terminal callbacks do not change finished attempts. A repeated
success acknowledgement can finish an interrupted final purge.

Removal payloads carry the owned project ID/slug and neutral fields in the
existing wire shape. They contain no source URL, commands, Dockerfile,
environment values, signed bundle URL or connector token. Regular deployment,
restart, edits, AI planning and new domain attachments are refused while removal
is pending. Health, webhook, domain and late worker updates cannot restore the
project to a runnable state.

After every required node succeeds, one SQLite write batch records the removal
reservation, deletes the project and its replica rows, and detaches its local
domain associations. Deployment history and audit records remain. No provider
zone, tunnel, subscription, cloud account or unrelated storage is deleted.

## Reservations and delayed legacy workers

An old worker can hold a removal payload after the server receives another
attempt's acknowledgement. Callback rejection alone cannot stop that payload
from executing. Completed project IDs/slugs therefore remain reserved in an
`activity` row with scope `project_removal`, using only a version, project ID and
slug. A new project with the same name receives the existing collision suffix
(`name-2`, then `name-3`, etc.); it cannot reuse the removed workload identifier.
The automatic InfectedNation bootstrap also honors these reservations.

These rows are operational safety records as well as audit records. Preserve
them with the projects, deployments and replica ledger during backup, restore,
account maintenance, archival and any future retention feature. No reviewed
Host API, account deletion, SQL migration or maintenance path currently deletes
activity records; the auth schema has no cascading activity relationship. The
central site's automatic account-deletion endpoint is not configured. Future
cleanup must retain these minimal opaque resource reservations even when it
removes account profile fields. The reservation contains no email, display name,
project display name or credential snapshot. Administrative database edits are
outside this API guarantee.

## Deployment and accounting limits

Before deployment, reconcile any in-flight legacy removal whose project row was
already deleted by the old implementation. Its missing slug/payload cannot be
safely reconstructed from a node ID, display name, email or guessed identifier.
Do not manufacture a tombstone or automatically delete an unknown workload.
Preserve a consistent current backup of the entire Host database. Restoring an
older project table without its matching job/reservation ledger is unsafe.

The updated worker checks tracked native PID termination and Docker command
results. Docker daemon/permission failures are errors, not successful absence.
Only a successful exact container/image listing establishes absence. Upgrade
workers to 1.3.1 to gain these checks and busy-heartbeat behavior; older installed
workers retain their previous command/error behavior. Real Windows/macOS/Linux
node acceptance, native child-process/PID identity behavior, Docker failures and
network interruption still need validation on the actual supported devices.
Synthetic tests execute no operating-system stop, Docker or provider commands.

Removal confirms the known workload identifier according to the authenticated
worker's report. It is not independent attestation of a hostile worker, secure
erasure, provider billing cancellation or full disk/resource accounting. Cached
checkouts, uploaded bundles, deployment logs, historical usage and shared domain
connectors remain outside this focused teardown. They require separate explicit
ownership/retention policies and verified cleanup before claiming full resource
or billing recovery.
