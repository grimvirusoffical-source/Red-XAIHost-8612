package query

import (
	"errors"
	"strings"
	"testing"
)

func TestLexPositions(t *testing.T) {
	toks, err := lex("SELECT a,\n  'x''y' <> 1.5e2 ? \"Q\"\"q\"", 100)
	if err != nil {
		t.Fatal(err)
	}
	type w struct {
		k    tokKind
		text string
		line int
		col  int
	}
	want := []w{
		{tIdent, "SELECT", 1, 1}, {tIdent, "a", 1, 8}, {tPunct, ",", 1, 9},
		{tString, "x'y", 2, 3}, {tPunct, "!=", 2, 10}, {tNumber, "1.5e2", 2, 13},
		{tParam, "?", 2, 19}, {tQIdent, `Q"q`, 2, 21}, {tEOF, "", 2, 27},
	}
	if len(toks) != len(want) {
		t.Fatalf("%d tokens: %+v", len(toks), toks)
	}
	for i, x := range want {
		g := toks[i]
		if g.kind != x.k || g.text != x.text || g.pos.Line != x.line || g.pos.Col != x.col {
			t.Errorf("token %d: got %v %q %d:%d, want %v %q %d:%d", i, g.kind, g.text, g.pos.Line, g.pos.Col, x.k, x.text, x.line, x.col)
		}
	}
}

func TestSyntaxErrorPositions(t *testing.T) {
	cases := []struct {
		q    string
		line int
		col  int
		msg  string
	}{
		{"", 1, 1, "expected SELECT"},
		{"SELEC x FROM f", 1, 1, "unknown statement"},
		{"SELECT FROM f", 1, 8, "unexpected keyword FROM"},
		{"SELECT a f", 1, 10, "expected FROM"},
		{"SELECT a FROM", 1, 14, "expected a file name"},
		{"SELECT a FROM f WHERE", 1, 22, "expected a value"},
		{"SELECT a FROM f WHERE a =", 1, 26, "expected a value"},
		{"SELECT a FROM f WHERE a = 'x", 1, 27, "unterminated string"},
		{"SELECT a FROM f WHERE a = \"x\"", 1, 27, ""}, // double quotes are identifiers: accepted as a field
		{"SELECT a FROM f WHERE a @ 1", 1, 25, "unexpected character"},
		{"SELECT a FROM f\nWHERE a = 1 AND", 2, 16, "expected a value"},
		{"SELECT a FROM f\n  WHERE (a = 1", 2, 15, "expected ')'"},
		{"SELECT a FROM f WHERE a IN ()", 1, 29, "expected a value"},
		{"SELECT a FROM f WHERE a IN 1", 1, 28, "expected '('"},
		{"SELECT a FROM f WHERE a LIKE b", 1, 30, "LIKE needs a string literal"},
		{"SELECT a FROM f WHERE a BETWEEN 1 2", 1, 35, "expected AND"},
		{"SELECT a FROM f WHERE a IS 3", 1, 28, "expected NELL"},
		{"SELECT a FROM f LIMIT -1", 1, 23, "expected a number or ?"},
		{"SELECT a FROM f LIMIT 1.5", 1, 23, "non-negative whole number"},
		{"SELECT a FROM f LIMIT 1 LIMIT 2", 1, 25, "LIMIT given twice"},
		{"SELECT a FROM f ORDER a", 1, 23, "expected BY"},
		{"SELECT a FROM f; SELECT b FROM f", 1, 18, "after the end of the statement"},
		{"SELECT a FROM f WHERE a = 1 ;;", 1, 30, "after the end of the statement"},
		{"SELECT a FROM f WHERE a = 1 -- c", 1, 29, ""}, // `1 -` then missing operand
		{"SELECT select FROM f", 1, 8, "unexpected keyword SELECT"},
		{"SELECT a FROM from", 1, 15, "reserved word FROM"},
		{"SELECT a FROM f WHERE a[ID=x] = 1", 1, 28, "whole number ID"},
		{"SELECT a FROM f WHERE a[1 = 1", 1, 24, "unclosed '['"},
		{"SELECT 99999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999e9 FROM f", 1, 8, "out of range"},
		{"SELECT 12ab FROM f", 1, 8, "malformed number"},
		{"UPDATE f SET a = 1", 1, 19, "requires a WHERE"},
		{"UPDATE f SET WHERE TRUE", 1, 14, "unexpected keyword WHERE"},
		{"DELETE FROM f", 1, 14, "requires a WHERE"},
		{"DELETE f WHERE TRUE", 1, 8, "expected FROM"},
		{"INSERT INTO f VALUES (1)", 1, 15, "expected '('"},
		{"INSERT INTO f (a, b) VALUES (1)", 1, 29, "row has 1 values but 2 columns"},
		{"INSERT INTO f (a) VALUES (b)", 1, 27, "field references are not allowed"},
		{"EXPLAIN EXPLAIN SELECT a FROM f", 1, 9, "cannot be nested"},
		{"SELECT a FROM f JOIN b g", 1, 22, "alias.field"},
		{"SELECT a FROM f WHERE a = 1\x00", 1, 28, "control"},
		{"SELECT a FROM f WHERE a = '\x00'", 1, 28, "NUL"},
		{"SELECT a FROM f WHERE a = \xff", 1, 27, "UTF-8"},
		{"SELECT a FROM \"\"", 1, 15, "empty quoted"},
		{"SELECT a FROM \"abc", 1, 15, "unterminated quoted"},
		{"SELECT a. FROM f", 1, 11, "unexpected keyword FROM"},
		{"SELECT a.b.c.d.e.f.g.h.i.j.k.l.m.n.o.p.q FROM f", 1, 40, "too deep"},
		{"SELECT MATCH(a) FROM f", 1, 15, "expected ','"},
		{"SELECT MATCH(a, b) FROM f", 1, 17, "string literal or ?"},
	}
	for _, c := range cases {
		_, _, err := Parse(c.q)
		if c.msg == "" {
			continue // only checking it does not panic
		}
		var se *SyntaxError
		if !errors.As(err, &se) {
			t.Errorf("%q: want syntax error, got %v", c.q, err)
			continue
		}
		if !errors.Is(err, ErrSyntax) {
			t.Errorf("%q: errors.Is(ErrSyntax) false", c.q)
		}
		if se.Pos.Line != c.line || se.Pos.Col != c.col || !strings.Contains(se.Msg, c.msg) {
			t.Errorf("%q:\n   got  %d:%d %q\n   want %d:%d %q", c.q, se.Pos.Line, se.Pos.Col, se.Msg, c.line, c.col, c.msg)
		}
	}
}

