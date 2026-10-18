//go:build !linux && !darwin && !freebsd && !windows

package main

import "errors"

func freeBytes(string) (uint64, error) { return 0, errors.New("unsupported") }
