package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"redx/host"
	"redx/host/client"
)

type check struct{ name, status, detail string }

func cmdDoctor(args []string) {
	var g globals
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	g.bind(fs)
	cfgFile := fs.String("config", os.Getenv("REDX_CONFIG"), "redxd config file to validate")
	parse(fs, args)
	var res []check
	add := func(name, status, detail string) { res = append(res, check{name, status, detail}) }

	add("platform", "ok", fmt.Sprintf("%s/%s go %s redx %s", runtime.GOOS, runtime.GOARCH, runtime.Version(), host.Version))

	// config
	cfg := host.DefaultConfig()
	cfg.DataDir = g.dataDir
	if *cfgFile != "" {
		if err := cfg.ApplyFile(*cfgFile, true); err != nil {
			add("config", "FAIL", err.Error())
		} else {
			add("config", "ok", *cfgFile)
		}
	} else if err := cfg.ApplyFile(filepath.Join(cfg.DataDir, "redx.toml"), false); err != nil {
		add("config", "FAIL", err.Error())
	} else {
		add("config", "ok", "defaults (no redx.toml in data dir)")
	}
	_ = cfg.ApplyEnv(os.Getenv)
	if err := cfg.Validate(); err != nil {
		add("config valid", "FAIL", err.Error())
	}

	// data dir
	if st, err := os.Stat(cfg.DataDir); err != nil {
		add("data dir", "warn", fmt.Sprintf("%s does not exist yet (redxd creates it on first start)", cfg.DataDir))
	} else if !st.IsDir() {
		add("data dir", "FAIL", cfg.DataDir+" is not a directory")
	} else {
		tf := filepath.Join(cfg.DataDir, ".doctor-write-test")
		if err := os.WriteFile(tf, []byte("x"), 0o600); err != nil {
			add("data dir", "FAIL", "not writable: "+err.Error())
		} else {
			os.Remove(tf)
			add("data dir", "ok", cfg.DataDir+" (writable)")
		}
		if free, err := freeBytes(cfg.DataDir); err == nil {
			s := "ok"
			if free < 512<<20 {
				s = "warn"
			}
			add("free disk", s, fmt.Sprintf("%.1f GiB free", float64(free)/(1<<30)))
		}
		ksPath := filepath.Join(cfg.DataDir, "keys.red-xai-keys")
		if st, err := os.Stat(ksPath); err == nil {
			if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
				add("keystore perms", "FAIL", fmt.Sprintf("%s is %v; run chmod 600", ksPath, st.Mode().Perm()))
			}
			ks, err := host.OpenKeystore(ksPath, host.KeystoreOptions{Passphrase: os.Getenv("REDX_KEYSTORE_PASSPHRASE")})
			if err != nil {
				add("keystore", "FAIL", err.Error())
			} else {
				active, expired := 0, 0
				for _, r := range ks.List() {
					if r.Active(time.Now()) == nil {
						active++
					} else {
						expired++
					}
				}
				add("keystore", "ok", fmt.Sprintf("%d active key(s), %d revoked/expired", active, expired))
			}
		} else {
			add("keystore", "warn", "no keystore yet (created with the first admin key on first start)")
		}
		if cert := filepath.Join(cfg.DataDir, "tls", "cert.pem"); fileExists(cert) {
			if b, _ := os.ReadFile(cert); b != nil {
				if blk, _ := pem.Decode(b); blk != nil {
					if c, err := x509.ParseCertificate(blk.Bytes); err == nil {
						d := time.Until(c.NotAfter)
						s := "ok"
						if d < 30*24*time.Hour {
							s = "warn"
						}
						add("tls cert", s, fmt.Sprintf("expires %s (%d days)", c.NotAfter.Format("2006-01-02"), int(d.Hours()/24)))
					}
				}
			}
		}
	}

	// port / daemon
	cl := client.New(g.addr, "", g.pin, g.insecure)
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, _, body, err := cl.Raw(c, "GET", "/healthz", nil, nil)
	switch {
	case err == nil && st == http.StatusOK:
		add("daemon", "ok", g.addr+" answered /healthz: "+trim(body))
		if g.key != "" {
			var cs host.ClusterStatus
			if err := client.New(g.addr, g.key, g.pin, g.insecure).JSON(c, "GET", "/v1/cluster/status", nil, &cs); err != nil {
				add("api key", "FAIL", err.Error())
			} else {
				add("api key", "ok", "admin key accepted")
				bad := 0
				for _, m := range cs.Members {
					if !m.Healthy {
						bad++
					}
				}
				s := "ok"
				if bad > 0 || cs.LastError != "" {
					s = "warn"
				}
				add("cluster", s, fmt.Sprintf("role=%s members=%d unhealthy=%d offset=%d %s", cs.Role, len(cs.Members), bad, cs.Offset, cs.LastError))
			}
		} else {
			add("api key", "warn", "no --key/REDX_KEY given; skipped authenticated checks")
		}
	default:
		// Not running: is the configured port free to bind?
		if ln, lerr := net.Listen("tcp", cfg.Listen); lerr != nil {
			add("daemon", "warn", fmt.Sprintf("%s unreachable (%v) and %s cannot be bound: %v", g.addr, err, cfg.Listen, lerr))
		} else {
			ln.Close()
			add("daemon", "warn", fmt.Sprintf("redxd is not running at %s; %s is free to bind", g.addr, cfg.Listen))
		}
	}
	if !host.IsLoopbackAddr(cfg.Listen) && cfg.TLS == "off" {
		add("exposure", "FAIL", fmt.Sprintf("listen=%s is not loopback and TLS is off: keys/data would travel in clear text", cfg.Listen))
	}

	bad := 0
	for _, r := range res {
		fmt.Printf("[%-4s] %-15s %s\n", r.status, r.name, r.detail)
		if r.status == "FAIL" {
			bad++
		}
	}
	if bad > 0 {
		fmt.Printf("\n%d problem(s) found.\n", bad)
		os.Exit(1)
	}
	fmt.Println("\nno blocking problems found.")
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func trim(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}
