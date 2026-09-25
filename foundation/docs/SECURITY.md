# Security boundaries

This is an engineering preview, not a reviewed production authentication platform.

Passwords are Argon2id hashes (64 MiB memory, three iterations, parallelism two).
The required 8–600 character policy is enforced without silent truncation. Random
32-byte API/session secrets are stored as SHA256 digests, not plaintext. These
random-token hashes are separate from the slow password hashes.

Source/history fields use AES-256-GCM with a fresh random nonce and database/revision
associated data. Master-key access is protected by OS-user directory/file permissions.
**The key is stored beside the database in the user profile.** Keychain/DPAPI,
Windows ACL validation, backup key recovery and rotation remain production gates.

Metadata (names, account emails, audit actions) is not encrypted by field encryption.
Use OS disk encryption as an additional layer; an attacker controlling the OS user
can access both the key and its data. A hash-linked local audit log helps detect
accidental/internal tampering but is not an externally anchored tamper-proof ledger.

The service refuses non-local Host headers, cross-origin browser mutations, missing
native-client headers, request bodies above 4 MiB, and invalid credentials. It uses
24 bounded request workers and a local auth rate limiter. Its localhost deployment
is not a substitute for a production TLS gateway, authorization review, CSRF review,
monitoring, backup retention controls, and threat modeling across real OS users.

First-run owner setup is local possession-based, not verification of the reserved
email address. Registering other local members requires owner approval. There is
no trusted global owner just because someone types the platform owner's email.

Exports contain key references only. A passphrase-encrypted export uses Argon2id
plus AES-GCM. Plain checksummed exports detect accidental damage, not an adversary
who can recompute the checksum. Do not upload a live SQLite/WAL file to a shared
network drive or mistake exported snapshots for multi-node replication.

`.RXAI` packages require independent trusted Ed25519 public keys. Embedded manifest
key IDs are not trust anchors. Declared filenames, size, SHA256, OS, architecture,
signatures, archive inventory and executable flags are verified. Package scripts
are not executed. Payload bytes are rechecked while staging to catch file replacement
between initial inspection and extraction. Trust configuration and installation
directories must remain within a protected OS account.

Never paste provider tokens into source, commits, generated prompts, audit events,
error logs, file exports, or agent handoffs. This project includes no production
provider credentials. Cloudflare's read-only token remains request-scoped in the
preview. OAuth, billing, platform support access, and automatic secret synchronization
are not connected.

Primary references consulted:
- https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html
- https://sqlite.org/wal.html
- https://developers.cloudflare.com/api/resources/zones/methods/list/
- https://theupdateframework.github.io/specification/latest/
