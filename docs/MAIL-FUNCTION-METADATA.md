# Static mail-function metadata

`scripts/inspect-mail-functions.go` is a standalone, standard-library-only diagnostic. It never runs its input executable, opens account stores, connects to SMTP or reads configuration/environment values. Build it separately; it is not native engine code or a server integration.

The controller must first establish the selected executable and independently obtain its SHA256 through the approved read-only process. Then pass the exact absolute path and expected hash:

```text
inspect-mail-functions --binary /absolute/selected/executable --expected-sha256 <independently-verified-64-hex-sha256>
```

The same bounded regular-file byte buffer is hashed and parsed. Following a selected `/proc/PID/exe` link is permitted because the opened file descriptor must be regular; this does not verify the live PID's identity, configuration or continued existence. A mismatch prints only a refusal code, never the computed hash or path.

Only ELF64 little-endian amd64 EXEC/PIE images with a supported named, uncompressed Go PCLN section and the modern Go 1.20 metadata format are supported. Local Go 1.27.3 still uses that format. Other layouts, extended ELF numbering and missing metadata refuse. There is no ordinary-symbol-table requirement or arbitrary-string fallback.

The reader uses the format offsets documented in the installed Go 1.27.3 standard-library source (`debug/gosym/pclntab.go` and `runtime/symtab.go`) but deliberately avoids `gosym.NewTable`, its all-function allocation/name cache, `PCToLine`, and ELF section decompression. It parses only bounded section/name metadata and function-name offsets. It does not output source paths, lines, code addresses, constants, environment selectors, passwords or disassembly.

Caps are fixed: 128 MiB input, 512 ELF sections, 128 program headers, 64 KiB section-name table, 96-byte section names, 32 MiB PCLN, 16 MiB function-name region, 100,000 function records, 200,000 declared file entries, 512-byte symbol searches, 180-byte output names, 64 distinct matching names and 16 KiB JSON output. Counts and offsets are checked before casts/slices; compressed metadata is rejected. Unknown/long symbol names are suppressed. A selected-name count overflow refuses rather than returning a misleading partial list.

Successful JSON contains only the verified binary hash, supported metadata-format label, bounded counts and strict ASCII `redx/` or `main.` function names containing `mail`, `smtp` or `relay`. Output explicitly says it is static-only and does not establish runtime precedence. Errors and recovered parser panics have fixed, sanitized JSON codes; there is no raw error or stack output.

Function presence cannot identify the active mail route, prove that a setting is supported, choose between relay and legacy SMTP passwords, establish a caller graph, or show which branch ran. Zero emitted names is not evidence that email is unsupported: package naming, inlining, format and name-policy limits can affect results. Do not change credentials or weaken TLS on this result alone.

Tests build an owned synthetic Linux executable with stripped symbols and an inert decoy literal, read it without executing it, and prove the literal/path is absent from output. Other tests cover wrong hashes, invalid arguments, directories, oversized files, malformed/count/offset/compression boundaries and strict name/output limits. All fixture files are temporary. No production binary, service, actual credential, network, SMTP command or target-program execution is part of validation.
