# Bounded GDG Accounts source metadata inspector

Prepared only; no server or browser action occurred. The helper uses Go's
standard `go/parser` and `go/ast` packages. It does not import, type-check,
compile or run the account application; load application configuration; read
account/database/device state; inspect credentials; or follow imported files.
Building this independent helper is separate from building the application.

The production executable accepts **no arguments** and reads only top-level
non-test `.go` files under this fixed directory:

`/root/gdg-signing-api-20261028/RedX/cmd/redx-gdg-accounts`

It refuses source-directory redirects and symlinked/nonregular source files.
Reads check inode identity, size and modification time before/after. This is
not an atomic snapshot of the entire directory and does not lock source editors.
Nested directories/imported package leads are not opened. No `host/acct` path,
server environment, `.env`, TOML, account data, binary or process state is read.

Output is one JSON object, with only:

- Bounded source filenames, byte counts and SHA256 hashes.
- Parsed package name, validated lowercase dependency paths and import aliases.
  Local `redx/...` imports become repository-relative **leads**, without reads.
- Declaration/function/type/field/parameter names and type-reference names.
  Oversized/unreviewed identifiers are omitted; type-reference lists cap at128.
- Static callee symbols, enclosing named function, source line/column, nested
  function-literal depth and lexical `defer`/`go` contexts. Direct scheduling is
  separate from lexical containment: a deferred call's argument is not labelled
  as itself directly deferred. This is source position order, not runtime order,
  a control-flow proof or resolved dynamic dispatch.
- A focus hint for Close/shutdown/signal/listen/serve/storage/backup-style names.
  All accepted call symbols remain present, so a hint is not a completeness gate.
- Only two explicit literal exceptions: the first literal argument to an
  imported `os` alias's `Getenv`, after uppercase environment-**name** validation;
  and the first literal argument to `Handle`/`HandleFunc`, matching the strict
  reviewed `/api/accounts/` or `/v1/accounts/` endpoint-word pattern in the code.
  No environment values are obtained. Unknown routes/imports/invalid names are
  counted without printing their values. Aliases are static/untyped and may be
  shadowed; this is not a proof of actual runtime binding.

The route expression permits only the reviewed account-contract words in the
helper: signup/register, login/2fa, me/logout/activate, verification/resend,
resend-verification, MFA enroll/verify/disable, sessions/{id} and password
reset/forgot variants, with an optional fixed HTTP method and trailing slash.
It does not emit arbitrary API path suffixes, query/fragment strings, hosts,
credentials or encoded paths. Unknown GDG-specific routes stay withheld until
their pattern is separately reviewed. No string concatenation or dynamic
argument expression is evaluated.

All other literals, declaration values, bodies, raw strings, comments and struct
tag contents are withheld. No AST pretty-printer or raw parser/OS error is used.
Failures contain only fixed reason codes, never exception/source text. Import,
declaration and symbol names are the intentionally allowed metadata boundary;
the helper cannot establish that an identifier chosen by a source author is
semantically nonsensitive.

Hard bounds:64 source files,1MiB each,8MiB total;256 directory entries accepted
(at most257 enumerated to detect refusal);150,000 AST nodes;4,000 declarations;8,000 calls;
128 type-reference names per field;1MiB JSON plus newline;15-second inspection
deadline. Bounds are refusals rather than public partial source output. The
deadline also terminates a parser worker if it fails to return in time. Go
compilation time and a blocked external stdout consumer are separate; write
the report to an exclusive regular private file.

The deployed binary observation is **reference-only** metadata supplied to this
review: module `redx/cmd/redx-gdg-accounts`, base revision
`b3cfa4ff2bac9789b1132d80cc07e34f26ccf9e3`, modified=true. The helper does not
inspect that binary and explicitly reports `sourceBinaryParityProven=false`
and `buildConstraintsEvaluated=false`. Finding a Close or signal handler in
this retained source does not establish that the deployed modified binary uses
it, safely drains requests, or writes a particular live namespace.

Local validation:

```sh
go test inspect-gdg-accounts-source.go inspect-gdg-accounts-source_test.go
```

Six tests passed on Windows, covering planted token/password/comment/raw-string/
tag/invalid-import/unreviewed-route secrecy, allowed metadata, call ordering,
defer/go argument semantics, parse-error privacy, cancellation/file bounds and
non-recursive imported-package handling. One separate symlink test was skipped
because this Windows account could not create a symlink; the Linux refusal path
still requires the equivalent real-platform check. The helper source is stdlib
only; no third-party modules or application packages are built by those tests.

Proposed server use after parent review (not performed): verify the exact
helper/test Git bytes and compile this helper outside the application tree with
an independently verified existing Go toolchain, clean environment and private
build/cache directories. For example, with reviewed absolute path variables:

```sh
env -i PATH=/usr/bin:/bin GOTOOLCHAIN=local GOENV=off GOWORK=off GO111MODULE=off GOPROXY=off GOSUMDB=off CGO_ENABLED=0 GOCACHE="$PRIVATE_CACHE" TMPDIR="$PRIVATE_TMP" "$VERIFIED_GO" build -trimpath -buildvcs=false -o "$INSPECTOR_BINARY" "$INSPECTOR_SOURCE"
```

Run the independent binary without arguments, redirecting its report exclusively
to a new root-private mode0600 regular file under a verified protected parent.
Do not append to/overwrite an earlier report or print an entire source dump if
inspection refuses. Review compact counts, selected call/field names and hashes
from that JSON. No service stop/restart, source edit, backup, permission change,
application execution or publication follows automatically from this report.
