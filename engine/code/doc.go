// Package code implements the `.red-xai-code` format: sandboxed scripts for
// RedX, run by a small interpreter written for this purpose.
//
// A .red-xai-code file is a Box document with one Script entry per script
// (name, parameters, the host functions it declares it needs, optional
// limits, source). The language has numbers, strings, booleans, nil,
// arrays, variables, arithmetic, conditions, loops, and user functions, and
// a few pure built-ins. That is all it has: there are no imports, no
// reflection, no first-class functions, no globals, and no way to name a
// file, a socket, a process or the clock. The only way out of the sandbox is
// a host function, and a host function runs only if
//
//  1. the script declared it in Uses (checked when the script is compiled),
//  2. the embedding program registered it in the Host, and
//  3. engine/sec's Authorize allows the running principal to perform the
//     function's action on the resource it touches (default deny).
//
// Every run is bounded by a step budget, a wall-clock limit, a cumulative
// allocation budget, maximum string and array sizes, a call depth and a
// host-call count. A script's own limits can only lower the host's.
//
// The interpreter runs on the caller's goroutine and starts none. It cannot
// interrupt a host function that ignores its context; host functions are
// trusted code and must honour ctx.
//
// Nothing here is wired to the real storage engines or the host yet.
package code
