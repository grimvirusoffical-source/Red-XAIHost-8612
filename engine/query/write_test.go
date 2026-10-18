package query

import (
	"context"
	"errors"
	"strings"
	"testing"

	"redx/box"
	"redx/engine/link"
)

func TestJoin(t *testing.T) {
	f := newFixture(t, fixOpt{links: true})
	r := f.q(t, "SELECT p.Name, g.Title, g.Rank FROM players.Player p JOIN p.Guild g ORDER BY p._id")
	eq(t, col(r, "p.Name"), "ann", "bob", "dee") // 3 has no link, 4 dangles
	eq(t, col(r, "g.Title"), "Red Order", "Red Order", "Red Order")
	r = f.q(t, "SELECT p.Name, g.Title FROM players.Player AS p LEFT JOIN p.Guild AS g ORDER BY p._id")
	eq(t, col(r, "p.Name"), "ann", "bob", "bobby", "cy", "dee")
	eq(t, col(r, "g.Title"), "Red Order", "Red Order", "NELL", "NELL", "Red Order")
	// filter on the joined side; pseudo fields of the joined record
	r = f.q(t, "SELECT p.Name, g._id FROM players.Player p JOIN p.Guild g WHERE g.Rank >= 3 AND p.Level > 5")
	eq(t, col(r, "p.Name"), "bob")
	eq(t, col(r, "g._id"), "10")
	// chained join: guild -> owner player
	r = f.q(t, "SELECT p.Name, o.Name AS owner FROM players.Player p JOIN p.Guild g JOIN g.Owner o WHERE p._id = 1")
	eq(t, col(r, "owner"), "bob")
	// alias.* expands the joined record
	r = f.q(t, "SELECT g.* FROM players.Player p JOIN p.Guild g WHERE p._id = 1")
	eq(t, r.Columns, "g.Title", "g.Owner", "g.Rank")
	// ORDER BY and LIMIT apply to joined rows
	r = f.q(t, "SELECT p.Name FROM players.Player p JOIN p.Guild g ORDER BY p.Name DESC LIMIT 1")
	eq(t, col(r, "p.Name"), "dee")
	// no link engine: refused with a clear error
	g := newFixture(t, fixOpt{})
	if err := g.qerr(t, "SELECT p.Name FROM players.Player p JOIN p.Guild g"); !errors.Is(err, ErrNoLinks) {
		t.Fatal(err)
	}
	// bad join syntax
	for _, q := range []string{
		"SELECT * FROM players.Player p JOIN Guild g",
		"SELECT * FROM players.Player p JOIN x.Guild g",
		"SELECT * FROM players.Player p JOIN p.Guild",
		"SELECT * FROM players.Player p JOIN p._id g",
		"SELECT * FROM players.Player p JOIN p.Guild p",
	} {
		if err := f.qerr(t, q); !errors.Is(err, ErrSyntax) {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestJoinArrayOfLinksAndCascadeLinkNotFollowed(t *testing.T) {
	f := newFixture(t, fixOpt{links: true})
	must(t, link.PutDoc(f.mem, "teams.red-xai.db", `
{Team[1]} = [ {Name[]} = "alpha", {Members[]} = ;["x":Y[ID=1] @> "players.red-xai.db":Player[ID=1], "x":Y[ID=1] @> "players.red-xai.db":Player[ID=2], "x":Y[ID=1] @> "players.red-xai.db":Player[ID=77]]:,
   {Boss[]} = "teams.red-xai.db":Team[ID=1].Boss @!> "players.red-xai.db":Player[ID=1] ];
`))
	r := f.q(t, "SELECT t.Name, m.Name AS member FROM teams.Team t JOIN t.Members m")
	eq(t, col(r, "member"), "ann", "bob")
	// a @!> link is a cascade link, not a read link
	r = f.q(t, "SELECT m.Name FROM teams.Team t JOIN t.Boss m")
	eq(t, col(r, "m.Name"))
}

func TestInsert(t *testing.T) {
	f := newFixture(t, fixOpt{})
	r := f.q(t, "INSERT INTO players.Player (_id, Name, Level, Stats.Gold) VALUES (10, 'eve', 3, 7), (11, ?, ?, NELL)", "fay", 4)
	if r.Affected != 2 || r.Kind != "insert" {
		t.Fatal(r.Affected)
	}
	got := f.q(t, "SELECT Name, Level, Stats.Gold AS g FROM players.Player WHERE _id >= 10 ORDER BY _id")
	eq(t, col(got, "Name"), "eve", "fay")
	eq(t, col(got, "g"), "7", "NELL")
	// exists
	if err := f.qerr(t, "INSERT INTO players.Player (_id, Name) VALUES (10, 'dup')"); !errors.Is(err, ErrExists) {
		t.Fatal(err)
	}
	// duplicate inside one statement; nothing is written
	if err := f.qerr(t, "INSERT INTO players.Player (_id, Name) VALUES (30, 'a'), (30, 'b')"); !errors.Is(err, ErrExists) {
		t.Fatal(err)
	}
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE _id = 30"), "_id"))
	// partial-failure safety: the exists check covers every row before the first write
	if err := f.qerr(t, "INSERT INTO players.Player (_id, Name) VALUES (31, 'a'), (10, 'b')"); !errors.Is(err, ErrExists) {
		t.Fatal(err)
	}
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE _id = 31"), "_id"))
	for _, q := range []string{
		"INSERT INTO players.Player (_id, _id) VALUES (1, 2)",
		"INSERT INTO players.Player (Name) VALUES (Name)", // no field references
		"INSERT INTO players.Player (Name) VALUES (1, 2)", // arity
		"INSERT INTO players.Player (_box) VALUES ('x')",  // read-only
		"INSERT INTO players.Player (Stats, Stats.Gold) VALUES (1, 2)",
		"INSERT INTO players.Player (\"bad name\") VALUES (1)",
		"INSERT INTO players.Player (_id) VALUES ('x')",
		"INSERT INTO players.Player (_id) VALUES (1.5)",
	} {
		if err := f.qerr(t, q); err == nil {
			t.Fatal(q)
		}
	}
	// a record without an ID
	f.q(t, "INSERT INTO players.Config (Motd) VALUES ('hi')")
	eq(t, col(f.q(t, "SELECT Motd FROM players.Config"), "Motd"), "hi")
	// the data survive a round trip through the Box text format
	e, ok, _ := f.mem.Get(addrOf(playersFile, "Player", 10))
	if !ok {
		t.Fatal("missing")
	}
	if _, err := box.ParseValue(e.Value.String()); err != nil {
		t.Fatal(err)
	}
}

func TestUpdate(t *testing.T) {
	f := newFixture(t, fixOpt{})
	r := f.q(t, "UPDATE players.Player SET Level = Level + 1, Stats.Gold = Stats.Gold * 2 WHERE Level >= 7 AND Level < 100")
	if r.Affected != 3 {
		t.Fatalf("affected %d", r.Affected) // bob, bobby, cy (dee has Level "seven")
	}
	got := f.q(t, "SELECT Level, Stats.Gold AS g FROM players.Player ORDER BY _id")
	eq(t, col(got, "Level"), "5", "13", "8", "8", "seven")
	eq(t, col(got, "g"), "100", "10", "500", "0", "NELL")
	// SET sees the old values: swapping works
	f.q(t, "UPDATE players.Player SET Level = Stats.Gold, Stats.Gold = Level WHERE _id = 1")
	got = f.q(t, "SELECT Level, Stats.Gold AS g FROM players.Player WHERE _id = 1")
	eq(t, col(got, "Level"), "100")
	eq(t, col(got, "g"), "5")
	// creating a new nested field; no-op updates are not counted
	r = f.q(t, "UPDATE players.Player SET Meta.Seen = 1 WHERE _id = 2")
	if r.Affected != 1 {
		t.Fatal(r.Affected)
	}
	r = f.q(t, "UPDATE players.Player SET Meta.Seen = 1 WHERE _id = 2")
	if r.Affected != 0 {
		t.Fatal("unchanged record counted", r.Affected)
	}
	// SET NELL stores NELL
	f.q(t, "UPDATE players.Player SET Class = NELL WHERE _id = 1")
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE Class IS NELL"), "_id"), "1", "4")
	// parameters
	f.q(t, "UPDATE players.Player SET Name = ? WHERE Name = ?", "it's \"quoted\"", "ann")
	eq(t, col(f.q(t, "SELECT Name FROM players.Player WHERE _id = 1"), "Name"), `it's "quoted"`)
	// type conflict: Level is a number, not a box
	if err := f.qerr(t, "UPDATE players.Player SET Level.Sub = 1 WHERE _id = 1"); !errors.Is(err, ErrType) {
		t.Fatal(err)
	}
	for _, q := range []string{
		"UPDATE players.Player SET _id = 5 WHERE _id = 1",
		"UPDATE players.Player SET Level = 1, Level = 2 WHERE _id = 1",
		"UPDATE players.Player SET ID = 1 WHERE _id = 1",
	} {
		if err := f.qerr(t, q); err == nil {
			t.Fatal(q)
		}
	}
	// the record's key and ID are untouched
	if _, ok, _ := f.mem.Get(addrOf(playersFile, "Player", 1)); !ok {
		t.Fatal("record lost")
	}
}

func TestDeleteAndCascade(t *testing.T) {
	f := newFixture(t, fixOpt{links: true})
	must(t, link.PutDoc(f.mem, "inv.red-xai.db", `
{Inv[100]} = [ {Items[]} = 3 ];
{Inv[101]} = [ {Items[]} = 4 ];
{Inv[102]} = [ {Items[]} = 5 ];
`))
	// player 1 owns Inv 100 (cascade link), 100 owns 101 (chain); 102 is not linked
	put := func(file, boxName string, id int64, mutate func(b *box.Box)) {
		e, _, _ := f.mem.Get(addrOf(file, boxName, id))
		mutate(e.Value.Box)
		must(t, f.mem.Put(addrOf(file, boxName, id), e))
	}
	cascade := func(from link.Addr, to link.Addr) box.Value {
		l, err := link.ParseLink(`"` + from.File + `":` + from.Box + `[ID=` + itoa(from.ID) + `] @!> "` + to.File + `":` + to.Box + `[ID=` + itoa(to.ID) + `]`)
		must(t, err)
		return box.LinkValue(l)
	}
	put(playersFile, "Player", 1, func(b *box.Box) {
		b.Put(box.NewEntry("Inv", nil, cascade(addrOf(playersFile, "Player", 1), addrOf("inv.red-xai.db", "Inv", 100))))
	})
	put("inv.red-xai.db", "Inv", 100, func(b *box.Box) {
		b.Put(box.NewEntry("Next", nil, cascade(addrOf("inv.red-xai.db", "Inv", 100), addrOf("inv.red-xai.db", "Inv", 101))))
	})
	r := f.q(t, "DELETE FROM players.Player WHERE Name = 'ann'")
	if r.Affected != 1 || len(r.Cascaded) != 2 {
		t.Fatalf("affected %d cascaded %v", r.Affected, r.Cascaded)
	}
	eq(t, col(f.q(t, "SELECT _id FROM inv.Inv"), "_id"), "102")
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE _id = 1"), "_id"))
	// plain deletes with an IN / LIKE
	r = f.q(t, "DELETE FROM players.Player WHERE Name LIKE 'bob%' OR _id IN (4, 99)")
	if r.Affected != 3 {
		t.Fatal(r.Affected)
	}
	eq(t, col(f.q(t, "SELECT _id FROM players.Player"), "_id"), "5")
	// deleting nothing is fine
	r = f.q(t, "DELETE FROM players.Player WHERE Name = 'nobody'")
	if r.Affected != 0 {
		t.Fatal(r.Affected)
	}
	// WHERE TRUE deletes every record of the box (and only that box)
	f.q(t, "DELETE FROM players.Player WHERE TRUE")
	eq(t, col(f.q(t, "SELECT _id FROM players.Player"), "_id"))
	eq(t, col(f.q(t, "SELECT _id FROM players.Npc"), "_id"), "1")
}

func itoa(i int64) string { return strings.TrimSpace(strings.Join([]string{box.Int(i).String()}, "")) }

func TestDeleteWithoutLinkEngineRefusesCascadeRecords(t *testing.T) {
	f := newFixture(t, fixOpt{})
	e, _, _ := f.mem.Get(addrOf(playersFile, "Player", 1))
	l, _ := link.ParseLink(`"players.red-xai.db":Player[ID=1] @!> "guilds.red-xai.db":Guild[ID=10]`)
	e.Value.Box.Put(box.NewEntry("Own", nil, box.LinkValue(l)))
	must(t, f.mem.Put(addrOf(playersFile, "Player", 1), e))
	if err := f.qerr(t, "DELETE FROM players.Player WHERE _id = 1"); !errors.Is(err, ErrNoLinks) {
		t.Fatal(err)
	}
	if _, ok, _ := f.mem.Get(addrOf(playersFile, "Player", 1)); !ok {
		t.Fatal("must not delete")
	}
	f.q(t, "DELETE FROM players.Player WHERE _id = 2") // no cascade links: fine
}

func TestContextCancelled(t *testing.T) {
	f := newFixture(t, fixOpt{})
	for i := 0; i < 600; i++ {
		must(t, f.mem.Put(addrOf(playersFile, "Player", int64(100+i)), mkEntry(t, "Player", int64(100+i))))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.db.Exec(ctx, anyone, "SELECT _id FROM players.Player WHERE Name = 'x'"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
