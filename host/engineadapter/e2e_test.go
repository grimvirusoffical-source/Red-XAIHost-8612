package engineadapter_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"redx/host/engineadapter"
)

// TestEndToEndEveryFileType drives one host (real engines) through every file type RedX has:
// .red-xai.db, -db-qs, -db-qs-ls, -db-link, -db-pulse, -db-idx, -pack, -media, -code, -sec,
// using only the HTTP API, with an admin key, a role-carrying key and a prefix-limited key.
func TestEndToEndEveryFileType(t *testing.T) {
	kf := filepath.Join(t.TempDir(), "master.key")
	if err := engineadapter.GenerateKeyFile(kf, "k1"); err != nil {
		t.Fatal(err)
	}
	ring, err := engineadapter.LoadKeyFile(kf)
	if err != nil {
		t.Fatal(err)
	}
	// the standalone link file is placed by hand before the host starts: deleting Player 1
	// cascades into inventory 100
	n := startFull(t, engineadapter.Config{Keyring: ring, CodeAllowedBoxes: []string{"players.red-xai.db:Player"}}, func(dir string) {
		src := `{Cleanup[1]} = "players.red-xai.db":Player[ID=1] @!> "inventories.red-xai.db":Inv[ID=100];` + "\n"
		if err := os.WriteFile(filepath.Join(dir, "rels.red-xai-db-link"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	D := n.url + "/v1/db/default"
	P := n.url + "/v1/path/"
	must := func(what string, want int, st int, b []byte) {
		t.Helper()
		if st != want {
			t.Fatalf("%s: want %d got %d %s", what, want, st, b)
		}
	}
	admin := n.key

	// ---- sec + code definitions (admin) ----
	pol := `{Role[1]} = [ {Name[]} = "guest" ];
{Rule[1]} = [ {Effect[]} = "deny", {Who[]} = ;["role:guest"]:, {Actions[]} = "*", {Resource[]} = "players.red-xai.db:Player.Email" ];
{Encrypt[1]} = [ {File[]} = "vault*", {Key[]} = "k1" ];`
	st, b, _ := req(t, "PUT", D+"/sec", admin, []byte(pol))
	must("sec", 200, st, b)
	st, b, _ = req(t, "PUT", D+"/code/file/game", admin, []byte(healScript))
	must("code", 200, st, b)

	// ---- .red-xai.db ----
	for id, p := range map[int][3]string{1: {"Ann", "ann@x.test", "30"}, 2: {"Bob", "bob@x.test", "5"}} {
		body := fmt.Sprintf(`[ {Name[]} = %q, {Email[]} = %q, {Level[]} = %s, {Health[]} = 10 ]`, p[0], p[1], p[2])
		st, b, _ = req(t, "PUT", fmt.Sprintf("%splayers.red-xai.db:Player[ID=%d]", P, id), admin, []byte(body))
		must("db put", 200, st, b)
	}
	st, b, _ = req(t, "PUT", P+"inventories.red-xai.db:Inv[ID=100]", admin, []byte(`[ {Items[]} = ;["sword"]: ]`))
	must("inv put", 200, st, b)
	st, b, _ = req(t, "PUT", P+"guilds.red-xai.db:Guild[ID=10]", admin, []byte(`[ {Title[]} = "Dragons" ]`))
	must("guild put", 200, st, b)
	st, b, _ = req(t, "PUT", P+"players.red-xai.db:Player[ID=2].Guild", admin, []byte(`"players.red-xai.db":Player[ID=2] @> "guilds.red-xai.db":Guild[ID=10]`))
	must("embedded link put", 200, st, b)
	if st, b, _ = req(t, "GET", P+"players.red-xai.db:Player[ID=2].Name", admin, nil); st != 200 || !strings.Contains(string(b), "Bob") {
		t.Fatalf("db get: %d %s", st, b)
	}
	if st, _, _ = req(t, "GET", P+"players.red-xai.db:Player[ID=77].Name", admin, nil); st != 404 {
		t.Fatalf("absent record must be 404: %d", st)
	}

	// ---- link (embedded + resolve, standalone cascade) ----
	if st, b, _ = req(t, "GET", P+"players.red-xai.db:Player[ID=2].Guild?resolve=1", admin, nil); st != 200 || !strings.Contains(string(b), "Dragons") {
		t.Fatalf("resolve: %d %s", st, b)
	}

	// ---- qs (TTL) and qs-ls ----
	st, b, _ = req(t, "PUT", P+"sess.red-xai-db-qs:Q.Otp?ttl=60", admin, []byte(`"4711"`))
	must("qs put", 200, st, b)
	if st, b, _ = req(t, "GET", P+"sess.red-xai-db-qs:Q.Otp", admin, nil); st != 200 || !strings.Contains(string(b), "4711") {
		t.Fatalf("qs get: %d %s", st, b)
	}
	if st, _, _ = req(t, "GET", P+"sess.red-xai-db-qs:Q.Nope", admin, nil); st != 404 {
		t.Fatalf("qs absent must be 404: %d", st)
	}
	st, b, _ = req(t, "PUT", P+"world.red-xai-db-qs-ls:Hot.Counter", admin, []byte(`41`))
	must("qs-ls put", 200, st, b)
	if st, b, _ = req(t, "GET", P+"world.red-xai-db-qs-ls:Hot.Counter", admin, nil); st != 200 || !strings.Contains(string(b), "41") {
		t.Fatalf("qs-ls get: %d %s", st, b)
	}

	// ---- idx (+ SQL planner uses it) ----
	st, b, _ = req(t, "PUT", D+"/idx/byname", admin, []byte(idxDecl))
	must("idx define", 200, st, b)
	if st, b, _ = req(t, "GET", D+"/idx/byname/PlayerByName/query?mode=exact&field=Name&value=bob", admin, nil); st != 200 || !strings.Contains(string(b), "Player[ID=2]") {
		t.Fatalf("idx query: %d %s", st, b)
	}
	if st, o, raw := sql(t, n, admin, "", "SELECT Name FROM players.Player WHERE Name = ?", "Bob"); st != 200 || o.Count != 1 || !strings.HasPrefix(o.Access, "index:") {
		t.Fatalf("sql over the index: %d %s", st, raw)
	}

	// ---- pulse (rule fired by a SQL write) ----
	st, b, _ = req(t, "PUT", D+"/pulse", admin, []byte(deathRule))
	must("pulse define", 200, st, b)
	ws := wsDial(t, D+"/pulse/subscribe", admin)
	if st, o, raw := sql(t, n, admin, "", "UPDATE players.Player SET Health = 0 WHERE _id = 2"); st != 200 || o.Affected != 1 {
		t.Fatalf("sql update: %d %s", st, raw)
	}
	ev, ok := readEvent(t, ws, 3*time.Second)
	if !ok || ev.Topic != "player.died" || ev.Path != "players.red-xai.db:Player[ID=2]" {
		t.Fatalf("pulse event: %+v ok=%v", ev, ok)
	}
	if _, b, _ = req(t, "GET", P+"players.red-xai.db:Player[ID=2].IsDead", admin, nil); !strings.Contains(string(b), "TRUE") {
		t.Fatalf("pulse action: %s", b)
	}

	// ---- code (sandboxed script) ----
	body, _ := json.Marshal(map[string]interface{}{"args": []interface{}{"players.red-xai.db:Player[ID=1]", 5}})
	st, b, _ = req(t, "POST", D+"/code/run/heal", admin, body, "Content-Type", "application/json")
	if st != 200 || !strings.Contains(string(b), "15") {
		t.Fatalf("code run: %d %s", st, b)
	}

	// ---- pack (encrypted by the sec policy) ----
	blob := bytes.Repeat([]byte("E2E-PLAINTEXT-MARKER "), 4000)
	st, b, _ = req(t, "PUT", D+"/pack/vault/file/d.bin", admin, blob)
	must("pack put", 201, st, b)
	if st, b, _ = req(t, "GET", D+"/pack/vault/file/d.bin", admin, nil); st != 200 || !bytes.Equal(b, blob) {
		t.Fatalf("pack get: %d", st)
	}
	st, b, _ = req(t, "PUT", D+"/pack/open/file/d.bin", admin, blob)
	must("plain pack put", 201, st, b)
	if st, b, _ = req(t, "GET", D+"/pack/open/file/d.bin", admin, nil, "Range", "bytes=21-41"); st != 206 || string(b) != string(blob[21:42]) {
		t.Fatalf("pack range: %d %q", st, b)
	}

	// ---- media ----
	img := pngBytes(t, 8, 8)
	st, b, _ = req(t, "PUT", D+"/media/art/file/x.png?type=image", admin, img)
	must("media put", 201, st, b)
	if st, b, _ = req(t, "GET", D+"/media/art/file/x.png", admin, nil, "Range", "bytes=0-7"); st != 206 || !bytes.Equal(b, img[:8]) {
		t.Fatalf("media range: %d %q", st, b)
	}

	// ---- sec: a guest key (role) and a prefix key see only what they should ----
	guest := mkKeyFull(t, n, map[string]interface{}{"scope": "write", "db": "default", "roles": []string{"guest"}})
	if st, b, _ = req(t, "GET", P+"players.red-xai.db:Player[ID=1]", guest, nil); st != 200 || strings.Contains(string(b), "x.test") {
		t.Fatalf("guest read: %d %s", st, b)
	}
	if st, _, _ = sql(t, n, guest, "", "SELECT Email FROM players.Player"); st != 403 {
		t.Fatalf("guest SQL on Email: %d", st)
	}
	pk := mkKey(t, n, "read", "default", "guilds.red-xai.db:")
	if st, _, _ = req(t, "GET", P+"players.red-xai.db:Player[ID=1].Name", pk, nil); st != 403 {
		t.Fatalf("prefix key outside its prefix: %d", st)
	}
	if st, _, raw := sql(t, n, pk, "", "SELECT Title FROM guilds.Guild"); st != 200 || !strings.Contains(raw, "Dragons") {
		t.Fatalf("prefix key SQL: %d %s", st, raw)
	}
	if st, _, _ = req(t, "GET", D+"/media/art/file/x.png", "bogus", nil); st != 401 {
		t.Fatalf("bad key: %d", st)
	}

	// ---- link cascade through SQL DELETE: the hand-placed .red-xai-db-link file ----
	st, o, raw := sql(t, n, admin, "", "DELETE FROM players.Player WHERE _id = 1")
	if st != 200 || o.Affected < 1 {
		t.Fatalf("sql delete: %d %s", st, raw)
	}
	if st, _, _ = req(t, "GET", P+"inventories.red-xai.db:Inv[ID=100]", admin, nil); st != 404 {
		t.Fatalf("cascade over the standalone link did not delete the inventory: %d (%s)", st, raw)
	}

	// ---- every file type exists on disk under its own name ----
	dir := filepath.Join(n.h.Cfg.DataDir, "dbs", "default")
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	all := strings.Join(names, " ")
	for _, want := range []string{"players.red-xai.db", "sess.red-xai-db-qs", "world.red-xai-db-qs-ls", "rels.red-xai-db-link", "red-xai-db-idx", "red-xai-db-pulse", "vault.red-xai-pack", "art.red-xai-media", "red-xai-code", "red-xai-sec"} {
		if !strings.Contains(all, want) {
			t.Errorf("no %q in the data dir: %v", want, names)
		}
	}
}
