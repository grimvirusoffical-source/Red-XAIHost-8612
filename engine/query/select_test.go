package query

import (
	"strings"
	"testing"

	"redx/engine/link"
)

func TestSelectBasics(t *testing.T) {
	f := newFixture(t, fixOpt{})
	r := f.q(t, "SELECT Name, Level FROM players.Player ORDER BY _id")
	eq(t, r.Columns, "Name", "Level")
	eq(t, col(r, "Name"), "ann", "bob", "bobby", "cy", "dee")
	eq(t, col(r, "Level"), "5", "12", "7", "7", "seven")
	if r.Access != "scan" {
		t.Fatal(r.Access)
	}
}

func TestSelectDefaultOrderIsDeterministic(t *testing.T) {
	f := newFixture(t, fixOpt{})
	r := f.q(t, "SELECT _id FROM players.Player")
	eq(t, col(r, "_id"), "1", "2", "3", "4", "5")
	if len(r.Addrs) != 5 || r.Addrs[0].ID != 1 {
		t.Fatalf("addrs %v", r.Addrs)
	}
}

func TestWhereOperators(t *testing.T) {
	f := newFixture(t, fixOpt{})
	ids := func(q string, args ...interface{}) []string {
		return col(f.q(t, q, args...), "_id")
	}
	eq(t, ids("SELECT _id FROM players.Player WHERE Level = 7"), "3", "4")
	eq(t, ids("SELECT _id FROM players.Player WHERE Level != 7"), "1", "2", "5") // "seven" != 7, missing != 7
	eq(t, ids("SELECT _id FROM players.Player WHERE Level <> 7"), "1", "2", "5")
	eq(t, ids("SELECT _id FROM players.Player WHERE Level > 5"), "2", "3", "4")
	eq(t, ids("SELECT _id FROM players.Player WHERE Level >= 7 AND Level < 12"), "3", "4")
	eq(t, ids("SELECT _id FROM players.Player WHERE Level < 6 OR Name = 'cy'"), "1", "4")
	eq(t, ids("SELECT _id FROM players.Player WHERE NOT (Level >= 7)"), "1", "5") // NELL/type mismatch is false for <, so NOT is true
	eq(t, ids("SELECT _id FROM players.Player WHERE Level IN (5, 12)"), "1", "2")
	eq(t, ids("SELECT _id FROM players.Player WHERE Level NOT IN (5, 12, 7)"), "5")
	eq(t, ids("SELECT _id FROM players.Player WHERE Name LIKE 'bob%'"), "2", "3")
	eq(t, ids("SELECT _id FROM players.Player WHERE Name NOT LIKE 'bob%'"), "1", "4", "5")
	eq(t, ids("SELECT _id FROM players.Player WHERE Name LIKE 'cy'"), "4")
	eq(t, ids("SELECT _id FROM players.Player WHERE Level BETWEEN 6 AND 12"), "2", "3", "4")
	eq(t, ids("SELECT _id FROM players.Player WHERE Level NOT BETWEEN 6 AND 12"), "1", "5")
	eq(t, ids("SELECT _id FROM players.Player WHERE Stats.Gold >= 100"), "1", "3")
	eq(t, ids("SELECT _id FROM players.Player WHERE Stats.Gold * 2 = 200"), "1")
	eq(t, ids("SELECT _id FROM players.Player WHERE Name = ?", "bob"), "2")
	eq(t, ids("SELECT _id FROM players.Player WHERE Level = ?", 12), "2")
	eq(t, ids("SELECT _id FROM players.Player WHERE ? < Level", 7), "2")
	eq(t, ids("SELECT _id FROM players.Player WHERE 7 = Level"), "3", "4")
}

