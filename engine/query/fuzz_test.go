package query

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var fuzzSeeds = []string{
	"SELECT * FROM players.Player",
	"SELECT Name, Stats.Gold AS g FROM players.Player p WHERE p.Level >= ? AND (Name LIKE 'b%' OR Name IN ('ann', ?)) ORDER BY g DESC, Name LIMIT 5 OFFSET 1",
	"SELECT p.Name, g.Title FROM players.Player p LEFT JOIN p.Guild g WHERE g.Rank BETWEEN 1 AND 5",
	"SELECT _id FROM players.Player WHERE MATCH(Bio, 'red dragon') AND NOT Class IS NELL",
	"INSERT INTO players.Player (_id, Name, Stats.Gold) VALUES (9, 'x', 1), (10, ?, NELL)",
	"UPDATE players.Player SET Level = Level * 2 + 1, Meta.A = 'x' WHERE Level > 3",
	"DELETE FROM players.Player WHERE _id IN (1, 2) OR Name = 'cy'",
	"EXPLAIN SELECT a FROM f WHERE a = 1",
	"SELECT \"odd \"\"name\"\"\" FROM \"players.red-xai.db\".\"Player\"",
	"SELECT ((((a)))) - -1 FROM f WHERE a[ID=3].b[4] <> 'it''s'",
	"SELECT a FROM f WHERE a = 1e400",
	"SELECT 1.2.3 FROM f",
	"'", "\"", "(", "?", "SELECT", "SELECT a FROM f WHERE a LIKE '%x%'", "\x00", "\xff\xfe",
}

func FuzzParse(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, q string) {
		st, n, err := Parse(q)
		if err != nil {
			var se *SyntaxError
			if !errors.As(err, &se) && !errors.Is(err, ErrTooLarge) {
				t.Fatalf("unexpected error type %T: %v", err, err)
			}
			if se != nil {
				if se.Pos.Line < 1 || se.Pos.Col < 1 || se.Pos.Offset < 0 || se.Pos.Offset > len(q) {
					t.Fatalf("bad position %+v for %q", se.Pos, q)
				}
				_ = se.Context(q)
				_ = se.Error()
			}
			return
		}
		if st == nil || n < 0 || n > len(q) {
			t.Fatalf("bad result %v %d", st, n)
		}
		if !utf8.ValidString(q) {
			t.Fatal("invalid UTF-8 was accepted")
		}
		// everything the EXPLAIN renderer prints for a WHERE must parse again
		var where Expr
		switch s := st.(type) {
		case *Select:
			where = s.Where
		case *Update:
			where = s.Where
		case *Delete:
			where = s.Where
		}
		if where != nil {
			rendered := exprString(where, nil)
			if _, _, err := Parse("SELECT * FROM f WHERE " + rendered); err != nil {
				t.Fatalf("%q rendered as %q does not parse: %v", q, rendered, err)
			}
		}
	})
}

func dump(f *fixture) string { return snapshot(f) }

// FuzzExec runs arbitrary text against two databases that differ only in
// having indexes, and requires that they agree on results and on the data
// left behind. It also requires that no run panics or exceeds a time budget.
func FuzzExec(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Add("SELECT * FROM players.Player WHERE Name = 'ann' OR Level > 6 ORDER BY Level")
	f.Add("UPDATE players.Player SET Name = 'zz' WHERE Name LIKE 'b%'")
	f.Add("DELETE FROM players.Player WHERE Level BETWEEN 5 AND 7")
	f.Fuzz(func(t *testing.T, q string) {
		if len(q) > 600 {
			t.Skip()
		}
		a := newFixture(t, fixOpt{indexes: true, links: true})
		b := newFixture(t, fixOpt{links: true})
		args := []interface{}{"bob", 7, nil, "r%"}
		run := func(f *fixture) (*Result, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, n, perr := Parse(q)
			if perr != nil {
				n = 0
			}
			as := make([]interface{}, n)
			for i := range as {
				as[i] = args[i%len(args)]
			}
			return f.db.Exec(ctx, anyone, q, as...)
		}
		ra, ea := run(a)
		rb, eb := run(b)
		if (ea == nil) != (eb == nil) {
			t.Fatalf("%q: indexed err=%v, scan err=%v", q, ea, eb)
		}
		if ea == nil {
			if fmt.Sprint(ra.Columns, ra.Rows, ra.Affected, ra.Kind) != fmt.Sprint(rb.Columns, rb.Rows, rb.Affected, rb.Kind) &&
				ra.Kind != "explain" {
				t.Fatalf("%q:\nindexed %v %v %d\nscan    %v %v %d", q, ra.Columns, ra.Rows, ra.Affected, rb.Columns, rb.Rows, rb.Affected)
			}
			if len(ra.Rows) > 10000 {
				t.Fatal("MaxRows exceeded")
			}
		} else if !strings.HasPrefix(ea.Error(), "query:") && !strings.HasPrefix(ea.Error(), "link:") && !strings.Contains(ea.Error(), "sec:") && !errors.Is(ea, context.DeadlineExceeded) {
			t.Fatalf("%q: error without a package prefix: %v", q, ea)
		}
		if dump(a) != dump(b) {
			t.Fatalf("%q: data differ after the statement:\n--indexed\n%s\n--scan\n%s", q, dump(a), dump(b))
		}
		// the index must still agree with the data after any write
		if ea == nil {
			if d, err := a.mgr.Check(a.cs); err == nil {
				for n, diff := range d {
					if diff.Stale() {
						t.Fatalf("%q left index %s stale", q, n)
					}
				}
			}
		}
	})
}
