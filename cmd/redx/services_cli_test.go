package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"redx/host"
	"redx/host/engineadapter"
)

func TestCLIServices(t *testing.T) {
	cfg := host.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.ArgonMemKiB, cfg.ArgonTime = 64, 1
	cfg.LogLevel = "quiet"
	h, err := host.New(cfg, engineadapter.Opener(engineadapter.Config{SweepMS: 50, CodeAllowedBoxes: []string{"players.red-xai.db:Player"}}), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(func() { srv.Close(); h.Close() })
	key := h.BootstrapKey
	dir := t.TempDir()
	ok := func(what string, args ...string) string {
		t.Helper()
		out, e, code := runCLI(t, srv.URL, key, args...)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", what, code, e)
		}
		return out
	}

	for i, v := range []string{`[ {Name[]} = "Ann", {Level[]} = 30, {Health[]} = 10 ]`, `[ {Name[]} = "Bob", {Level[]} = 5, {Health[]} = 10 ]`} {
		ok("set", "set", "players.red-xai.db:Player[ID="+string(rune('1'+i))+"]", v)
	}
	// sql
	out := ok("sql", "sql", "SELECT Name FROM players.Player WHERE Level > ? ORDER BY Name", "--param", "10")
	if !strings.Contains(out, "Ann") || strings.Contains(out, "Bob") {
		t.Fatalf("sql: %q", out)
	}
	if out := ok("sql json", "sql", "SELECT Name FROM players.Player WHERE Name = ?", "--sparam", "Bob", "--json"); !strings.Contains(out, "Bob") || !strings.Contains(out, `"kind":"select"`) {
		t.Fatalf("sql --json: %q", out)
	}
	if out := ok("sql write", "sql", "UPDATE players.Player SET Level = ? WHERE _id = 2", "--param", "6"); !strings.Contains(out, "1") {
		t.Fatalf("sql update: %q", out)
	}
	if out := ok("explain", "sql", "EXPLAIN SELECT Name FROM players.Player WHERE _id = 1"); out == "" {
		t.Fatal("explain printed nothing")
	}
	if _, e, code := runCLI(t, srv.URL, key, "sql", "SELEKT"); code == 0 || !strings.Contains(e, "syntax") {
		t.Fatalf("bad sql: %d %s", code, e)
	}

	// media
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for i := 0; i < 10; i++ {
		img.Set(i, i, color.White)
	}
	var pb bytes.Buffer
	png.Encode(&pb, img)
	src := filepath.Join(dir, "a.png")
	os.WriteFile(src, pb.Bytes(), 0o600)
	if out := ok("media put", "media", "put", "art", src, "hero.png", "--type", "image/png"); !strings.Contains(out, "hero.png") {
		t.Fatalf("media put: %q", out)
	}
	if out := ok("media ls", "media", "ls", "art"); !strings.Contains(out, "hero.png") {
		t.Fatalf("media ls: %q", out)
	}
	if out := ok("media stat", "media", "stat", "art", "hero.png"); !strings.Contains(out, "image/png") {
		t.Fatalf("media stat: %q", out)
	}
	dst := filepath.Join(dir, "got.png")
	ok("media get", "media", "get", "art", "hero.png", "--out", dst)
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, pb.Bytes()) {
		t.Fatal("media get differs")
	}
	part := filepath.Join(dir, "part.bin")
	ok("media range", "media", "get", "art", "hero.png", "--out", part, "--range", "4-11")
	if got, _ := os.ReadFile(part); !bytes.Equal(got, pb.Bytes()[4:12]) {
		t.Fatalf("media range bytes: %q", got)
	}
	if _, _, code := runCLI(t, srv.URL, key, "media", "get", "art", "hero.png", "--out", dst); code == 0 {
		t.Fatal("media get must refuse to overwrite without --force")
	}
	txt := filepath.Join(dir, "t.txt")
	os.WriteFile(txt, []byte("not media at all"), 0o600)
	if _, e, code := runCLI(t, srv.URL, key, "media", "put", "art", txt, "t.txt"); code == 0 || !strings.Contains(e, "415") && !strings.Contains(strings.ToLower(e), "media") {
		t.Fatalf("non-media put: %d %s", code, e)
	}
	if out := ok("media fsck", "media", "fsck", "art", "--deep"); out == "" {
		t.Fatal("fsck printed nothing")
	}
	ok("media rm", "media", "rm", "art", "hero.png")
	if _, _, code := runCLI(t, srv.URL, key, "media", "stat", "art", "hero.png"); code == 0 {
		t.Fatal("stat after rm")
	}

	// code
	script := filepath.Join(dir, "game.red-xai-code")
	os.WriteFile(script, []byte(`{Script[1]} = [ {Name[]} = "heal", {Params[]} = ;["target", "amount"]:, {Uses[]} = ;["db_get", "db_set"]:, {Source[]} = "let hp = db_get(target, \"Health\"); db_set(target, \"Health\", hp + amount); return hp + amount;" ];`), 0o600)
	ok("code define", "code", "define", "game", script)
	if out := ok("code ls", "code", "ls"); !strings.Contains(out, "heal") {
		t.Fatalf("code ls: %q", out)
	}
	if out := ok("code run", "code", "run", "heal", "players.red-xai.db:Player[ID=1]", "5"); !strings.Contains(out, "15") {
		t.Fatalf("code run: %q", out)
	}
	ok("code rm", "code", "rm", "game")

	// sec
	pol := filepath.Join(dir, "p.red-xai-sec")
	os.WriteFile(pol, []byte(`{Role[1]} = [ {Name[]} = "guest" ]; {Rule[1]} = [ {Effect[]} = "deny", {Who[]} = ;["role:guest"]:, {Actions[]} = "*", {Resource[]} = "players.red-xai.db:Player.Level" ];`), 0o600)
	ok("sec put", "sec", "put", pol)
	if out := ok("sec show", "sec", "show"); !strings.Contains(out, "guest") {
		t.Fatalf("sec show: %q", out)
	}
	gk := ok("key create", "key", "create", "--db", "default", "--scope", "read", "--roles", "guest", "--json")
	var m struct{ Key string }
	if i := strings.Index(gk, `"key":"`); i >= 0 {
		m.Key = gk[i+7 : i+7+strings.Index(gk[i+7:], `"`)]
	}
	if m.Key == "" {
		t.Fatalf("key create output: %q", gk)
	}
	if _, e, code := runCLI(t, srv.URL, m.Key, "sql", "SELECT Level FROM players.Player"); code == 0 || !strings.Contains(e, "403") {
		t.Fatalf("guest asking for a denied field: %d %s", code, e)
	}
	if out, e, code := runCLI(t, srv.URL, m.Key, "sql", "SELECT Name FROM players.Player"); code != 0 || !strings.Contains(out, "Ann") {
		t.Fatalf("guest allowed select: %d %q %s", code, out, e)
	}
	ok("sec rm", "sec", "rm")
}
