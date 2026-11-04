# Source-only account realm consumer prototype

This work is **not deployed and is not approved for runtime activation**.
The Host and Hub runtime entry points refuse a realm-prototype environment;
the native DB environment factory refuses it too. The source consumers run
only through isolated test dependencies. Existing legacy defaults, native
client endpoints, account data and engine/BoxQL/files remain unchanged.

## Agreed authority contract

Agreed directly with the fresh-normal authority prototype agent:

- Legacy issuer: `urn:redxai:legacy-site`.
- Fresh issuer: `urn:redxai:fresh-normal-prototype:v1`.
- Subjects are nonempty UTF-8, at most 200 bytes, without whitespace/control.
- Trusted resolver: `http://127.0.0.1:14330/__prototype/identity`.
- Legacy cookie: `redx_session`; fresh cookie:
  `redx_fresh_normal_prototype_session`.
- The fixed resolver revalidates the selected authority on every request.
  Both cookies mean **409**, never priority, merge, cached identity or fallback.
  Consumers forward only these cookies and JSON Accept; caller realm/identity,
  role, Authorization and unrelated cookie headers are never forwarded.
- Response envelope: `identityRealm:{version:1,issuer,subject,canonicalId}`,
  `account:{id:canonicalId,email,username,name,roles,emailVerified,active}`,
  and top-level `amr`, integer future `expiresMs`, and `active`.
- Fresh responses require roles **exactly `['user']`**, active true at both
  levels, and verified email true. Legacy omitted verification metadata remains
  unknown; consumers must not fabricate verified email for original accounts.

An envelope is not authentication by itself. It is trusted only after the
fixed private resolver verifies the live opaque session against its authority.
No browser field/header, raw subject, email, username or hash is issuer proof.

The authoritative identity is the tuple `(issuer, subject)`. The public DTO
mapping preserves every accepted legacy subject exactly and derives a fresh
display/account ID as:

```text
rxf1_ + lowerhex(SHA256(UTF8(issuer) || 0x00 || UTF8(subject)))
```

The account ID and envelope canonical ID must both match the recomputed value.
This DTO hash is **not** a consumer ownership key. An existing valid legacy ID
may equal that exact string and must retain its original resources. Fresh
consumer storage keys instead encode the full tuple reversibly:

```text
ASCII_SPACE + "rxf1:" + base64url(UTF8(issuer)) + "." + base64url(UTF8(subject))
```

The leading space is intentional and is outside the accepted legacy subject
domain. Native/legacy identity inputs reject that domain; the internal key is
never trimmed, accepted as a native legacy subject or sent as the public ID.
Each consumer persists and verifies the issuer/subject/DTO/key binding. Legacy
storage keys remain the exact existing bare ID, including valid `rxf1_` IDs.
Cross-language vectors are pinned in Node and Go tests. Fresh `same-subject`
maps to `rxf1_f7fb084c3229d8dcc7bcf980bbb75b59afa1ff9224066c7bf4786145ec99015f`;
fresh `normal-ü-😀` maps to
`rxf1_76fe6aa0ebd69ef522f99f5aa1806aabc00119cbf04010504b641e2e9740d2d4`.

## Consumers and preserved boundaries

Host website requests use request-local tenant keys derived from the validated
envelope and a persistent SQLite ownership registry. Fresh same-email/raw-subject users cannot receive legacy tenant IDs
or the special owner tenant. Owner requires the legacy issuer, exact configured
legacy ID, authoritative owner role and actual MFA together. Historical native
Host email attachment explicitly rejects nonlegacy principals; its native
account adapter rejects realm DTOs and remains legacy-only. No account is
automatically merged or attached by matching email/name.

Hub/CMS preferences and audit actors hash the typed ownership key, so all
legacy hashes remain unchanged and fresh users remain separate even when a
legacy bare ID equals the fresh public DTO. Session/UI cache identities use
separate legacy/fresh prefixes and the authoritative tuple. Publishing, draft/media
access and administrative actions remain legacy-owner-only with actual role
and MFA. Existing same-origin mutation checks remain in force.

