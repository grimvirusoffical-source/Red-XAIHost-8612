package engineadapter

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"redx/engine/sec"
)

// Master key file format (text, UTF-8, one key per line, '#' starts a comment):
//
//	<key id> <64 hex characters>
//
// The first key is the current one (used to encrypt); the others only decrypt (key rotation).
// The key is read from this file only: never from the environment or the command line (both
// leak through process listings and crash reports). On Unix the file must not be accessible
// by group or others (mode 0600); the loader refuses it otherwise, like ssh does.

const maxKeyFileBytes = 64 << 10

// LoadKeyFile reads a master key file into a keyring. The error never contains key material.
func LoadKeyFile(path string) (*sec.Keyring, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("master key file %s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("master key file %s has mode %04o: it must not be readable by group or others (chmod 600)", path, fi.Mode().Perm())
	}
	if fi.Size() > maxKeyFileBytes {
		return nil, fmt.Errorf("master key file %s is too large", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range b {
			b[i] = 0
		}
	}()
	var keys []*sec.MasterKey
	for n, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			return nil, fmt.Errorf("master key file %s line %d: want \"<id> <64 hex characters>\"", path, n+1)
		}
		raw, err := hex.DecodeString(f[1])
		if err != nil || len(raw) != sec.KeySize {
			return nil, fmt.Errorf("master key file %s line %d: the key must be %d hex characters", path, n+1, 2*sec.KeySize)
		}
		k, err := sec.NewMasterKey(f[0], raw)
		for i := range raw {
			raw[i] = 0
		}
		if err != nil {
			return nil, fmt.Errorf("master key file %s line %d: %w", path, n+1, err)
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, errors.New("master key file " + path + " holds no keys")
	}
	ring, err := sec.NewKeyring(keys...)
	if err != nil {
		return nil, fmt.Errorf("master key file %s: %w", path, err)
	}
	if err := ring.SetCurrent(keys[0].ID()); err != nil {
		return nil, err
	}
	return ring, nil
}

// GenerateKeyFile writes a new random master key file (mode 0600, never overwriting).
func GenerateKeyFile(path, id string) error {
	raw, err := sec.RandomKey()
	if err != nil {
		return err
	}
	if _, err := sec.NewMasterKey(id, raw); err != nil { // validates the id
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintf(f, "# RedX master key (.red-xai-sec). Keep this file secret and backed up separately from the data.\n%s %s\n", id, hex.EncodeToString(raw))
	for i := range raw {
		raw[i] = 0
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		return werr
	}
	return nil
}
