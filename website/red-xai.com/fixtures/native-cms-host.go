// Compiled in a pinned RedX checkout by native-cms-integration.mjs. This test
// host only opens guarded, disposable fixture directories, never a deployment.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"redx/host"
	"redx/host/engineadapter"
)

type input struct{ Root, Mode, Data string }

func main() {
	if err := run(); err != nil {
		// Never echo request bodies, key material or underlying transport errors.
		fmt.Fprintln(os.Stderr, "Disposable native CMS fixture failed")
		os.Exit(1)
	}
}

func run() error {
	lines := bufio.NewScanner(os.Stdin)
	if !lines.Scan() {
		return fmt.Errorf("missing fixture configuration")
	}
	var in input
	if json.Unmarshal(lines.Bytes(), &in) != nil {
		return fmt.Errorf("invalid fixture configuration")
	}
	root, err := filepath.Abs(in.Root)
	if err != nil || root != filepath.Clean(in.Root) || !strings.HasPrefix(filepath.Base(root), "redx-native-cms-") {
		return fmt.Errorf("invalid fixture root")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return fmt.Errorf("linked fixture root")
	}
	marker, err := os.ReadFile(filepath.Join(root, "fixture-marker"))
	if err != nil || string(marker) != "isolated-native-cms-v1\n" {
		return fmt.Errorf("fixture marker required")
	}
	for _, part := range []string{"native", "restored"} {
		p := filepath.Join(root, part)
		if st, err := os.Lstat(p); err == nil && (!st.IsDir() || st.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("invalid fixture data")
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if in.Mode == "backup-restore" {
		archive, err := os.OpenFile(filepath.Join(root, "native-backup.tgz"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		err = host.OfflineBackup(filepath.Join(root, "native"), archive, "", false)
		closeErr := archive.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		archive, err = os.Open(filepath.Join(root, "native-backup.tgz"))
		if err != nil {
			return err
		}
		defer archive.Close()
		if _, err := os.Lstat(filepath.Join(root, "restored")); !os.IsNotExist(err) {
			return fmt.Errorf("restore destination must not exist")
		}
		if _, err := host.OfflineRestore(filepath.Join(root, "restored"), archive, "", false); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]bool{"backupRestored": true})
	}
	if in.Mode != "serve" || (in.Data != "native" && in.Data != "restored") {
		return fmt.Errorf("invalid fixture mode")
	}
	server := httptest.NewUnstartedServer(nil)
	config := host.DefaultConfig()
	config.DataDir = filepath.Join(root, in.Data)
	config.Advertise = "http://" + server.Listener.Addr().String()
	config.AuditLog = filepath.Join(root, in.Data+"-audit.jsonl")
	config.ArgonMemKiB, config.ArgonTime = 64, 1 // Disposable test keys only.
	config.Heartbeat = 1
	config.EngineDurability = "sync"
	config.Bootstrap = "console"
	h, err := host.New(config, engineadapter.Opener(engineadapter.Config{Durability: "sync", SweepMS: 50}), nil)
	if err != nil {
		_ = server.Listener.Close()
		return err
	}
	defer h.Close()
	server.Config.Handler = h.Handler()
	server.Start()
	defer server.Close()
	// This stdout is a private pipe to the test coordinator, never a CI log.
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"ready": true, "url": server.URL, "key": h.BootstrapKey, "nativeEngine": true, "durability": "sync"}); err != nil {
		return err
	}
	if !lines.Scan() || strings.TrimSpace(lines.Text()) != "stop" {
		return fmt.Errorf("missing fixture stop")
	}
	server.Close()
	return nil
}