Native DB work is developed in a separate RedX integration worktree. Engine,
adapter implementation, BoxQL and custom data formats remain unchanged. The
integration layer must use the private ownership key consistently for DB,
key-owner, plan, quota, audit and tenant checks and persist its tuple metadata.
It must validate legacy native-session/OIDC/key owners before accepting a
bare subject, rather than trusting a public fresh DTO or adopting unlabelled
data. API-key scope/DB/prefix/ownership and
normal paid-plan gates remain authoritative. Test plans are created only inside
controlled temporary fixtures, never in production. Password/session login
does not imply a paid subscription.

## Blocking activation conditions

**A one-time inventory cannot prove future legacy IDs will stay disjoint.**
The Host source now has persistent tuple bindings and SQL write guards; see
`HOST-REALM-OWNERSHIP.md`. This supersedes the earlier candidate prefix
reservation: valid legacy `rxf1_` IDs are accepted unchanged. Namespace proof
comes from disjoint input domains plus persistent ownership metadata and
enforcement on every subsequent binding/write, rather than an inventory or
assumption that a public hash never matches an old ID.

Runtime activation remains hard-refused pending independent integration
review, staged production-copy migration/restore and real-account acceptance
across the actual consumers. Do not silently rename, reassign, reset or deny
existing legacy ownership to make a collision disappear. No generic environment
override is provided for these runtime gates.

The intended future selector name `REDX_WEBSITE_SESSION_REALMS=prototype-v1`
is reserved and currently refused by runtime entry points. Do not set it on
Kamatera. Existing `REDX_WEBSITE_SESSION=true`, original owner allowlist,
authority at loopback 4200, cookie and native clients stay in place. Future
configuration must be coordinated across the gateway, Host, Hub and native DB
only after namespace/storage proof, reversible consistent backups/restores,
old owner/phone MFA/Quick Login/CLI/Secrets parity and real normal-user A/B
acceptance. No port, account, key, entitlement or service is provisioned here.

The fresh authority proposes `/api/fresh-accounts/`; original `/api/accounts/*`
and `/v1/accounts/*` remain legacy-only. Fresh manual Bridge/default native
client login is **not ready**. This work does not establish original account
migration, actual mail delivery, real device/VPS/subscription acceptance,
account-deletion/key-revocation lifecycle or all-client compatibility.

## Validation scope

Tests cover same issuer/raw subject/email collisions, forged owner/role/header
claims, expiry/revocation and both-cookie denial, exact-origin CSRF, legacy
defaults, fresh A/B contexts and CMS preferences. A real temporary RedX engine
adapter in the native-host test verifies custom Box literal storage and scoped
key access, legacy/fresh DB/key isolation, and the unchanged plan-required
response before a fresh fixture receives an explicit test plan. No production
credentials, customer resources or network authorities are accessed.

Initial prototype validation on 2026-11-03 (before persistent extension):

- Host consumer/native-attachment/mounted session tests: **16 passed**, including
  a real temporary SQL legacy same-email row left unbound and no minted session.
- Hub/CMS full Node suite: **67 passed**, including fresh/legacy preference
  separation, owner denial and explicit public-runtime activation refusal.
- Both Host TypeScript checks passed with pinned Bun 1.3.14.
- Native `go test ./host ./host/engineadapter -count=1` passed (host 91.035s,
  adapter 16.354s); focused website/realm tests and `go vet ./host` passed too.
- Native changed files are only the website session adapter, a new realm
  decoder, tests and this document. `engine/`, `box/`, `cmd/redxd/` and
  `host/engineadapter/` match base `8512e1704e589d24535e08082c3ab9f832d7655d`.

Host/Hub source base is `c443ce7eb01c0a33261c049bc7071dbc90fd46d4`.
No push, deployment, production credential, account creation or service change
was performed by this prototype task. These are local controlled-fixture
results; no hosted Linux CI/race run or real-account acceptance is claimed.
