package engine

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"redx/box"
	"redx/engine/idx"
	"redx/engine/link"
	"redx/engine/pack"
)

// @!> cascade from a durable .red-xai.db record into a volatile .red-xai-db-qs file.
func TestCascadeFromDbIntoQS(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	db := open(t, dir, Config{Clock: clk.Now, Durability: "sync"})
	defer db.Close()
	putDoc(t, db, fLobby, `{Session[ID=1]= [ {Token[]} = "abc" ] (60)};
{Session[ID=2]= [ {Token[]} = "def" ] (60)};`)
	putDoc(t, db, fPlayers, `{Player[ID=1]= [
	    {Name[]} = "Josh",
	    {SessionCleanup[]} = "players.red-xai.db":Player[ID=1] @!> "lobby.red-xai-db-qs":Session[ID=1]
	]};
{Player[ID=2]= [
	    {Name[]} = "Ana",
	    {SessionCleanup[]} = "players.red-xai.db":Player[ID=2] @!> "lobby.red-xai-db-qs":Session[ID=2]
	]};`)
	le := db.NewLinkEngine(link.Options{})
	res, err := le.Delete(addr(fPlayers, "Player", 1), link.DeleteOptions{})
	if err != nil || len(res.Deleted) != 2 {
		t.Fatalf("cascade: %+v %v", res, err)
	}
	if v, err := db.Get(fLobby + ":Session[ID=1].Token"); err != nil || !v.IsNell() {
		t.Fatalf("session 1 survived: %v %v", v, err)
	}
	if v, _ := db.Get(fLobby + ":Session[ID=2].Token"); v.Str != "def" {
		t.Fatal("cascade hit the wrong session")
	}
	// Player 2's session expires first: the cascade must still delete the player, tolerating the vanished target.
	clk.Advance(61 * time.Second)
	if res, err := le.Delete(addr(fPlayers, "Player", 2), link.DeleteOptions{}); err != nil || len(res.Deleted) < 1 {
		t.Fatalf("cascade over an expired target: %+v %v", res, err)
	}
	if _, ok, _ := db.Records().Get(addr(fPlayers, "Player", 2)); ok {
		t.Fatal("player 2 survived")
	}
}

const idxDecls = `
{PlayerByName[]} = [ {File[]} = "players.red-xai.db", {Box[]} = "Player", {Fields[]} = ;["Name"]:, {Kind[]} = "exact", {Fold[]} = TRUE ];
{PlayerLevel[]}  = [ {File[]} = "players.red-xai.db", {Box[]} = "Player", {Fields[]} = ;["Level"]:, {Kind[]} = "range" ];
`

func ids(t *testing.T, hits []idx.Hit, err error) []int64 {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	var out []int64
	for _, h := range hits {
		out = append(out, h.Addr.ID)
	}
	return out
}