func TestNellSemantics(t *testing.T) {
	f := newFixture(t, fixOpt{})
	ids := func(q string, args ...interface{}) []string { return col(f.q(t, q, args...), "_id") }
	// Player 3 has no Guild, 4 and 5 have no Class... fields that do not exist read as NELL
	eq(t, ids("SELECT _id FROM players.Player WHERE Class IS NELL"), "4")
	eq(t, ids("SELECT _id FROM players.Player WHERE Class IS NOT NELL"), "1", "2", "3", "5")
	eq(t, ids("SELECT _id FROM players.Player WHERE Class = NELL"), "4")
	eq(t, ids("SELECT _id FROM players.Player WHERE Nope IS NELL"), "1", "2", "3", "4", "5")
	eq(t, ids("SELECT _id FROM players.Player WHERE Nope = 1"))
	eq(t, ids("SELECT _id FROM players.Player WHERE Nope.Deeper.Still IS NELL"), "1", "2", "3", "4", "5")
	eq(t, ids("SELECT _id FROM players.Player WHERE Nope < 5 OR Nope >= 5"))
	// a missing field in a select list is a NELL column, not an error
	r := f.q(t, "SELECT Name, Class FROM players.Player WHERE _id = 4")
	eq(t, col(r, "Class"), "NELL")
	// arithmetic with NELL or a string is NELL, division by zero is NELL
	r = f.q(t, "SELECT Level + 1 AS a, Level / 0 AS b, Class + 1 AS c FROM players.Player WHERE _id = 1")
	eq(t, col(r, "a"), "6")
	eq(t, col(r, "b"), "NELL")
	eq(t, col(r, "c"), "NELL")
	// ordering across kinds is total: NELL < number < string
	r = f.q(t, "SELECT _id FROM players.Player ORDER BY Level, _id")
	eq(t, col(r, "_id"), "1", "3", "4", "2", "5")
	r = f.q(t, "SELECT _id FROM players.Player ORDER BY Class DESC, _id")
	eq(t, col(r, "_id"), "2", "5", "1", "3", "4") // warrior, healer, Mage, MAGE, NELL
	// bare boolean field as a condition
	f.q(t, "UPDATE players.Player SET Active = TRUE WHERE _id = 2")
	eq(t, ids("SELECT _id FROM players.Player WHERE Active"), "2")
	eq(t, ids("SELECT _id FROM players.Player WHERE Active = TRUE"), "2")
	eq(t, ids("SELECT _id FROM players.Player WHERE NOT Active"), "1", "3", "4", "5")
}

func TestOrderLimitOffset(t *testing.T) {
	f := newFixture(t, fixOpt{})
	r := f.q(t, "SELECT Name FROM players.Player WHERE Level >= 5 ORDER BY Level DESC, Name ASC LIMIT 3")
	eq(t, col(r, "Name"), "bob", "bobby", "cy")
	r = f.q(t, "SELECT Name FROM players.Player ORDER BY Name DESC LIMIT 2 OFFSET 1")
	eq(t, col(r, "Name"), "cy", "bobby")
	r = f.q(t, "SELECT Name FROM players.Player ORDER BY Name OFFSET 3")
	eq(t, col(r, "Name"), "cy", "dee")
	r = f.q(t, "SELECT Name FROM players.Player ORDER BY Name LIMIT ? OFFSET ?", 2, 1)
	eq(t, col(r, "Name"), "bob", "bobby")
	r = f.q(t, "SELECT Name FROM players.Player LIMIT 0")
	if len(r.Rows) != 0 {
		t.Fatal("limit 0")
	}
	r = f.q(t, "SELECT Name FROM players.Player OFFSET 100")
	if len(r.Rows) != 0 {
		t.Fatal("offset past the end")
	}
	// ORDER BY a select alias
	r = f.q(t, "SELECT Name, Stats.Gold AS gold FROM players.Player WHERE Stats.Gold IS NOT NELL ORDER BY gold DESC")
	eq(t, col(r, "Name"), "bobby", "ann", "bob", "cy")
	// LIMIT without ORDER BY keeps the deterministic address order
	r = f.q(t, "SELECT _id FROM players.Player LIMIT 2")
	eq(t, col(r, "_id"), "1", "2")
}

