//go:build windows || plan9 || js

package engine

// lockDir is a no-op where flock is unavailable; the in-process registry
// still prevents double-opening within one process (known gap: no
// cross-process lock on Windows yet).
func lockDir(dir string) (func(), error) { return func() {}, nil }
