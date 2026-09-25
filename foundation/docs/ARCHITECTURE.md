# Architecture and remaining milestones

## Implemented process boundaries

The four Tk clients use `redxai.client.Client` to talk to a localhost service.
The service authenticates requests before document storage or host operations.
`language.py` builds a typed model; `store.py` validates revisions and authorization
before committing encrypted source/history in SQLite. `format.py` serializes
consistent snapshots. `distribution.py` verifies and stages signed packages.

This preview uses Python/Tk rather than the earlier proposed C#/Avalonia and Go
stack so the delivered local implementation could be executed and inspected in the
available environment. The parser, API protocol and format are separated from the
UI; a later native UI replacement must pass the same compatibility tests. Do not
silently describe this preview as the earlier C# implementation.

## Product boundaries planned for separate repositories

`Red-XAI-Database`, `Red-XAI-Host`, `Red-XAI-Installer`, `Red-XAI-Updator`.
Shared modules need a versioned common package, not copy-and-diverge forks. The
preview deliberately keeps them in one source tree and separate entry directories
until the repo-creation capability and shared-package release flow are ready.

A fifth repository, `Red-XAI-Agent-Forge`, is for orchestration. Its existing Railway
prototype performs role-prompted model calls only; it has no build sandbox, tool
bridge or executable stress-test runner. Bundled agent instructions are not evidence
that those capabilities exist. Public execution also needs owner authorization and
spend/concurrency limits before enabling billable model calls.

## Next engineering gates

1. Native Windows/macOS builds and functional checks; harden owner bootstrap,
   password recovery, OS secret stores, and error/keyboard/accessibility behavior.
2. Verified shared platform identity, OAuth provider registration, owner MFA,
   verified emails, sessions, device pairing and explicit remote-support grants.
3. Production HTTPS API, authorization audit, signed project manifests, trusted
   compatible workload adapters and persistent backup/restore policy.
4. Remote PC/VPS enrollment, a scheduler, workload isolation and outbound gateways.
   Adding nodes adds eligible capacity, not magically combined RAM or internet speed.
5. Replication with a single fenced writer, ordered logs, recovery and failover.
   Never implement replication as shared-write access to the same SQLite file.
6. Cloudflare account/domain wizard, token scopes, change preview, tunnel/service
   health checks and DNS rollback; do not replace unrelated records automatically.
7. Stripe test-mode checkout/webhooks and quotas with reconciled usage, then a
   costed managed-hosting pilot before accepting paid production workloads.
8. Independent release signing, TUF update metadata, Windows signing, Mac Developer
   ID/notarization, safe bootstrap installation and self-update/recovery tests.

Phones initially manage infrastructure. Standard iOS apps cannot be relied on as
unrestricted always-on background web servers. Android node tasks would require
explicit opt-in, power/network constraints and background-service compliance.
Host app backends; do not claim a Linux VPS can run arbitrary iOS/Mac client binaries.

Managed hosting and self-hosting remain separate. Self-hosters do not donate their
machine to other customers or upload all database content to the platform owner.
Any managed-customer content inspection needs disclosure, least privilege, owner
reauthentication, and a durable audit event; independent self-hosts require consent.
