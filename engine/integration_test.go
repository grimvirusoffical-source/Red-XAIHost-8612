package engine

import (
	"errors"
	"testing"
	"time"

	"redx/box"
	"redx/engine/link"
	"redx/engine/pulse"
)

func putDoc(t *testing.T, db *Database, file, src string) {
	t.Helper()
	if err := link.PutDoc(db.Records(), file, src); err != nil {
		t.Fatalf("PutDoc %s: %v", file, err)
	}
}

func addr(file, name string, id int64) link.Addr {
	return link.Addr{File: file, Box: name, ID: id, HasID: true}
}

const (
	fPlayers = "players.red-xai.db"
	fGuilds  = "guilds.red-xai.db"
	fInv     = "inventories.red-xai.db"
	fLobby   = "lobby.red-xai-db-qs"
	fWorld   = "world.red-xai-db-qs-ls"
)

func TestRecordStoreImplementsLinkStoreSemantics(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	db := open(t, dir, Config{Clock: clk.Now, Durability: "sync"})
	rs := db.Records()

	putDoc(t, db, fPlayers, `{Player[ID=1]= [ {Name[]} = "Josh", {Health[]} = 100 ]};
{Player[ID=2]= [ {Name[]} = "Ana", {Health[]} = 80 ]};`)
	e, ok, err := rs.Get(addr(fPlayers, "Player", 1))
	if err != nil || !ok || e.Key != "Player" {
		t.Fatalf("get: %v %v %v", e, ok, err)
	}
	if id, has := e.ID(); !has || id != 1 {
		t.Fatalf("id attr lost: %+v", e.Attrs)
	}
	e.Value.Box.Put(&box.Entry{Key: "Hacked", Value: box.Bool(true)}) // returned copy must not alias
	if e2, _, _ := rs.Get(addr(fPlayers, "Player", 1)); e2.Value.Box.Get("Hacked", 0, false) != nil {
		t.Fatal("Get aliased engine memory")
	}
	if _, ok, _ := rs.Get(addr(fPlayers, "Player", 99)); ok {
		t.Fatal("phantom record")
	}
	if _, ok, err := rs.Get(addr("nofile.red-xai.db", "P", 1)); ok || err != nil {
		t.Fatalf("missing file: %v %v", ok, err)
	}
	var seen []int64
	rs.Scan(fPlayers, func(a link.Addr, e *box.Entry) bool { seen = append(seen, a.ID); return true })
	if len(seen) != 2 || seen[0] != 1 || seen[1] != 2 {
		t.Fatalf("scan order: %v", seen)
	}
	if ok, err := rs.Delete(addr(fPlayers, "Player", 2)); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if ok, err := rs.Delete(addr(fPlayers, "Player", 2)); ok || err != nil {
		t.Fatalf("double delete should be (false,nil): %v %v", ok, err)
	}
	if ok, err := rs.Delete(addr("nofile.red-xai.db", "P", 1)); ok || err != nil {
		t.Fatalf("delete in missing file: %v %v", ok, err)
	}
	if got := rs.Files(); len(got) != 1 || got[0] != fPlayers {
		t.Fatalf("files: %v", got)
	}

	// TTL markers on entries are honoured and expired records are hidden
	ent := &box.Entry{Key: "Lobby", Attrs: []box.Attr{{Name: "ID", Value: box.Int(5)}}, Value: box.BoxValue(box.NewBox())}
	if err := rs.Put(addr(fLobby, "Lobby", 5), ent); !errors.Is(err, ErrTTLRequired) {
		t.Fatalf("qs record without TTL: %v", err)
	}
	ent.TTL = box.TTL{Kind: box.TTLFixed, Seconds: 30}
	if err := rs.Put(addr(fLobby, "Lobby", 5), ent); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := rs.Get(addr(fLobby, "Lobby", 5)); !ok {
		t.Fatal("lobby missing")
	}
	clk.Advance(31 * time.Second)
	if _, ok, _ := rs.Get(addr(fLobby, "Lobby", 5)); ok {
		t.Fatal("expired record visible")
	}
	n := 0
	rs.Scan(fLobby, func(link.Addr, *box.Entry) bool { n++; return true })
	if n != 0 {
		t.Fatal("expired record scanned")
	}
	db.crash()
}

