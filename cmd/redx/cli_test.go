package main

import (
	"bytes"
	"crypto/rand"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"redx/host"
	"redx/host/engineadapter"
)

// The test binary doubles as the CLI: REDX_CLI_AS_MAIN=1 runs main().
func TestMain(m *testing.M) {
	if os.Getenv("REDX_CLI_AS_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runCLI(t *testing.T, addr, key string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "REDX_CLI_AS_MAIN=1", "REDX_ADDR="+addr, "REDX_KEY="+key)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return so.String(), se.String(), code
}

func TestCLIEndToEnd(t *testing.T) {
	cfg := host.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.ArgonMemKiB, cfg.ArgonTime = 64, 1
	cfg.LogLevel = "quiet"
	h, err := host.New(cfg, engineadapter.Opener(engineadapter.Config{SweepMS: 50}), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(func() { srv.Close(); h.Close() })
	key := h.BootstrapKey
	dir := t.TempDir()

	if out, _, code := runCLI(t, srv.URL, key, "--version"); code != 0 || !strings.HasPrefix(out, "redx ") {
		t.Fatalf("--version: %q %d", out, code)
	}
	// set/get/resolve
	if _, e, code := runCLI(t, srv.URL, key, "set", "guilds.red-xai.db:Guild[ID=1]", `[ {Name[]} = "Dragons" ]`); code != 0 {
		t.Fatalf("set: %s", e)
	}
	runCLI(t, srv.URL, key, "set", "players.red-xai.db:Player[ID=1]", `[ {Name[]} = "Ann", {Level[]} = 7, {Guild[]} = "players.red-xai.db":Player[ID=1] @> "guilds.red-xai.db":Guild[ID=1] ]`)
	runCLI(t, srv.URL, key, "set", "players.red-xai.db:Player[ID=2]", `[ {Name[]} = "Bob", {Level[]} = 30 ]`)
	if out, e, code := runCLI(t, srv.URL, key, "get", "players.red-xai.db:Player[ID=1].Guild", "--resolve"); code != 0 || !strings.Contains(out, "Dragons") || strings.Contains(out, "@>") {
		t.Fatalf("get --resolve: %q %s", out, e)
	}
	// idx
	decl := filepath.Join(dir, "idx.red-xai-db-idx")
	os.WriteFile(decl, []byte(`{ByName[]} = [ {File[]} = "players.red-xai.db", {Box[]} = "Player", {Fields[]} = ;["Name"]:, {Kind[]} = "exact" ];
{ByLevel[]} = [ {File[]} = "players.red-xai.db", {Box[]} = "Player", {Fields[]} = ;["Level"]:, {Kind[]} = "range" ];
`), 0o600)
	if _, e, code := runCLI(t, srv.URL, key, "idx", "define", "players", decl); code != 0 {
		t.Fatalf("idx define: %s", e)
	}
	if out, e, code := runCLI(t, srv.URL, key, "idx", "query", "players", "ByName", "--field", "Name", "--value", "Ann"); code != 0 || !strings.Contains(out, "players.red-xai.db:Player[ID=1]") || strings.Contains(out, "ID=2") {
		t.Fatalf("idx query exact: %q %s", out, e)
	}
	if out, _, code := runCLI(t, srv.URL, key, "idx", "query", "players", "ByLevel", "--field", "Level", "--lo", "10", "--hi", "50"); code != 0 || !strings.Contains(out, "ID=2") || strings.Contains(out, "ID=1]") {
		t.Fatalf("idx query range: %q", out)
	}
	if out, _, code := runCLI(t, srv.URL, key, "idx", "list"); code != 0 || !strings.Contains(out, "ByLevel") {
		t.Fatalf("idx list: %q", out)
	}
	// pack put / ls / get (+range)
	data := make([]byte, 2<<20+777)
	rand.Read(data)
	src := filepath.Join(dir, "blob.bin")
	os.WriteFile(src, data, 0o600)
	if out, e, code := runCLI(t, srv.URL, key, "pack", "put", "media", src, "dir/blob.bin"); code != 0 || !strings.Contains(out, "stored dir/blob.bin") {
		t.Fatalf("pack put: %q %s", out, e)
	}
	if out, _, code := runCLI(t, srv.URL, key, "pack", "ls", "media", "dir"); code != 0 || !strings.Contains(out, "blob.bin") {
		t.Fatalf("pack ls: %q", out)
	}
	dst := filepath.Join(dir, "got.bin")
	if _, e, code := runCLI(t, srv.URL, key, "pack", "get", "media", "dir/blob.bin", "--out", dst); code != 0 || !strings.Contains(e, "sha256 verified") {
		t.Fatalf("pack get: %s", e)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, data) {
		t.Fatal("downloaded file differs")
	}
	if _, _, code := runCLI(t, srv.URL, key, "pack", "get", "media", "dir/blob.bin", "--out", dst); code == 0 {
		t.Fatal("must refuse to overwrite without --force")
	}
	part := filepath.Join(dir, "part.bin")
	from, to := (1<<20)-10, (1<<20)+10
	if _, e, code := runCLI(t, srv.URL, key, "pack", "get", "media", "dir/blob.bin", "--out", part, "--range", "1048566-1048585"); code != 0 {
		t.Fatalf("ranged get: %s", e)
	}
	if got, _ := os.ReadFile(part); !bytes.Equal(got, data[from:to]) {
		t.Fatalf("range bytes differ (%d)", len(got))
	}
	part2 := filepath.Join(dir, "part2.bin")
	if _, e, code := runCLI(t, srv.URL, key, "pack", "get", "media", "dir/blob.bin", "--out", part2, "--offset", "100", "--length", "50"); code != 0 {
		t.Fatalf("offset/length get: %s", e)
	}
	if got, _ := os.ReadFile(part2); !bytes.Equal(got, data[100:150]) {
		t.Fatal("offset/length bytes differ")
	}
	// bad key => failure with 401, never data
	for _, args := range [][]string{
		{"get", "players.red-xai.db:Player[ID=1].Name"},
		{"idx", "query", "players", "ByName", "--field", "Name", "--value", "Ann"},
		{"pack", "ls", "media"},
		{"pack", "get", "media", "dir/blob.bin", "--out", filepath.Join(dir, "x.bin")},
	} {
		out, e, code := runCLI(t, srv.URL, "rx_live_"+strings.Repeat("z", 43), args...)
		if code == 0 || !strings.Contains(e, "401") || out != "" {
			t.Errorf("%v with a bad key: code=%d out=%q err=%q", args, code, out, e)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "x.bin")); err == nil {
		t.Error("a refused download left a file")
	}
}
