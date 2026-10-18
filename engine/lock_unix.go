//go:build !windows && !plan9 && !js

package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockDir takes an exclusive advisory lock so two processes cannot append to
// the same WAL.
func lockDir(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, "LOCK"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("engine: database %s is locked by another process", dir)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
