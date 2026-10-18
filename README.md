# RedX Host 0.2.0 (Go implementation)

This branch (`redx-host-0.2.0`) holds the 0.2.0 Go implementation of the RedX host: server, `redx` CLI, storage engines, relations, live rules, search, file vault, media, encryption, sandboxed scripts, and JS/Python SDKs.

It is a separate, orphan history and is unrelated to the older RedX code on `main`. Binaries (`dist/`, `bin/`) are not included; build with `make` (see `Makefile`) and read `docs/HOSTING.md` for install steps. `deploy/release.yml.example` is an example release workflow and is intentionally not under `.github/`.
