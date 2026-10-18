//go:build !redx_stub

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"redx/engine/sec"
	"redx/host"
	"redx/host/engineadapter"
)

const engineName = "redx/engine"

// masterKeyPath returns the key file to load: the configured one (must exist) or, if present,
// <data_dir>/master.key. "" = no master key.
func masterKeyPath(cfg host.Config) (string, error) {
	if cfg.MasterKeyFile != "" {
		if _, err := os.Stat(cfg.MasterKeyFile); err != nil {
			return "", fmt.Errorf("master_key_file: %w", err)
		}
		return cfg.MasterKeyFile, nil
	}
	def := filepath.Join(cfg.DataDir, "master.key")
	if _, err := os.Stat(def); err == nil {
		return def, nil
	}
	return "", nil
}

func opener(cfg host.Config, logf func(string, ...interface{})) (host.Opener, error) {
	var ring *sec.Keyring
	path, err := masterKeyPath(cfg)
	if err != nil {
		return nil, err
	}
	if path != "" {
		if ring, err = engineadapter.LoadKeyFile(path); err != nil {
			return nil, fmt.Errorf("master key: %w", err)
		}
		cur, _ := ring.Current()
		logf("master key file %s loaded: key ids %v (current %s); key bytes are held in memory only", path, ring.IDs(), cur.ID())
	}
	return engineadapter.Opener(engineadapter.Config{
		Durability: cfg.EngineDurability, DefaultTTLSeconds: cfg.EngineDefaultTTL,
		Keyring: ring, CodeAllowedBoxes: cfg.CodeAllowedBoxes, MaxMediaBytes: int64(cfg.MaxUploadMB) << 20,
		Logf: func(f string, a ...interface{}) {
			// definition-file problems and policy notices are always logged; engine chatter only in debug
			logf(f, a...)
		},
	}), nil
}

// runKeygen implements `redxd keygen`: it writes a new random master key file (mode 0600, never
// overwriting). The key is written to the file only; it is not printed.
func runKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	out := fs.String("out", "", "key file to create (default <data-dir>/master.key)")
	id := fs.String("id", "main", "key id stored in every ciphertext (letters, digits, _ - .)")
	dir := fs.String("data-dir", host.DefaultDataDir(), "data directory (for the default --out)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		if err := os.MkdirAll(*dir, 0o700); err != nil {
			return err
		}
		*out = filepath.Join(*dir, "master.key")
	}
	if err := engineadapter.GenerateKeyFile(*out, *id); err != nil {
		return err
	}
	fmt.Printf("master key %q written to %s (mode 0600). Back it up separately from the data: without it encrypted packs cannot be read.\n", *id, *out)
	return nil
}
