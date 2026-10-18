package main

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runDaemon end to end on loopback with the build's engine: boots, serves /healthz, stops on cancel.
func TestRunDaemonServesAndStops(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "redx.toml")
	ln := freeAddr(t)
	writeFile(t, cfgPath, "data_dir = '"+filepath.ToSlash(dir)+"'\nlisten = '"+ln+"'\nlog_level = 'quiet'\nargon_memory_kib = 64\nargon_time = 1\n")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runDaemon(ctx, []string{"--config", cfgPath}, func(string) {}) }()
	var body string
	for i := 0; i < 100; i++ {
		resp, err := http.Get("http://" + ln + "/healthz")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(b)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("healthz: %q", body)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("daemon did not stop")
	}
}
