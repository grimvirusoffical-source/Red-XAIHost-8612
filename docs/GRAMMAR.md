# RedX Box grammar (v1, frozen)

Implemented by `redx/box` (`lexer.go`, `parser.go`, `writer.go`, `validate.go`).
The parser is covered by table tests and a fuzzer (`FuzzParse`: parse → write → parse is a fixed point).

## 1. Lexical structure

```
whitespace    = " " | "\t" | "\r" | "\n" ;
comment       = "<<" , { any char } , ">>" ;            (* not nested; may not be unterminated *)
ident         = ( letter | "_" ) , { letter | digit | "_" | "-" } ;
number        = [ "-" ] , digits , [ "." , digits ] , [ ( "e" | "E" ) , [ "+" | "-" ] , digits ] ;
string        = '"' , { char | escape } , '"' ;
escape        = "\\" , ( '"' | "\\" | "/" | "n" | "r" | "t" | "b" | "f" | "u" hex hex hex hex ) ;
punct         = "{" "}" "[" "]" "(" ")" "=" "," ";" ":" "." "~" "*" "@" "@>" "@!>" ;
```

* Keywords (`TRUE`, `FALSE`, `NELL`, `ID`) are case-insensitive. Every other identifier is case-sensitive.
* Invalid UTF-8 inside a string is replaced with U+FFFD at lex time; raw control characters are written as `\u00xx`.
* Errors carry `line, col` (1-based) and the offending token.

## 2. Syntax (EBNF, ISO 14977 flavoured)

```
document      = { top_item , [ ";" | "," ] } ;

top_item      = "{" , key_attrs , "}" , entry_tail                          (* entry form *)
              | "{" , key_attrs , "=" , "[" , box_body , [ ttl ] , [ flag ] ,
                { "," , ident , "=" , meta_value } , "}" ;                  (* box declaration *)

entry         = "{" , key_attrs , "}" , entry_tail ;
entry_tail    = "=" , value , [ ttl ] ;

key_attrs     = ident , [ "[" , [ attr , { "," , attr } ] , "]" ] ;
attr          = number                                    (* shorthand for ID=n *)
              | ident , [ "=" , attr_value ] ;            (* bare ident = TRUE *)
attr_value    = string | number | keyword | raw_word ;
raw_word      = { ident | number | "." | "*" | "[" raw_word "]" } ;   (* e.g. PlayerStatsBox.Health, ID=* *)

box_body      = { entry , [ "," ] } , "]" ;               (* the "[" was consumed by the caller *)

value         = string
              | number
              | "TRUE" | "FALSE" | "NELL"
              | ";" , "[" , [ value , { "," , value } ] , "]" , ":"        (* array: ;[1, 2, 3]: *)
              | "[" , box_body                                              (* nested box *)
              | link ;

link          = ref , [ ( "@>" | "@!>" ) , ref ] ;
ref           = string , ":" , seg , { "." , seg } ;       (* "file.red-xai.db":Box[ID=1].Key *)
seg           = ident , [ "[" , [ sel , { "," , sel } ] , "]" ] ;
sel           = number | "ID" , "=" , ( number | "*" ) ;

ttl           = "(" , [ "~" ] , int , [ "@" , int ] , ")" , [ "*" ] ;
flag          = "TRUE" | "FALSE" ;                        (* trailing box flag: ]True *)
meta_value    = ident | value ;                           (* MODE = db ; ID = 1 ... *)
int           = digits ;                                  (* 0 <= N <= 2^31-1 *)
```

Disambiguation rules the parser applies:

* A `string` followed by `:` and an `ident` starts a `link`; any other `string` is a plain string.
* `ttl` markers: `(N)` fixed seconds; `(~N)` sliding; `(N)*` checkpoint cadence (qs-ls). `(~N)*` is a syntax error.
  `(N@T)` carries the **absolute expiry in unix milliseconds** `T`; the writer emits it when serializing a
  file that is read back by the engine (checkpoints, snapshots), so a restart never restarts a clock.
  Hand-written files normally omit `@T`; the engine counts from load time in that case.
* Duplicate keys (same name **and** same `ID`) in one box are a parse error. Entry order is preserved.
* `ID` attributes may be written `[1]`, `[ID=1]` or `[id=1]`; they normalize to `ID`.
* Nesting is limited to 200 levels.

## 3. Mode rules (`Validate(root, mode)`)

| mode | file | TTL markers |
|------|------|-------------|
| `db` | `.red-xai.db`, `.red-xai-db` | none allowed |
| `qs` | `.red-xai-db-qs` | every leaf must be covered by an ancestor/own `(N)` or `(~N)`; structural parent boxes need none; `(N)*` is rejected |
| `qs-ls` | `.red-xai-db-qs-ls` | `(N)`, `(~N)` and `(N)*` all allowed; unmarked entries are journaled immediately |

## 4. Paths (query language used by `Get/Set/Delete/Query`)

```
path     = [ file ":" ] seg { "." seg } ;
seg      = ident [ "[" ( "ID=" number | "ID=*" | number ) "]" ] ;
```

* `Box[ID=1].Inventory[id=10]` selects by identity; `Box[ID=*]` matches every ID (Query only).
* A path with a `file:` prefix goes to that file; the prefix may be abbreviated to the part before the type
  extension (`accounts:` for `accounts.red-xai.db`) if unique. Without a prefix the file is chosen by the first segment
  (it must exist in exactly one open file) – ambiguity is `ErrAmbiguous`.
* An unquoted ident may contain `-` and `_`.

## 5. JSON mapping

| Box | JSON |
|-----|------|
| string / number / TRUE / FALSE | string / number / bool |
| NELL | `null` |
| `;[...]:` | array |
| nested box | object keyed `Key` or `Key[ID=n]` (box metadata/flag/TTL are not part of the JSON form) |
| link | `{"$link": "<text>"}` |

`Value.MarshalJSON` / `UnmarshalJSON` round-trip every value kind (NaN/±Inf numbers marshal as `null`).

## 6. Binary form

The WAL uses a compact binary encoding of the same value tree (`box/binary.go`); it is an implementation detail
of the journal and not a public format. Checkpoints are always this text grammar plus a one-line footer comment
(see ENGINES.md §4).