func TestIndexSetOverRealEngines(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	putDoc(t, db, fPlayers, `{Player[ID=1]= [ {Name[]} = "Josh", {Level[]} = 5 ]};
{Player[ID=2]= [ {Name[]} = "Ana", {Level[]} = 9 ]};`)
	os.WriteFile(filepath.Join(dir, "main.red-xai-db-idx"), []byte(idxDecls), 0o644)

	set, err := db.OpenIndexes("main")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range set.Loaded {
		if r.Status != idx.Rebuilt || r.Reason != "missing" {
			t.Fatalf("first open should rebuild: %+v", r)
		}
	}
	byName, byLevel := set.Manager.Index("PlayerByName"), set.Manager.Index("PlayerLevel")
	h, err := byName.Exact("Name", box.String("JOSH"), 0)
	if g := ids(t, h, err); len(g) != 1 || g[0] != 1 {
		t.Fatalf("exact (folded): %v", g)
	}
	// writes through the wrapper (also what Link/Pulse engines use) keep the index current
	ent, _ := box.ParseDocument(`{Player[ID=3]= [ {Name[]} = "Zed", {Level[]} = 7 ]};`)
	if err := set.Store.Put(addr(fPlayers, "Player", 3), ent.Entries()[0]); err != nil {
		t.Fatal(err)
	}
	h, err = byLevel.Range("Level", idx.Incl(6), idx.Incl(9), 0)
	if g := ids(t, h, err); len(g) != 2 || g[0] != 3 || g[1] != 2 {
		t.Fatalf("range 6..9 (ordered by level): %v", g)
	}
	if _, err := set.Store.Delete(addr(fPlayers, "Player", 2)); err != nil {
		t.Fatal(err)
	}
	h, err = byLevel.Range("Level", idx.Incl(6), nil, 0)
	if g := ids(t, h, err); len(g) != 1 || g[0] != 3 {
		t.Fatalf("after delete: %v", g)
	}
	if d, err := set.Check(); err != nil || d["PlayerByName"].Stale() || d["PlayerLevel"].Stale() {
		t.Fatalf("index should be exact: %+v %v", d, err)
	}
	// a write that bypasses the wrapper is detected and repaired
	if err := db.Set(fPlayers+":Player[ID=1].Level", box.Int(50), SetOpts{}); err != nil {
		t.Fatal(err)
	}
	if d, _ := set.Check(); !d["PlayerLevel"].Stale() {
		t.Fatal("bypassing write not detected")
	}
	if err := set.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if d, _ := set.Check(); d["PlayerLevel"].Stale() {
		t.Fatal("rebuild did not repair")
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	db.crash()

	// restart: snapshot verified against the recovered data, no rebuild
	db2 := open(t, dir, Config{})
	defer db2.Close()
	set2, err := db2.OpenIndexes("main")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range set2.Loaded {
		if r.Status != idx.LoadedVerified {
			t.Fatalf("restart: %+v", r)
		}
	}
	h, err = set2.Manager.Index("PlayerLevel").Range("Level", idx.Incl(10), nil, 0)
	if g := ids(t, h, err); len(g) != 1 || g[0] != 1 {
		t.Fatalf("restored index wrong: %v", g)
	}
	// data changed while no one was watching: the old snapshot must NOT be trusted
	db2.Set(fPlayers+":Player[ID=3].Level", box.Int(1000), SetOpts{})
	set3, _ := db2.OpenIndexes("main")
	for _, r := range set3.Loaded {
		if r.Name == "PlayerLevel" && (r.Status != idx.Rebuilt || r.Reason != "stale") {
			t.Fatalf("stale snapshot trusted: %+v", r)
		}
	}
	// bad names / missing decl file
	if _, err := db2.OpenIndexes("../evil"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := db2.OpenIndexes("nope"); err == nil {
		t.Fatal("missing declaration file accepted")
	}
}

func TestPackOverDatabaseDir(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	pk, err := db.OpenPack("assets", pack.Config{ChunkSize: 1024})
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("redx-asset-"), 1000) // ~11 KB, 11 chunks
	if _, err := pk.Put("maps/world1.bin", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if _, err := pk.Put("maps/copy.bin", bytes.NewReader(data)); err != nil { // dedupe
		t.Fatal(err)
	}
	// the database keeps working next to a pack and does not mistake it for a data file
	mustSet(t, db, "a.red-xai.db:A.B", num(1), SetOpts{})
	if got := db.Records().Files(); len(got) != 1 {
		t.Fatalf("pack leaked into data files: %v", got)
	}
	db.Close()

	db2 := open(t, dir, Config{})
	defer db2.Close()
	pk2, err := db2.OpenPack("assets", pack.Config{})
	if err != nil {
		t.Fatal(err)
	}
	rc, info, err := pk2.Get("maps/world1.bin")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, data) || info.Size != int64(len(data)) {
		t.Fatalf("pack content lost across reopen: %v", err)
	}
	if err := pk2.Verify("maps/copy.bin"); err != nil {
		t.Fatal(err)
	}
	// corruption of a stored chunk is reported, not served
	ch, _ := filepath.Glob(filepath.Join(dir, "assets.red-xai-pack", "chunks", "*", "*"))
	if len(ch) == 0 {
		t.Fatal("no chunk files")
	}
	os.WriteFile(ch[0], []byte("garbage"), 0o644)
	if err := pk2.Verify("maps/world1.bin"); err == nil {
		t.Fatal("corrupt chunk not detected")
	}
	if _, err := db2.OpenPack("a/b", pack.Config{}); err == nil {
		t.Fatal("bad pack name accepted")
	}
}
