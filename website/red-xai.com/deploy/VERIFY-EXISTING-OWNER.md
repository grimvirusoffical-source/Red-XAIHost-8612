# Verify the existing owner without exporting a session

Use the already authenticated normal website session in Edge. The authority is
the existing `redx-panel` `/api/accounts/me` contract, not Kamatera root access,
an email address, display name, pasted user header or the new CMS allowlist.
The immutable account ID must be recovered from a fresh canonical response
after browser-runtime reset; do not guess it from logs or reconstruct a cookie.

1. Restore the documented CUA browser runtime and select the already established
   Edge browser binding. Create a dedicated agent-owned tab and navigate once
   to `https://red-xai.com/api/accounts/me`. This is an ordinary read-only GET;
   the browser supplies its existing cookie internally. Never inspect cookies,
   request headers, local storage, access tokens or raw network captures.
2. Check the actual final origin/path and the DOM-rendered JSON. If redirected
   to sign-in, signed out, presented with a certificate warning, or if no single
   JSON response can be identified in the DOM, stop this verification. Use the
   normal website sign-in if needed; do not bypass browser security or create a
   replacement owner identity.
3. Parse the JSON into a private CUA variable without printing its body, ID,
   email or name. Require the same canonical constraints used by this release:
   valid nonempty account ID/email; safe-integer future `expiresMs`; nonempty
   string `amr` list; no inactive, disabled, banned, deleted, explicitly
   unverified or non-active status/state flags. Additionally require the
   backend-issued `owner` role and an MFA method from
   `otp`, `totp`, `sms`, `call`, `mfa`, `webauthn`. This recovers existing owner
   authority; it does not grant it. This legacy contract does not establish a
   fresh MFA timestamp or prove email delivery; do not claim either.
4. Keep the canonical ID only in the private runtime variable. Report only
   booleans such as `canonicalOwnerConfirmed`, `mfaEvidencePresent` and
   `sessionCurrent`; never print the full object. For this exact private-stage
   installer the ID must also satisfy its dotenv-safe shape
   `[A-Za-z0-9_.:@-]{1,200}`. Do not alter a real ID to fit this requirement.
5. Start the reviewed installer with `--owner-hex` and wait until its hidden
   `Verified canonical owner ID hex (hidden):` prompt is visibly present. Only
   then encode the verified ID in the private runtime, type the encoded value
   into that prompt and send Enter. Never put the actual or encoded ID in the
   shell command, URL, logs, a screenshot or a public source file. The installer
   writes the decoded ID only to its private mode0600 environment file.
6. After successful private service startup, clear the raw response and encoded
   value from the runtime. Actual owner draft/media/preference checks still
   require the browser's normal session and the CMS acceptance procedure.

Example read-only CUA sequence after the existing Edge binding has been
restored (variable names are illustrative; reuse fresh actual handles):

```js
var ownerProofTab = await productionEdge.tabs.new();
await ownerProofTab.goto('https://red-xai.com/api/accounts/me');
var ownerProofUrl = new URL(await ownerProofTab.url());
if (ownerProofUrl.origin !== 'https://red-xai.com' ||
    ownerProofUrl.pathname !== '/api/accounts/me' || ownerProofUrl.search) {
  throw new Error('Canonical owner endpoint not reached');
}
// Count observes the live DOM. If another JSON viewer is installed, inspect
// its visible DOM and target the actual JSON node instead of guessing selectors.
if (await ownerProofTab.playwright.locator('pre').count() !== 1) {
  throw new Error('Inspect the visible JSON response before proceeding');
}
var ownerCanonicalResponse = JSON.parse(
  await ownerProofTab.playwright.locator('pre').innerText());
var ownerCanonicalAccount = ownerCanonicalResponse?.account;
var ownerCanonicalAmr = ownerCanonicalResponse?.amr;
var ownerMfaMethods = new Set(['otp','totp','sms','call','mfa','webauthn']);
var ownerCanonicalVerified = !!ownerCanonicalAccount &&
  typeof ownerCanonicalAccount.id === 'string' &&
  /^[A-Za-z0-9_.:@-]{1,200}$/u.test(ownerCanonicalAccount.id) &&
  typeof ownerCanonicalAccount.email === 'string' &&
  ownerCanonicalAccount.email.length <= 254 &&
  /^[^\s@]+@[^\s@]+\.[^\s@]+$/u.test(ownerCanonicalAccount.email) &&
  Number.isSafeInteger(ownerCanonicalResponse.expiresMs) &&
  ownerCanonicalResponse.expiresMs > Date.now() &&
  Array.isArray(ownerCanonicalAmr) && ownerCanonicalAmr.length > 0 &&
  ownerCanonicalAmr.every(x => typeof x === 'string' && x.length > 0 && x.length <= 40) &&
  ownerCanonicalAmr.some(x => ownerMfaMethods.has(x)) &&
  Array.isArray(ownerCanonicalAccount.roles) && ownerCanonicalAccount.roles.includes('owner') &&
  ownerCanonicalResponse.active !== false && ownerCanonicalAccount.active !== false &&
  ownerCanonicalAccount.disabled !== true && ownerCanonicalAccount.banned !== true &&
  ownerCanonicalAccount.deleted !== true && ownerCanonicalAccount.emailVerified !== false &&
  ownerCanonicalAccount.email_verified !== false &&
  (ownerCanonicalAccount.status === undefined || ['active','activated'].includes(ownerCanonicalAccount.status)) &&
  (ownerCanonicalAccount.state === undefined || ['active','activated'].includes(ownerCanonicalAccount.state));
if (!ownerCanonicalVerified) throw new Error('Canonical owner verification failed');
nodeRepl.write({canonicalOwnerConfirmed: true, mfaEvidencePresent: true, sessionCurrent: true});
// Only after the installer's hidden prompt is visibly confirmed:
var ownerIdHexForPrompt = Buffer.from(ownerCanonicalAccount.id, 'utf8').toString('hex');
// Type ownerIdHexForPrompt using the current documented VNC input handle;
// do not print it. Clear it and the raw response after acceptance.
```

This procedure performs no account edits, token creation, MFA enrollment,
verification overrides, session export, production routing or authentication
authority replacement.
