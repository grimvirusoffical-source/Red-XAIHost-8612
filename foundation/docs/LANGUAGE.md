# Red-XAI language version 0.2

Every document has exactly one `{Red-XAI}[1]{` root. Other boxes are nested under it;
“outside another box” means a sibling box under the root, not text outside the root.
Box names and Packer names are case-sensitive; boolean/NELL keywords are not.

```text
/- One line
{Red-XAI}[1]{
    {PInfo}[232]{
        {PlayerVerified}[2] = [False][2],
        {PlayerAge}[2] = [21][2],
        {PlayerName}[2] = ["Player"][2],
        {PlayerExists}[2] = [NELL][2],
        {PlayerArray}[2] = [1,1.2,True,false,NELL][2],
        {Table}[3] = [{coins=120, nested={enabled=True}, items=["Potion"]}][3],
        {Rules}[4] = [meta({role="player"},{schema="ProfileV1"})][4],
        <[GAccsess,"ProfileReader","tokenref:generated-reference",]>
    <[True,13,false]>}
<[True,1,false,"keyref:database-reference"]>}
```

The final root value is an API-key **reference**, not a secret credential. Public
identifiers and flags never bypass API authentication. Typing a grant marker does
not create a server-side credential. A real grant is generated through the API/UI.

## Comments and separators

`/- text` ends at the newline. `/- text -\` is an inline closed comment.
For multiline comments put `/-` on its own line and close with `-\`:

```text
/-
This is a multiline comment.
-\
```

Packer assignments end with a comma. Box endings do not require a comma. The root
footer must include its complete brackets and closing brace; malformed quoted
strings are errors, not silently repaired by the storage layer.

## Values

Numbers are finite integer/decimal literals retained as strings in the typed JSON
API so a language with limited integer precision does not silently round them.
Quoted strings use JSON-style escaping. Booleans accept True/FALSE/true etc. NELL
is a present, explicit nonexistent-value marker, distinct from a missing object.

Assignment `[1]` contains one scalar. `[1,2]` is the legacy shorthand for an array.
Use `[[]]` for an empty array and `[[1]]` for a single-element array. Nested arrays
use ordinary square brackets. Array API indices are **one-based**. NELL array
members occupy a position; they do not disappear like Lua nil holes.

Tables use `{key=value, [1]="numeric key", nested={other=True}}`. String and number
keys remain typed; duplicate keys are rejected. `meta(table, table)` attaches
non-executable metadata. No script is run while reading a metatable.

## Identifiers

Local/global ID values are integer or quoted-string identifiers. Brackets may be
omitted, empty, or contain `False` to disable that public lookup ID. Every document
object still has a structural editor path even without a public lookup ID.

Ordinary box local IDs are unique within a project. Ordinary global box IDs use a
separate project namespace and are also unique. Root integer ID 1 is reserved per
database and excluded from ordinary cross-file collision checks. Different Packer
names can share IDs: local identity is (containing box, name, local ID), global
identity is (project, name, global ID). An ID alone can therefore return multiple
Packers; filter by name and use the returned structural path for edits.

A box footer is `<[shareAcrossFiles,globalID,shareAcrossNamespaces]>}`. Both sharing
flags are declarations/validation metadata in this preview; an actual cross-file
reference resolver is a later milestone. Global means project-scoped, not public.

## Reading order

Text is lexed normally so strings and arrays retain their written order. Semantically,
a box's opening local ID is identified first, then its footer/global metadata;
its contents are processed top-to-bottom. Packer assignment metadata is interpreted
from right to left (global ID, value, local ID, name). The implementation uses this
as a declarative model, not right-to-left character reversal or executable code.

## Bounds and formatting

Maximum source size is 2 MiB, nesting depth 64, tokens 200,000, and objects 50,000.
Malformed or excessive input is rejected before storage. Canonical formatting drops
comments; the native UI warns before applying it. Preserve a source copy when
comments are important. Format/version compatibility is not yet a stable guarantee.
