package query

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"redx/box"
)

var hostile = []string{
	`x' OR '1'='1`,
	`'; DELETE FROM players.Player WHERE TRUE; --`,
	`" OR ""="`,
	`bob' --`,
	`\' OR 1=1 --`,
	`%`,
	"a\x00b",
	`'`,
	`''`,
	`?`,
	`) OR (1 = 1`,
	`ann'); UPDATE players.Player SET Level = 0 WHERE TRUE; SELECT ('`,
	"newline\nWHERE TRUE",
	`日本語 ' OR 1=1`,
	`players.Player; DROP`,
	strings.Repeat("'", 1000),
}

func snapshot(f *fixture) string {
	var sb strings.Builder
	for _, file := range f.mem.Files() {
		f.mem.Scan(file, func(a linkAddr, e *box.Entry) bool {
			fmt.Fprintf(&sb, "%s=%s\n", a, e.Value.String())
			return true
		})
	}
	return sb.String()
}

func TestParametersAreNeverParsedAsQueryText(t *testing.T) {
	f := newFixture(t, fixOpt{indexes: true, links: true})
	before := snapshot(f)
	for _, h := range hostile {
		// as a comparison value, an IN member, a LIKE prefix source, BETWEEN bound, LIMIT is not text
		for _, q := range []string{
			"SELECT _id FROM players.Player WHERE Name = ?",
			"SELECT _id FROM players.Player WHERE Name IN (?, 'ann')",
			"SELECT _id FROM players.Player WHERE Name != ? AND Name = 'cy'",
			"SELECT _id FROM players.Player WHERE Bio IS NELL OR Name BETWEEN ? AND ?",
			"SELECT _id FROM players.Player WHERE MATCH(Bio, ?)",
			"UPDATE players.Player SET Name = Name WHERE Name = ?",
			"DELETE FROM players.Player WHERE Name = ?",
		} {
			args := []interface{}{h}
			if strings.Count(q, "?") == 2 {
				args = []interface{}{h, h}
			}
			res, err := f.db.Exec(context.Background(), anyone, q, args...)
			if err != nil {
				t.Fatalf("%s with %q: %v", q, h, err)
			}
			if strings.HasPrefix(q, "SELECT _id FROM players.Player WHERE Name IN") {
				eq(t, col(res, "_id"), "1") // only 'ann' matches; the hostile text is just a string
			}
			if strings.Contains(q, "Name = ?") && strings.HasPrefix(q, "SELECT") && len(res.Rows) != 0 {
				t.Fatalf("%q matched %d rows", h, len(res.Rows))
			}
		}
		if got := snapshot(f); got != before {
			t.Fatalf("data changed by parameter %q", h)
		}
	}
	// stored verbatim by INSERT / UPDATE and read back unchanged
	for i, h := range hostile {
		id := int64(100 + i)
		f.q(t, "INSERT INTO players.Player (_id, Name, Level) VALUES (?, ?, ?)", id, h, i)
		r := f.q(t, "SELECT Name FROM players.Player WHERE _id = ?", id)
		if r.Rows[0][0].Str != h {
			t.Fatalf("stored %q came back as %q", h, r.Rows[0][0].Str)
		}
		r = f.q(t, "SELECT _id FROM players.Player WHERE Name = ?", h) // via the exact index
		if len(r.Rows) != 1 || (len(h) <= 256 && r.Access != "index:ByName") {
			t.Fatalf("%q: %v %s", h, col(r, "_id"), r.Access)
		}
		f.q(t, "UPDATE players.Player SET Name = ? WHERE _id = ?", h+"!", id)
		eq(t, col(f.q(t, "SELECT Name FROM players.Player WHERE _id = ?", id), "Name"), h+"!")
	}
	if n := len(f.q(t, "SELECT _id FROM players.Player").Rows); n != 5+len(hostile) {
		t.Fatalf("row count %d", n)
	}
}

func TestStackedAndConcatenatedQueriesFail(t *testing.T) {
	f := newFixture(t, fixOpt{})
	before := snapshot(f)
	evil := `x'; DELETE FROM players.Player WHERE TRUE; --`
	// what string concatenation would build
	q := "SELECT _id FROM players.Player WHERE Name = '" + evil + "'"
	if err := f.qerr(t, q); !errors.Is(err, ErrSyntax) {
		t.Fatal(err)
	}
	if err := f.qerr(t, "SELECT _id FROM players.Player; DELETE FROM players.Player WHERE TRUE"); !errors.Is(err, ErrSyntax) {
		t.Fatal(err)
	}
	if snapshot(f) != before {
		t.Fatal("data changed")
	}
	// the classic tautology only works when the caller concatenates, which is exactly what ? avoids
	r := f.q(t, "SELECT _id FROM players.Player WHERE Name = '"+`x' OR '1'='1`+"'")
	if len(r.Rows) != 5 {
		t.Fatal("expected the concatenation attack to work in this demonstration")
	}
	r = f.q(t, "SELECT _id FROM players.Player WHERE Name = ?", `x' OR '1'='1`)
	if len(r.Rows) != 0 {
		t.Fatal("parameter must not be interpreted")
	}
}