func TestLinkEngineOverRealEngines(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	putDoc(t, db, fGuilds, `{Guild[ID=10]= [ {Name[]} = "Red", {Members[]} = 3 ]};`)
	putDoc(t, db, fInv, `{Inv[ID=100]= [ {Gold[]} = 500 ]};`)
	putDoc(t, db, fPlayers, `{Player[ID=1]= [
	    {Name[]} = "Josh",
	    {Guild[]} = "players.red-xai.db":Player[ID=1] @> "guilds.red-xai.db":Guild[ID=10],
	    {Bag[]}   = "players.red-xai.db":Player[ID=1] @!> "inventories.red-xai.db":Inv[ID=100]
	]};`)
	le := db.NewLinkEngine(link.Options{})

	// read link across files
	ref := box.Ref{File: fGuilds, Segs: []box.Seg{{Name: "Guild", ID: 10, HasID: true}, {Name: "Name"}}}
	v, err := le.Resolve(ref)
	if err != nil || v.Str != "Red" {
		t.Fatalf("resolve: %v %v", v, err)
	}
	exp, err := le.Expand(addr(fPlayers, "Player", 1), 3)
	if err != nil {
		t.Fatal(err)
	}
	if g := exp.Entry.Value.Box.Get("Guild", 0, false); g == nil || g.Value.Kind != box.KindBox {
		t.Fatalf("guild link not expanded: %+v", exp.Entry)
	}
	// dangling detection
	db.Records().Delete(addr(fGuilds, "Guild", 10))
	d, err := le.CheckDangling(fPlayers)
	if err != nil || len(d) != 1 {
		t.Fatalf("dangling: %v %v", d, err)
	}
	// cascade delete through the real engines: deleting the player deletes the inventory
	res, err := le.Delete(addr(fPlayers, "Player", 1), link.DeleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 2 {
		t.Fatalf("cascade deleted %v", res.Deleted)
	}
	for _, a := range []link.Addr{addr(fPlayers, "Player", 1), addr(fInv, "Inv", 100)} {
		if _, ok, _ := db.Records().Get(a); ok {
			t.Fatalf("%s survived the cascade", a)
		}
	}
	db.crash()
	// ...and the cascade is durable
	db2 := open(t, dir, Config{})
	defer db2.Close()
	if _, ok, _ := db2.Records().Get(addr(fInv, "Inv", 100)); ok {
		t.Fatal("cascade lost on recovery")
	}
}

func TestPulseEngineOverRealEnginesAndBridge(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	t0 := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	clk := pulse.NewManualClock(t0)
	bus := pulse.NewBus()
	pe := db.NewPulseEngine(pulse.Config{Clock: clk, Publisher: bus})
	defer pe.Close()
	if err := pe.LoadRules(`
{PlayerDeath[ON="players.red-xai.db:Player.Health"]} = [
    {When[]}      = "<= 0",
    {SetTarget[]} = "Player.IsDead = TRUE",
    {Timer[]}     = [ {Name[]} = "Respawn", {Seconds[]} = 10,
                      {Do[]} = ;["set Health = 100", "set IsDead = FALSE", "emit player.respawned"]: ],
    {Emit[]}      = "player.died"
];`); err != nil {
		t.Fatal(err)
	}
	ev, cancel := bus.Subscribe("*", 64)
	defer cancel()
	stopBridge := db.BridgeChanges("*", bus)
	defer stopBridge()

	rec, _ := box.ParseDocument(`{Player[ID=1]= [ {Health[]} = 100, {IsDead[]} = FALSE ]};`)
	pl := rec.Entries()[0]
	if _, err := pe.Put(addr(fPlayers, "Player", 1), pl); err != nil {
		t.Fatal(err)
	}
	res, err := pe.Set(addr(fPlayers, "Player", 1), "Health", box.Number(0))
	if err != nil || len(res.Fired) != 1 || len(res.Errors) != 0 {
		t.Fatalf("rule did not fire: %+v %v", res, err)
	}
	// the rule's write is in the REAL engine, durably
	if v, err := db.Get(fPlayers + ":Player[ID=1].IsDead"); err != nil || !v.Bool {
		t.Fatalf("IsDead not set in storage: %v %v", v, err)
	}
	clk.Advance(10 * time.Second) // timer fires: respawn
	if v, _ := db.Get(fPlayers + ":Player[ID=1].Health"); v.Num != 100 {
		t.Fatalf("respawn timer did not restore health: %v", v)
	}
	if v, _ := db.Get(fPlayers + ":Player[ID=1].IsDead"); v.Bool {
		t.Fatal("IsDead not cleared")
	}
	// a direct (non-rule) storage write reaches bus subscribers through the bridge
	if err := db.Set(fWorld+":Counter[ID=1].Kills", box.Int(7), SetOpts{}); err != nil {
		t.Fatal(err)
	}
	topics := map[string]int{}
	var change *pulse.Event
	deadline := time.After(2 * time.Second)
	for change == nil {
		select {
		case e := <-ev:
			topics[e.Topic]++
			if e.Kind == pulse.KindChange && e.Addr.File == fWorld {
				ee := e
				change = &ee
			}
		case <-deadline:
			t.Fatalf("no change event through the bridge; saw %v", topics)
		}
	}
	if change.Addr.Box != "Counter" || change.Addr.ID != 1 || change.Data == nil {
		t.Fatalf("bridge event: %+v", change)
	}
	if topics["player.died"] != 1 || topics["player.respawned"] != 1 {
		t.Fatalf("rule events missing: %v", topics)
	}
	db.crash()
	db2 := open(t, dir, Config{})
	defer db2.Close()
	if v, _ := db2.Get(fPlayers + ":Player[ID=1].Health"); v.Num != 100 {
		t.Fatal("rule-driven writes not durable")
	}
}