func TestSyntaxErrorContext(t *testing.T) {
	q := "SELECT a\nFROM f\nWHERE a = = 1"
	_, _, err := Parse(q)
	var se *SyntaxError
	if !errors.As(err, &se) {
		t.Fatal(err)
	}
	got := se.Context(q)
	if got != "WHERE a = = 1\n          ^" {
		t.Fatalf("%q", got)
	}
}

func TestParseValid(t *testing.T) {
	for _, q := range []string{
		"SELECT * FROM f",
		"select * from f;",
		"SELECT a, b AS c, a.b.c, \"odd name\", Inv[ID=3].Count, Inv[3].Count FROM \"x.red-xai.db\".Box",
		"SELECT f.* FROM f",
		"SELECT -a, 1 + 2 * 3 - 4 / 2, (1 + 2) * 3 FROM f",
		"SELECT a FROM f x WHERE x.a = 1 AND NOT (b < 2 OR c >= 3) ORDER BY a DESC, b ASC, c LIMIT 5 OFFSET 2",
		"SELECT a FROM f OFFSET 2 LIMIT 5",
		"SELECT a FROM f WHERE a IN (1, 'x', ?) AND b NOT IN (2) AND c NOT LIKE 'a%' AND d NOT BETWEEN 1 AND 2 AND e IS NOT NELL",
		"SELECT a FROM f WHERE MATCH(a, 'x y') OR MATCH_ANY(b, ?)",
		"SELECT p.a, g.b FROM f p JOIN p.g g LEFT JOIN g.h AS h INNER JOIN h.i j",
		"SELECT a FROM f WHERE a = TRUE AND b = false AND c = nell",
		"SELECT a FROM f WHERE a.order = 1 AND \"order\" = 2",
		"INSERT INTO f.B (_id, a, b.c) VALUES (1, 'x', -2.5), (2, ?, NELL)",
		"UPDATE f.B SET a = a + 1, b.c = 'x' WHERE _id = 1",
		"DELETE FROM f.B b WHERE b._id IN (1, 2)",
		"EXPLAIN SELECT a FROM f",
		"EXPLAIN DELETE FROM f WHERE TRUE",
	} {
		if _, _, err := Parse(q); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
	_, n, err := Parse("SELECT ? FROM f WHERE a = ? LIMIT ? OFFSET ?")
	if err != nil || n != 4 {
		t.Fatal(n, err)
	}
}

func TestParseRoundTripThroughExprString(t *testing.T) {
	// the EXPLAIN renderer must be re-parseable for the conditions it prints
	for _, w := range []string{
		"a = 1 AND b = 'x''y'",
		"a IN (1, 2) OR NOT b LIKE 'p%'",
		"a BETWEEN 1 AND 2 AND b IS NELL",
		"MATCH(a, 'x') AND c.d[ID=1].e > -3",
	} {
		st, _, err := Parse("SELECT * FROM f WHERE " + w)
		if err != nil {
			t.Fatal(w, err)
		}
		s := exprString(st.(*Select).Where, nil)
		st2, _, err := Parse("SELECT * FROM f WHERE " + s)
		if err != nil {
			t.Fatalf("%q rendered as %q does not re-parse: %v", w, s, err)
		}
		if s2 := exprString(st2.(*Select).Where, nil); s2 != s {
			t.Fatalf("render not stable: %q vs %q", s, s2)
		}
	}
}

func TestParserLimits(t *testing.T) {
	deep := "SELECT a FROM f WHERE " + strings.Repeat("(", 200) + "a = 1" + strings.Repeat(")", 200)
	if _, _, err := Parse(deep); !errors.Is(err, ErrSyntax) || !strings.Contains(err.Error(), "nested too deeply") {
		t.Fatal(err)
	}
	nots := "SELECT a FROM f WHERE " + strings.Repeat("NOT ", 500) + "a"
	if _, _, err := Parse(nots); !errors.Is(err, ErrSyntax) {
		t.Fatal(err)
	}
	minus := "SELECT " + strings.Repeat("- ", 500) + "1 FROM f"
	if _, _, err := Parse(minus); !errors.Is(err, ErrSyntax) {
		t.Fatal(err)
	}
	// long flat expressions are fine (iterative loops, not recursion)
	flat := "SELECT a FROM f WHERE a = 1" + strings.Repeat(" OR a = 1", 2000)
	if _, _, err := Parse(flat); err != nil {
		t.Fatal(err)
	}
	big := "SELECT a FROM f WHERE a = '" + strings.Repeat("x", 70000) + "'"
	if _, _, err := Parse(big); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	many := "SELECT a FROM f WHERE a IN (" + strings.Repeat("1,", 1500) + "1)"
	if _, _, err := Parse(many); !errors.Is(err, ErrSyntax) || !strings.Contains(err.Error(), "IN list too long") {
		t.Fatal(err)
	}
	toks := "SELECT " + strings.Repeat("a,", 5000) + "a FROM f"
	if _, _, err := Parse(toks); !errors.Is(err, ErrSyntax) || !strings.Contains(err.Error(), "too many tokens") {
		t.Fatal(err)
	}
	rows := "INSERT INTO f.B (a) VALUES " + strings.Repeat("(1),", 1500) + "(1)"
	if _, _, err := Parse(rows); !errors.Is(err, ErrSyntax) {
		t.Fatal(err)
	}
	if _, _, err := Parse("SELECT a FROM f JOIN a.b b1 JOIN a.c b2 JOIN a.d b3 JOIN a.e b4 JOIN a.f b5"); !errors.Is(err, ErrSyntax) {
		t.Fatal(err)
	}
}