func TestPlaceholdersOnlyStandForValues(t *testing.T) {
	f := newFixture(t, fixOpt{})
	// a ? can never name a field, file or box
	for _, q := range []string{
		"SELECT _id FROM ?",
		"SELECT _id FROM players.?",
		"SELECT ?.x FROM players.Player",
		"SELECT _id FROM players.Player WHERE ?.x = 1",
		"INSERT INTO players.Player (?) VALUES (1)",
		"UPDATE players.Player SET ? = 1 WHERE TRUE",
		"SELECT _id FROM players.Player ORDER BY ? DESC LIMIT ?, 2",
	} {
		args := make([]interface{}, strings.Count(q, "?"))
		for i := range args {
			args[i] = "Name"
		}
		if err := f.qerr(t, q, args...); !errors.Is(err, ErrSyntax) {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// ? in a select list or ORDER BY is a constant, not a column
	r := f.q(t, "SELECT ? AS x FROM players.Player WHERE _id = 1", "Name")
	eq(t, col(r, "x"), "Name")
	r = f.q(t, "SELECT _id FROM players.Player ORDER BY ? DESC", "Name")
	eq(t, col(r, "_id"), "1", "2", "3", "4", "5") // constant key: stable, address order
}

func TestParameterTypesAndCounts(t *testing.T) {
	f := newFixture(t, fixOpt{})
	// no implicit coercion between strings and numbers
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE Level = ?", "7"), "_id"))
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE Level = ?", 7), "_id"), "3", "4")
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE Level = ?", int8(7)), "_id"), "3", "4")
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE Level = ?", uint64(7)), "_id"), "3", "4")
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE Level = ?", 7.0), "_id"), "3", "4")
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE Class = ?", nil), "_id"), "4")
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE Name IN (?)", []string{"ann", "cy"}), "_id"))
	f.q(t, "UPDATE players.Player SET Tags = ? WHERE _id = 5", []string{"x", "y"})
	r := f.q(t, "SELECT Tags FROM players.Player WHERE _id = 5")
	if r.Rows[0][0].Kind != box.KindArray || len(r.Rows[0][0].Arr) != 2 {
		t.Fatal(r.Rows[0][0])
	}
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE Level = ?", box.String("seven")), "_id"), "5")
	for _, bad := range [][]interface{}{
		{}, {1, 2},
	} {
		if err := f.qerr(t, "SELECT _id FROM players.Player WHERE Level = ?", bad...); !errors.Is(err, ErrParams) {
			t.Fatal(err)
		}
	}
	if err := f.qerr(t, "SELECT _id FROM players.Player WHERE Level = 1", 1); !errors.Is(err, ErrParams) {
		t.Fatal(err)
	}
	for _, bad := range []interface{}{struct{}{}, map[string]int{}, make(chan int), func() {}, []byte("x"), strings.Repeat("x", 2<<20), "bad\xffutf8"} {
		if err := f.qerr(t, "SELECT _id FROM players.Player WHERE Name = ?", bad); !errors.Is(err, ErrParams) {
			t.Fatalf("%T: %v", bad, err)
		}
	}
	if err := f.qerr(t, "SELECT _id FROM players.Player WHERE Level = ?", math.Inf(1)); !errors.Is(err, ErrParams) {
		t.Fatal(err)
	}
}

func TestLikePrefixArg(t *testing.T) {
	f := newFixture(t, fixOpt{})
	p, err := LikePrefixArg("bob")
	if err != nil || p != "bob%" {
		t.Fatal(p, err)
	}
	eq(t, col(f.q(t, "SELECT _id FROM players.Player WHERE Name LIKE ?", p), "_id"), "2", "3")
	if _, err := LikePrefixArg("100%"); err == nil {
		t.Fatal("a % in user input must be refused")
	}
	// a hostile % cannot sneak in a wildcard in the middle
	if err := f.qerr(t, "SELECT _id FROM players.Player WHERE Name LIKE ?", "a%b%"); err == nil {
		t.Fatal("middle %")
	}
}

func TestResourceLimits(t *testing.T) {
	small := func(l Limits) *fixture { return newFixture(t, fixOpt{lim: l, links: true}) }
	exec := func(f *fixture, q string, a ...interface{}) error {
		_, err := f.db.Exec(context.Background(), anyone, q, a...)
		return err
	}
	// result rows
	f := small(Limits{MaxRows: 3})
	if err := exec(f, "SELECT _id FROM players.Player"); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if err := exec(f, "SELECT _id FROM players.Player LIMIT 3"); err != nil {
		t.Fatal(err)
	}
	if err := exec(f, "SELECT _id FROM players.Player LIMIT 4"); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if err := exec(f, "SELECT _id FROM players.Player OFFSET 2 LIMIT 3"); err != nil {
		t.Fatal(err)
	}
	if err := exec(f, "SELECT _id FROM players.Player ORDER BY _id OFFSET 2"); err != nil {
		t.Fatal(err)
	}
	if err := exec(f, "SELECT _id FROM players.Player ORDER BY _id OFFSET 1"); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	// records read
	f = small(Limits{MaxScan: 4})
	if err := exec(f, "SELECT _id FROM players.Player WHERE Name = 'zz'"); !errors.Is(err, ErrLimit) {
		t.Fatalf("scan of 5 records with MaxScan=4: %v", err)
	}
	if err := exec(f, "SELECT _id FROM players.Player WHERE _id = 1"); err != nil {
		t.Fatal(err)
	}
	// writes: nothing is written when the limit trips
	f = small(Limits{MaxWrites: 2})
	before := snapshot(f)
	for _, q := range []string{
		"UPDATE players.Player SET Level = 0 WHERE TRUE",
		"DELETE FROM players.Player WHERE TRUE",
		"INSERT INTO players.Player (_id) VALUES (91), (92), (93)",
	} {
		if err := exec(f, q); !errors.Is(err, ErrLimit) {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if snapshot(f) != before {
		t.Fatal("a refused statement changed data")
	}
	// result size
	f = small(Limits{MaxResultBytes: 200})
	if err := exec(f, "SELECT * FROM players.Player"); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	// columns
	f = small(Limits{MaxColumns: 3})
	if err := exec(f, "SELECT * FROM players.Player"); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	// query text
	f = small(Limits{MaxQueryBytes: 40})
	if err := exec(f, "SELECT _id FROM players.Player WHERE Name = 'a long string'"); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	// IN list and nesting come from the parser limits, configurable
	f = small(Limits{MaxInList: 2, MaxDepth: 2})
	if err := exec(f, "SELECT _id FROM players.Player WHERE Name IN ('a','b','c')"); !errors.Is(err, ErrSyntax) {
		t.Fatal(err)
	}
	if err := exec(f, "SELECT _id FROM players.Player WHERE ((((Name = 'a'))))"); !errors.Is(err, ErrSyntax) {
		t.Fatal(err)
	}
	// an enormous OFFSET/LIMIT does no work and no allocation
	f = small(Limits{})
	if err := exec(f, "SELECT _id FROM players.Player LIMIT 9999999"); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	r, err := f.db.Exec(context.Background(), anyone, "SELECT _id FROM players.Player OFFSET 99999999999")
	if err != nil || len(r.Rows) != 0 {
		t.Fatal(err)
	}
}

func TestCascadeCountsTowardWriteLimit(t *testing.T) {
	f := newFixture(t, fixOpt{lim: Limits{MaxWrites: 2}, links: true})
	// player 1 cascades to three guild records
	for i := int64(20); i < 23; i++ {
		must(t, f.mem.Put(addrOf(guildsFile, "Guild", i), mkEntry(t, "Guild", i)))
	}
	e, _, _ := f.mem.Get(addrOf(playersFile, "Player", 1))
	for i := int64(20); i < 23; i++ {
		l, _ := linkParse(fmt.Sprintf(`"players.red-xai.db":Player[ID=1] @!> "guilds.red-xai.db":Guild[ID=%d]`, i))
		e.Value.Box.Put(box.NewEntry(fmt.Sprintf("C%d", i), nil, box.LinkValue(l)))
	}
	must(t, f.mem.Put(addrOf(playersFile, "Player", 1), e))
	before := snapshot(f)
	if err := f.qerr(t, "DELETE FROM players.Player WHERE _id = 1"); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if snapshot(f) != before {
		t.Fatal("cascade over the limit must delete nothing")
	}
}
