# Delivery status — 25 September 2026

This document separates implemented features from the requested final product.

| Area | Delivered here | Not yet delivered |
|---|---|---|
| Database language | Lexer, parser, canonical formatter, typed values, local/global uniqueness validation | LSP, completion, folding, rich search, executable queries, schema migrations |
| IDs/references | File/project uniqueness checks; box grants bound to document revision | Runtime cross-file joins or shared-namespace resolution |
| Storage | Local single-writer SQLite transactions, AES-GCM document/history fields, conflict checks, restore | Replication protocol, consensus, failover, sharding, multi-node writes |
| Database files | Versioned custom container, plain checksummed snapshots, encrypted snapshots | Streaming huge-file import, production format compatibility guarantees |
| Accounts | Local owner bootstrap, password policy, Argon2id, approved local members | Verified email, Google/Discord/Apple OAuth, MFA, password-reset mail, global account service |
| API | Bounded authenticated localhost HTTP service, Python client, documented HTTP examples | Production HTTPS gateway, remote enrollment, broad generated language SDK coverage |
| Native UI | Four working Tk desktop clients; real Linux rendering exercised | Native Windows/Mac execution verified only when the native CI jobs actually pass; editor parity with VS Code/Notepad++ |
| Host | One local static-site node, real start/stop, metrics, guarded dedicated folders | Arbitrary app-server runtimes, containers, remote nodes, VPS scheduling, public traffic gateway |
| Cloudflare | Detect cloudflared; authenticated read-only zone-list implementation | Live provider test, OAuth/account wizard, tunnel creation, DNS changes or paid load balancing |
| Owner tools | Local user/database inventory, local approval endpoint, audit/integrity inspection | Managed-customer data inspector, billing dashboards, remote-support grants, support impersonation |
| Installer | Signed package inspection, staging, integrity checks, atomic version pointer | Trusted production signing root, MSI/PKG bootstrap, file associations, application relaunch |
| Updator | Final-HTTPS package download, verification, install and explicit previous-version pointer | Hosted release feed, TUF metadata, automatic updates, update-self, automatic migration rollback |
| Themes | Blood-red/purple/black, light mode, named CSS token import/export | Persistent per-user theme preference, contrast/accessibility audit across all OSes |
| Billing | Three proposed plans in configuration; no billing writes | Stripe checkout/webhooks, subscriptions, tenant metering and real plan enforcement |
| Agent Forge | Bundled instruction skills, explicit handoff state | These skills do not create tool access, independent runtimes, or completed engineering work |

## Actual local verification

The accompanying test report records **99 passing automated tests** on Python
3.13.5 / Linux. Tests include auth isolation, optimistic conflicts, nested values,
key scope/revocation, snapshots, package integrity, cancellation and input rejection.

A separate bounded stress run performed three rounds. Across those rounds:

- 900 authenticated localhost reads at concurrency eight, with zero request errors.
- 9,000 generated malformed parser candidates, rejected without unexpected exceptions.
- 192 contending writes: three successful commits and 189 expected conflict rejections.
- Three child-process kills after an explicit marker confirmed an open, uncommitted
  SQLite transaction. Each reopened database retained the committed revision and
  returned an `ok` integrity check.

All four native windows were instantiated against a temporary real API and captured
under Linux/Xvfb. These are real UI screenshots, not design mockups. This is a bounded
engineering test, not a long-duration production load certification or proof of
universal correctness. Windows and macOS status must come from their actual runners.

## Key limitations that must not be obscured

Self-hosted software can avoid a managed-database subscription, but infrastructure,
electricity, network capacity, domains, backups, cloud services, and distribution
accounts may still cost money.

The local master encryption key is a protected file under the same OS user profile,
not a Keychain/DPAPI integration. It protects copied database fields without the key,
not a compromised operating-system account or a stolen directory containing both.

Box access grants in this version are intentionally read-only and invalidated by
any database revision change. This avoids granting access to a different object
when structural paths shift, but is not a final high-convenience grants design.

Do not put real customer credentials into this preview, expose its localhost server
to the internet, or sell the plan configuration as an already operational service.
