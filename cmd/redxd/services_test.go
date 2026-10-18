//go:build !redx_stub

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func httpDo(t *testing.T, method, url, key string, body []byte) (int, []byte, http.Header) {
	t.Helper()
	r, _ := http.NewRequest(method, url, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

// start the daemon with a master key file and hand-placed .red-xai-* files; returns the admin key
// (read from the daemon's startup banner on stdout) and a stop function.
func startDaemon(t *testing.T, dir string, extraCfg string) (addr, key string, stop func() error) {
	t.Helper()
	addr = freeAddr(t)
	cfgPath := filepath.Join(dir, "redx.toml")
	writeFile(t, cfgPath, "data_dir = '"+filepath.ToSlash(dir)+"'\nlisten = '"+addr+"'\nlog_level = 'quiet'\nargon_memory_kib = 64\nargon_time = 1\n"+extraCfg)
	rd, wr, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = wr
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runDaemon(ctx, []string{"--config", cfgPath}, func(string) {}) }()
	captured := make(chan string, 1)
	go func() { b, _ := io.ReadAll(rd); captured <- string(b) }()
	up := false
	for i := 0; i < 100 && !up; i++ {
		select {
		case err := <-done:
			os.Stdout = old
			wr.Close()
			t.Fatalf("daemon exited early: %v", err)
		default:
		}
		if resp, err := http.Get("http://" + addr + "/healthz"); err == nil {
			resp.Body.Close()
			up = true
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
	os.Stdout = old
	wr.Close()
	out := <-captured
	if !up {
		cancel()
		t.Fatalf("daemon did not come up: %s", out)
	}
	key = regexp.MustCompile(`rx_live_[A-Za-z0-9_-]+`).FindString(out)
	return addr, key, func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(15 * time.Second):
			return context.DeadlineExceeded
		}
	}
}

func TestDaemonLoadsHandPlacedFilesAndMasterKeyFile(t *testing.T) {
	dir := t.TempDir()
	kf := filepath.Join(t.TempDir(), "k.key")
	if err := runKeygen([]string{"--out", kf, "--id", "daemon-key"}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(kf); len(raw) == 0 {
		t.Fatal("empty key file")
	}
	db := filepath.Join(dir, "dbs", "default")
	os.MkdirAll(db, 0o755)
	writeFile(t, filepath.Join(db, "site.red-xai-sec"), `{Role[1]} = [ {Name[]} = "guest" ];
{Rule[1]} = [ {Effect[]} = "deny", {Who[]} = ;["role:guest"]:, {Actions[]} = "*", {Resource[]} = "players.red-xai.db:Player.Email" ];
{Encrypt[1]} = [ {File[]} = "vault*", {Key[]} = "daemon-key" ];`)
	writeFile(t, filepath.Join(db, "game.red-xai-code"), `{Script[1]} = [ {Name[]} = "heal", {Params[]} = ;["target", "amount"]:, {Uses[]} = ;["db_get", "db_set"]:, {Source[]} = "let hp = db_get(target, \"Health\"); db_set(target, \"Health\", hp + amount); return hp + amount;" ];`)
	writeFile(t, filepath.Join(db, "rels.red-xai-db-link"), `{Cleanup[1]} = "players.red-xai.db":Player[ID=1] @!> "inventories.red-xai.db":Inv[ID=100];`+"\n")

	addr, key, stop := startDaemon(t, dir, "master_key_file = '"+filepath.ToSlash(kf)+"'\ncode_allowed_boxes = ['players.red-xai.db:Player']\n")
	u := "http://" + addr
	if key == "" {
		t.Fatal("no bootstrap key on stdout")
	}
	if st, b, _ := httpDo(t, "PUT", u+"/v1/path/players.red-xai.db:Player[ID=1]", key, []byte(`[ {Name[]} = "Ann", {Email[]} = "ann@x.test", {Health[]} = 10 ]`)); st != 200 {
		t.Fatalf("put: %d %s", st, b)
	}
	// hand-placed code file + AllowedBoxes from the config
	st, b, _ := httpDo(t, "POST", u+"/v1/db/default/code/run/heal", key, []byte(`{"args":["players.red-xai.db:Player[ID=1]", 4]}`))
	if st != 200 || !strings.Contains(string(b), "14") {
		t.Fatalf("hand-placed script: %d %s", st, b)
	}
	// hand-placed policy: role guest is masked
	_, kb, _ := httpDo(t, "POST", u+"/v1/keys", key, []byte(`{"scope":"read","db":"default","roles":["guest"]}`))
	var km struct{ Key string }
	json.Unmarshal(kb, &km)
	if st, b, _ := httpDo(t, "GET", u+"/v1/path/players.red-xai.db:Player[ID=1]", km.Key, nil); st != 200 || strings.Contains(string(b), "x.test") {
		t.Fatalf("hand-placed policy: %d %s", st, b)
	}
	// encryption through the key file, ciphertext at rest
	if st, b, _ := httpDo(t, "PUT", u+"/v1/db/default/pack/vault/file/a.txt", key, []byte("DAEMON-PLAINTEXT-MARKER")); st != 201 || !strings.Contains(string(b), `"encrypted":true`) {
		t.Fatalf("encrypted pack: %d %s", st, b)
	}
	filepath.Walk(filepath.Join(db, "vault.red-xai-pack"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if data, _ := os.ReadFile(p); bytes.Contains(data, []byte("PLAINTEXT-MARKER")) {
				t.Errorf("plaintext at rest: %s", p)
			}
		}
		return nil
	})
	_, ab, _ := httpDo(t, "POST", u+"/v1/keys", key, []byte(`{"scope":"admin"}`))
	var am struct{ Key string }
	json.Unmarshal(ab, &am)
	if am.Key == "" {
		t.Fatalf("admin key: %s", ab)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	// restart on the same data dir: the encrypted pack still reads back
	addr2, key2, stop2 := startDaemon(t, dir, "master_key_file = '"+filepath.ToSlash(kf)+"'\ncode_allowed_boxes = ['players.red-xai.db:Player']\n")
	_ = key2
	defer stop2()
	if st, b, h := httpDo(t, "GET", "http://"+addr2+"/v1/db/default/pack/vault/file/a.txt", am.Key, nil); st != 200 || string(b) != "DAEMON-PLAINTEXT-MARKER" || h.Get("X-Redx-Encrypted") != "1" {
		t.Fatalf("after restart: %d %q", st, b)
	}
}

func TestDaemonRefusesLooseOrMissingKeyFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := t.TempDir()
	kf := filepath.Join(t.TempDir(), "k.key")
	if err := runKeygen([]string{"--out", kf}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(kf, 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "redx.toml")
	writeFile(t, cfgPath, "data_dir = '"+filepath.ToSlash(dir)+"'\nlisten = '"+freeAddr(t)+"'\nlog_level = 'quiet'\nargon_memory_kib = 64\nargon_time = 1\nmaster_key_file = '"+filepath.ToSlash(kf)+"'\n")
	err := runDaemon(context.Background(), []string{"--config", cfgPath}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "master key") {
		t.Fatalf("a group/world-readable key file must stop the daemon: %v", err)
	}
	writeFile(t, cfgPath, "data_dir = '"+filepath.ToSlash(dir)+"'\nlisten = '"+freeAddr(t)+"'\nlog_level = 'quiet'\nargon_memory_kib = 64\nargon_time = 1\nmaster_key_file = '"+filepath.ToSlash(filepath.Join(dir, "nonexistent.key"))+"'\n")
	if err := runDaemon(context.Background(), []string{"--config", cfgPath}, func(string) {}); err == nil {
		t.Fatal("a configured but missing key file must stop the daemon")
	}
	// keygen refuses to overwrite
	if err := runKeygen([]string{"--out", kf}); err == nil {
		t.Fatal("keygen must not overwrite an existing key file")
	}
}
