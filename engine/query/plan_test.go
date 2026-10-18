package query

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func planOf(t *testing.T, f *fixture, q string, args ...interface{}) string {
	t.Helper()
	res, err := f.db.Explain(context.Background(), anyone, q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return strings.Join(res.Plan, "\n")
}

func TestExplainShowsPlan(t *testing.T) {
	f := newFixture(t, fixOpt{indexes: true, links: true})
	cases := []struct {
		q    string
		want []string
	}{
		{"SELECT * FROM players.Player WHERE Name = 'bob'", []string{"INDEX ByName (exact) on Name: (Name = 'bob')"}},
		{"SELECT * FROM players.Player WHERE Name = ? AND Level > 3", []string{"INDEX ByName (exact)", "filter: (Name = 'bob') AND (Level > 3)"}},
		{"SELECT * FROM players.Player WHERE Name LIKE 'bo%'", []string{"INDEX ByNamePfx (prefix)"}},
		{"SELECT * FROM players.Player WHERE Level >= 5 AND Level < 10", []string{"INDEX ByLevel (range) on Level", "Level >= 5"}},
		{"SELECT * FROM players.Player WHERE Level BETWEEN 5 AND 10", []string{"INDEX ByLevel (range)"}},
		{"SELECT * FROM players.Player WHERE Stats.Gold > 100", []string{"INDEX ByLevel (range) on Stats.Gold"}},
		{"SELECT * FROM players.Player WHERE MATCH(Bio, 'red dragon')", []string{"INDEX ByBio (text-all)"}},
		{"SELECT * FROM players.Player WHERE MATCH_ANY(Bio, 'red blue')", []string{"INDEX ByBio (text-any)"}},
		{"SELECT * FROM players.Player WHERE _id = 3", []string{"PRIMARY KEY LOOKUP"}},
		{"SELECT * FROM players.Player WHERE _id IN (1, 2)", []string{"PRIMARY KEY LOOKUP", "2 record(s)"}},
		{"SELECT * FROM players.Player WHERE Name IN ('ann', 'bob')", []string{"INDEX ByName (in)"}},
		{"SELECT * FROM players.Player WHERE Name = 'ann' OR Level > 11", []string{"UNION of 2 lookups"}},
		{"SELECT * FROM players.Player WHERE Name = 'ann' OR Class = 'x'", []string{"UNION of 2 lookups", "ByClass"}},
		// not indexable: falls back to a scan
		{"SELECT * FROM players.Player WHERE Name != 'bob'", []string{"FULL SCAN"}},
		{"SELECT * FROM players.Player WHERE Level + 1 = 6", []string{"FULL SCAN"}},
		{"SELECT * FROM players.Player WHERE Name = 'ann' OR Tags = 'x'", []string{"FULL SCAN"}},
		{"SELECT * FROM players.Player WHERE NOT (Name = 'ann')", []string{"FULL SCAN"}},
		{"SELECT * FROM players.Player WHERE Name = NELL", []string{"FULL SCAN"}},
		{"SELECT * FROM players.Player WHERE Level > 'x'", []string{"FULL SCAN"}},
		{`SELECT * FROM "players.red-xai.db" WHERE Name = 'ann'`, []string{"FULL SCAN"}}, // index is limited to Player
		{"SELECT * FROM players.Player WHERE Level LIKE 'x%'", []string{"FULL SCAN"}},
		{"SELECT * FROM players.Player", []string{"FULL SCAN"}},
		// exact beats range when both are possible
		{"SELECT * FROM players.Player WHERE Level > 3 AND Name = 'bob'", []string{"INDEX ByName (exact)"}},
		// joins and write statements
		{"SELECT p.Name, g.Title FROM players.Player p LEFT JOIN p.Guild g WHERE p.Name = 'ann' AND g.Title LIKE 'Red%'",
			[]string{"INDEX ByName", "LEFT JOIN p.Guild AS g via @>", "post-join filter: g.Title LIKE 'Red%'"}},
		{"UPDATE players.Player SET Level = 1 WHERE Name = 'ann'", []string{"UPDATE", "INDEX ByName"}},
		{"DELETE FROM players.Player WHERE _id = 1", []string{"DELETE", "PRIMARY KEY", "cascade: follows @!>"}},
		{"INSERT INTO players.Player (_id, Name) VALUES (9, 'x')", []string{"INSERT 1 row(s)"}},
	}
	for _, c := range cases {
		args := []interface{}{}
		if strings.Contains(c.q, "?") {
			args = append(args, "bob")
		}
		p := planOf(t, f, c.q, args...)
		for _, w := range c.want {
			if !strings.Contains(p, w) {
				t.Errorf("%s\nplan:\n%s\nmissing %q", c.q, p, w)
			}
		}
	}
	// EXPLAIN as a statement gives the same text and runs nothing
	before := f.cs.puts.Load() + f.cs.dels.Load()
	r := f.q(t, "EXPLAIN DELETE FROM players.Player WHERE _id = 1")
	if r.Kind != "explain" || len(r.Plan) == 0 || f.cs.puts.Load()+f.cs.dels.Load() != before {
		t.Fatal("EXPLAIN must not execute")
	}
	if r.Rows[0][0].Str != r.Plan[0] || r.Columns[0] != "plan" {
		t.Fatal("rows mirror plan")
	}
}

func TestIndexPlanAvoidsScan(t *testing.T) {
	f := newFixture(t, fixOpt{indexes: true})
	f.cs.scans.Store(0)
	r := f.q(t, "SELECT _id FROM players.Player WHERE Name = 'bob'")
	eq(t, col(r, "_id"), "2")
	if f.cs.scans.Load() != 0 || r.Access != "index:ByName" || r.Scanned != 1 {
		t.Fatalf("scans=%d access=%s scanned=%d", f.cs.scans.Load(), r.Access, r.Scanned)
	}
	r = f.q(t, "SELECT _id FROM players.Player WHERE Name != 'bob'")
	if f.cs.scans.Load() != 1 || r.Access != "scan" {
		t.Fatalf("expected a scan: %d %s", f.cs.scans.Load(), r.Access)
	}
	r = f.q(t, "SELECT _id FROM players.Player WHERE _id = 3")
	eq(t, col(r, "_id"), "3")
	if r.Access != "pk" || r.Scanned != 1 {
		t.Fatal(r.Access)
	}
	// a folded index finds a superset; the residual filter keeps scan semantics
	r = f.q(t, "SELECT _id FROM players.Player WHERE Class = 'Mage'")
	eq(t, col(r, "_id"), "1")
	if r.Access != "index:ByClass" || r.Scanned != 2 {
		t.Fatalf("%s scanned %d", r.Access, r.Scanned)
	}
	// text search
	r = f.q(t, "SELECT _id FROM players.Player WHERE MATCH(Bio, 'red DRAGON')")
	eq(t, col(r, "_id"), "1", "3")
	r = f.q(t, "SELECT _id FROM players.Player WHERE MATCH_ANY(Bio, 'blue slayer')")
	eq(t, col(r, "_id"), "1", "2")
	// without an index MATCH scans with the same tokenizer
	g := newFixture(t, fixOpt{})
	r = g.q(t, "SELECT _id FROM players.Player WHERE MATCH(Bio, 'red DRAGON')")
	eq(t, col(r, "_id"), "1", "3")
}

func TestStaleIndexEntriesAreIgnored(t *testing.T) {
	f := newFixture(t, fixOpt{indexes: true})
	// remove a record behind the index's back
	f.mem.Delete(addrOf(playersFile, "Player", 2))
	r := f.q(t, "SELECT _id FROM players.Player WHERE Name = 'bob'")
	eq(t, col(r, "_id"))
}

// The planner must never change results: run many random conditions with and
// without indexes and compare.
func TestIndexedAndScanResultsAgree(t *testing.T) {
	a := newFixture(t, fixOpt{indexes: true})
	b := newFixture(t, fixOpt{})
	rng := rand.New(rand.NewSource(7))
	names := []string{"ann", "bob", "bobby", "cy", "dee", "zed", "Bob", ""}
	atoms := func() string {
		switch rng.Intn(9) {
		case 0:
			return fmt.Sprintf("Name = '%s'", names[rng.Intn(len(names))])
		case 1:
			return fmt.Sprintf("Name LIKE '%s%%'", names[rng.Intn(len(names))])
		case 2:
			return fmt.Sprintf("Level %s %d", []string{"<", "<=", ">", ">=", "=", "!="}[rng.Intn(6)], rng.Intn(15))
		case 3:
			return fmt.Sprintf("Level BETWEEN %d AND %d", rng.Intn(10), rng.Intn(15))
		case 4:
			return fmt.Sprintf("Class = '%s'", []string{"Mage", "mage", "MAGE", "healer", "warrior"}[rng.Intn(5)])
		case 5:
			return fmt.Sprintf("Stats.Gold %s %d", []string{"<", ">=", ">"}[rng.Intn(3)], rng.Intn(300))
		case 6:
			return fmt.Sprintf("MATCH(Bio, '%s')", []string{"red", "dragon", "red dragon", "knight", "zzz", "the"}[rng.Intn(6)])
		case 7:
			return fmt.Sprintf("_id IN (%d, %d)", rng.Intn(7), rng.Intn(7))
		}
		return fmt.Sprintf("Name IN ('%s', '%s')", names[rng.Intn(len(names))], names[rng.Intn(len(names))])
	}
	var gen func(d int) string
	gen = func(d int) string {
		if d == 0 || rng.Intn(3) == 0 {
			return atoms()
		}
		switch rng.Intn(4) {
		case 0:
			return "(" + gen(d-1) + " AND " + gen(d-1) + ")"
		case 1:
			return "(" + gen(d-1) + " OR " + gen(d-1) + ")"
		case 2:
			return "NOT " + gen(d-1)
		}
		return atoms() + " AND " + atoms() + " AND " + atoms()
	}
	indexedUsed := 0
	for i := 0; i < 600; i++ {
		w := gen(3)
		q := "SELECT _id, _box FROM players.Player WHERE " + w
		ra, err1 := a.db.Exec(context.Background(), anyone, q)
		rb, err2 := b.db.Exec(context.Background(), anyone, q)
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: %v / %v", q, err1, err2)
		}
		if !reflect.DeepEqual(col(ra, "_id"), col(rb, "_id")) {
			t.Fatalf("%s\nindexed %v (%s)\nscan    %v", q, col(ra, "_id"), ra.Access, col(rb, "_id"))
		}
		if ra.Access != "scan" {
			indexedUsed++
		}
	}
	if indexedUsed < 100 {
		t.Fatalf("only %d of 600 queries used an index; the generator is not exercising the planner", indexedUsed)
	}
}

func TestIndexesFollowWritesThroughTheDB(t *testing.T) {
	f := newFixture(t, fixOpt{indexes: true, links: true})
	f.q(t, "INSERT INTO players.Player (_id, Name, Level) VALUES (20, 'newbie', 1)")
	r := f.q(t, "SELECT _id FROM players.Player WHERE Name = 'newbie'")
	eq(t, col(r, "_id"), "20")
	if r.Access != "index:ByName" {
		t.Fatal(r.Access)
	}
	f.q(t, "UPDATE players.Player SET Name = 'veteran', Level = 99 WHERE _id = 20")
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE Name = 'newbie'"), "_id"))
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE Name = 'veteran'"), "_id"), "20")
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE Level > 90"), "_id"), "20")
	f.q(t, "DELETE FROM players.Player WHERE Name = 'veteran'")
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE Name = 'veteran'"), "_id"))
	if d, err := f.mgr.Check(f.cs); err != nil {
		t.Fatal(err)
	} else {
		for n, diff := range d {
			if diff.Stale() {
				t.Fatalf("index %s drifted: %+v", n, diff)
			}
		}
	}
}
