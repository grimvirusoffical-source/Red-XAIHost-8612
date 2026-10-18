// Package stubopen exposes the in-memory stub backend to commands outside the
// host tree (host/internal/stubstore is not importable from cmd/...).
package stubopen

import (
	"redx/host"
	"redx/host/internal/stubstore"
)

// Opener returns a host.Opener backed by the stub store.
func Opener() host.Opener { return stubstore.Open }
