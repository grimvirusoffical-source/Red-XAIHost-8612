# Red-XAI Database 0.4.0 — native local workspace

Database-only engineering beta. Host feature development is frozen and Host is compiled only for shared-framework compatibility. This release replaces the earlier line-splitting parser and single-document landing screen. It is not a complete hosted database/authentication platform.

## Implemented

- Bounded Unicode lexer, token spans, recursive Box/Packer AST, nested Boxes, numbers, JSON-escaped strings, case-insensitive Booleans and NELL, arrays, ordered named tables and inert metatables.
- Box closing visibility metadata, public key references and GAccsess declarations; strict/compatibility validation; distinct file-wide Box ID namespaces and scoped Packer name/ID checks.
- Real local database library: create/import/rename/duplicate/export, automatic .Red-XAI naming, soft Trash/recovery and confirmed permanent deletion.
- Revision-checked atomic saves, autosave, history with twenty retained revisions and restore-as-new-revision. Corrupt saved data is preserved rather than silently reset.
- Native TextKit editor with explicit white dark-mode text, token-based colors, visible line numbers, find/replace, undo/redo, automatic indentation, Go to Line, source-preserving re-indent and tap-to-jump diagnostics.
- Searchable Box/Packer outline by name, local ID or global ID; forms to add/remove Boxes/Packers, edit nested values and visibility, and add named access declarations.
- Dark/light editor settings, font size, wrapping, autosave/line-number preferences, restricted CSS color-template import/export with contrast checks. No CSS scripts, imports or URLs execute.
- A Swift package uses the same core source as iOS. Parser/mutation/theme APIs are portable; the file-locking library in this release targets Apple/Linux, not Windows.
- Apple privacy manifests declare on-device preferences and file metadata access. No network analytics or tracking is added.

## Compatibility and explicit semantics

One first root {Red-XAI}[1] is required. Missing old-style Box closing metadata warns in compatibility mode and fails in strict mode; the app never invents remote access rights. Names are case-sensitive. Box local IDs and Box global IDs have separate file-wide namespaces. Packer local uniqueness is parent Box + name + ID; global uniqueness is name + ID across this file. Packers and Boxes have separate namespaces. Empty/False ID brackets mean no ID.

For unambiguous comments, a bare /- line starts a multiline comment terminated by -\; /- followed by text is a line comment unless terminated on the same line. Tables use table[Key = value] or named bracket entries. meta[...] contains data only. Arrays preserve order, and strings are not reversed. Executable right-to-left expressions and cross-file reference resolution remain unimplemented.

Previous document-browser files are not deleted or overwritten. Import a copy from Files into the new library; exports remain ordinary UTF-8 .Red-XAI text. Back up important beta data before upgrading.

## Safety limits

2 MiB per source file; 64 parser nesting units; 200,000 tokens; 20,000 Box/Packer elements; 100 diagnostics. The local library allows 200 databases and caps storage including backups/Trash at 256 MiB. Syntax coloration stops above 200,000 bytes and automatic analysis above 512,000 bytes; explicit validation remains available. These are deliberate responsive/safety limits, not proof of unlimited scalability.

## Test gates

New core/storage tests include 300 generated nested-value serialization round trips and a 500-input malformed corpus. Native CI must compile both apps, run the full XCTest suite and run Database UI create/structured-edit/save/reopen tests before distribution. Test definitions alone are not proof that a run passed; check the actual CI results and Apple processing state.

## Not delivered as working cloud features

No live Accounts.Red-XAI registration, email delivery, Apple/Google/Discord OAuth, MFA/SMS, remote API-key/token service, owner console, hosted database service, subscriptions, network SDKs or cross-database authorization are connected in this release. Naming a file Accounts does not create authentication. GAccsess and closing flags are metadata, not credentials. Real account secrets must not be placed in example databases. This is not a claim that every item in the original product specification is complete.

## Phone acceptance test

Create Accounts.txt and confirm Accounts.Red-XAI; open the native editor and verify white/readable syntax colors. Open Outline, add a Packer, change a value, Save, close and reopen. Test find/replace and diagnostics navigation. Save another revision, inspect History and restore an older snapshot. Duplicate a database, move the duplicate to Trash and restore it. Import/export a copy using Files; try light/dark mode and a theme template. Report the in-app version/build with any issue.
