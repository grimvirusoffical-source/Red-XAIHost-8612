//go:build redx_stub

package main

import (
	"errors"

	"redx/host"
	"redx/host/stubopen"
)

// engineName: this build uses the in-memory stub store (go build -tags redx_stub); NOT for production.
const engineName = "STUB in-memory store (built with -tags redx_stub)"

func opener(cfg host.Config, logf func(string, ...interface{})) (host.Opener, error) {
	return stubopen.Opener(), nil
}

func runKeygen(args []string) error { return errors.New("keygen needs the real engine build") }