func TestStarAndNested(t *testing.T) {
	f := newFixture(t, fixOpt{})
	r := f.q(t, "SELECT * FROM players.Player WHERE _id = 4")
	eq(t, r.Columns, "Name", "Level", "Stats", "Guild")
	r = f.q(t, "SELECT * FROM players.Player WHERE _id IN (3, 4)")
	// schema-less: columns are the union, missing ones are NELL
	eq(t, r.Columns, "Name", "Level", "Class", "Bio", "Stats", "Guild")
	eq(t, col(r, "Class"), "MAGE", "NELL")
	eq(t, col(r, "Guild"), "NELL", `"players.red-xai.db":Player[ID=4].Guild @> "guilds.red-xai.db":Guild[ID=99]`)
	// a nested box comes back as a box value
	r = f.q(t, "SELECT Stats FROM players.Player WHERE _id = 1")
	if r.Rows[0][0].Box == nil || r.Rows[0][0].Box.Len() != 2 {
		t.Fatalf("%v", r.Rows[0][0])
	}
	// pseudo fields
	r = f.q(t, "SELECT _file, _box, _id FROM players.Player WHERE _id = 2")
	eq(t, col(r, "_file"), playersFile)
	eq(t, col(r, "_box"), "Player")
	// whole-file query sees every box
	r = f.q(t, `SELECT _box, _id FROM "players.red-xai.db" WHERE Name = 'ann'`)
	eq(t, col(r, "_box"), "Npc", "Player") // Npc < Player by address order
	// quoted identifiers and qualification
	r = f.q(t, `SELECT "Name", Player.Level FROM "players.red-xai.db"."Player" WHERE Player._id = 2`)
	eq(t, r.Columns, "Name", "Player.Level")
}

func TestFileResolution(t *testing.T) {
	f := newFixture(t, fixOpt{})
	f.q(t, `SELECT _id FROM "players.red-xai.db".Player`)
	f.q(t, `SELECT _id FROM players.Player`)
	err := f.qerr(t, "SELECT _id FROM nosuch.Player")
	if !strings.Contains(err.Error(), "unknown file") {
		t.Fatal(err)
	}
	// ambiguity
	f.mem.Put(link.Addr{File: "players.red-xai-db-qs", Box: "X", ID: 1, HasID: true}, mkEntry(t, "X", 1))
	err = f.qerr(t, "SELECT _id FROM players.Player")
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatal(err)
	}
	f.q(t, `SELECT _id FROM "players.red-xai.db".Player`)
}

func TestExplicitErrors(t *testing.T) {
	f := newFixture(t, fixOpt{})
	for _, q := range []string{
		"UPDATE players.Player SET Level = 1",
		"DELETE FROM players.Player",
		"INSERT INTO players (_id) VALUES (1)",
	} {
		err := f.qerr(t, q)
		if err == nil {
			t.Fatal(q)
		}
	}
	if err := f.qerr(t, "SELECT _id FROM players.Player WHERE Name LIKE '%bob'"); !strings.Contains(err.Error(), "trailing %") {
		t.Fatal(err)
	}
	if err := f.qerr(t, "SELECT _id FROM players.Player WHERE Name LIKE ?", 5); !strings.Contains(err.Error(), "string") {
		t.Fatal(err)
	}
	if err := f.qerr(t, "SELECT _id FROM players.Player WHERE Name LIKE 'a%b%'"); err == nil {
		t.Fatal("two %")
	}
	if err := f.qerr(t, "SELECT _id FROM players.Player LIMIT ?", -1); err == nil {
		t.Fatal("negative limit")
	}
	if err := f.qerr(t, "SELECT _id FROM players.Player LIMIT ?", "x"); err == nil {
		t.Fatal("string limit")
	}
}
